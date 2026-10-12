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
	"sync/atomic"
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
			"WEBCODEX_BROWSER_ENABLED=0", "WEBCODEX_BROWSER_PERSONAL_ENABLED=0", "WEBCODEX_BROWSER_DIR="+filepath.Join(root, "browser"), "WEBCODEX_BROWSER_ALLOW_LOCAL=1",
		)
		if os.Getenv("WEBCODEX_BROWSER_TEST") == "1" {
			command.Env = append(command.Env, "WEBCODEX_BROWSER_ENABLED=1")
		}
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
	retryArgs := map[string]any{"argv": e2eArgvCommand("RETRY", ""), "cwd": root, "request_key": "e2e-once", "yield_time_ms": 3000}
	launch := func() map[string]any {
		response := postMCPVersion(t, httpServer.URL, mcpToken, "v5", map[string]any{
			"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{"name": "exec_command", "arguments": retryArgs},
		})
		result := response["result"].(map[string]any)
		if result["isError"] == true {
			t.Fatalf("v5 launch: %v", result)
		}
		return result["structuredContent"].(map[string]any)
	}
	once := launch()
	if retry := launch(); retry["session_id"] != once["session_id"] {
		t.Fatal("v5 Retry duplicated process")
	}
	monitor := func(method, suffix, body string, authenticated bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/admin/agents/real-e2e/commands"+suffix, strings.NewReader(body))
		if authenticated {
			request.SetBasicAuth(srv.adminUser, srv.adminPassword)
		}
		if method == http.MethodPost {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		recorder := httptest.NewRecorder()
		srv.routes().ServeHTTP(recorder, request)
		return recorder
	}
	if response := monitor("GET", "", "", false); response.Code != http.StatusUnauthorized {
		t.Fatal("monitor allowed unauthenticated access")
	}
	if response := monitor("GET", "", "", true); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Команды:") {
		t.Fatalf("monitor page: %s", response.Body.String())
	}
	if response := monitor("GET", "/data", "", true); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), once["session_id"].(string)) {
		t.Fatalf("monitor list: %s", response.Body.String())
	}
	query := "?session_id=" + once["session_id"].(string)
	if response := monitor("GET", "/data"+query+"&stdout_offset=0&stderr_offset=0", "", true); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "RETRY") {
		t.Fatalf("monitor poll: %s", response.Body.String())
	}
	if response := monitor("GET", "/log"+query+"&stream=stdout", "", true); response.Code != http.StatusOK || response.Body.String() != "RETRY" {
		t.Fatalf("monitor log: %s", response.Body.String())
	}
	if response := monitor("GET", "/data?session_id=proc_foreign&stdout_offset=0&stderr_offset=0", "", true); response.Code != http.StatusBadGateway {
		t.Fatal("foreign session accepted")
	}
	if response := monitor("POST", "/stop", "session_id="+once["session_id"].(string), true); response.Code != http.StatusForbidden {
		t.Fatal("stop bypassed CSRF")
	}
	if err := srv.store.UpdateToolPolicy(context.Background(), "real-e2e", "", "poll_command,cancel_command"); err != nil {
		t.Fatal(err)
	}
	if response := monitor("GET", "/log"+query+"&stream=stdout", "", true); response.Code != http.StatusForbidden {
		t.Fatal("download bypassed poll policy")
	}
	if response := monitor("POST", "/stop", "csrf="+srv.adminCSRF+"&session_id="+once["session_id"].(string), true); response.Code != http.StatusForbidden {
		t.Fatal("stop bypassed cancel policy")
	}
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
	if os.Getenv("WEBCODEX_BROWSER_TEST") == "1" {
		testBrowserThroughMCP(t, srv, httpServer.URL, mcpToken)
	}
}

func testBrowserThroughMCP(t *testing.T, srv *server, endpoint, token string) {
	t.Helper()
	var submits atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit" {
			submits.Add(1)
			fmt.Fprint(w, "submitted")
			return
		}
		fmt.Fprint(w, `<!doctype html><form action="/submit" method="post"><input id="field" name="field"><button id="submit">Submit</button></form>`)
	}))
	defer site.Close()
	request := func(name string, args map[string]any) map[string]any {
		response := postMCPVersion(t, endpoint, token, "v5", map[string]any{"jsonrpc": "2.0", "id": 31, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		return response["result"].(map[string]any)
	}
	call := func(name string, args map[string]any) map[string]any {
		result := request(name, args)
		for deadline := time.Now().Add(25 * time.Second); ; {
			if result["isError"] == true {
				t.Fatalf("browser MCP %s: %v", name, result)
			}
			data := result["structuredContent"].(map[string]any)
			if data["status"] != "running" {
				return result
			}
			if time.Now().After(deadline) {
				t.Fatal("browser operation did not finish")
			}
			time.Sleep(50 * time.Millisecond)
			result = request("browser_control", map[string]any{"action": "status", "operation_id": data["operation_id"]})
		}
	}
	opened := call("browser_open", map[string]any{"mode": "clean", "request_key": "mcp-browser-open"})["structuredContent"].(map[string]any)
	id := opened["browser_session_id"]
	defer call("browser_control", map[string]any{"browser_session_id": id, "action": "disconnect"})
	args := func(properties map[string]any) map[string]any {
		properties["browser_session_id"] = id
		return properties
	}
	call("browser_navigate", args(map[string]any{"action": "url", "url": site.URL, "request_key": "mcp-nav"}))
	call("browser_interact", args(map[string]any{"action": "fill", "selector": "#field", "text": "MCP", "request_key": "mcp-fill"}))
	submit := args(map[string]any{"action": "click", "selector": "#submit", "request_key": "mcp-submit"})
	call("browser_interact", submit)
	call("browser_interact", submit)
	call("browser_wait", args(map[string]any{"condition": "text", "text": "submitted"}))
	if submits.Load() != 1 {
		t.Fatalf("MCP Retry repeated submit: %d", submits.Load())
	}
	image := call("browser_screenshot", args(map[string]any{}))
	found := false
	for _, item := range image["content"].([]any) {
		if item.(map[string]any)["type"] == "image" {
			found = true
		}
	}
	if !found {
		t.Fatal("MCP image content lost at Gate")
	}
	if err := srv.store.UpdateToolPolicy(context.Background(), "real-e2e", "", "browser_navigate"); err != nil {
		t.Fatal(err)
	}
	if denied := request("browser_navigate", args(map[string]any{"action": "url", "url": site.URL, "request_key": "denied-nav"})); denied["isError"] != true {
		t.Fatal("browser policy deny bypassed")
	}
}

func postMCP(t *testing.T, endpoint, token string, payload map[string]any) map[string]any {
	return postMCPVersion(t, endpoint, token, "v4", payload)
}

func postMCPVersion(t *testing.T, endpoint, token, version string, payload map[string]any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(payload)
	request, err := http.NewRequest(http.MethodPost, endpoint+"/mcp/"+version, bytes.NewReader(body))
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
