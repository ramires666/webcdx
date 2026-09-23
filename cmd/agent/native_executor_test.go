package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeExecutorInitialize(t *testing.T) {
	exec := newNativeExecutor()
	req := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	resp, err := exec.call(context.Background(), req)
	if err != nil {
		t.Fatalf("call initialize failed: %v", err)
	}

	var res struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &res); err != nil {
		t.Fatalf("unmarshal initialize resp: %v", err)
	}
	if res.Result.ServerInfo.Name != "webcodex-direct" {
		t.Errorf("got server name %q, want 'webcodex-direct'", res.Result.ServerInfo.Name)
	}
}

func TestNativeExecutorToolsList(t *testing.T) {
	exec := newNativeExecutor()
	req := json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	resp, err := exec.call(context.Background(), req)
	if err != nil {
		t.Fatalf("call tools/list failed: %v", err)
	}

	var res struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &res); err != nil {
		t.Fatalf("unmarshal tools/list resp: %v", err)
	}

	toolNames := map[string]bool{}
	for _, tool := range res.Result.Tools {
		toolNames[tool.Name] = true
	}

	requiredTools := []string{
		"codex", "codex-reply",
		"exec_command", "shell_command",
		"read_file", "write_file",
		"list_dir", "apply_patch", "grep_search",
	}
	for _, reqName := range requiredTools {
		if !toolNames[reqName] {
			t.Errorf("expected tool %q not found in tools/list", reqName)
		}
	}
}

func TestNativeExecutorFileOperations(t *testing.T) {
	tmpDir := t.TempDir()
	exec := newNativeExecutor()
	testFile := filepath.Join(tmpDir, "sub", "test.txt")

	// 1. Write file
	writeReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      10,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "write_file",
			"arguments": map[string]any{
				"path":    testFile,
				"content": "Line 1\nLine 2\nLine 3\nHello WebCodex Direct!",
			},
		},
	}
	writeBytes, _ := json.Marshal(writeReq)
	resp, err := exec.call(context.Background(), writeBytes)
	if err != nil {
		t.Fatalf("write_file call failed: %v", err)
	}
	if !strings.Contains(string(resp), "Successfully wrote") {
		t.Fatalf("unexpected write_file output: %s", string(resp))
	}

	// 2. Read full file
	readReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      11,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": testFile,
			},
		},
	}
	readBytes, _ := json.Marshal(readReq)
	resp, err = exec.call(context.Background(), readBytes)
	if err != nil {
		t.Fatalf("read_file call failed: %v", err)
	}
	if !strings.Contains(string(resp), "Hello WebCodex Direct!") {
		t.Fatalf("unexpected read_file output: %s", string(resp))
	}

	// 3. Read slice (offset=2, limit=2)
	readSliceReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      12,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path":   testFile,
				"offset": 2,
				"limit":  2,
			},
		},
	}
	readSliceBytes, _ := json.Marshal(readSliceReq)
	resp, err = exec.call(context.Background(), readSliceBytes)
	if err != nil {
		t.Fatalf("read_file slice call failed: %v", err)
	}
	if !strings.Contains(string(resp), "Line 2") || !strings.Contains(string(resp), "Line 3") {
		t.Fatalf("unexpected read_file slice output: %s", string(resp))
	}

	// 4. List dir
	listReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      13,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "list_dir",
			"arguments": map[string]any{
				"path": filepath.Dir(testFile),
			},
		},
	}
	listBytes, _ := json.Marshal(listReq)
	resp, err = exec.call(context.Background(), listBytes)
	if err != nil {
		t.Fatalf("list_dir call failed: %v", err)
	}
	if !strings.Contains(string(resp), "test.txt") {
		t.Fatalf("unexpected list_dir output: %s", string(resp))
	}

	// 5. Grep search
	grepReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      14,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "grep_search",
			"arguments": map[string]any{
				"path":  tmpDir,
				"query": "WebCodex",
			},
		},
	}
	grepBytes, _ := json.Marshal(grepReq)
	resp, err = exec.call(context.Background(), grepBytes)
	if err != nil {
		t.Fatalf("grep_search call failed: %v", err)
	}
	if !strings.Contains(string(resp), "Hello WebCodex Direct!") {
		t.Fatalf("unexpected grep_search output: %s", string(resp))
	}
}

