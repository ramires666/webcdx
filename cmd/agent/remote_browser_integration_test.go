package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func integrationExecutor(t *testing.T, backend string, remote remoteBrowserMCPConfig, enabled bool) *nativeExecutor {
	t.Helper()
	root := t.TempDir()
	executor, err := newNativeExecutorWithConfig(executorConfig{
		AllowedRoots: []string{root}, LogDir: filepath.Join(root, "logs"), ProcessTTL: time.Hour,
		MaxLogBytes: 1 << 20, MaxResponseBytes: 1 << 20,
		BrowserBackend: backend, RemoteBrowser: remote,
		Browser: browserConfig{Enabled: enabled, Dir: filepath.Join(root, "browser")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(executor.Close)
	return executor
}

func remoteBrowserTestServer(t *testing.T, fail bool) (*httptest.Server, *string) {
	t.Helper()
	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if fail {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode MCP request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "test", "version": "1"}}
		case "tools/list":
			tools := make([]map[string]any, 0, len(remoteBrowserToolNames))
			for name := range remoteBrowserToolNames {
				props := map[string]any{}
				if name == "browser_task_start" {
					props["sync_cookies"] = map[string]any{"type": "boolean", "default": true}
				}
				tools = append(tools, map[string]any{"name": name, "description": "remote tool", "inputSchema": map[string]any{"type": "object", "properties": props}})
			}
			tools = append(tools, map[string]any{"name": "unrelated_tool", "inputSchema": map[string]any{"type": "object"}})
			result = map[string]any{"tools": tools}
		case "tools/call":
			if request.Params.Name == "browser_task_start" {
				encoded, _ := json.Marshal(request.Params.Arguments)
				captured = string(encoded)
			}
			result = map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "downstream task error"}}, "structuredContent": map[string]any{"task_id": "task-test", "error": "downstream"}}
		default:
			t.Errorf("unexpected MCP method %q", request.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	return server, &captured
}

