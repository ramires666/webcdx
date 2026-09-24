package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRealGateAndLocalExecutorEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the real local agent")
	}
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	root := t.TempDir()
	logs := filepath.Join(root, "logs")
	agentToken := "wc_agent_real_e2e"
	mcpToken := "wc_mcp_real_e2e"
	if err := srv.store.CreateAgent(context.Background(), Agent{
		ID: "real-e2e", Name: "Real E2E", Enabled: true, AgentTokenHash: hashSecret(agentToken), OAuthClientID: "real-e2e-client",
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateAccessToken(context.Background(), AccessToken{
		TokenHash: hashSecret(mcpToken), AgentID: "real-e2e", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.routes())
	defer httpServer.Close()
	srv.publicURL = httpServer.URL

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goBinary += ".exe"
	}
	_, sourceFile, _, _ := runtime.Caller(0)
	packageDir := filepath.Dir(sourceFile)
	agentBinary := filepath.Join(root, "webcodex-agent")
	if runtime.GOOS == "windows" {
		agentBinary += ".exe"
	}
	build := exec.Command(goBinary, "build", "-o", agentBinary, "../agent")
	build.Dir = packageDir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real agent: %v\n%s", err, output)
	}
	command := exec.CommandContext(ctx, agentBinary)
	command.Dir = packageDir
	command.Env = append(os.Environ(),
		"WEBCODEX_GATE_URL="+httpServer.URL,
		"WEBCODEX_AGENT_TOKEN="+agentToken,
		"WEBCODEX_ALLOWED_ROOTS="+root,
		"WEBCODEX_LOG_DIR="+logs,
	)
	agentLogPath := filepath.Join(root, "agent-e2e.log")
	agentLog, err := os.Create(agentLogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer agentLog.Close()
	command.Stdout, command.Stderr = agentLog, agentLog
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Logf("agent did not stop; log: %s", agentLogPath)
		}
	}()

	runtimeState := srv.runtimeFor("real-e2e")
	connectDeadline := time.Now().Add(20 * time.Second)
	for !runtimeState.isOnline() && time.Now().Before(connectDeadline) {
		select {
		case err := <-done:
			data, _ := os.ReadFile(agentLogPath)
			t.Fatalf("agent stopped before connecting: %v\n%s", err, data)
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !runtimeState.isOnline() {
		data, _ := os.ReadFile(agentLogPath)
		t.Fatalf("agent did not connect\n%s", data)
	}

	initialize := postMCP(t, httpServer.URL, mcpToken, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	serverInfo := initialize["result"].(map[string]any)["serverInfo"].(map[string]any)
	if serverInfo["name"] != "local-workspace" {
		t.Fatalf("server info: %v", serverInfo)
	}
	listed := postMCP(t, httpServer.URL, mcpToken, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	tools := listed["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 7 {
		t.Fatalf("tools/list count = %d", len(tools))
	}

	filePath := filepath.Join(root, "data", "hello.txt")
	invokeTool(t, httpServer.URL, mcpToken, "write_file", map[string]any{"path": filePath, "content": "hello e2e"})
	read := invokeTool(t, httpServer.URL, mcpToken, "read_file", map[string]any{"path": filePath})
	if read["content"] != "hello e2e" {
		t.Fatalf("read content = %q", read["content"])
	}

	short := invokeTool(t, httpServer.URL, mcpToken, "exec_command", map[string]any{
		"command": e2eOutputCommand("TEST"), "cwd": root, "timeout_seconds": 5, "yield_time_ms": 3000,
	})
	if short["exit_code"].(float64) != 0 || !strings.Contains(short["output"].(string), "TEST") {
		t.Fatalf("short command: %v", short)
	}
	long := invokeTool(t, httpServer.URL, mcpToken, "exec_command", map[string]any{
		"command": e2eOutputThenSleepCommand(), "cwd": root, "timeout_seconds": 10, "yield_time_ms": 10,
	})
	sessionID := long["session_id"].(string)
	offset := 0
	var output strings.Builder
	completed := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		poll := invokeTool(t, httpServer.URL, mcpToken, "poll_command", map[string]any{
			"session_id": sessionID, "offset": offset, "max_bytes": 1024,
		})
		output.WriteString(poll["output"].(string))
		offset = int(poll["next_offset"].(float64))
		if poll["status"] != "running" {
			if poll["exit_code"].(float64) != 0 || !strings.Contains(output.String(), "LONG") || !strings.Contains(output.String(), "DONE") {
				t.Fatalf("long command: %v, output=%q", poll, output.String())
			}
			if _, err := os.Stat(poll["log_path"].(string)); err != nil {
				t.Fatalf("long command log: %v", err)
			}
			completed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !completed {
		t.Fatalf("long command did not finish; output=%q", output.String())
	}

	runtimeState.disconnect()
	reconnectDeadline := time.Now().Add(10 * time.Second)
	for !runtimeState.isOnline() && time.Now().Before(reconnectDeadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !runtimeState.isOnline() {
		t.Fatal("agent did not reconnect after stream replacement")
	}
	readAfterReconnect := invokeTool(t, httpServer.URL, mcpToken, "read_file", map[string]any{"path": filePath})
	if readAfterReconnect["content"] != "hello e2e" {
		t.Fatalf("read after reconnect = %q", readAfterReconnect["content"])
	}

	marker := filepath.Join(root, "must-not-exist.txt")
	if err := srv.store.UpdateToolPolicy(context.Background(), "real-e2e", "", "exec_command"); err != nil {
		t.Fatal(err)
	}
	denied := postMCP(t, httpServer.URL, mcpToken, map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "tools/call",
		"params": map[string]any{"name": "exec_command", "arguments": map[string]any{
			"command": e2eCreateFileCommand(marker), "cwd": root, "timeout_seconds": 5, "yield_time_ms": 3000,
		}},
	})
	if denied["result"].(map[string]any)["isError"] != true {
		t.Fatalf("denied response: %v", denied)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("denied command reached local executor: %v", err)
	}
}

func postMCP(t *testing.T, endpoint, token string, payload map[string]any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(payload)
	request, err := http.NewRequest(http.MethodPost, endpoint+"/mcp/v3", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode MCP response (status %d): %v", response.StatusCode, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("MCP status %d: %v", response.StatusCode, decoded)
	}
	return decoded
}

func invokeTool(t *testing.T, endpoint, token, name string, arguments map[string]any) map[string]any {
	t.Helper()
	response := postMCP(t, endpoint, token, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments},
	})
	result := response["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("tool %s failed: %v", name, result)
	}
	return result["structuredContent"].(map[string]any)
}

func e2eOutputCommand(text string) string {
	if runtime.GOOS == "windows" {
		return "Write-Output '" + text + "'"
	}
	return "printf '%s\\n' '" + text + "'"
}

func e2eOutputThenSleepCommand() string {
	if runtime.GOOS == "windows" {
		return "Write-Output 'LONG'; Start-Sleep -Milliseconds 300; Write-Output 'DONE'"
	}
	return "printf 'LONG\\n'; sleep 0.3; printf 'DONE\\n'"
}

func e2eCreateFileCommand(path string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("Set-Content -LiteralPath %q -Value bad", path)
	}
	return fmt.Sprintf("printf bad > %q", path)
}
