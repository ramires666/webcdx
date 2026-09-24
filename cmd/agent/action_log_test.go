package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFormatActionRequest(t *testing.T) {
	tests := []struct{ input, contains string }{
		{`{"jsonrpc":"2.0","id":1,"method":"initialize"}`, "Инициализация"},
		{`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"exec_command","arguments":{"command":"git status","cwd":"C:/repo"}}}`, "[exec_command] cwd: C:/repo"},
		{`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"C:/test/file.txt"}}}`, "[read_file] C:/test/file.txt"},
		{`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"poll_command","arguments":{"session_id":"proc_1"}}}`, "proc_1"},
	}
	for _, test := range tests {
		if got := formatActionRequest([]byte(test.input)); !strings.Contains(got, test.contains) {
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
	if got := formatActionResponse(response, nil, time.Millisecond); !strings.Contains(got, "Успешно") {
		t.Fatalf("success = %q", got)
	}
	if got := formatActionResponse(nil, errors.New("timeout"), time.Second); !strings.Contains(got, "Ошибка выполнения") {
		t.Fatalf("error = %q", got)
	}
	if got := compactString("абвгд", 3); got != "абв..." {
		t.Fatalf("unicode truncation = %q", got)
	}
}