func TestNativeExecutorCodexWriteAndVerify(t *testing.T) {
	tmpDir := t.TempDir()
	exec := newNativeExecutor()

	// 1. ChatGPT instructs to create pacman.html
	chatGPTPrompt := `Create or overwrite the file pacman.html with the following content:

` + "```html" + `
<!DOCTYPE html>
<html>
<head><title>Pacman Test</title></head>
<body><h1>Pacman</h1></body>
</html>
` + "```"

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      50,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex",
			"arguments": map[string]any{
				"prompt":  chatGPTPrompt,
				"cwd":     tmpDir,
				"sandbox": "danger-full-access",
			},
		},
	}
	reqBytes, _ := json.Marshal(req)
	resp, err := exec.call(context.Background(), reqBytes)
	if err != nil {
		t.Fatalf("codex call failed: %v", err)
	}
	if !strings.Contains(string(resp), "Successfully created and wrote") {
		t.Fatalf("expected success in writing file, got: %s", string(resp))
	}

	// Verify file was written to disk
	targetFile := filepath.Join(tmpDir, "pacman.html")
	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("file was not written to disk: %v", err)
	}
	if !strings.Contains(string(data), "<title>Pacman Test</title>") {
		t.Fatalf("file content mismatch: %s", string(data))
	}

	// 2. ChatGPT checks if file exists
	checkPrompt := "Check whether " + targetFile + " exists"
	checkReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      51,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex",
			"arguments": map[string]any{
				"prompt": checkPrompt,
				"cwd":    tmpDir,
			},
		},
	}
	checkBytes, _ := json.Marshal(checkReq)
	resp, err = exec.call(context.Background(), checkBytes)
	if err != nil {
		t.Fatalf("check exists failed: %v", err)
	}
	if !strings.Contains(string(resp), "exists") || strings.Contains(string(resp), "does not exist") {
		t.Fatalf("expected file exists confirmation, got: %s", string(resp))
	}

	// 3. Read file through codex prompt
	readPrompt := "Read the file pacman.html"
	readReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      52,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex",
			"arguments": map[string]any{
				"prompt": readPrompt,
				"cwd":    tmpDir,
			},
		},
	}
	readBytes, _ := json.Marshal(readReq)
	resp, err = exec.call(context.Background(), readBytes)
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}
	if !strings.Contains(string(resp), "Pacman Test") {
		t.Fatalf("expected file contents, got: %s", string(resp))
	}
}

func TestNativeExecutorCodexRunCommand(t *testing.T) {
	exec := newNativeExecutor()
	prompt := "Run the following command:\n```powershell\necho \"pacman_command_success\"\n```"
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      60,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex",
			"arguments": map[string]any{
				"prompt": prompt,
			},
		},
	}
	reqBytes, _ := json.Marshal(req)
	resp, err := exec.call(context.Background(), reqBytes)
	if err != nil {
		t.Fatalf("codex run command failed: %v", err)
	}
	if !strings.Contains(string(resp), "pacman_command_success") {
		t.Fatalf("expected command output in response, got: %s", string(resp))
	}
}

func TestNativeExecutorExecCommand(t *testing.T) {
	exec := newNativeExecutor()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      20,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "exec_command",
			"arguments": map[string]any{
				"command": "echo test_output_123",
			},
		},
	}
	reqBytes, _ := json.Marshal(req)
	resp, err := exec.call(context.Background(), reqBytes)
	if err != nil {
		t.Fatalf("exec_command call failed: %v", err)
	}
	if !strings.Contains(string(resp), "test_output_123") {
		t.Fatalf("expected output 'test_output_123' in response, got: %s", string(resp))
	}
}

func TestNativeExecutorUnknownTool(t *testing.T) {
	exec := newNativeExecutor()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      40,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "non_existent_tool",
			"arguments": map[string]any{},
		},
	}
	reqBytes, _ := json.Marshal(req)
	resp, err := exec.call(context.Background(), reqBytes)
	if err != nil {
		t.Fatalf("unknown tool call failed: %v", err)
	}
	if !strings.Contains(string(resp), "unknown tool") {
		t.Fatalf("expected unknown tool error, got: %s", string(resp))
	}
}
