package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMCPBridgeAuthenticationRequiresGrantAndExactOrigin(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	ctx := context.Background()
	if err := createTestAgent(srv.store, ctx, Agent{ID: "bridge-policy", Name: "Bridge", Enabled: true, AgentTokenHash: hashSecret("worker")}, "bridge-policy-oauth", "secret"); err != nil {
		t.Fatal(err)
	}
	issue := func(raw string) {
		t.Helper()
		if err := srv.store.CreateAccessToken(ctx, AccessToken{TokenHash: hashSecret(raw), ClientID: "bridge-policy", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	issue("wc_bridge_missing")
	issue("wc_bridge_valid")
	issue("ordinary-token")
	if _, err := srv.authenticateMCP(httptest.NewRequest("POST", "/mcp/v5", nil)); err == nil {
		t.Fatal("bridge access token without grant authenticated")
	}
	extension := "abcdefghijklmnopabcdefghijklmnop"
	allowed, _ := json.Marshal([]string{"read_file"})
	if _, err := srv.store.db.Exec(`INSERT INTO bridge_clients(client_id,extension_id,allowed_tools) VALUES(?,?,?)`, "bridge-policy", extension, string(allowed)); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.db.Exec(`INSERT INTO bridge_token_grants(token_hash,client_id,extension_id,allowed_tools) VALUES(?,?,?,?)`, hashSecret("wc_bridge_valid"), "bridge-policy", extension, string(allowed)); err != nil {
		t.Fatal(err)
	}
	wrong := httptest.NewRequest("POST", "/mcp/v5", nil)
	wrong.Header.Set("Authorization", "Bearer wc_bridge_valid")
	wrong.Header.Set("Origin", "chrome-extension://wrong")
	if _, err := srv.authenticateMCP(wrong); err == nil {
		t.Fatal("bridge token accepted a different extension origin")
	}
	correct := httptest.NewRequest("POST", "/mcp/v5", nil)
	correct.Header.Set("Authorization", "Bearer wc_bridge_valid")
	correct.Header.Set("Origin", "chrome-extension://"+extension)
	client, err := srv.authenticateMCP(correct)
	if err != nil || client.BridgeGrant == nil {
		t.Fatalf("valid bridge token: client=%+v err=%v", client, err)
	}
	noOrigin := httptest.NewRequest("POST", "/mcp/v5", nil)
	noOrigin.Header.Set("Authorization", "Bearer wc_bridge_valid")
	if _, err := srv.authenticateMCP(noOrigin); err != nil {
		t.Fatalf("token-authenticated request without Origin: %v", err)
	}
	legacy := httptest.NewRequest("POST", "/mcp/v5", nil)
	legacy.Header.Set("Authorization", "Bearer ordinary-token")
	legacy.Header.Set("Origin", "https://unrelated.example")
	legacyClient, err := srv.authenticateMCP(legacy)
	if err != nil || legacyClient.BridgeGrant != nil {
		t.Fatalf("ordinary MCP token flow changed: client=%+v err=%v", legacyClient, err)
	}
}

func TestExecuteMCPToolAppliesBridgePolicyAndKeepsLegacyAccess(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	ctx := context.Background()
	if err := createTestAgent(srv.store, ctx, Agent{ID: "bridge-call", Name: "Bridge", Enabled: true, AgentTokenHash: hashSecret("worker")}, "bridge-call-oauth", "secret"); err != nil {
		t.Fatal(err)
	}
	request := json.RawMessage(`{"jsonrpc":"2.0","id":"request-7","method":"tools/call","params":{"name":"exec_command","arguments":{"command":"dir"}}}`)
	bridge := &MCPClient{ID: "bridge-call", BridgeGrant: &bridgeGrant{AllowedTools: []string{"read_file"}}}
	if _, err := srv.executeMCPTool(ctx, bridge, request, "v5"); err == nil {
		t.Fatal("restricted bridge called exec_command")
	}
	legacy := &MCPClient{ID: "bridge-call"}
	response, err := srv.executeMCPTool(ctx, legacy, json.RawMessage(`{"jsonrpc":"2.0","id":"legacy-1","method":"tools/call","params":{"name":"list_agents","arguments":{}}}`), "v5")
	if err != nil {
		t.Fatal(err)
	}
	var message struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(response, &message); err != nil || string(message.ID) != `"legacy-1"` {
		t.Fatalf("legacy response ID changed: %s err=%v", response, err)
	}
}

func TestMCPToolsListHidesToolsOutsideBridgeGrant(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	createMCPGroupAgents(t, srv)
	connectMCPGroupWorker(t, srv, "home")
	connectMCPGroupWorker(t, srv, "work")
	token := "wc_bridge_tools_list"
	if err := srv.store.CreateAccessToken(context.Background(), AccessToken{TokenHash: hashSecret(token), ClientID: "home", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	extension := "abcdefghijklmnopabcdefghijklmnop"
	allowed, _ := json.Marshal([]string{"read_file"})
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO bridge_clients(client_id,extension_id,allowed_tools) VALUES(?,?,?)`, []any{"home", extension, string(allowed)}},
		{`INSERT INTO bridge_token_grants(token_hash,client_id,extension_id,allowed_tools) VALUES(?,?,?,?)`, []any{hashSecret(token), "home", extension, string(allowed)}},
	} {
		if _, err := srv.store.db.Exec(stmt.query, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest("POST", "/mcp/v5", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("tools/list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range response.Result.Tools {
		seen[tool.Name] = true
	}
	if !seen["read_file"] || seen["write_file"] || seen["list_agents"] {
		t.Fatalf("tools/list did not match bridge allowlist: %v", seen)
	}
}
