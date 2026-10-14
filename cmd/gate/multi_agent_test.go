package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"webcodex/internal/protocol"
)

func setupTestServer(t *testing.T) (*server, func()) {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_gate.db")

	store, err := newStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	srv := &server{
		publicURL:     "https://codex.grom.world",
		timeout:       2 * time.Second,
		store:         store,
		runtimes:      make(map[string]*agentRuntime),
		adminUser:     "admin",
		adminPassword: "secret-password",
		adminCSRF:     "csrf-token-123",
		oauthCodes:    make(map[string]oauthCode),
	}

	cleanup := func() {
		_ = store.Close()
	}
	return srv, cleanup
}

func TestAgentAuthenticationAndIsolation(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.Background()

	rawTokenA := "wc_agent_token_a"
	rawTokenB := "wc_agent_token_b"

	// Create Agent A
	if err := createTestAgent(srv.store, ctx, Agent{
		ID:             "agent-a",
		Name:           "Agent Alpha",
		Enabled:        true,
		AgentTokenHash: hashSecret(rawTokenA),
	}, "client-a", hashSecret("secret-a")); err != nil {
		t.Fatalf("create agent a: %v", err)
	}

	// Create Agent B (Disabled)
	if err := createTestAgent(srv.store, ctx, Agent{
		ID:             "agent-b",
		Name:           "Agent Beta",
		Enabled:        false,
		AgentTokenHash: hashSecret(rawTokenB),
	}, "client-b", hashSecret("secret-b")); err != nil {
		t.Fatalf("create agent b: %v", err)
	}

	// 1. Authenticate valid enabled agent
	reqA := httptest.NewRequest("GET", "/agent/stream", nil)
	reqA.Header.Set("Authorization", "Bearer "+rawTokenA)
	agentA, rtA, err := srv.authenticateAgent(reqA)
	if err != nil {
		t.Fatalf("authenticate agent a failed: %v", err)
	}
	if agentA.ID != "agent-a" || rtA.id != "agent-a" {
		t.Fatalf("unexpected agent a: %+v", agentA)
	}

	// 2. Authenticate disabled agent
	reqB := httptest.NewRequest("GET", "/agent/stream", nil)
	reqB.Header.Set("Authorization", "Bearer "+rawTokenB)
	_, _, err = srv.authenticateAgent(reqB)
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled agent error, got: %v", err)
	}

	// 3. Authenticate invalid token
	reqInvalid := httptest.NewRequest("GET", "/agent/stream", nil)
	reqInvalid.Header.Set("Authorization", "Bearer wc_agent_invalid")
	_, _, err = srv.authenticateAgent(reqInvalid)
	if err == nil {
		t.Fatalf("expected error for invalid agent token")
	}
}

func TestAgentResultCannotCompleteOtherAgentRequest(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.Background()

	rawTokenA := "wc_agent_a"
	rawTokenB := "wc_agent_b"

	_ = createTestAgent(srv.store, ctx, Agent{
		ID:             "agent-a",
		Name:           "Agent A",
		Enabled:        true,
		AgentTokenHash: hashSecret(rawTokenA),
	}, "client-a", "")
	_ = createTestAgent(srv.store, ctx, Agent{
		ID:             "agent-b",
		Name:           "Agent B",
		Enabled:        true,
		AgentTokenHash: hashSecret(rawTokenB),
	}, "client-b", "")

	rtA := srv.runtimeFor("agent-a")
	srv.runtimeFor("agent-b")

	// Setup pending request in Agent A's runtime with ID "req-123"
	respChA := make(chan protocol.AgentResponse, 1)
	rtA.mu.Lock()
	rtA.pending["req-123"] = respChA
	rtA.mu.Unlock()

	// Agent B tries to submit result for "req-123"
	reqResult := protocol.AgentResponse{
		ID:       "req-123",
		Response: json.RawMessage(`{"result":"from-b"}`),
	}
	body, _ := json.Marshal(reqResult)

	req := httptest.NewRequest("POST", "/agent/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+rawTokenB)
	rec := httptest.NewRecorder()

	srv.handleAgentResult(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for agent B trying to complete agent A request, got %d", rec.Code)
	}

	// Agent A channel should still be empty
	select {
	case <-respChA:
		t.Fatal("agent A received response sent by agent B")
	default:
		// OK
	}

	// Agent A submits result for "req-123"
	reqA := httptest.NewRequest("POST", "/agent/result", bytes.NewReader(body))
	reqA.Header.Set("Authorization", "Bearer "+rawTokenA)
	recA := httptest.NewRecorder()

	srv.handleAgentResult(recA, reqA)
	if recA.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for agent A completing own request, got %d", recA.Code)
	}

	select {
	case res := <-respChA:
		if string(res.Response) != `{"result":"from-b"}` {
			t.Fatalf("unexpected response: %s", string(res.Response))
		}
	default:
		t.Fatal("agent A did not receive expected response")
	}
}

