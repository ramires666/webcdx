package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserJournalRejectsOversizeAndNonRegularRecords(t *testing.T) {
	root := t.TempDir()
	large := filepath.Join(root, "large.json")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(browserJournalRecordMax + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	for _, path := range []string{large, root} {
		if _, err := readBrowserJournalRecord(path); !errors.Is(err, errBrowserJournalCorrupt) {
			t.Fatalf("journal path %q accepted: %v", path, err)
		}
	}
}

func TestBrowserIncompleteJournalCannotExpireUnknownRequestKey(t *testing.T) {
	e, root := testExecutor(t)
	dir := filepath.Join(root, "incomplete-browser")
	operations := filepath.Join(dir, "operations")
	if err := os.MkdirAll(operations, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(operations, "bop_aaaaaaaaaaaaaaaaaaaaaaaa.json")
	data := []byte(`{"operation_id":"bop_aaaaaaaaaaaaaaaaaaaaaaaa","key_hash":"known-key","hash":"intent","status":"running"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newBrowserManager(e, browserConfig{Enabled: true, Dir: dir}); !errors.Is(err, errBrowserJournalCorrupt) {
		t.Fatalf("journal without reservation time was treated as expired: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unknown request key evidence was deleted: %v", err)
	}
}
