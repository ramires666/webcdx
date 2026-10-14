package main

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Opt in only when the user has enabled permanent extension access. This probes
// the installed native host and real profile without exposing page contents.
func TestInstalledPersonalBrowserLive(t *testing.T) {
	if os.Getenv("WEBCODEX_PERSONAL_LIVE_TEST") != "1" {
		t.Skip("requires explicit permission to use the installed personal extension")
	}
	binary := os.Getenv("WEBCODEX_PERSONAL_LIVE_AGENT")
	if binary == "" {
		t.Fatal("WEBCODEX_PERSONAL_LIVE_AGENT must name the registered agent executable")
	}
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	const agentID, agentToken, accessToken = "personal-live-test", "wc_agent_personal_live_test", "wc_mcp_personal_live_test"
	if err := createTestAgent(srv.store, context.Background(), Agent{ID: agentID, Name: "Personal live test", Enabled: true, AgentTokenHash: hashSecret(agentToken)}, "personal-live-client", ""); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateAccessToken(context.Background(), AccessToken{TokenHash: hashSecret(accessToken), ClientID: agentID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	gate := httptest.NewServer(srv.routes())
	defer gate.Close()
	srv.publicURL = gate.URL
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent := exec.CommandContext(ctx, binary)
	agent.Dir = filepath.Dir(binary)
	agent.Env = append(os.Environ(), "WEBCODEX_GATE_URL="+gate.URL, "WEBCODEX_AGENT_TOKEN="+agentToken,
		"WEBCODEX_ALLOWED_ROOTS="+root, "WEBCODEX_LOG_DIR="+filepath.Join(root, "logs"),
		"WEBCODEX_BROWSER_DIR="+filepath.Join(root, "browser"), "WEBCODEX_BROWSER_ENABLED=1", "WEBCODEX_BROWSER_PERSONAL_ENABLED=1")
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = agent.Wait() }()
	runtime := srv.runtimeFor(agentID)
	for deadline := time.Now().Add(15 * time.Second); !runtime.isOnline(); {
		if time.Now().After(deadline) {
			t.Fatal("test agent did not connect to the isolated Gate")
		}
		time.Sleep(100 * time.Millisecond)
	}
	call := func(name string, args map[string]any) (map[string]any, string) {
		response := postMCPVersion(t, gate.URL, accessToken, "v5", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		result, ok := response["result"].(map[string]any)
		if !ok {
			t.Fatal("invalid MCP envelope")
		}
		data, _ := result["structuredContent"].(map[string]any)
		if result["isError"] == true {
			failure, _ := data["error"].(map[string]any)
			code, _ := failure["code"].(string)
			return nil, code
		}
		if data["status"] == "running" {
			time.Sleep(200 * time.Millisecond)
			return callPersonalLiveOperation(t, gate.URL, accessToken, data["operation_id"].(string))
		}
		return data, ""
	}
	var opened map[string]any
	for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); {
		var code string
		opened, code = call("browser_open", map[string]any{"mode": "personal", "request_key": "personal-live-open-" + time.Now().Format("150405.000000000")})
		if code == "" {
			break
		}
		if code != "waiting_user" && code != "disconnected" {
			t.Fatalf("personal connection failed: %s", code)
		}
		time.Sleep(time.Second)
	}
	if opened == nil || opened["mode"] != "personal" {
		t.Fatal("the installed extension did not reconnect to the new agent")
	}
	id := opened["browser_session_id"].(string)
	listed, code := call("browser_tabs", map[string]any{"browser_session_id": id, "action": "list"})
	if code != "" {
		t.Fatalf("personal tab list failed: %s", code)
	}
	tabs := listed["tabs"].([]any)
	if len(tabs) == 0 {
		t.Fatal("the installed personal extension shared no website tabs")
	}
	t.Logf("REAL PERSONAL PROFILE CONNECTED: %d permitted tabs; page contents were not exported", len(tabs))
	snapshot, code := call("browser_snapshot", map[string]any{"browser_session_id": id})
	if code != "" || snapshot == nil {
		t.Fatalf("reading the connected personal tab failed: %s", code)
	}
	frames, _ := snapshot["frames"].([]any)
	if len(frames) == 0 {
		t.Fatal("personal snapshot returned no page frames")
	}
	t.Logf("REAL PERSONAL SNAPSHOT OK: %d frames; page contents were not exported", len(frames))
	if os.Getenv("WEBCODEX_PERSONAL_LIVE_MUTATIONS") != "1" {
		return
	}
	created, code := call("browser_tabs", map[string]any{"browser_session_id": id, "action": "new", "request_key": "personal-live-new"})
	if code != "" || created == nil || created["status"] == "waiting_user" {
		t.Fatalf("creating a personal task tab failed: %s (new-tab permission must be enabled)", code)
	}
	newTab := created["tab_id"].(string)
	defer func() {
		_, code := call("browser_tabs", map[string]any{"browser_session_id": id, "tab_id": newTab, "action": "close", "request_key": "personal-live-close"})
		if code != "" {
			t.Errorf("closing only the personal test tab failed: %s", code)
		}
	}()
	_, code = call("browser_navigate", map[string]any{"browser_session_id": id, "tab_id": newTab, "action": "url", "url": "https://www.google.com/", "request_key": "personal-live-google"})
	if code != "" {
		t.Fatalf("navigating the new personal task tab failed: %s", code)
	}
	_, code = call("browser_wait", map[string]any{"browser_session_id": id, "tab_id": newTab, "condition": "load", "timeout_seconds": 15})
	if code != "" {
		t.Fatalf("loading Google in the personal task tab failed: %s", code)
	}
	snapshot, code = call("browser_snapshot", map[string]any{"browser_session_id": id, "tab_id": newTab})
	if code != "" || snapshot == nil {
		t.Fatalf("reading the new personal task tab failed: %s", code)
	}
	t.Log("REAL PERSONAL NEW TAB OK: created, navigated to Google, read; cleanup closes only the test tab")
}

func callPersonalLiveOperation(t *testing.T, endpoint, token, operationID string) (map[string]any, string) {
	t.Helper()
	for deadline := time.Now().Add(35 * time.Second); time.Now().Before(deadline); {
		response := postMCPVersion(t, endpoint, token, "v5", map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "browser_control", "arguments": map[string]any{"action": "status", "operation_id": operationID}}})
		result := response["result"].(map[string]any)
		data, _ := result["structuredContent"].(map[string]any)
		if result["isError"] == true {
			failure, _ := data["error"].(map[string]any)
			code, _ := failure["code"].(string)
			return nil, code
		}
		if data["status"] != "running" {
			return data, ""
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, "timeout"
}
