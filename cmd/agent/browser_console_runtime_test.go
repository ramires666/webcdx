package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"webcodex/internal/protocol"
)

func captureBrowserConsole(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &output
}

func browserConsoleRPC(name string, args map[string]any) []byte {
	request, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"_webcodex_contract": "v5", "name": name, "arguments": args},
	})
	return request
}

func callBrowserConsoleGate(t *testing.T, executor *nativeExecutor, endpoint string, name string, args map[string]any) {
	t.Helper()
	handleRequest(context.Background(), http.DefaultClient, endpoint, "worker", executor, protocol.AgentRequest{
		ID: "console-test-request", Request: browserConsoleRPC(name, args), Deadline: time.Now().Add(15 * time.Second),
	})
}

func TestBrowserConsoleHandleRequestLogsOneReadableFailure(t *testing.T) {
	console := captureBrowserConsole(t)
	executor, _ := testExecutor(t)
	executor.browser.config.Enabled = true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	callBrowserConsoleGate(t, executor, server.URL, "browser_snapshot", map[string]any{"browser_session_id": "missing-session"})
	lines := console.String()
	if !strings.Contains(lines, "браузерная сессия не найдена") || strings.Count(lines, "⚠") != 1 {
		t.Fatalf("unknown session should have one readable warning: %s", lines)
	}
	if strings.Count(lines, "▶") != 1 || strings.Contains(lines, "unknown_session") || strings.Contains(lines, "Ошибка:") {
		t.Fatalf("generic or duplicate browser log leaked through: %s", lines)
	}
}

func TestBrowserConsoleHandleRequestLogsOneSuccessfulControl(t *testing.T) {
	console := captureBrowserConsole(t)
	executor, _ := testExecutor(t)
	executor.browser.config.Enabled = true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	callBrowserConsoleGate(t, executor, server.URL, "browser_control", map[string]any{"action": "discover"})
	lines := console.String()
	if strings.Count(lines, "\u2713") != 1 || strings.Contains(lines, "\u26a0") {
		t.Fatalf("successful native browser control should log one result without warning: %s", lines)
	}
	if strings.Contains(lines, "unknown_session") || strings.Contains(lines, "console-test-request") {
		t.Fatalf("browser result included hidden request details: %s", lines)
	}
}

func TestBrowserConsoleSnapshotGateAndAsyncRetryLifecycle(t *testing.T) {
	var submissions atomic.Int32
	console := captureBrowserConsole(t)
	f := newBrowserFixtureHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submissions.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>Console\u202eTitle\x1b[31m</title><main>fixture ready VISIBLE_PAGE_SNIPPET</main><input id='draft'><span id='count'>0</span><button id='save' onclick=\"fetch('/submit',{method:'POST'}).then(()=>setTimeout(()=>document.querySelector('#count').textContent='1',1700))\">Save</button>"))
	}))
	console.Reset()
	executor := f.b.executor
	executor.browser = f.b
	args := map[string]any{"browser_session_id": f.session, "tab_id": f.tab}
	ackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer ackServer.Close()
	callBrowserConsoleGate(t, executor, ackServer.URL, "browser_snapshot", args)
	first := console.String()
	for _, want := range []string{"VISIBLE_PAGE_SNIPPET", "Console", "http://127.0.0.1"} {
		if !strings.Contains(first, want) {
			t.Errorf("snapshot log missing %q: %s", want, first)
		}
	}
	if strings.Count(first, "\u2713") != 1 || strings.Contains(first, "\u26a0") {
		t.Fatalf("successful native browser request should log once without warning: %s", first)
	}
	if strings.ContainsRune(first, '\x1b') || strings.ContainsRune(first, '\u202e') {
		t.Fatalf("terminal formatting control was not escaped: %q", first)
	}
	console.Reset()

	snapshot, err := f.b.call(f.ctx, "browser_snapshot", args)
	if err != nil {
		t.Fatal(err)
	}
	pageURL := snapshot.(map[string]any)["url"].(string)
	navigateArgs := map[string]any{"browser_session_id": f.session, "tab_id": f.tab, "action": "url", "url": pageURL + "?token=URL_QUERY_SECRET", "request_key": "console-nav-secret"}
	if _, err := f.b.call(f.ctx, "browser_navigate", navigateArgs); err != nil {
		t.Fatal(err)
	}
	fillArgs := map[string]any{"browser_session_id": f.session, "tab_id": f.tab, "action": "fill", "selector": "#draft", "text": "SENSITIVE_FILL_TEXT", "value": "SENSITIVE_VALUE", "request_key": "console-fill-secret"}
	if _, err := f.b.call(f.ctx, "browser_interact", fillArgs); err != nil {
		t.Fatal(err)
	}
	secretLogs := console.String()
	for _, secret := range []string{"URL_QUERY_SECRET", "SENSITIVE_FILL_TEXT", "SENSITIVE_VALUE"} {
		if strings.Contains(secretLogs, secret) {
			t.Errorf("browser console exposed input secret %q: %s", secret, secretLogs)
		}
	}
	console.Reset()

	mutation := map[string]any{"browser_session_id": f.session, "tab_id": f.tab, "action": "click", "selector": "#save", "verify_selector": "#count", "verify_text": "1", "verify_timeout_seconds": float64(5), "request_key": "save-exactly-once"}
	value, err := f.b.call(f.ctx, "browser_interact", mutation)
	if err != nil {
		t.Fatal(err)
	}
	running, _ := value.(map[string]any)
	if running["status"] != "running" {
		t.Fatalf("delayed verification should return running first, got %#v", value)
	}
	pendingLogs := console.String()
	if !strings.Contains(pendingLogs, "\u23f3") || strings.Contains(pendingLogs, "\u2713") || strings.Contains(pendingLogs, "\u26a0") {
		t.Fatalf("running RPC should log a pending state before completion: %s", pendingLogs)
	}
	opID, _ := running["operation_id"].(string)
	f.b.mu.Lock()
	op := f.b.operations[opID]
	f.b.mu.Unlock()
	if op == nil {
		t.Fatalf("operation %q not found", opID)
	}
	select {
	case <-op.done:
	case <-time.After(8 * time.Second):
		t.Fatal("asynchronous browser operation did not finish")
	}
	completedLogs := console.String()
	if !strings.Contains(completedLogs, "✓") || strings.Contains(completedLogs, "⚠") {
		t.Fatalf("successful async completion was not logged after the running response: %s", completedLogs)
	}
	if submissions.Load() != 1 {
		t.Fatalf("expected one submitted action, got %d", submissions.Load())
	}
	if _, err := f.b.call(f.ctx, "browser_interact", mutation); err != nil {
		t.Fatal(err)
	}
	if submissions.Load() != 1 {
		t.Fatalf("retry repeated the page mutation; submissions=%d", submissions.Load())
	}
}
