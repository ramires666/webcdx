package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type browserJournalRuntimeFixture struct {
	b        *browserManager
	root     string
	writer   *browserJournalWriter
	mu       sync.Mutex
	gate     chan struct{}
	gateOnce sync.Once
	started  chan struct{}
	writes   atomic.Int32
}

func newBrowserJournalRuntimeFixture(t *testing.T) *browserJournalRuntimeFixture {
	t.Helper()
	e, root := testExecutor(t)
	dir := filepath.Join(root, "browser")
	b, err := newBrowserManager(e, browserConfig{Enabled: true, Dir: dir, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	f := &browserJournalRuntimeFixture{b: b, root: root, gate: make(chan struct{}), started: make(chan struct{})}
	f.writer = newBrowserJournalWriter(func(path string, data []byte) error {
		if f.writes.Add(1) == 1 {
			close(f.started)
			<-f.gate
		}
		return atomicWrite(path, data, false)
	})
	if b.journalWriter != nil {
		_ = b.journalWriter.close(context.Background())
	}
	b.journalWriter = f.writer
	t.Cleanup(func() { _ = f.writer.close(context.Background()) })
	t.Cleanup(f.releaseGate)
	return f
}

func (f *browserJournalRuntimeFixture) releaseGate() { f.gateOnce.Do(func() { close(f.gate) }) }

func (f *browserJournalRuntimeFixture) mutation(ctx context.Context, key string) (any, error) {
	return f.b.call(ctx, "browser_tabs", map[string]any{"browser_session_id": "missing-session", "action": "new", "request_key": key})
}

func (f *browserJournalRuntimeFixture) waitOperation(t *testing.T) *browserOperation {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		f.b.mu.Lock()
		for _, op := range f.b.operations {
			if op.Status == "running" && op.Phase == "reserved" {
				f.b.mu.Unlock()
				return op
			}
		}
		f.b.mu.Unlock()
		select {
		case <-deadline:
			t.Fatal("reserved operation did not become visible")
		case <-ticker.C:
		}
	}
}

func TestBrowserJournalReservationDoesNotHoldManagerLockAndRetriesShareOperation(t *testing.T) {
	f := newBrowserJournalRuntimeFixture(t)
	firstResult := make(chan struct {
		value any
		err   error
	}, 1)
	go func() {
		value, err := f.mutation(context.Background(), "same-key")
		firstResult <- struct {
			value any
			err   error
		}{value, err}
	}()
	select {
	case <-f.started:
	case <-time.After(time.Second):
		t.Fatal("reservation writer did not block")
	}
	op := f.waitOperation(t)
	if op.Status != "running" || op.Phase != "reserved" {
		t.Fatalf("pending reservation operation = %s/%s", op.Status, op.Phase)
	}

	// A status query for another session must not wait behind the journal IO.
	f.b.mu.Lock()
	f.b.sessions["unrelated-session"] = &browserSession{id: "unrelated-session", tabs: map[string]*browserTab{}, cancel: func() {}}
	f.b.mu.Unlock()
	controlDone := make(chan error, 1)
	go func() {
		_, err := f.b.call(context.Background(), "browser_control", map[string]any{"action": "status", "browser_session_id": "unrelated-session"})
		controlDone <- err
	}()
	select {
	case err := <-controlDone:
		if err != nil {
			t.Fatalf("unrelated status query failed: %v", err)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("unrelated browser_control status waited for blocked journal IO")
	}

	value, err := f.mutation(context.Background(), "same-key")
	if err != nil {
		t.Fatalf("same-key retry should return pending operation, got %v", err)
	}
	retry, ok := value.(map[string]any)
	if !ok || retry["operation_id"] != op.ID || retry["status"] != "running" {
		t.Fatalf("retry did not join reservation: %#v", value)
	}
	if f.writes.Load() != 1 {
		t.Fatalf("same-key retry caused duplicate journal writes: %d", f.writes.Load())
	}

	f.releaseGate()
	select {
	case <-op.done:
	case <-time.After(2 * time.Second):
		t.Fatal("operation did not finalize after durable reservation")
	}
	result := <-firstResult
	if result.err != nil {
		t.Fatalf("initial RPC unexpectedly failed before returning running: %v", result.err)
	}
	if op.ErrorCode != "unknown_session" {
		t.Fatalf("operation after durable reservation = status %q code %q", op.Status, op.ErrorCode)
	}
	if f.writes.Load() < 2 {
		t.Fatalf("expected reservation and final journal writes, got %d", f.writes.Load())
	}
}

func TestBrowserJournalReservationCancellationBeforeDurabilityNeverDispatches(t *testing.T) {
	f := newBrowserJournalRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan struct {
		value any
		err   error
	}, 1)
	go func() {
		value, err := f.mutation(ctx, "cancel-before-durable")
		result <- struct {
			value any
			err   error
		}{value, err}
	}()
	select {
	case <-f.started:
	case <-time.After(time.Second):
		t.Fatal("reservation writer did not block")
	}
	op := f.waitOperation(t)
	cancel()
	select {
	case <-result:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("caller cancellation did not return while reservation IO was blocked")
	}
	if op.execution.dispatched.Load() {
		t.Fatal("browser dispatch started before durable reservation completed")
	}
	if _, err := f.b.control(map[string]any{"action": "cancel", "operation_id": op.ID}); err != nil {
		t.Fatalf("cancelling operation before durable reservation failed: %v", err)
	}
	f.releaseGate()
	select {
	case <-op.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled reservation did not reach a final state")
	}
	if op.Status != "cancelled" || op.ErrorCode != "cancelled_before_dispatch" || op.execution.dispatched.Load() {
		t.Fatalf("cancel-before-durable result = status %q code %q dispatched %t", op.Status, op.ErrorCode, op.execution.dispatched.Load())
	}
	if _, err := os.Stat(filepath.Join(f.b.config.Dir, "operations", op.ID+".json")); err != nil {
		t.Fatalf("final cancellation outcome was not persisted: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(f.b.config.Dir, "operations", op.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted browserOperation
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.ErrorCode != "cancelled_before_dispatch" || persisted.Status != "cancelled" {
		t.Fatalf("persisted cancellation outcome unsafe: %#v", persisted)
	}
}

func TestBrowserJournalReservationSlotOverflowFailsBeforeOperationAdmission(t *testing.T) {
	f := newBrowserJournalRuntimeFixture(t)
	var held []*browserJournalSlot
	for i := 0; i < browserJournalWriterSlots; i++ {
		slot, ok := f.writer.tryReserve()
		if !ok {
			t.Fatalf("could not prefill writer slot %d", i)
		}
		held = append(held, slot)
	}
	_, err := f.mutation(context.Background(), "overflow-key")
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != "browser_busy" {
		t.Fatalf("full writer should reject operation admission with browser_busy, got %v", err)
	}
	f.b.mu.Lock()
	operations, indexed := len(f.b.operations), f.b.journal[commandHash("overflow-key")]
	f.b.mu.Unlock()
	if operations != 0 || indexed.ID != "" {
		t.Fatalf("overflow admitted an operation or key: operations=%d index=%#v", operations, indexed)
	}
	if f.writes.Load() != 0 {
		t.Fatalf("overflow reached journal IO: writes=%d", f.writes.Load())
	}
	for _, slot := range held {
		slot.release()
	}
}
