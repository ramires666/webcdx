package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testExecutor(t *testing.T) (*nativeExecutor, string) {
	t.Helper()
	root := t.TempDir()
	executor, err := newNativeExecutorWithConfig(executorConfig{
		AllowedRoots: []string{root}, LogDir: filepath.Join(root, "logs"), ProcessTTL: time.Hour,
		MaxLogBytes: 1 << 20, MaxResponseBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(executor.Close)
	return executor, root
}

func callTool(t *testing.T, executor *nativeExecutor, name string, arguments map[string]any) map[string]any {
	t.Helper()
	request, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": arguments},
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
		t.Fatalf("tool %s failed: %v", name, decoded.Result.Structured)
	}
	return decoded.Result.Structured
}

func TestToolContractAndForbiddenTerms(t *testing.T) {
	executor, _ := testExecutor(t)
	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}
	var toolsResponse []byte
	for _, request := range requests {
		response, err := executor.call(context.Background(), json.RawMessage(request))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(response))
		for _, forbidden := range []string{"codex", "model", "reasoning", "thinking", "threadid", `"prompt"`} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("response contains %q: %s", forbidden, response)
			}
		}
		toolsResponse = response
	}
	var listed struct {
		Result struct {
			Tools []mcpToolDefinition `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(toolsResponse, &listed); err != nil {
		t.Fatal(err)
	}
	want := []string{"read_file", "write_file", "list_directory", "search_files", "exec_command", "poll_command", "cancel_command"}
	if len(listed.Result.Tools) != len(want) {
		t.Fatalf("tool count = %d, want %d", len(listed.Result.Tools), len(want))
	}
	for index, tool := range listed.Result.Tools {
		if tool.Name != want[index] || tool.OutputSchema == nil || tool.Annotations == nil {
			t.Fatalf("tool %d = %#v", index, tool)
		}
	}
}

func TestUnknownLegacyToolHasNoSideEffects(t *testing.T) {
	executor, root := testExecutor(t)
	request, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "codex", "arguments": map[string]any{"prompt": "write a file", "cwd": root}},
	})
	response, err := executor.call(context.Background(), request)
	if err != nil || !strings.Contains(string(response), "unknown tool") {
		t.Fatalf("response = %s, err = %v", response, err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 || entries[0].Name() != "logs" {
		t.Fatalf("unexpected side effects: %v", entries)
	}
}

func TestFileTools(t *testing.T) {
	executor, root := testExecutor(t)
	emptyDir := filepath.Join(root, "empty")
	_ = os.Mkdir(emptyDir, 0o700)
	emptyListing := callTool(t, executor, "list_directory", map[string]any{"path": emptyDir})
	if len(emptyListing["entries"].([]any)) != 0 {
		t.Fatalf("empty listing: %v", emptyListing)
	}
	path := filepath.Join(root, "юникод", "file.txt")
	written := callTool(t, executor, "write_file", map[string]any{"path": path, "content": "one\nдва\nthree\n"})
	if written["bytes_written"].(float64) != float64(len([]byte("one\nдва\nthree\n"))) {
		t.Fatalf("write result: %v", written)
	}
	read := callTool(t, executor, "read_file", map[string]any{"path": path, "offset": 2, "limit": 1})
	if read["content"] != "два\n" {
		t.Fatalf("range content = %q", read["content"])
	}
	callTool(t, executor, "write_file", map[string]any{"path": path, "content": ""})
	read = callTool(t, executor, "read_file", map[string]any{"path": path})
	if read["content"] != "" {
		t.Fatalf("empty content = %q", read["content"])
	}
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "binary.txt"), []byte{'a', 0, 'b'}, 0o600)
	_ = os.Mkdir(filepath.Join(root, "folder"), 0o700)
	listing := callTool(t, executor, "list_directory", map[string]any{"path": root, "max_entries": 2})
	if listing["truncated"] != true {
		t.Fatalf("listing should be truncated: %v", listing)
	}
	search := callTool(t, executor, "search_files", map[string]any{"path": root, "pattern": "needle", "glob": "*.txt", "max_results": 1})
	if len(search["matches"].([]any)) != 1 {
		t.Fatalf("search result: %v", search)
	}
}

func TestBinaryAndAllowedRoots(t *testing.T) {
	executor, root := testExecutor(t)
	binary := filepath.Join(root, "binary.bin")
	_ = os.WriteFile(binary, []byte{0, 1, 2}, 0o600)
	if _, err := executor.readFile(map[string]any{"path": binary}); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary error = %v", err)
	}
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if _, err := executor.writeFile(map[string]any{"path": outside, "content": "no"}); err == nil {
		t.Fatal("outside path was accepted")
	}
	if _, err := executor.readFile(map[string]any{"path": "relative.txt"}); err == nil {
		t.Fatal("relative path was accepted")
	}
}

func TestCommandsExitTimeoutPollAndCancel(t *testing.T) {
	executor, root := testExecutor(t)
	success := callTool(t, executor, "exec_command", map[string]any{
		"command": outputCommand("TEST"), "cwd": root, "timeout_seconds": 5, "yield_time_ms": 3000,
	})
	if success["status"] != "exited" || success["exit_code"].(float64) != 0 || !strings.Contains(success["output"].(string), "TEST") {
		t.Fatalf("success result: %v", success)
	}
	failure := callTool(t, executor, "exec_command", map[string]any{
		"command": exitCommand(7), "cwd": root, "timeout_seconds": 5, "yield_time_ms": 3000,
	})
	if failure["exit_code"].(float64) != 7 {
		t.Fatalf("failure result: %v", failure)
	}
	timed := callTool(t, executor, "exec_command", map[string]any{
		"command": outputThenSleepCommand("PARTIAL", 5), "cwd": root, "timeout_seconds": 1, "yield_time_ms": 2500,
	})
	if timed["status"] != "timed_out" || !strings.Contains(timed["output"].(string), "PARTIAL") {
		t.Fatalf("timeout result: %v", timed)
	}
	if _, err := os.Stat(timed["log_path"].(string)); err != nil {
		t.Fatalf("timeout log missing: %v", err)
	}
	running := callTool(t, executor, "exec_command", map[string]any{
		"command": outputThenSleepCommand("FIRST", 10), "cwd": root, "timeout_seconds": 30, "yield_time_ms": 100,
	})
	if running["status"] != "running" {
		t.Fatalf("long command result: %v", running)
	}
	id := running["session_id"].(string)
	var poll map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		poll = callTool(t, executor, "poll_command", map[string]any{"session_id": id, "offset": 0, "max_bytes": 1024})
		if strings.Contains(poll["output"].(string), "FIRST") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !strings.Contains(poll["output"].(string), "FIRST") || poll["next_offset"].(float64) == 0 {
		t.Fatalf("poll result: %v", poll)
	}
	cancelled := callTool(t, executor, "cancel_command", map[string]any{"session_id": id})
	if cancelled["status"] != "cancelled" {
		t.Fatalf("cancel result: %v", cancelled)
	}
	secondCancel := callTool(t, executor, "cancel_command", map[string]any{"session_id": id})
	if secondCancel["status"] != "cancelled" {
		t.Fatalf("second cancel result: %v", secondCancel)
	}
}

func TestParallelCommandDirectoriesAreIndependent(t *testing.T) {
	executor, root := testExecutor(t)
	dirs := []string{filepath.Join(root, "one"), filepath.Join(root, "two")}
	for _, dir := range dirs {
		_ = os.Mkdir(dir, 0o700)
	}
	results := make([]map[string]any, len(dirs))
	var wait sync.WaitGroup
	for index, dir := range dirs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index] = callTool(t, executor, "exec_command", map[string]any{
				"command": workingDirectoryCommand(), "cwd": dir, "timeout_seconds": 5, "yield_time_ms": 3000,
			})
		}()
	}
	wait.Wait()
	for index, result := range results {
		if !strings.Contains(strings.ToLower(result["output"].(string)), strings.ToLower(dirs[index])) {
			t.Fatalf("cwd %s result: %v", dirs[index], result)
		}
	}
}

func TestCancelTerminatesChildProcess(t *testing.T) {
	executor, root := testExecutor(t)
	marker := filepath.Join(root, "child-must-not-write.txt")
	running := callTool(t, executor, "exec_command", map[string]any{
		"command": childWriteCommand(marker), "cwd": root, "timeout_seconds": 30, "yield_time_ms": 300,
	})
	if running["status"] != "running" {
		t.Fatalf("child command result: %v", running)
	}
	callTool(t, executor, "cancel_command", map[string]any{"session_id": running["session_id"]})
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("child process survived cancellation: %v", err)
	}
}

func outputCommand(text string) string {
	if runtime.GOOS == "windows" {
		return "Write-Output '" + text + "'"
	}
	return "printf '%s\\n' '" + text + "'"
}

func exitCommand(code int) string {
	return "exit " + strconv.Itoa(code)
}

func outputThenSleepCommand(text string, seconds int) string {
	if runtime.GOOS == "windows" {
		return "Write-Output '" + text + "'; Start-Sleep -Seconds " + strconv.Itoa(seconds)
	}
	return "printf '%s\\n' '" + text + "'; sleep " + strconv.Itoa(seconds)
}

func workingDirectoryCommand() string {
	if runtime.GOOS == "windows" {
		return "(Get-Location).Path"
	}
	return "pwd"
}

func childWriteCommand(path string) string {
	if runtime.GOOS == "windows" {
		escaped := strings.ReplaceAll(path, "'", "''")
		return "Start-Job { Start-Sleep -Seconds 2; Set-Content -LiteralPath '" + escaped + "' -Value child }; Start-Sleep -Seconds 30"
	}
	return "(sleep 2; printf child > '" + strings.ReplaceAll(path, "'", "'\\''") + "') & sleep 30"
}