func callExecutorRaw(t *testing.T, executor *nativeExecutor, version, name string, arguments map[string]any) json.RawMessage {
	t.Helper()
	request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 17, "method": "tools/call", "params": map[string]any{
		contractMarker: version, "name": name, "arguments": arguments,
	}})
	response, err := executor.call(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestRemoteBrowserDefaultBackendAndMasterDisable(t *testing.T) {
	t.Setenv("WEBCODEX_BROWSER_BACKEND", "")
	t.Setenv("WEBCODEX_BROWSER_ENABLED", "")
	backend, enabled, err := browserBackendFromEnvironment()
	if err != nil || backend != "mcp" || enabled {
		t.Fatalf("default backend = %q enabled=%t err=%v", backend, enabled, err)
	}
	t.Setenv("WEBCODEX_BROWSER_BACKEND", "native")
	t.Setenv("WEBCODEX_BROWSER_ENABLED", "0")
	backend, enabled, err = browserBackendFromEnvironment()
	if err != nil || backend != "disabled" || enabled {
		t.Fatalf("master disable = %q enabled=%t err=%v", backend, enabled, err)
	}
	t.Setenv("WEBCODEX_BROWSER_ENABLED", "1")
	backend, enabled, err = browserBackendFromEnvironment()
	if err != nil || backend != "native" || !enabled {
		t.Fatalf("native opt-in = %q enabled=%t err=%v", backend, enabled, err)
	}
	t.Setenv("WEBCODEX_BROWSER_BACKEND", "unknown")
	if _, _, err := browserBackendFromEnvironment(); err == nil {
		t.Fatal("invalid browser backend was accepted")
	}
}

func TestRemoteBrowserDiscoveryRoutingAndRawToolResult(t *testing.T) {
	server, capturedArgs := remoteBrowserTestServer(t, false)
	remote := remoteBrowserMCPConfig{URL: server.URL, TokenFile: filepath.Join(t.TempDir(), "token")}
	if err := os.WriteFile(remote.TokenFile, []byte("test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := integrationExecutor(t, "mcp", remote, true)
	if executor.browser != nil {
		t.Fatal("remote backend initialized native browser manager")
	}
	list, err := executor.call(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_webcodex_contract":"v5"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Result struct {
			Tools []mcpToolDefinition `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(list, &listed); err != nil {
		t.Fatal(err)
	}
	seen := map[string]mcpToolDefinition{}
	for _, tool := range listed.Result.Tools {
		seen[tool.Name] = tool
	}
	if len(seen) != len(localTools("v5"))+len(remoteBrowserToolNames) {
		t.Fatalf("unexpected tool count %d", len(seen))
	}
	if _, ok := seen["unrelated_tool"]; ok {
		t.Fatal("unrelated upstream tool was exposed")
	}
	start := seen["browser_task_start"]
	props, _ := start.InputSchema["properties"].(map[string]any)
	cookie, _ := props["sync_cookies"].(map[string]any)
	if cookie["default"] != false {
		t.Fatalf("sync_cookies schema default = %#v", cookie["default"])
	}

	response := callExecutorRaw(t, executor, "v5", "browser_task_start", map[string]any{"task": "a bounded task"})
	var decoded map[string]any
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatal(err)
	}
	result := decoded["result"].(map[string]any)
	if result["isError"] != true || result["structuredContent"].(map[string]any)["task_id"] != "task-test" {
		t.Fatalf("upstream MCP result was not preserved: %s", response)
	}
	if !strings.Contains(*capturedArgs, `"sync_cookies":false`) {
		t.Fatalf("default sync_cookies argument missing: %s", *capturedArgs)
	}
	callExecutorRaw(t, executor, "v5", "browser_task_start", map[string]any{"task": "explicit", "sync_cookies": true})
	if !strings.Contains(*capturedArgs, `"sync_cookies":true`) {
		t.Fatalf("explicit sync_cookies choice was lost: %s", *capturedArgs)
	}

	root := executor.allowedRoots[0]
	file := filepath.Join(root, "kept.txt")
	if err := os.WriteFile(file, []byte("local file tools remain available"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := callTool(t, executor, "read_file", map[string]any{"path": file})["content"]; got != "local file tools remain available" {
		t.Fatalf("local read_file content = %#v", got)
	}
}

func TestRemoteBrowserFailureDoesNotFallBackToNative(t *testing.T) {
	server, _ := remoteBrowserTestServer(t, true)
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := integrationExecutor(t, "mcp", remoteBrowserMCPConfig{URL: server.URL, TokenFile: tokenPath}, true)
	list, err := executor.call(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_webcodex_contract":"v5"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(list), "browser_task_") || strings.Contains(string(list), "browser_open") {
		t.Fatalf("unavailable remote browser tools were advertised: %s", list)
	}
	response := callExecutorRaw(t, executor, "v5", "browser_task_start", map[string]any{"task": "do not run locally"})
	if !strings.Contains(string(response), "browser_mcp_unavailable") || executor.browser != nil {
		t.Fatalf("remote failure fell back to native browser: %s", response)
	}
	file := filepath.Join(executor.allowedRoots[0], "local.txt")
	if err := os.WriteFile(file, []byte("local still works"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := callTool(t, executor, "read_file", map[string]any{"path": file})["content"]; got != "local still works" {
		t.Fatalf("local read_file failed after upstream failure: %#v", got)
	}
}

func TestRemoteBrowserToolsOnlyAvailableInV5AndDisabledBackendHasNoBrowser(t *testing.T) {
	server, _ := remoteBrowserTestServer(t, false)
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	remote := remoteBrowserMCPConfig{URL: server.URL, TokenFile: tokenPath}
	executor := integrationExecutor(t, "mcp", remote, false)
	legacy, err := executor.call(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_webcodex_contract":"v4"}}`))
	if err != nil || strings.Contains(string(legacy), "browser_task_") {
		t.Fatalf("v4 exposed remote browser tools: %s, %v", legacy, err)
	}
	disabled := integrationExecutor(t, "disabled", remote, false)
	list, err := disabled.call(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_webcodex_contract":"v5"}}`))
	if err != nil || strings.Contains(string(list), "browser_task_") || strings.Contains(string(list), "browser_open") || disabled.browser != nil {
		t.Fatalf("disabled backend exposed or initialized browser: %s, %v", list, err)
	}
}
