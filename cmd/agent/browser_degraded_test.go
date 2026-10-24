package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const corruptBrowserJournal = `{"id":"bop_aaaaaaaaaaaaaaaaaaaaaaaa","key_hash":"JOURNAL_PRIVATE_MARKER"` // Deliberately incomplete JSON.

func newCorruptJournalExecutor(t *testing.T) (*nativeExecutor, string, string, error) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "browser")
	operations := filepath.Join(dir, "operations")
	if err := os.MkdirAll(operations, 0o700); err != nil {
		t.Fatal(err)
	}
	corruptPath := filepath.Join(operations, "bop_aaaaaaaaaaaaaaaaaaaaaaaa.json")
	if err := os.WriteFile(corruptPath, []byte(corruptBrowserJournal), 0o600); err != nil {
		t.Fatal(err)
	}
	executor, err := newNativeExecutorWithConfig(executorConfig{
		AllowedRoots: []string{root}, LogDir: filepath.Join(root, "logs"), ProcessTTL: time.Hour,
		MaxLogBytes: 1 << 20, MaxResponseBytes: 1 << 20,
		Browser: browserConfig{Enabled: true, Dir: dir, Headless: true},
	})
	return executor, root, corruptPath, err
}

func assertJournalCorruptDegraded(t *testing.T, executor *nativeExecutor) {
	t.Helper()
	value, err := executor.browser.call(context.Background(), "browser_control", map[string]any{"action": "discover"})
	if err != nil {
		t.Fatalf("discover should report degraded health, got error: %v", err)
	}
	discovery, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("unexpected discover result %T: %#v", value, value)
	}
	if discovery["health"] != "degraded" || discovery["health_reason"] != "journal_corrupt" {
		t.Fatalf("expected degraded journal health, got %#v", discovery)
	}
	encoded, err := json.Marshal(discovery)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "JOURNAL_PRIVATE_MARKER") {
		t.Fatalf("discovery exposed corrupt journal contents: %s", encoded)
	}
}

func assertBrowserMutationRejectedBeforeReservation(t *testing.T, executor *nativeExecutor, corruptPath, requestKey string) {
	t.Helper()
	_, err := executor.browser.call(context.Background(), "browser_open", map[string]any{"mode": "clean", "request_key": requestKey})
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != "browser_unhealthy" {
		t.Fatalf("browser mutation should fail closed with browser_unhealthy, got %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(corruptPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(corruptPath) {
		t.Fatalf("rejected mutation wrote a journal reservation: entries=%v", entries)
	}
	data, err := os.ReadFile(corruptPath)
	if err != nil {
		t.Fatalf("corrupt journal evidence was removed: %v", err)
	}
	if string(data) != corruptBrowserJournal {
		t.Fatalf("corrupt journal evidence changed: %q", data)
	}
}

func TestBrowserCorruptJournalDegradesWithoutBlockingLocalCommands(t *testing.T) {
	executor, _, corruptPath, err := newCorruptJournalExecutor(t)
	if err != nil {
		t.Fatalf("corrupt operation journal must not prevent agent startup: %v", err)
	}
	t.Cleanup(executor.Close)

	read := callTool(t, executor, "read_file", map[string]any{"path": corruptPath})
	if read["content"] != corruptBrowserJournal {
		t.Fatalf("read_file did not return the corrupt journal evidence: %#v", read)
	}
	assertJournalCorruptDegraded(t, executor)
	assertBrowserMutationRejectedBeforeReservation(t, executor, corruptPath, "new-mutation-key")
}

func TestBrowserCorruptJournalDegradationSurvivesRestartAndRejectsRetries(t *testing.T) {
	executor, root, corruptPath, err := newCorruptJournalExecutor(t)
	if err != nil {
		t.Fatalf("first degraded startup failed: %v", err)
	}
	config := executor.browser.config
	executor.Close()

	restarted, err := newNativeExecutorWithConfig(executorConfig{
		AllowedRoots: []string{root}, LogDir: filepath.Join(root, "logs"), ProcessTTL: time.Hour,
		MaxLogBytes: 1 << 20, MaxResponseBytes: 1 << 20, Browser: config,
	})
	if err != nil {
		t.Fatalf("restart should remain available with a corrupt browser journal: %v", err)
	}
	t.Cleanup(restarted.Close)
	assertJournalCorruptDegraded(t, restarted)
	assertBrowserMutationRejectedBeforeReservation(t, restarted, corruptPath, "old-arbitrary-request-key")
}
