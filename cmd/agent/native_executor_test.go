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
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &res); err != nil {
		t.Fatalf("unmarshal tools/list resp: %v", err)
	}

	toolNames := map[string]bool{}
	for _, tool := range res.Result.Tools {
		toolNames[tool.Name] = true
		lowerDesc := strings.ToLower(tool.Description)
		if strings.Contains(lowerDesc, "codex") || strings.Contains(lowerDesc, "кодекс") {
			t.Errorf("tool %q description must not contain 'codex' or 'кодекс', got: %q", tool.Name, tool.Description)
		}
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
			"name":      "non_existent_tool",
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

func TestNativeExecutorCodexFolderListing(t *testing.T) {
	tmpDir := t.TempDir()
	exec := newNativeExecutor()

	// Create test files in tmpDir
	_ = os.WriteFile(filepath.Join(tmpDir, "strategy_test.py"), []byte("print('hello')"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "config.json"), []byte("{}"), 0644)

	// Test 1: "Re-read the folder" with cwd
	req1 := map[string]any{
		"jsonrpc": "2.0",
		"id":      70,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex-reply",
			"arguments": map[string]any{
				"prompt": "Re-read the folder",
				"cwd":    tmpDir,
			},
		},
	}
	req1Bytes, _ := json.Marshal(req1)
	resp1, err := exec.call(context.Background(), req1Bytes)
	if err != nil {
		t.Fatalf("re-read folder failed: %v", err)
	}
	if strings.Contains(string(resp1), "Ready for operations") {
		t.Fatalf("must never return dummy Ready for operations string!")
	}
	if !strings.Contains(string(resp1), "strategy_test.py") || !strings.Contains(string(resp1), "config.json") {
		t.Fatalf("expected file list in output, got: %s", string(resp1))
	}

	// Test 2: "Re-read the folder <path>"
	req2 := map[string]any{
		"jsonrpc": "2.0",
		"id":      71,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex",
			"arguments": map[string]any{
				"prompt": "Re-read the folder " + tmpDir,
			},
		},
	}
	req2Bytes, _ := json.Marshal(req2)
	resp2, err := exec.call(context.Background(), req2Bytes)
	if err != nil {
		t.Fatalf("re-read path failed: %v", err)
	}
	if !strings.Contains(string(resp2), "strategy_test.py") {
		t.Fatalf("expected file in path listing, got: %s", string(resp2))
	}

	// Test 3: Fallback on arbitrary prompt returns directory contents, NEVER "Ready for operations"
	req3 := map[string]any{
		"jsonrpc": "2.0",
		"id":      72,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex-reply",
			"arguments": map[string]any{
				"prompt": "Tell me what we have here",
				"cwd":    tmpDir,
			},
		},
	}
	req3Bytes, _ := json.Marshal(req3)
	resp3, err := exec.call(context.Background(), req3Bytes)
	if err != nil {
		t.Fatalf("arbitrary prompt failed: %v", err)
	}
	if strings.Contains(string(resp3), "Ready for operations") {
		t.Fatalf("must never return dummy Ready for operations string!")
	}
	if !strings.Contains(string(resp3), "strategy_test.py") {
		t.Fatalf("fallback must provide directory listing, got: %s", string(resp3))
	}
}

