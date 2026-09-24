package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	agentLogPath := filepath.Join(root, "agent-e2e.log")
	agentLog, err := os.Create(agentLogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer agentLog.Close()
	runtimeState := srv.runtimeFor("real-e2e")
	startAgent := func() (context.CancelFunc, <-chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		command := exec.CommandContext(ctx, agentBinary)
		command.Dir = packageDir
		command.Env = append(os.Environ(),
			"WEBCODEX_GATE_URL="+httpServer.URL,
			"WEBCODEX_AGENT_TOKEN="+agentToken,
			"WEBCODEX_ALLOWED_ROOTS="+root,
			"WEBCODEX_LOG_DIR="+logs,
		)
		command.Stdout, command.Stderr = agentLog, agentLog
		if err := command.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		deadline := time.Now().Add(20 * time.Second)
		for !runtimeState.isOnline() && time.Now().Before(deadline) {
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
		return cancel, done
	}
	stopAgent := func(cancel context.CancelFunc, done <-chan error) {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("agent did not stop; log: %s", agentLogPath)
		}
		for deadline := time.Now().Add(5 * time.Second); runtimeState.isOnline() && time.Now().Before(deadline); {
			time.Sleep(25 * time.Millisecond)
		}
	}
	cancelAgent, agentDone := startAgent()
	defer func() {
		if cancelAgent != nil {
			stopAgent(cancelAgent, agentDone)
		}
	}()

	initialize := postMCP(t, httpServer.URL, mcpToken, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	serverInfo := initialize["result"].(map[string]any)["serverInfo"].(map[string]any)
	if serverInfo["name"] != "local-workspace" {
		t.Fatalf("server info: %v", serverInfo)
	}
	listed := postMCP(t, httpServer.URL, mcpToken, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	tools := listed["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 11 {
		t.Fatalf("tools/list count = %d", len(tools))
	}

	filePath := filepath.Join(root, "data", "hello.txt")
	invokeTool(t, httpServer.URL, mcpToken, "write_file", map[string]any{"path": filePath, "content": "hello e2e", "if_absent": true})
	read := invokeTool(t, httpServer.URL, mcpToken, "read_file", map[string]any{"path": filePath})
	if read["content"] != "hello e2e" || read["sha256"] == "" {
		t.Fatalf("read result: %v", read)
	}
	edited := invokeTool(t, httpServer.URL, mcpToken, "edit_file", map[string]any{
		"path": filePath, "expected_sha256": read["sha256"],
		"edits": []any{map[string]any{"old_text": "hello", "new_text": "edited"}},
	})
	stale := invokeToolError(t, httpServer.URL, mcpToken, "edit_file", map[string]any{
		"path": filePath, "expected_sha256": read["sha256"],
		"edits": []any{map[string]any{"old_text": "edited", "new_text": "bad"}},
	})
	if stale["code"] != "stale_hash" {
		t.Fatalf("stale edit: %v", stale)
	}
	found := invokeTool(t, httpServer.URL, mcpToken, "find_files", map[string]any{"path": root, "name_glob": "*.txt"})
	if len(found["entries"].([]any)) != 1 {
		t.Fatalf("find result: %v", found)
	}
	movedPath := filepath.Join(root, "data", "moved.txt")
	invokeTool(t, httpServer.URL, mcpToken, "move_path", map[string]any{
		"source": filePath, "destination": movedPath, "expected_sha256": edited["new_sha256"],
	})

	short := invokeTool(t, httpServer.URL, mcpToken, "exec_command", map[string]any{
		"argv": e2eArgvCommand("TEST", "ERR"), "cwd": root, "timeout_seconds": 5, "yield_time_ms": 3000,
	})
	if short["exit_code"].(float64) != 0 || !strings.Contains(short["stdout"].(string), "TEST") || !strings.Contains(short["stderr"].(string), "ERR") {
		t.Fatalf("short command: %v", short)
	}
	long := invokeTool(t, httpServer.URL, mcpToken, "exec_command", map[string]any{
		"argv": e2eLongArgvCommand(), "cwd": root, "timeout_seconds": 10, "yield_time_ms": 10,
	})
	sessionID := long["session_id"].(string)
	stdoutOffset, stderrOffset := 0, 0
	var stdout, stderr strings.Builder
	completed := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		poll := invokeTool(t, httpServer.URL, mcpToken, "poll_command", map[string]any{
			"session_id": sessionID, "stdout_offset": stdoutOffset, "stderr_offset": stderrOffset, "max_bytes": 1024,
		})
		stdout.WriteString(poll["stdout"].(string))
		stderr.WriteString(poll["stderr"].(string))
		stdoutOffset = int(poll["stdout_next_offset"].(float64))
		stderrOffset = int(poll["stderr_next_offset"].(float64))
		if poll["status"] != "running" {
			if poll["exit_code"].(float64) != 0 || !strings.Contains(stdout.String(), "LONG") || !strings.Contains(stderr.String(), "DONE") {
				t.Fatalf("long command: %v stdout=%q stderr=%q", poll, stdout.String(), stderr.String())
			}
			for _, field := range []string{"stdout_log_path", "stderr_log_path"} {
				if _, err := os.Stat(poll[field].(string)); err != nil {
					t.Fatalf("long command %s: %v", field, err)
				}
			}
			completed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !completed {
		t.Fatalf("long command did not finish; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	runtimeState.disconnect()
	for deadline := time.Now().Add(10 * time.Second); !runtimeState.isOnline() && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	if !runtimeState.isOnline() {
		t.Fatal("agent did not reconnect after stream replacement")
	}

	stopAgent(cancelAgent, agentDone)
	cancelAgent = nil
	cancelAgent, agentDone = startAgent()
	restored := invokeTool(t, httpServer.URL, mcpToken, "poll_command", map[string]any{
		"session_id": sessionID, "stdout_offset": 0, "stderr_offset": 0,
	})
	if restored["status"] != "exited" || !strings.Contains(restored["stdout"].(string), "LONG") {
		t.Fatalf("restored session: %v", restored)
	}

	protected := invokeTool(t, httpServer.URL, mcpToken, "read_file", map[string]any{"path": movedPath})
	if err := srv.store.UpdateToolPolicy(context.Background(), "real-e2e", "", "delete_path"); err != nil {
		t.Fatal(err)
	}
	denied := invokeToolError(t, httpServer.URL, mcpToken, "delete_path", map[string]any{
		"path": movedPath, "expected_sha256": protected["sha256"],
	})
	if denied == nil {
		t.Fatal("delete_path policy denial was not returned")
	}
	if _, err := os.Stat(movedPath); err != nil {
		t.Fatalf("denied delete reached local executor: %v", err)
	}
}

func postMCP(t *testing.T, endpoint, token string, payload map[string]any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(payload)
	request, err := http.NewRequest(http.MethodPost, endpoint+"/mcp/v4", bytes.NewReader(body))
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

func invokeToolError(t *testing.T, endpoint, token, name string, arguments map[string]any) map[string]any {
	t.Helper()
	response := postMCP(t, endpoint, token, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments},
	})
	result := response["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("tool %s unexpectedly succeeded: %v", name, result)
	}
	structured, _ := result["structuredContent"].(map[string]any)
	if errorObject, ok := structured["error"].(map[string]any); ok {
		return errorObject
	}
	return map[string]any{"message": result["content"]}
}

func e2eArgvCommand(stdout, stderr string) []any {
	if runtime.GOOS == "windows" {
		return []any{"powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "[Console]::Out.Write('" + stdout + "'); [Console]::Error.Write('" + stderr + "')"}
	}
	return []any{"/bin/sh", "-c", "printf '" + stdout + "'; printf '" + stderr + "' >&2"}
}

func e2eLongArgvCommand() []any {
	if runtime.GOOS == "windows" {
		return []any{"powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "[Console]::Out.Write('LONG'); Start-Sleep -Milliseconds 300; [Console]::Error.Write('DONE')"}
	}
	return []any{"/bin/sh", "-c", "printf LONG; sleep 0.3; printf DONE >&2"}
}
