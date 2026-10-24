package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cacheTestManager(t *testing.T) *browserManager {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "operations"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &browserManager{config: browserConfig{Dir: dir}, operations: map[string]*browserOperation{}, journal: map[string]browserJournalEntry{}}
}

func expectedBrowserOperationCacheBytes(t *testing.T, op *browserOperation) int64 {
	t.Helper()
	encoded, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	transient, err := json.Marshal(op.errorTransient)
	if err != nil {
		t.Fatal(err)
	}
	return int64(len(encoded) + len(op.image) + len(transient))
}

func TestBrowserOperationCacheBytesAccountsForPayloadAndReplacement(t *testing.T) {
	b := cacheTestManager(t)
	op := &browserOperation{
		ID: "bop_000000000000000000000001", Status: "completed", At: time.Now(),
		Result:         map[string]any{"text": strings.Repeat("payload", 400)},
		image:          strings.Repeat("i", 4096),
		errorTransient: map[string]any{"verification": strings.Repeat("v", 2048)},
	}
	b.cacheOperation(op)
	want := expectedBrowserOperationCacheBytes(t, op)
	if op.cacheBytes != want || b.cacheBytes != want {
		t.Fatalf("initial cached byte count = op %d manager %d, want %d", op.cacheBytes, b.cacheBytes, want)
	}

	// Finalization often updates the same operation pointer in place.
	op.Result["text"] = strings.Repeat("larger-payload", 2000)
	op.image += strings.Repeat("j", 1024)
	op.errorTransient["detail"] = strings.Repeat("d", 1024)
	b.cacheOperation(op)
	want = expectedBrowserOperationCacheBytes(t, op)
	if len(b.operations) != 1 || b.operations[op.ID] != op || op.cacheBytes != want || b.cacheBytes != want {
		t.Fatalf("same-pointer replacement double-counted or missed payload: size=%d op=%d want=%d entries=%d", b.cacheBytes, op.cacheBytes, want, len(b.operations))
	}

	replacement := &browserOperation{ID: op.ID, Status: "completed", At: op.At, Result: map[string]any{"text": "replacement"}}
	b.cacheOperation(replacement)
	want = expectedBrowserOperationCacheBytes(t, replacement)
	if len(b.operations) != 1 || b.operations[op.ID] != replacement || replacement.cacheBytes != want || b.cacheBytes != want {
		t.Fatalf("new-pointer replacement did not subtract old payload: size=%d op=%d want=%d", b.cacheBytes, replacement.cacheBytes, want)
	}
}

func TestBrowserOperationByteTrimPreservesRunningAndDiskRetryRecord(t *testing.T) {
	b := cacheTestManager(t)
	now := time.Now()
	oldRead := &browserOperation{ID: "bop_000000000000000000000011", Status: "completed", At: now.Add(-2 * time.Hour), image: strings.Repeat("a", 17<<20)}
	newRead := &browserOperation{ID: "bop_000000000000000000000012", Status: "completed", At: now.Add(-time.Hour), image: strings.Repeat("b", 17<<20)}
	mutation := &browserOperation{ID: "bop_000000000000000000000013", KeyHash: "saved-key-hash", Hash: "saved-hash", Status: "completed", At: now, Result: map[string]any{"text": "preserved on disk"}}
	running := &browserOperation{ID: "bop_000000000000000000000014", Status: "running", At: now, image: strings.Repeat("r", 1<<20)}
	if err := b.saveOperation(mutation); err != nil {
		t.Fatal(err)
	}
	b.journal[mutation.KeyHash] = browserJournalEntry{ID: mutation.ID, At: mutation.At}
	for _, op := range []*browserOperation{oldRead, newRead, mutation, running} {
		b.cacheOperation(op)
	}
	if b.cacheBytes <= browserOperationCacheBytesMax {
		t.Fatalf("fixture payload should exceed budget: %d <= %d", b.cacheBytes, browserOperationCacheBytesMax)
	}

	b.trimOperations(browserOperationCacheMax)
	if b.operations[oldRead.ID] != nil {
		t.Fatal("oldest read was not evicted first")
	}
	if b.operations[newRead.ID] == nil {
		t.Fatal("newer read should fit after the oldest read is evicted")
	}
	if b.operations[running.ID] != running {
		t.Fatal("running operation must never be evicted")
	}
	if b.operations[mutation.ID] != mutation {
		t.Fatal("completed mutation should remain cached after read eviction meets the budget")
	}
	if b.cacheBytes > browserOperationCacheBytesMax {
		t.Fatalf("trim left cache over byte budget: %d", b.cacheBytes)
	}
	if entry, ok := b.journal[mutation.KeyHash]; !ok || entry.ID != mutation.ID {
		t.Fatalf("read-cache trimming removed retry index: %#v", b.journal)
	}

	path := filepath.Join(b.config.Dir, "operations", mutation.ID+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("mutation journal file was removed by cache trimming: %v", err)
	}
	b.removeCachedOperation(mutation.ID)
	b.mu.Lock()
	reloaded, err := b.loadOperation(mutation.ID)
	b.mu.Unlock()
	if err != nil || reloaded.Result["text"] != "preserved on disk" {
		t.Fatalf("evicted mutation result was not recoverable from disk: %#v, %v", reloaded, err)
	}
}