func TestNativeExecutorSnakeHtmlAndVariations(t *testing.T) {
	tmpDir := t.TempDir()
	exec := newNativeExecutor()

	// 1. Exact real-world ChatGPT prompt with "Use the filesystem only..." and 18KB content
	largeContent := "<!DOCTYPE html>\n<html><head><title>Snake Game</title></head>\n<body>\n" +
		strings.Repeat("<div>Snake Game Canvas Logic and Data</div>\n", 400) +
		"</body></html>"

	realWorldPrompt := "Use the filesystem only to inspect or create files in " + tmpDir + ".\n\n" +
		"Create or overwrite the file `snake.html` with the following content:\n\n```html\n" +
		largeContent + "\n```"

	req1 := map[string]any{
		"jsonrpc": "2.0",
		"id":      101,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex",
			"arguments": map[string]any{
				"prompt": realWorldPrompt,
				"cwd":    tmpDir,
			},
		},
	}
	req1Bytes, _ := json.Marshal(req1)
	resp1, err := exec.call(context.Background(), req1Bytes)
	if err != nil {
		t.Fatalf("real-world snake.html write failed: %v", err)
	}
	if !strings.Contains(string(resp1), "Successfully created and wrote") {
		t.Fatalf("expected write success, got: %s", string(resp1))
	}

	snakePath := filepath.Join(tmpDir, "snake.html")
	data, err := os.ReadFile(snakePath)
	if err != nil {
		t.Fatalf("snake.html not found on disk: %v", err)
	}
	if len(data) != len(largeContent) {
		t.Fatalf("expected %d bytes, got %d bytes", len(largeContent), len(data))
	}

	// 2. Prompt variation: "Target file: snake2.html\n```html\n..."
	promptVar2 := "Target file: snake2.html\n```html\n<h1>Snake 2</h1>\n```"
	req2 := map[string]any{
		"jsonrpc": "2.0",
		"id":      102,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex",
			"arguments": map[string]any{
				"prompt": promptVar2,
				"cwd":    tmpDir,
			},
		},
	}
	req2Bytes, _ := json.Marshal(req2)
	resp2, err := exec.call(context.Background(), req2Bytes)
	if err != nil {
		t.Fatalf("prompt variation 2 failed: %v", err)
	}
	if !strings.Contains(string(resp2), "Successfully created and wrote") {
		t.Fatalf("expected success for target file variation, got: %s", string(resp2))
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "snake2.html")); err != nil {
		t.Fatalf("snake2.html was not written to disk: %v", err)
	}

	// 3. Prompt variation: "Here is `game.js`:\n```javascript\nconsole.log('game');\n```"
	promptVar3 := "Here is `game.js`:\n```javascript\nconsole.log('game');\n```"
	req3 := map[string]any{
		"jsonrpc": "2.0",
		"id":      103,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex",
			"arguments": map[string]any{
				"prompt": promptVar3,
				"cwd":    tmpDir,
			},
		},
	}
	req3Bytes, _ := json.Marshal(req3)
	resp3, err := exec.call(context.Background(), req3Bytes)
	if err != nil {
		t.Fatalf("prompt variation 3 failed: %v", err)
	}
	if !strings.Contains(string(resp3), "Successfully created and wrote") {
		t.Fatalf("expected success for 'Here is' variation, got: %s", string(resp3))
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "game.js")); err != nil {
		t.Fatalf("game.js was not written to disk: %v", err)
	}

	// 4. Prompt variation: Russian instruction "Запиши в файл script.py:\n```python\nprint(42)\n```"
	promptVar4 := "Запиши в файл script.py:\n```python\nprint(42)\n```"
	req4 := map[string]any{
		"jsonrpc": "2.0",
		"id":      104,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex",
			"arguments": map[string]any{
				"prompt": promptVar4,
				"cwd":    tmpDir,
			},
		},
	}
	req4Bytes, _ := json.Marshal(req4)
	resp4, err := exec.call(context.Background(), req4Bytes)
	if err != nil {
		t.Fatalf("prompt variation 4 failed: %v", err)
	}
	if !strings.Contains(string(resp4), "Successfully created and wrote") {
		t.Fatalf("expected success for Russian variation, got: %s", string(resp4))
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "script.py")); err != nil {
		t.Fatalf("script.py was not written to disk: %v", err)
	}

	// 5. Multiple files in single prompt
	promptVar5 := "Create or overwrite `style.css`:\n```css\nbody { margin: 0; }\n```\n\nCreate or overwrite `app.js`:\n```js\nalert(1);\n```"
	req5 := map[string]any{
		"jsonrpc": "2.0",
		"id":      105,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex",
			"arguments": map[string]any{
				"prompt": promptVar5,
				"cwd":    tmpDir,
			},
		},
	}
	req5Bytes, _ := json.Marshal(req5)
	resp5, err := exec.call(context.Background(), req5Bytes)
	if err != nil {
		t.Fatalf("prompt variation 5 failed: %v", err)
	}
	if !strings.Contains(string(resp5), "Successfully created and wrote 2 file(s)") {
		t.Fatalf("expected 2 files written, got: %s", string(resp5))
	}

	// 6. Raw HTML without fences
	promptVar6 := "Save the file raw.html with:\n<!DOCTYPE html><html><body>Raw HTML</body></html>"
	req6 := map[string]any{
		"jsonrpc": "2.0",
		"id":      106,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex",
			"arguments": map[string]any{
				"prompt": promptVar6,
				"cwd":    tmpDir,
			},
		},
	}
	req6Bytes, _ := json.Marshal(req6)
	resp6, err := exec.call(context.Background(), req6Bytes)
	if err != nil {
		t.Fatalf("prompt variation 6 failed: %v", err)
	}
	if !strings.Contains(string(resp6), "Successfully created and wrote") {
		t.Fatalf("expected success for raw HTML variation, got: %s", string(resp6))
	}
}

