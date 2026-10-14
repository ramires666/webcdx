package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"webcodex/internal/protocol"
)

func TestConsoleRequestSummaryAndDebug(t *testing.T) {
	var console bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&console)
	t.Cleanup(func() { log.SetOutput(previous) })
	executor, root := testExecutor(t)
	path := filepath.Join(root, "text.md")
	if err := os.WriteFile(path, []byte("LONG_PRIVATE_FILE_CONTENT"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	call := func(path string, debug bool) string {
		console.Reset()
		executor.verbose = debug
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "read_file", "arguments": map[string]any{"path": path}}})
		handleRequest(context.Background(), server.Client(), server.URL, "worker", executor, protocol.AgentRequest{ID: "debug_request_123", Request: body, Deadline: time.Now().Add(time.Minute)})
		return console.String()
	}
	text := call(path, false)
	if !strings.Contains(text, "Читаю файл "+path) || strings.Contains(text, "debug_request_123") || strings.Contains(text, "LONG_PRIVATE_FILE_CONTENT") || strings.Contains(text, "bytes=") {
		t.Fatalf("normal console too noisy: %s", text)
	}
	text = call(filepath.Join(root, "missing.md"), false)
	if !strings.Contains(text, "файл или папка не найдены") || !strings.Contains(text, "missing.md") {
		t.Fatalf("missing readable error: %s", text)
	}
	if text = call(path, true); !strings.Contains(text, "id=debug_request_123") || !strings.Contains(text, "bytes=") {
		t.Fatalf("debug details missing: %s", text)
	}
}
