package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var remoteTestTools = []map[string]any{
	{"name": "browser_task_start", "description": "Start task", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"goal": map[string]any{"type": "string"}}}},
	{"name": "browser_task_status", "inputSchema": map[string]any{"type": "object"}},
	{"name": "browser_task_events", "inputSchema": map[string]any{"type": "object"}},
	{"name": "browser_task_result", "inputSchema": map[string]any{"type": "object"}},
	{"name": "browser_task_pause", "inputSchema": map[string]any{"type": "object"}},
	{"name": "browser_task_resume", "inputSchema": map[string]any{"type": "object"}},
	{"name": "browser_task_cancel", "inputSchema": map[string]any{"type": "object"}},
}

func remoteFixture(t *testing.T, handler func(http.ResponseWriter, *http.Request, map[string]any)) (*remoteBrowserMCP, *httptest.Server) {
	t.Helper()
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("secret-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			http.Error(w, "bad headers", 400)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if req["method"] == "initialize" && r.Header.Get("MCP-Protocol-Version") != "" {
			http.Error(w, "initialize must not carry negotiated protocol header", 400)
			return
		}
		if req["method"] != "initialize" && r.Header.Get("MCP-Protocol-Version") != remoteBrowserProtocolVersion {
			http.Error(w, "missing protocol header", 400)
			return
		}
		handler(w, r, req)
	}))
	c, err := newRemoteBrowserMCP(remoteBrowserMCPConfig{URL: s.URL, TokenFile: tokenPath, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return c, s
}

func remoteReply(w http.ResponseWriter, req map[string]any, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": result})
}

func TestRemoteBrowserMCPToolsAndCall(t *testing.T) {
	var calls []string
	c, _ := remoteFixture(t, func(w http.ResponseWriter, r *http.Request, req map[string]any) {
		method := req["method"].(string)
		calls = append(calls, method)
		switch method {
		case "initialize":
			remoteReply(w, req, map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "test", "version": "1"}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			remoteReply(w, req, map[string]any{"tools": append(remoteTestTools, map[string]any{"name": "other"})})
		case "tools/call":
			p := req["params"].(map[string]any)
			a := p["arguments"].(map[string]any)
			if a["sync_cookies"] != false {
				t.Errorf("default sync_cookies=%v, want false", a["sync_cookies"])
			}
			remoteReply(w, req, map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}, "isError": true})
		default:
			t.Errorf("unexpected MCP method %s", method)
		}
	})
	tools, err := c.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 7 {
		t.Fatalf("got %d tools, want 7", len(tools))
	}
	if !strings.Contains(tools[0].Description, "Docker Camoufox") {
		t.Fatalf("missing lifecycle description: %q", tools[0].Description)
	}
	args := map[string]any{"goal": "read title"}
	raw, err := c.Call(context.Background(), "browser_task_start", args)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := args["sync_cookies"]; exists {
		t.Fatal("Call mutated caller arguments")
	}
	var result struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || !result.IsError {
		t.Fatalf("CallToolResult not preserved: %s (%v)", raw, err)
	}
	want := []string{"initialize", "notifications/initialized", "tools/list", "initialize", "notifications/initialized", "tools/call"}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("methods %v, want %v", calls, want)
	}
}