func TestNativeExecutorCwdPreservationAndAliases(t *testing.T) {
	tmpDir := t.TempDir()
	exec := newNativeExecutor()

	// Step 1: Call codex with cwd set
	req1 := map[string]any{
		"jsonrpc": "2.0",
		"id":      201,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex",
			"arguments": map[string]any{
				"prompt": "list the files in the directory",
				"cwd":    tmpDir,
			},
		},
	}
	req1Bytes, _ := json.Marshal(req1)
	_, _ = exec.call(context.Background(), req1Bytes)

	// Step 2: Call write_file without cwd, using alias 'filePath' and 'code'
	req2 := map[string]any{
		"jsonrpc": "2.0",
		"id":      202,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "write_file",
			"arguments": map[string]any{
				"filePath": "alias_test.txt",
				"code":     "content from alias",
			},
		},
	}
	req2Bytes, _ := json.Marshal(req2)
	resp2, err := exec.call(context.Background(), req2Bytes)
	if err != nil {
		t.Fatalf("write_file with aliases failed: %v", err)
	}
	if !strings.Contains(string(resp2), "Successfully wrote") {
		t.Fatalf("expected success in write_file, got: %s", string(resp2))
	}

	// Verify it wrote into tmpDir because cwd was preserved
	expectedFile := filepath.Join(tmpDir, "alias_test.txt")
	content, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Fatalf("file was not written in preserved cwd (%s): %v", tmpDir, err)
	}
	if string(content) != "content from alias" {
		t.Fatalf("unexpected content: %s", string(content))
	}

	// Step 3: Call read_file using alias 'file' without cwd
	req3 := map[string]any{
		"jsonrpc": "2.0",
		"id":      203,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"file": "alias_test.txt",
			},
		},
	}
	req3Bytes, _ := json.Marshal(req3)
	resp3, err := exec.call(context.Background(), req3Bytes)
	if err != nil {
		t.Fatalf("read_file failed: %v", err)
	}
	if !strings.Contains(string(resp3), "content from alias") {
		t.Fatalf("expected content from alias in read_file, got: %s", string(resp3))
	}
}

func TestNativeExecutorFolderListingSafetyWithCodeBlocks(t *testing.T) {
	// Ensure that prompts containing code blocks are NEVER treated as folder listings,
	// even if they mention words like "project", "files", "tree", "list".
	promptWithCode := "Here is the project tree viewer script for our files:\n```html\n<div>Tree</div>\n```"
	if isFolderListingRequest(promptWithCode) {
		t.Errorf("prompt containing code block must never return true for isFolderListingRequest!")
	}

	promptWithWrite := "Write the file project_files_list.txt with content: hello"
	if isFolderListingRequest(promptWithWrite) {
		t.Errorf("prompt containing 'write' must never return true for isFolderListingRequest!")
	}
}

