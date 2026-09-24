package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func callToolV4(t *testing.T, executor *nativeExecutor, name string, arguments map[string]any) map[string]any {
	t.Helper()
	result, callErr := callToolV4Result(t, executor, name, arguments)
	if callErr != nil {
		t.Fatalf("tool %s failed: %v", name, callErr)
	}
	return result
}

func callToolV4Result(t *testing.T, executor *nativeExecutor, name string, arguments map[string]any) (map[string]any, map[string]any) {
	t.Helper()
	request, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": arguments, contractMarker: "v4"},
	})
	response, err := executor.call(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Result struct {
			IsError    bool           `json:"isError"`
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatalf("decode response: %v: %s", err, response)
	}
	if decoded.Result.IsError {
		return nil, decoded.Result.Structured["error"].(map[string]any)
	}
	return decoded.Result.Structured, nil
}

func TestV4ToolContract(t *testing.T) {
	executor, _ := testExecutor(t)
	response, err := executor.call(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_webcodex_contract":"v4"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Result struct {
			Tools []mcpToolDefinition `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &listed); err != nil {
		t.Fatal(err)
	}
	want := []string{"read_file", "write_file", "list_directory", "search_files", "exec_command", "poll_command", "cancel_command", "edit_file", "find_files", "move_path", "delete_path"}
	if len(listed.Result.Tools) != len(want) {
		t.Fatalf("tool count = %d, want %d", len(listed.Result.Tools), len(want))
	}
	for index, tool := range listed.Result.Tools {
		if tool.Name != want[index] {
			t.Fatalf("tool %d = %q, want %q", index, tool.Name, want[index])
		}
		if tool.InputSchema["additionalProperties"] != false || tool.OutputSchema["additionalProperties"] != false {
			t.Fatalf("tool %s has open schema: input=%v output=%v", tool.Name, tool.InputSchema, tool.OutputSchema)
		}
		readOnly := tool.Name == "read_file" || tool.Name == "list_directory" || tool.Name == "search_files" || tool.Name == "poll_command" || tool.Name == "find_files"
		if tool.Annotations["readOnlyHint"] != readOnly {
			t.Fatalf("tool %s annotations: %v", tool.Name, tool.Annotations)
		}
	}
	if !strings.Contains(string(response), `"expected_sha256"`) || !strings.Contains(string(response), `"stdout_offset"`) {
		t.Fatalf("v4 schemas are missing strict fields: %s", response)
	}
}

func TestV4SafeWriteAndEdit(t *testing.T) {
	executor, root := testExecutor(t)
	path := filepath.Join(root, "юникод", "file.txt")
	written := callToolV4(t, executor, "write_file", map[string]any{
		"path": path, "content": "one\ntwo two\nthree\n", "if_absent": true,
	})
	if written["sha256"] == "" {
		t.Fatalf("write result: %v", written)
	}
	if _, callErr := callToolV4Result(t, executor, "write_file", map[string]any{"path": path, "content": "bad"}); callErr["code"] != "invalid_arguments" {
		t.Fatalf("missing precondition error: %v", callErr)
	}
	if _, callErr := callToolV4Result(t, executor, "write_file", map[string]any{"path": path, "content": "bad", "if_absent": true}); callErr["code"] != "already_exists" {
		t.Fatalf("if_absent overwrite error: %v", callErr)
	}
	read := callToolV4(t, executor, "read_file", map[string]any{"path": path, "offset": 2, "limit": 1})
	if read["content"] != "two two\n" || read["sha256"] != written["sha256"] || read["size"].(float64) != 18 {
		t.Fatalf("read result: %v", read)
	}
	before, _ := os.ReadFile(path)
	if _, callErr := callToolV4Result(t, executor, "edit_file", map[string]any{
		"path": path, "expected_sha256": read["sha256"], "edits": []any{
			map[string]any{"old_text": "one", "new_text": "ONE"},
			map[string]any{"old_text": "missing", "new_text": "x"},
		},
	}); callErr["code"] != "text_not_found" {
		t.Fatalf("atomic edit error: %v", callErr)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatalf("failed edit changed file: %q", after)
	}
	edited := callToolV4(t, executor, "edit_file", map[string]any{
		"path": path, "expected_sha256": read["sha256"], "edits": []any{
			map[string]any{"old_text": "two", "new_text": "два", "replace_all": true},
			map[string]any{"old_text": "three\n", "new_text": ""},
		},
	})
	if edited["replacements"].(float64) != 3 {
		t.Fatalf("edit result: %v", edited)
	}
	if _, callErr := callToolV4Result(t, executor, "edit_file", map[string]any{
		"path": path, "expected_sha256": read["sha256"], "edits": []any{map[string]any{"old_text": "ONE", "new_text": "bad"}},
	}); callErr["code"] != "stale_hash" {
		t.Fatalf("stale edit error: %v", callErr)
	}
	final, _ := os.ReadFile(path)
	if string(final) != "one\nдва два\n" {
		t.Fatalf("edited content = %q", final)
	}
}

func TestV4FindMoveAndDelete(t *testing.T) {
	executor, root := testExecutor(t)
	paths := []string{
		filepath.Join(root, "b", "z.go"), filepath.Join(root, "A", "a.go"), filepath.Join(root, "A", "note.txt"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	found := callToolV4(t, executor, "find_files", map[string]any{"path": root, "name_glob": "*.go", "kind": "file"})
	entries := found["entries"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["relative_path"] != "A/a.go" || entries[1].(map[string]any)["relative_path"] != "b/z.go" {
		t.Fatalf("find result: %v", found)
	}
	read := callToolV4(t, executor, "read_file", map[string]any{"path": paths[1]})
	moved := filepath.Join(root, "A", "moved.go")
	callToolV4(t, executor, "move_path", map[string]any{"source": paths[1], "destination": moved, "expected_sha256": read["sha256"]})
	if _, callErr := callToolV4Result(t, executor, "delete_path", map[string]any{"path": moved, "expected_sha256": strings.Repeat("0", 64)}); callErr["code"] != "stale_hash" {
		t.Fatalf("stale delete error: %v", callErr)
	}
	callToolV4(t, executor, "delete_path", map[string]any{"path": moved, "expected_sha256": read["sha256"]})
	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	callToolV4(t, executor, "delete_path", map[string]any{"path": empty})
	if _, callErr := callToolV4Result(t, executor, "delete_path", map[string]any{"path": filepath.Join(root, "A")}); callErr["code"] != "not_empty" {
		t.Fatalf("non-empty delete error: %v", callErr)
	}

	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err == nil {
		if _, callErr := callToolV4Result(t, executor, "find_files", map[string]any{"path": filepath.Join(link, "child")}); callErr["code"] != "outside_allowed_roots" {
			t.Fatalf("symlink escape error: %v", callErr)
		}
	}
}

func TestV4CommandStreamsAndPersistence(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	executor, err := newNativeExecutorWithConfig(executorConfig{
		AllowedRoots: []string{root}, LogDir: logDir, ProcessTTL: time.Hour, MaxLogBytes: 512, MaxResponseBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result := callToolV4(t, executor, "exec_command", map[string]any{
		"argv": []any{executable, "-test.run=TestV4CommandHelper", "--", "streams", "arg with spaces", "a&b"},
		"cwd":  root, "env": map[string]any{"GO_WANT_V4_HELPER": "1", "V4_HELPER_VALUE": "ENV"}, "stdin": "STDIN",
		"timeout_seconds": 10, "yield_time_ms": 5000,
	})
	if result["status"] != "exited" || !strings.Contains(result["stdout"].(string), "arg with spaces|a&b|STDIN|ENV") || !strings.Contains(result["stderr"].(string), "ERR") {
		t.Fatalf("argv command result: %v", result)
	}
	metadata, err := os.ReadFile(filepath.Join(logDir, result["session_id"].(string), "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"arg with spaces", "STDIN", "ENV", "V4_HELPER_VALUE"} {
		if strings.Contains(string(metadata), secret) {
			t.Fatalf("metadata contains command input %q: %s", secret, metadata)
		}
	}
	capped := callToolV4(t, executor, "exec_command", map[string]any{
		"argv": []any{executable, "-test.run=TestV4CommandHelper", "--", "flood"}, "cwd": root,
		"env": map[string]any{"GO_WANT_V4_HELPER": "1"}, "timeout_seconds": 10, "yield_time_ms": 5000,
	})
	stdoutInfo, _ := os.Stat(capped["stdout_log_path"].(string))
	stderrInfo, _ := os.Stat(capped["stderr_log_path"].(string))
	if stdoutInfo.Size()+stderrInfo.Size() > 512 || capped["stdout_truncated"] != true && capped["stderr_truncated"] != true {
		t.Fatalf("log cap result: %v sizes=%d", capped, stdoutInfo.Size()+stderrInfo.Size())
	}
	sessionID := result["session_id"].(string)
	executor.Close()
	restored, err := newNativeExecutorWithConfig(executorConfig{
		AllowedRoots: []string{root}, LogDir: logDir, ProcessTTL: time.Hour, MaxLogBytes: 512, MaxResponseBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	poll := callToolV4(t, restored, "poll_command", map[string]any{"session_id": sessionID, "stdout_offset": 0, "stderr_offset": 0})
	if poll["status"] != "exited" || !strings.Contains(poll["stdout"].(string), "STDIN") {
		t.Fatalf("restored result: %v", poll)
	}
}

func TestV4RunningSessionRestoresAndCancelsByIdentity(t *testing.T) {
	root := t.TempDir()
	config := executorConfig{
		AllowedRoots: []string{root}, LogDir: filepath.Join(root, "logs"), ProcessTTL: time.Hour,
		MaxLogBytes: 1 << 20, MaxResponseBytes: 1 << 20,
	}
	first, err := newNativeExecutorWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	running := callToolV4(t, first, "exec_command", map[string]any{
		"argv": []any{executable, "-test.run=TestV4CommandHelper", "--", "sleep"}, "cwd": root,
		"env": map[string]any{"GO_WANT_V4_HELPER": "1"}, "timeout_seconds": 30, "yield_time_ms": 50,
	})
	if running["status"] != "running" {
		t.Fatalf("long command: %v", running)
	}
	first.Close()
	restored, err := newNativeExecutorWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	cancelled := callToolV4(t, restored, "cancel_command", map[string]any{"session_id": running["session_id"]})
	if cancelled["status"] != "cancelled" || cancelled["already_finished"] != false {
		t.Fatalf("restored cancellation: %v", cancelled)
	}
}

func TestV4CommandHelper(t *testing.T) {
	if os.Getenv("GO_WANT_V4_HELPER") != "1" {
		return
	}
	separator := 0
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index + 1
			break
		}
	}
	mode := os.Args[separator]
	switch mode {
	case "streams":
		input, _ := io.ReadAll(os.Stdin)
		fmt.Fprintf(os.Stdout, "%s|%s|%s|%s", os.Args[separator+1], os.Args[separator+2], input, os.Getenv("V4_HELPER_VALUE"))
		fmt.Fprint(os.Stderr, "ERR")
	case "flood":
		fmt.Fprint(os.Stdout, strings.Repeat("O", 2048))
		fmt.Fprint(os.Stderr, strings.Repeat("E", 2048))
	case "sleep":
		fmt.Fprint(os.Stdout, "STARTED")
		time.Sleep(10 * time.Second)
	}
	os.Exit(0)
}