func TestRemoteBrowserMCPExplicitCookieAndUnknownName(t *testing.T) {
	c, _ := remoteFixture(t, func(w http.ResponseWriter, _ *http.Request, req map[string]any) {
		if req["method"] == "initialize" {
			remoteReply(w, req, map[string]any{"protocolVersion": remoteBrowserProtocolVersion})
			return
		}
		if req["method"] == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if req["method"] == "tools/call" {
			args := req["params"].(map[string]any)["arguments"].(map[string]any)
			if args["sync_cookies"] != true {
				t.Errorf("explicit sync_cookies=%v, want true", args["sync_cookies"])
			}
		}
		remoteReply(w, req, map[string]any{"content": []any{}, "isError": false})
	})
	if _, err := c.Call(context.Background(), "browser_click", nil); err == nil {
		t.Fatal("unexpected tool accepted")
	}
	_, err := c.Call(context.Background(), "browser_task_start", map[string]any{"sync_cookies": true})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRemoteBrowserMCPContextCancellation(t *testing.T) {
	c, _ := remoteFixture(t, func(w http.ResponseWriter, r *http.Request, req map[string]any) {
		if req["method"] == "initialize" {
			<-r.Context().Done()
			return
		}
		remoteReply(w, req, map[string]any{"tools": remoteTestTools})
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Tools(ctx); err == nil {
		t.Fatal("expected context cancellation")
	}
}

func TestRemoteBrowserMCPUpstreamErrorRedirectAndBodyLimit(t *testing.T) {
	t.Run("upstream error", func(t *testing.T) {
		c, _ := remoteFixture(t, func(w http.ResponseWriter, _ *http.Request, req map[string]any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "error": map[string]any{"code": -32000, "message": "unavailable"}})
		})
		if _, err := c.Tools(context.Background()); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		c, _ := remoteFixture(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.Header().Set("Location", "/other")
			w.WriteHeader(http.StatusTemporaryRedirect)
		})
		if _, err := c.Tools(context.Background()); err == nil || !strings.Contains(err.Error(), "redirect rejected") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("large body", func(t *testing.T) {
		c, _ := remoteFixture(t, func(w http.ResponseWriter, _ *http.Request, req map[string]any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":`))
			id, _ := json.Marshal(req["id"])
			_, _ = w.Write(id)
			_, _ = w.Write([]byte(`,"result":{"padding":"` + strings.Repeat("x", 3<<20) + `"}}`))
		})
		raw, _, err := c.request(context.Background(), "tools/call", remoteBrowserProtocolVersion, map[string]any{}, "", false)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Padding string `json:"padding"`
		}
		if json.Unmarshal(raw, &result) != nil || len(result.Padding) != 3<<20 {
			t.Fatal("large remote MCP result was changed")
		}
	})
}

func TestRemoteBrowserMCPSSEAndMissingToken(t *testing.T) {
	c, _ := remoteFixture(t, func(w http.ResponseWriter, _ *http.Request, req map[string]any) {
		if req["method"] == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		id, _ := json.Marshal(req["id"])
		var result any = map[string]any{"protocolVersion": "2025-03-26"}
		if req["method"] == "tools/list" {
			result = map[string]any{"tools": remoteTestTools}
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
	})
	if tools, err := c.Tools(context.Background()); err != nil || len(tools) != 7 {
		t.Fatalf("SSE tools: %d, %v", len(tools), err)
	}
	c.tokenFile = filepath.Join(t.TempDir(), "missing")
	if _, err := c.Tools(context.Background()); err == nil {
		t.Fatal("expected missing token error")
	}
}

func TestRemoteBrowserMCPRejectsProtocolVersionMismatch(t *testing.T) {
	c, _ := remoteFixture(t, func(w http.ResponseWriter, _ *http.Request, req map[string]any) {
		remoteReply(w, req, map[string]any{"protocolVersion": "unsupported-version"})
	})
	if _, err := c.Tools(context.Background()); err == nil || !strings.Contains(err.Error(), "unsupported protocol version") {
		t.Fatalf("error=%v", err)
	}
}

func TestRemoteBrowserMCPLiveDiscovery(t *testing.T) {
	endpoint := os.Getenv("WEBCODEX_TEST_MCP_URL")
	tokenFile := os.Getenv("WEBCODEX_TEST_MCP_TOKEN_FILE")
	if endpoint == "" || tokenFile == "" {
		t.Skip("set WEBCODEX_TEST_MCP_URL and WEBCODEX_TEST_MCP_TOKEN_FILE for read-only live MCP discovery")
	}
	client, err := newRemoteBrowserMCP(remoteBrowserMCPConfig{URL: endpoint, TokenFile: tokenFile, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := client.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 7 {
		t.Fatalf("got %d allowed tools, want 7", len(tools))
	}
	for _, tool := range tools {
		if !isRemoteBrowserTool(tool.Name) {
			t.Errorf("unexpected tool %q", tool.Name)
		}
		if tool.Name == "browser_task_start" {
			props, _ := tool.InputSchema["properties"].(map[string]any)
			cookie, _ := props["sync_cookies"].(map[string]any)
			if cookie["default"] != false {
				t.Errorf("sync_cookies schema default=%v, want false", cookie["default"])
			}
		}
	}
}