func TestOAuthAndMCPRoutingEndToEnd(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.Background()

	rawAgentToken := "wc_agent_home"
	rawOAuthSecret := "wc_oauth_secret_home"
	clientID := "client-home"

	if err := createTestAgent(srv.store, ctx, Agent{
		ID:             "home",
		Name:           "Home Worker",
		Enabled:        true,
		AgentTokenHash: hashSecret(rawAgentToken),
		AllowedTools:   "exec_command,read_file",
		DeniedTools:    "danger_tool",
	}, clientID, hashSecret(rawOAuthSecret)); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	var tokens []string
	for _, redirectURI := range []string{
		"https://chatgpt.com/oauth/callback",
		"https://chatgpt.com/connector_platform_oauth_redirect",
		"https://chatgpt.com/connector/oauth/callback-id",
		"https://oauth-redirect.googleusercontent.com/r/user_bound_custom-mcp-104381269201799843617-codex_grom_world",
	} {
		t.Run(redirectURI, func(t *testing.T) {
			tokens = append(tokens, exchangeOAuthToken(t, srv, clientID, rawOAuthSecret, redirectURI))
		})
	}
	// Both clients' tokens must remain usable on the same agent after linking.
	for _, token := range tokens {
		for _, endpoint := range []string{"/mcp", "/mcp/v5"} {
			reqMCP := httptest.NewRequest("POST", endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
			reqMCP.Header.Set("Authorization", "Bearer "+token)
			recMCP := httptest.NewRecorder()
			srv.routes().ServeHTTP(recMCP, reqMCP)
			if recMCP.Code != http.StatusOK {
				t.Fatalf("%s initialize status = %d, body = %s", endpoint, recMCP.Code, recMCP.Body.String())
			}
		}
	}
}

func exchangeOAuthToken(t *testing.T, srv *server, clientID, rawOAuthSecret, redirectURI string) string {
	t.Helper()
	// 1. Authorize endpoint with PKCE
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])

	authURL := "/oauth/authorize?client_id=" + clientID +
		"&response_type=code" +
		"&redirect_uri=" + url.QueryEscape(redirectURI) +
		"&code_challenge=" + challenge +
		"&code_challenge_method=S256&state=state-123"

	reqAuth := httptest.NewRequest("GET", authURL, nil)
	recAuth := httptest.NewRecorder()

	srv.handleAuthorize(recAuth, reqAuth)

	if recAuth.Code != http.StatusFound {
		t.Fatalf("authorize status = %d, want 302", recAuth.Code)
	}
	loc := recAuth.Header().Get("Location")
	parsedLoc, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect URL: %v", err)
	}
	if parsedLoc.Scheme+"://"+parsedLoc.Host+parsedLoc.Path != redirectURI || parsedLoc.Query().Get("state") != "state-123" || parsedLoc.Query().Get("iss") != srv.publicURL {
		t.Fatalf("unexpected OAuth callback: %s", loc)
	}
	code := parsedLoc.Query().Get("code")
	if code == "" || !strings.HasPrefix(code, "wc_code_") {
		t.Fatalf("invalid code in redirect: %q", code)
	}

	// 2. Token exchange with PKCE
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("client_secret", rawOAuthSecret)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)

	reqToken := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
	reqToken.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recToken := httptest.NewRecorder()

	srv.handleToken(recToken, reqToken)

	if recToken.Code != http.StatusOK {
		t.Fatalf("token status = %d, body = %s", recToken.Code, recToken.Body.String())
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(recToken.Body.Bytes(), &tokenResp); err != nil {
		t.Fatalf("parse token response: %v", err)
	}
	if tokenResp.AccessToken == "" || tokenResp.TokenType != "Bearer" {
		t.Fatalf("unexpected token response: %+v", tokenResp)
	}

	// 3. Replay attack: using the same code again MUST fail
	reqReplay := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
	reqReplay.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recReplay := httptest.NewRecorder()

	srv.handleToken(recReplay, reqReplay)
	if recReplay.Code != http.StatusBadRequest {
		t.Fatalf("expected code replay to fail with 400, got %d", recReplay.Code)
	}

	return tokenResp.AccessToken
}

