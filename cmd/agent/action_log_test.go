package main

import (
	"errors"
	"strings"
	"testing"
)

func TestFormatActionRequest(t *testing.T) {
	tests := []struct{ input, contains string }{
		{`{"jsonrpc":"2.0","id":1,"method":"initialize"}`, ""},
		{`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"exec_command","arguments":{"command":"git status","cwd":"C:/repo"}}}`, "Запускаю команду в C:/repo"},
		{`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"C:/test/file.txt"}}}`, "Читаю файл C:/test/file.txt"},
		{`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"poll_command","arguments":{"session_id":"proc_1"}}}`, ""},
	}
	for _, test := range tests {
		if got := formatActionRequest([]byte(test.input)); got != test.contains {
			t.Errorf("formatActionRequest() = %q, want %q", got, test.contains)
		}
	}
	sensitive := formatActionRequest([]byte(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"exec_command","arguments":{"command":"SECRET_COMMAND","cwd":"C:/repo","stdin":"SECRET_STDIN","env":{"TOKEN":"SECRET_ENV"}}}}`))
	for _, secret := range []string{"SECRET_COMMAND", "SECRET_STDIN", "SECRET_ENV"} {
		if strings.Contains(sensitive, secret) {
			t.Fatalf("action log contains %q: %s", secret, sensitive)
		}
	}
}

func TestFormatActionResponseAndUnicodeTruncation(t *testing.T) {
	response := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}],"isError":false}}`)
	if got := formatActionResponse(response, nil); got != "" {
		t.Fatalf("success = %q", got)
	}
	if got := formatActionResponse(nil, errors.New("timeout")); !strings.Contains(got, "Ошибка") {
		t.Fatalf("error = %q", got)
	}
	if got := compactString("абвгд", 3); got != "абв..." {
		t.Fatalf("unicode truncation = %q", got)
	}
	for _, test := range []struct {
		status string
		code   any
		want   string
	}{
		{"running", nil, ""},
		{"exited", 0, ""},
		{"exited", 7, ""},
		{"failed", nil, ""},
		{"timed_out", nil, ""},
		{"cancelled", nil, ""},
	} {
		response := toolResult([]byte(`1`), map[string]any{"session_id": "proc_test", "status": test.status, "exit_code": test.code}, nil)
		got := formatActionResponse(response, nil)
		if got != test.want {
			t.Fatalf("status %s code %v: %s", test.status, test.code, got)
		}
	}
	response = toolResult([]byte(`1`), nil, toolErr("access_denied", "open file"))
	if got := formatActionResponse(response, nil); got != "Ошибка: доступ запрещён" {
		t.Fatalf("permission warning: %q", got)
	}
	if got := compactString("file\x1b[2J\u202e.txt", 100); strings.Contains(got, "\x1b") || strings.Contains(got, "\u202e") {
		t.Fatalf("terminal controls are unescaped: %q", got)
	}
}
