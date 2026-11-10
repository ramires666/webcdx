package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBridgeAllows(t *testing.T) {
	client := &MCPClient{}
	if !bridgeAllows(client, "anything") {
		t.Fatal("legacy client should keep its existing access")
	}
	client.BridgeGrant = &bridgeGrant{}
	if bridgeAllows(client, "list_agents") {
		t.Fatal("empty bridge grant must deny all tools")
	}
	client.BridgeGrant.AllowedTools = []string{"read_file", "list_agents"}
	if !bridgeAllows(client, "read_file") || bridgeAllows(client, "delete_path") {
		t.Fatal("bridge tool allowlist was not enforced")
	}
}

func TestBridgeDefaultsAreReadOnly(t *testing.T) {
	for _, tool := range bridgeDefaultTools {
		if tool == "" || !bridgeToolName.MatchString(tool) {
			t.Fatalf("invalid default tool %q", tool)
		}
		if tool == "edit_file" || tool == "move_path" || tool == "delete_path" {
			t.Fatalf("write tool in bridge defaults: %q", tool)
		}
	}
}

func TestBridgeConsentAndSingleUseToken(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	ctx := context.Background()
	if err := srv.store.initBridgeAuth(ctx); err != nil {
		t.Fatal(err)
	}
	if err := createTestAgent(srv.store, ctx, Agent{ID: "bridge", Name: "Bridge client", Enabled: true, AgentTokenHash: hashSecret("worker")}, "oauth-bridge", ""); err != nil {
		t.Fatal(err)
	}
	extension := "abcdefghijklmnopabcdefghijklmnop"
	tools, _ := json.Marshal(bridgeDefaultTools)
	if _, err := srv.store.db.Exec(`INSERT INTO bridge_clients(client_id,extension_id,allowed_tools) VALUES(?,?,?)`, "bridge", extension, string(tools)); err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	redirect := bridgeRedirect(extension)
	query := url.Values{"client_id": {"oauth-bridge"}, "redirect_uri": {redirect}, "response_type": {"code"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"opaque-state"}}
	get := httptest.NewRequest(http.MethodGet, "/bridge/authorize?"+query.Encode(), nil)
	get.SetBasicAuth(srv.adminUser, srv.adminPassword)
	page := httptest.NewRecorder()
	srv.adminAuth(srv.handleBridgeAuthorize)(page, get)
	if page.Code != http.StatusOK || page.Header().Get("Cache-Control") != "no-store" || !strings.Contains(page.Body.String(), "opaque-state") {
		t.Fatalf("consent page: status=%d headers=%v body=%s", page.Code, page.Header(), page.Body.String())
	}
	wrongCallback := cloneValues(query)
	wrongCallback.Set("redirect_uri", "https://attacker.example/callback")
	badCallback := httptest.NewRequest(http.MethodGet, "/bridge/authorize?"+wrongCallback.Encode(), nil)
	badCallbackRec := httptest.NewRecorder()
	srv.handleBridgeAuthorize(badCallbackRec, badCallback)
	if badCallbackRec.Code != http.StatusBadRequest {
		t.Fatalf("wrong callback status=%d", badCallbackRec.Code)
	}
	form := url.Values{"csrf": {srv.adminCSRF}, "client_id": {"oauth-bridge"}, "redirect_uri": {redirect}, "response_type": {"code"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"opaque-state"}, "decision": {"allow"}, "config_hash": {bridgeConfigHash(extension, string(tools))}}
	post := httptest.NewRequest(http.MethodPost, "/bridge/authorize", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.SetBasicAuth(srv.adminUser, srv.adminPassword)
	approved := httptest.NewRecorder()
	srv.adminAuth(srv.handleBridgeAuthorize)(approved, post)
	if approved.Code != http.StatusFound {
		t.Fatalf("consent POST: status=%d body=%s", approved.Code, approved.Body.String())
	}
	code := approved.Header().Get("Location")
	parsed, _ := url.Parse(code)
	codeValue := parsed.Query().Get("code")
	tokenForm := url.Values{"grant_type": {"authorization_code"}, "client_id": {"oauth-bridge"}, "redirect_uri": {redirect}, "code": {codeValue}, "code_verifier": {verifier}}
	exchange := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/bridge/token", strings.NewReader(tokenForm.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		srv.handleBridgeToken(rec, req)
		return rec
	}
	badVerifierForm := cloneValues(tokenForm)
	badVerifierForm.Set("code_verifier", strings.Repeat("x", 43))
	badVerifierReq := httptest.NewRequest(http.MethodPost, "/bridge/token", strings.NewReader(badVerifierForm.Encode()))
	badVerifierReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badVerifier := httptest.NewRecorder()
	srv.handleBridgeToken(badVerifier, badVerifierReq)
	if badVerifier.Code != http.StatusBadRequest {
		t.Fatalf("bad PKCE verifier status=%d", badVerifier.Code)
	}
	issued := exchange()
	if issued.Code != http.StatusOK {
		t.Fatalf("token exchange: status=%d body=%s", issued.Code, issued.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(issued.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	raw, ok := result["access_token"].(string)
	if !ok || !strings.HasPrefix(raw, "wc_bridge_") {
		t.Fatalf("invalid bridge token response: %s", issued.Body.String())
	}
	grant, err := srv.store.loadBridgeGrant(ctx, raw)
	if err != nil || grant.ExtensionID != extension || len(grant.AllowedTools) != len(bridgeDefaultTools) {
		t.Fatalf("grant=%+v err=%v", grant, err)
	}
	if replay := exchange(); replay.Code != http.StatusBadRequest {
		t.Fatalf("authorization code replay status=%d", replay.Code)
	}
	legacyToken := "wc_mcp_legacy"
	if err := srv.store.CreateAccessToken(ctx, AccessToken{TokenHash: hashSecret(legacyToken), ClientID: "bridge", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	config := url.Values{"csrf": {srv.adminCSRF}, "extension_id": {extension}, "allowed_tools": {strings.Join(bridgeDefaultTools, ",")}}
	configReq := httptest.NewRequest(http.MethodPost, "/admin/mcp-clients/bridge/bridge", strings.NewReader(config.Encode()))
	configReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	configReq.SetPathValue("id", "bridge")
	configRec := httptest.NewRecorder()
	srv.handleAdminBridgeConfig(configRec, configReq)
	if configRec.Code != http.StatusSeeOther {
		t.Fatalf("bridge config save: status=%d body=%s", configRec.Code, configRec.Body.String())
	}
	if _, _, err := srv.store.FindMCPClientByAccessTokenHash(ctx, hashSecret(legacyToken)); err != nil {
		t.Fatal("bridge config update revoked a legacy token")
	}
	if _, err := srv.store.loadBridgeGrant(ctx, raw); err == nil {
		t.Fatal("grant remained valid after bridge configuration update")
	}
	if _, err := srv.store.db.Exec(`DELETE FROM bridge_clients WHERE client_id=?`, "bridge"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.loadBridgeGrant(ctx, raw); err == nil {
		t.Fatal("grant remained valid after bridge configuration removal")
	}
}

func TestBridgeAuthorizationRequiresLoginCSRFAndCurrentConfig(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	ctx := context.Background()
	if err := srv.store.initBridgeAuth(ctx); err != nil {
		t.Fatal(err)
	}
	if err := createTestAgent(srv.store, ctx, Agent{ID: "bridge", Name: "Bridge", Enabled: true, AgentTokenHash: hashSecret("worker")}, "oauth-bridge", ""); err != nil {
		t.Fatal(err)
	}
	extension := "abcdefghijklmnopabcdefghijklmnop"
	tools, _ := json.Marshal(bridgeDefaultTools)
	_, _ = srv.store.db.Exec(`INSERT INTO bridge_clients(client_id,extension_id,allowed_tools) VALUES(?,?,?)`, "bridge", extension, string(tools))
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	params := url.Values{"client_id": {"oauth-bridge"}, "redirect_uri": {bridgeRedirect(extension)}, "response_type": {"code"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"state"}}
	unauth := httptest.NewRecorder()
	srv.adminAuth(srv.handleBridgeAuthorize)(unauth, httptest.NewRequest(http.MethodGet, "/bridge/authorize?"+params.Encode(), nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated authorize status=%d", unauth.Code)
	}
	badChallenge := cloneValues(params)
	badChallenge.Set("code_challenge", strings.Repeat("!", 43))
	badChallengeRec := httptest.NewRecorder()
	srv.handleBridgeAuthorize(badChallengeRec, httptest.NewRequest(http.MethodGet, "/bridge/authorize?"+badChallenge.Encode(), nil))
	if badChallengeRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid challenge status=%d", badChallengeRec.Code)
	}
	pageReq := httptest.NewRequest(http.MethodGet, "/bridge/authorize?"+params.Encode(), nil)
	pageReq.SetBasicAuth(srv.adminUser, srv.adminPassword)
	page := httptest.NewRecorder()
	srv.adminAuth(srv.handleBridgeAuthorize)(page, pageReq)
	staleHash := bridgeConfigHash(extension, string(tools))
	changed, _ := json.Marshal([]string{"read_file"})
	_, _ = srv.store.db.Exec(`UPDATE bridge_clients SET allowed_tools=? WHERE client_id=?`, string(changed), "bridge")
	post := url.Values{"csrf": {srv.adminCSRF}, "client_id": {"oauth-bridge"}, "redirect_uri": {bridgeRedirect(extension)}, "response_type": {"code"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"state"}, "decision": {"allow"}, "config_hash": {staleHash}}
	req := httptest.NewRequest(http.MethodPost, "/bridge/authorize", strings.NewReader(post.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(srv.adminUser, srv.adminPassword)
	denied := httptest.NewRecorder()
	srv.adminAuth(srv.handleBridgeAuthorize)(denied, req)
	if denied.Code != http.StatusBadRequest {
		t.Fatalf("stale consent config status=%d", denied.Code)
	}
	post.Set("config_hash", bridgeConfigHash(extension, string(changed)))
	post.Del("csrf")
	req = httptest.NewRequest(http.MethodPost, "/bridge/authorize", strings.NewReader(post.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(srv.adminUser, srv.adminPassword)
	noCSRF := httptest.NewRecorder()
	srv.adminAuth(srv.handleBridgeAuthorize)(noCSRF, req)
	if noCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", noCSRF.Code)
	}
	for _, entry := range []struct {
		raw, config string
		expiry      time.Time
	}{
		{"expired-bridge-code", bridgeConfigHash(extension, string(changed)), time.Now().Add(-time.Second)},
		{"stale-bridge-code", staleHash, time.Now().Add(time.Minute)},
	} {
		if _, err := srv.store.db.Exec(`INSERT INTO bridge_codes(code_hash,client_id,redirect_uri,challenge,config_hash,expires_at) VALUES(?,?,?,?,?,?)`, hashSecret(entry.raw), "bridge", bridgeRedirect(extension), challenge, entry.config, entry.expiry.UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		form := url.Values{"grant_type": {"authorization_code"}, "client_id": {"oauth-bridge"}, "redirect_uri": {bridgeRedirect(extension)}, "code": {entry.raw}, "code_verifier": {verifier}}
		tokenReq := httptest.NewRequest(http.MethodPost, "/bridge/token", strings.NewReader(form.Encode()))
		tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		tokenRec := httptest.NewRecorder()
		srv.handleBridgeToken(tokenRec, tokenReq)
		if tokenRec.Code != http.StatusBadRequest {
			t.Fatalf("expired or stale authorization code status=%d", tokenRec.Code)
		}
	}
	if err := srv.store.SetMCPClientEnabled(ctx, "bridge", false); err != nil {
		t.Fatal(err)
	}
	disabled := httptest.NewRecorder()
	srv.handleBridgeAuthorize(disabled, httptest.NewRequest(http.MethodGet, "/bridge/authorize?"+params.Encode(), nil))
	if disabled.Code != http.StatusUnauthorized {
		t.Fatalf("disabled client status=%d", disabled.Code)
	}
}

func cloneValues(source url.Values) url.Values {
	copy := make(url.Values, len(source))
	for key, values := range source {
		copy[key] = append([]string(nil), values...)
	}
	return copy
}