func TestToolPolicyEnforcementPerAgent(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.Background()

	rawTokenA := "wc_mcp_a"
	rawTokenB := "wc_mcp_b"

	_ = createTestAgent(srv.store, ctx, Agent{
		ID:             "agent-a",
		Name:           "Agent A",
		Enabled:        true,
		AgentTokenHash: hashSecret("at-a"),
		AllowedTools:   "allowed_tool",
		DeniedTools:    "denied_tool",
	}, "client-a", "")
	_ = srv.store.CreateAccessToken(ctx, AccessToken{
		TokenHash: hashSecret(rawTokenA),
		ClientID:  "agent-a",
		ExpiresAt: time.Now().Add(time.Hour),
	})

	_ = createTestAgent(srv.store, ctx, Agent{
		ID:             "agent-b",
		Name:           "Agent B",
		Enabled:        true,
		AgentTokenHash: hashSecret("at-b"),
		AllowedTools:   "", // All allowed
		DeniedTools:    "",
	}, "client-b", "")
	_ = srv.store.CreateAccessToken(ctx, AccessToken{
		TokenHash: hashSecret(rawTokenB),
		ClientID:  "agent-b",
		ExpiresAt: time.Now().Add(time.Hour),
	})

	// 1. Agent A denied tool
	deniedCall := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"denied_tool","arguments":{}}}`
	reqA := httptest.NewRequest("POST", "/mcp", strings.NewReader(deniedCall))
	reqA.Header.Set("Authorization", "Bearer "+rawTokenA)
	recA := httptest.NewRecorder()

	srv.handleMCP(recA, reqA)

	if !strings.Contains(recA.Body.String(), "tool not allowed") {
		t.Fatalf("expected tool not allowed error for agent A, got: %s", recA.Body.String())
	}

	// 2. Agent A unlisted tool (not in allowed list)
	unlistedCall := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"unlisted_tool","arguments":{}}}`
	reqA2 := httptest.NewRequest("POST", "/mcp", strings.NewReader(unlistedCall))
	reqA2.Header.Set("Authorization", "Bearer "+rawTokenA)
	recA2 := httptest.NewRecorder()

	srv.handleMCP(recA2, reqA2)
	if !strings.Contains(recA2.Body.String(), "tool not allowed") {
		t.Fatalf("expected tool not allowed error for unlisted tool, got: %s", recA2.Body.String())
	}
}

func TestToolCallErrorsAreAlwaysEncapsulatedAsMCPResults(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.Background()
	rawAgentToken := "wc_agent_test_encap"
	_ = createTestAgent(srv.store, ctx, Agent{
		ID:             "agent-encap",
		Name:           "Agent Encap",
		Enabled:        true,
		AgentTokenHash: hashSecret(rawAgentToken),
	}, "client-encap", "")

	rawToken, _ := generateSecret("wc_mcp_test_")
	_ = srv.store.CreateAccessToken(ctx, AccessToken{
		TokenHash: hashSecret(rawToken),
		ClientID:  "agent-encap",
		ExpiresAt: time.Now().Add(time.Hour),
	})

	// When calling a tool with no agent connected (which causes an agent call error/timeout)
	callBody := `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"exec_command","arguments":{"command":"dir","cwd":"C:/"}}}`
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(callBody))
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()

	srv.timeout = 100 * time.Millisecond
	srv.handleMCP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 OK for MCP tool call with error result, got %d", rec.Code)
	}

	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("failed to parse JSON response: %v", err)
	}

	if _, hasError := parsed["error"]; hasError {
		t.Fatalf("expected NO JSON-RPC error field in response, but got: %v", parsed["error"])
	}

	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object in JSON-RPC response, got: %v", parsed)
	}

	if result["isError"] != true {
		t.Fatalf("expected isError: true in result, got: %v", result["isError"])
	}
}

func TestRotateAgentTokenDisconnectsStream(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.Background()

	rawAgentToken := "wc_agent_test"
	_ = createTestAgent(srv.store, ctx, Agent{
		ID:             "agent-rot",
		Name:           "Agent Rot",
		Enabled:        true,
		AgentTokenHash: hashSecret(rawAgentToken),
	}, "client-rot", "")

	rt := srv.runtimeFor("agent-rot")
	streamCtx, stream, _ := rt.activateAgentStream(context.Background())
	defer rt.deactivateAgentStream(stream)

	if !rt.isOnline() {
		t.Fatal("expected agent to be online")
	}

	// Rotate agent token
	form := url.Values{}
	form.Set("csrf", srv.adminCSRF)
	req := httptest.NewRequest("POST", "/admin/agents/agent-rot/rotate-agent-token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "agent-rot")
	rec := httptest.NewRecorder()

	srv.handleAdminRotateAgentToken(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("rotate status = %d", rec.Code)
	}

	select {
	case <-streamCtx.Done():
		// Successfully disconnected
	case <-time.After(time.Second):
		t.Fatal("stream was not disconnected after token rotation")
	}

	// Old agent token must no longer authenticate
	oldReq := httptest.NewRequest("GET", "/agent/stream", nil)
	oldReq.Header.Set("Authorization", "Bearer "+rawAgentToken)
	_, _, err := srv.authenticateAgent(oldReq)
	if err == nil {
		t.Fatal("old agent token succeeded after rotation")
	}
}
