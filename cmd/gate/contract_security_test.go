package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"webcodex/internal/protocol"
)

func TestInitializeMetadataAndBodyLimit(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	token := "wc_mcp_contract"
	if err := srv.store.CreateAgent(context.Background(), Agent{
		ID: "contract", Name: "Contract", Enabled: true, AgentTokenHash: hashSecret("agent"), OAuthClientID: "contract-client",
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateAccessToken(context.Background(), AccessToken{
		TokenHash: hashSecret(token), AgentID: "contract", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	srv.handleMCP(recorder, request)
	lower := strings.ToLower(recorder.Body.String())
	if recorder.Code != http.StatusOK || !strings.Contains(lower, `"name":"local-workspace"`) || strings.Contains(lower, `"resources"`) {
		t.Fatalf("initialize status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{"codex", "model", "reasoning", "thinking", "threadid", `"prompt"`} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("initialize contains %q: %s", forbidden, recorder.Body.String())
		}
	}

	large := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("x", maxMCPRequestBytes+1)))
	large.Header.Set("Authorization", "Bearer "+token)
	largeRecorder := httptest.NewRecorder()
	srv.handleMCP(largeRecorder, large)
	if largeRecorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status = %d", largeRecorder.Code)
	}

	unauthorized := httptest.NewRequest(http.MethodPost, "/mcp/v3", strings.NewReader(`{}`))
	unauthorizedRecorder := httptest.NewRecorder()
	srv.handleMCP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized || !strings.Contains(unauthorizedRecorder.Header().Get("WWW-Authenticate"), "/mcp/v3") {
		t.Fatalf("unauthorized status=%d header=%q", unauthorizedRecorder.Code, unauthorizedRecorder.Header().Get("WWW-Authenticate"))
	}
	v4Unauthorized := httptest.NewRequest(http.MethodPost, "/mcp/v4", strings.NewReader(`{}`))
	v4Recorder := httptest.NewRecorder()
	srv.handleMCP(v4Recorder, v4Unauthorized)
	if v4Recorder.Code != http.StatusUnauthorized || !strings.Contains(v4Recorder.Header().Get("WWW-Authenticate"), "/mcp/v4") || !strings.Contains(v4Recorder.Header().Get("WWW-Authenticate"), `scope="mcp"`) {
		t.Fatalf("v4 unauthorized status=%d header=%q", v4Recorder.Code, v4Recorder.Header().Get("WWW-Authenticate"))
	}
	resource := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp/v4", nil)
	resourceRecorder := httptest.NewRecorder()
	srv.handleProtectedResource(resourceRecorder, resource)
	if !strings.Contains(resourceRecorder.Body.String(), `"resource":"`+srv.publicURL+`/mcp/v4"`) || !strings.Contains(resourceRecorder.Body.String(), `"scopes_supported":["mcp"]`) {
		t.Fatalf("v4 protected resource: %s", resourceRecorder.Body.String())
	}
	metadata := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	metadataRecorder := httptest.NewRecorder()
	srv.handleOAuthServer(metadataRecorder, metadata)
	if !strings.Contains(metadataRecorder.Body.String(), `"authorization_response_iss_parameter_supported":true`) {
		t.Fatalf("OAuth metadata lacks issuer identification: %s", metadataRecorder.Body.String())
	}
	openid := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
	openidRecorder := httptest.NewRecorder()
	srv.routes().ServeHTTP(openidRecorder, openid)
	if openidRecorder.Code != http.StatusNotFound {
		t.Fatalf("non-OIDC server published OIDC metadata: status=%d", openidRecorder.Code)
	}

	largeResultBody := `{"id":"` + strings.Repeat("x", maxAgentResultBytes) + `"}`
	largeResult := httptest.NewRequest(http.MethodPost, "/agent/result", strings.NewReader(largeResultBody))
	largeResult.Header.Set("Authorization", "Bearer agent")
	largeResultRecorder := httptest.NewRecorder()
	srv.handleAgentResult(largeResultRecorder, largeResult)
	if largeResultRecorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large agent result status = %d", largeResultRecorder.Code)
	}
}

func TestOAuthRequiresKnownRedirectAndS256(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	if err := srv.store.CreateAgent(context.Background(), Agent{
		ID: "oauth-security", Name: "OAuth", Enabled: true, AgentTokenHash: hashSecret("agent"), OAuthClientID: "oauth-security-client",
	}); err != nil {
		t.Fatal(err)
	}
	base := "/oauth/authorize?client_id=oauth-security-client&response_type=code&code_challenge=abc&code_challenge_method=S256&redirect_uri="
	badRedirect := httptest.NewRequest(http.MethodGet, base+url.QueryEscape("https://evil.example/callback"), nil)
	badRecorder := httptest.NewRecorder()
	srv.handleAuthorize(badRecorder, badRedirect)
	if badRecorder.Code != http.StatusBadRequest {
		t.Fatalf("bad redirect status = %d", badRecorder.Code)
	}
	callbackRedirect := httptest.NewRequest(http.MethodGet,
		base+url.QueryEscape("https://chatgpt.com/connector/oauth/callback-id"), nil)
	callbackRecorder := httptest.NewRecorder()
	srv.handleAuthorize(callbackRecorder, callbackRedirect)
	if callbackRecorder.Code != http.StatusFound {
		t.Fatalf("callback-specific redirect status = %d body=%q", callbackRecorder.Code, callbackRecorder.Body.String())
	}
	callbackLocation, err := url.Parse(callbackRecorder.Header().Get("Location"))
	if err != nil || callbackLocation.Query().Get("iss") != srv.publicURL {
		t.Fatalf("callback-specific redirect issuer: location=%q err=%v", callbackRecorder.Header().Get("Location"), err)
	}
	if allowedOAuthRedirect("https://chatgpt.com/connector/oauth/a/b") {
		t.Fatal("nested callback path accepted")
	}
	plain := httptest.NewRequest(http.MethodGet,
		"/oauth/authorize?client_id=oauth-security-client&response_type=code&redirect_uri="+url.QueryEscape("https://chatgpt.com/oauth/callback")+"&code_challenge=abc&code_challenge_method=plain", nil)
	plainRecorder := httptest.NewRecorder()
	srv.handleAuthorize(plainRecorder, plain)
	plainLocation, err := url.Parse(plainRecorder.Header().Get("Location"))
	if plainRecorder.Code != http.StatusFound || err != nil || plainLocation.Query().Get("error") != "invalid_request" || plainLocation.Query().Get("iss") != srv.publicURL || verifyPKCE("abc", "abc", "plain") {
		t.Fatalf("plain PKCE accepted: status=%d", plainRecorder.Code)
	}
}

func TestExpiredRequestCannotEnterQueue(t *testing.T) {
	runtimeState := newAgentRuntime("expired")
	_, stream, _ := runtimeState.activateAgentStream(context.Background())
	defer runtimeState.deactivateAgentStream(stream)
	err := runtimeState.enqueue(context.Background(), protocol.AgentRequest{
		ID: "expired", Request: []byte(`{"jsonrpc":"2.0"}`), Deadline: time.Now().Add(-time.Second),
	})
	if err == nil || len(runtimeState.queue) != 0 {
		t.Fatalf("expired request queued: err=%v len=%d", err, len(runtimeState.queue))
	}
}