func TestBrowserOperationExpiryUpdatesCacheByteCounter(t *testing.T) {
	b := cacheTestManager(t)
	old := time.Now().Add(-browserOperationTTL - time.Minute)
	mutation := &browserOperation{ID: "bop_000000000000000000000021", KeyHash: "expired-key", Hash: "expired-hash", Status: "completed", At: old, Result: map[string]any{"text": strings.Repeat("expired", 128)}}
	read := &browserOperation{ID: "bop_000000000000000000000022", Status: "completed", At: old, image: strings.Repeat("expired-image", 256)}
	if err := b.saveOperation(mutation); err != nil {
		t.Fatal(err)
	}
	b.journal[mutation.KeyHash] = browserJournalEntry{ID: mutation.ID, At: mutation.At}
	b.cacheOperation(mutation)
	b.cacheOperation(read)
	if b.cacheBytes == 0 {
		t.Fatal("test setup failed to account cached payload")
	}

	if err := b.expireOperations(time.Now()); err != nil {
		t.Fatal(err)
	}
	if b.cacheBytes != 0 || len(b.operations) != 0 || len(b.journal) != 0 {
		t.Fatalf("expiry did not clear cached byte accounting: bytes=%d operations=%d journal=%d", b.cacheBytes, len(b.operations), len(b.journal))
	}
	for _, id := range []string{mutation.ID, read.ID} {
		if _, err := os.Stat(filepath.Join(b.config.Dir, "operations", id+".json")); id == mutation.ID && !os.IsNotExist(err) {
			t.Fatalf("expired mutation journal file remains: %v", err)
		}
	}
}

func TestBrowserOperationByteEvictionKeepsMutationRetryJournal(t *testing.T) {
	b := cacheTestManager(t)
	now := time.Now()
	mutation := &browserOperation{ID: "bop_000000000000000000000031", KeyHash: "retry-index", Hash: "retry-hash", Status: "completed", At: now, Result: map[string]any{"text": "recoverable result"}}
	running := &browserOperation{ID: "bop_000000000000000000000032", Status: "running", At: now, image: strings.Repeat("r", browserOperationCacheBytesMax+1)}
	if err := b.saveOperation(mutation); err != nil {
		t.Fatal(err)
	}
	b.journal[mutation.KeyHash] = browserJournalEntry{ID: mutation.ID, At: mutation.At}
	b.cacheOperation(mutation)
	b.cacheOperation(running)
	b.trimOperations(browserOperationCacheMax)

	if b.operations[mutation.ID] != nil || b.operations[running.ID] != running {
		t.Fatalf("oversized running payload should remain while completed cache entry is evicted: %#v", b.operations)
	}
	if entry, ok := b.journal[mutation.KeyHash]; !ok || entry.ID != mutation.ID {
		t.Fatalf("byte eviction removed idempotency index: %#v", b.journal)
	}
	if _, err := os.Stat(filepath.Join(b.config.Dir, "operations", mutation.ID+".json")); err != nil {
		t.Fatalf("byte eviction removed mutation journal file: %v", err)
	}
	b.mu.Lock()
	reloaded, err := b.loadOperation(mutation.ID)
	b.mu.Unlock()
	if err != nil || reloaded.Result["text"] != "recoverable result" {
		t.Fatalf("mutation result no longer recoverable after cache eviction: %#v, %v", reloaded, err)
	}
}
