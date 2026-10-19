package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBrowserHistoryOverflowPreservesRetriesAndRestart(t *testing.T) {
	e, root := testExecutor(t)
	config := browserConfig{Enabled: true, Dir: filepath.Join(root, "browser")}
	b, err := newBrowserManager(e, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	args := map[string]any{"browser_session_id": "missing", "action": "url", "url": "https://example.com", "request_key": "original"}
	original := &browserOperation{
		ID: browserID("bop_"), KeyHash: commandHash("original"),
		Hash: commandHash(map[string]any{"name": "browser_navigate", "arguments": args}),
		At:   time.Now().Add(-time.Hour), Status: "completed",
		Result: map[string]any{"status": "completed", "text": "original result", "truncated": false},
	}
	// Seed more mutations than the entire cache can hold, all inside the TTL.
	// These are real journal files, so eviction and restart exercise disk replay.
	for i := 0; i < browserOperationCacheMax+8; i++ {
		op := original
		if i != 0 {
			op = &browserOperation{ID: browserID("bop_"), KeyHash: commandHash(fmt.Sprint(i)), At: time.Now(), Status: "completed"}
		}
		data, err := json.Marshal(op)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(config.Dir, "operations", op.ID+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		b.mu.Lock()
		b.operations[op.ID] = op
		b.journal[op.KeyHash] = browserJournalEntry{ID: op.ID, At: op.At}
		b.mu.Unlock()
	}
	// An accepted request reaches normal session validation instead of failing
	// with limit_reached merely because completed history filled the journal.
	_, err = b.call(context.Background(), "browser_navigate", map[string]any{"browser_session_id": "missing", "action": "url", "url": "https://example.com", "request_key": "new-after-overflow"})
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != "unknown_session" {
		t.Fatalf("new request blocked by history: %v", err)
	}
	b.mu.Lock()
	cached := len(b.operations)
	_, retained := b.operations[original.ID]
	b.mu.Unlock()
	if cached > browserOperationCacheMax || retained {
		t.Fatalf("cache was not pruned: size=%d original retained=%t", cached, retained)
	}
	checkReplay := func(manager *browserManager) {
		t.Helper()
		result, err := manager.call(context.Background(), "browser_navigate", args)
		if err != nil || result.(map[string]any)["text"] != "original result" {
			t.Fatalf("retry reran the action or lost the result: %v %v", result, err)
		}
		result, err = manager.call(context.Background(), "browser_control", map[string]any{"action": "status", "operation_id": original.ID})
		if err != nil || result.(map[string]any)["text"] != "original result" {
			t.Fatalf("evicted operation cannot be polled: %v %v", result, err)
		}
		conflict := map[string]any{}
		for key, value := range args {
			conflict[key] = value
		}
		conflict["url"] = "https://example.org"
		_, err = manager.call(context.Background(), "browser_navigate", conflict)
		if !errors.As(err, &coded) || coded.code != "request_key_conflict" {
			t.Fatalf("eviction lost retry conflict protection: %v", err)
		}
	}
	checkReplay(b)
	b.Close()
	restarted, err := newBrowserManager(e, config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if len(restarted.operations) > browserOperationCacheMax || len(restarted.journal) != browserOperationCacheMax+9 {
		t.Fatalf("restart cache/index sizes: %d/%d", len(restarted.operations), len(restarted.journal))
	}
	checkReplay(restarted)
}

func TestBrowserOperationConcurrencyAndRunningProtection(t *testing.T) {
	e, root := testExecutor(t)
	b, err := newBrowserManager(e, browserConfig{Enabled: true, Dir: filepath.Join(root, "browser")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.mu.Lock()
	for i := 0; i < browserOperationRunningMax; i++ {
		op := &browserOperation{ID: browserID("bop_"), Status: "running", At: time.Now().Add(-25 * time.Hour)}
		b.operations[op.ID] = op
	}
	if err := b.expireOperations(time.Now()); err != nil {
		t.Fatal(err)
	}
	b.trimOperations(0)
	if len(b.operations) != browserOperationRunningMax {
		t.Fatal("cleanup removed active operations")
	}
	b.mu.Unlock()
	_, err = b.call(context.Background(), "browser_tabs", map[string]any{"browser_session_id": "missing", "action": "list"})
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != "limit_reached" {
		t.Fatalf("concurrency guard lost: %v", err)
	}
	b.mu.Lock()
	for id := range b.operations {
		delete(b.operations, id)
		break
	}
	b.mu.Unlock()
	_, err = b.call(context.Background(), "browser_tabs", map[string]any{"browser_session_id": "missing", "action": "list"})
	if !errors.As(err, &coded) || coded.code != "unknown_session" {
		t.Fatalf("freed concurrency slot not reusable: %v", err)
	}
}

func TestBrowserOperationIdleCleanup(t *testing.T) {
	e, root := testExecutor(t)
	b, err := newBrowserManager(e, browserConfig{Enabled: true, Dir: filepath.Join(root, "browser")})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	op := &browserOperation{ID: browserID("bop_"), KeyHash: commandHash("expired"), At: time.Now().Add(-25 * time.Hour), Status: "completed"}
	running := &browserOperation{ID: browserID("bop_"), KeyHash: commandHash("active"), At: op.At, Status: "running"}
	fresh := &browserOperation{ID: browserID("bop_"), KeyHash: commandHash("fresh"), At: time.Now(), Status: "completed"}
	b.mu.Lock()
	for _, entry := range []*browserOperation{op, running, fresh} {
		if err := b.saveOperation(entry); err != nil {
			t.Fatal(err)
		}
	}
	// The expired result has already left memory, so cleanup must use the disk
	// retry index as well. Keep a running record and a fresh record alongside it.
	b.operations[running.ID] = running
	b.operations[fresh.ID] = fresh
	b.mu.Unlock()
	unrelated := filepath.Join(b.config.Dir, "operations", "notes.txt")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.runOperationCleanup(ctx, 5*time.Millisecond)
	}()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		b.mu.Lock()
		_, exists := b.journal[op.KeyHash]
		b.mu.Unlock()
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle cleanup never expired the journal")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(b.config.Dir, "operations", op.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("expired journal file retained: %v", err)
	}
	for _, path := range []string{unrelated, filepath.Join(b.config.Dir, "operations", running.ID+".json"), filepath.Join(b.config.Dir, "operations", fresh.ID+".json")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cleanup deleted a protected file: %s: %v", path, err)
		}
	}
}

func TestBrowserEvictedUncertainMutationFailsClosed(t *testing.T) {
	e, root := testExecutor(t)
	config := browserConfig{Enabled: true, Dir: filepath.Join(root, "browser")}
	b, err := newBrowserManager(e, config)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"browser_session_id": "missing", "action": "url", "url": "https://example.com", "request_key": "uncertain"}
	op := &browserOperation{
		ID: browserID("bop_"), KeyHash: commandHash("uncertain"), At: time.Now(), Status: "running",
		Hash: commandHash(map[string]any{"name": "browser_navigate", "arguments": args}),
	}
	b.mu.Lock()
	err = b.saveOperation(op)
	b.mu.Unlock()
	b.Close()
	if err != nil {
		t.Fatal(err)
	}
	b, err = newBrowserManager(e, config)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.mu.Lock()
	b.trimOperations(0)
	b.mu.Unlock()
	_, err = b.call(context.Background(), "browser_navigate", args)
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != "outcome_unknown" {
		t.Fatalf("uncertain action was replayed: %v", err)
	}
	if err := os.Remove(filepath.Join(config.Dir, "operations", op.ID+".json")); err != nil {
		t.Fatal(err)
	}
	_, err = b.call(context.Background(), "browser_navigate", args)
	if !errors.As(err, &coded) || coded.code != "io_error" {
		t.Fatalf("missing journal allowed a duplicate action: %v", err)
	}
	_, err = b.call(context.Background(), "browser_control", map[string]any{"action": "status", "operation_id": "../../secrets"})
	if !errors.As(err, &coded) || coded.code != "unknown_operation" {
		t.Fatalf("unvalidated operation path: %v", err)
	}
}
