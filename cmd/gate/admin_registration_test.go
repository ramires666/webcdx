package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"webcodex/internal/protocol"
)

func adminRegistrationRequest(t *testing.T, srv *server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	method := http.MethodGet
	if form != nil {
		method = http.MethodPost
	}
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(srv.adminUser, srv.adminPassword)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	return rec
}

func TestIndependentMCPAndWorkerRegistrationLifecycle(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	ctx := context.Background()
	created := adminRegistrationRequest(t, srv, "/admin/agents", url.Values{"csrf": {srv.adminCSRF}, "id": {"computer"}, "name": {"Computer"}})
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), "WEBCODEX_BROWSER_ENABLED=1") || !strings.Contains(created.Body.String(), `C:\\projects;W:\\`) {
		t.Fatalf("worker launch defaults missing: %d %s", created.Code, created.Body.String())
	}
	workerToken := regexp.MustCompile(`id="val-agent-token">([^<]+)`).FindStringSubmatch(created.Body.String())
	if len(workerToken) != 2 {
		t.Fatal("worker token missing")
	}
	clients, err := srv.store.ListMCPClients(ctx)
	if err != nil || len(clients) != 0 || strings.Contains(created.Body.String(), `id="val-client-secret"`) {
		t.Fatal("creating worker also registered OAuth client")
	}
	agent, err := srv.store.GetAgent(ctx, "computer")
	if err != nil || agent.AllowedRoots != protocol.DefaultAllowedRoots {
		t.Fatalf("default roots: %+v %v", agent, err)
	}
	page := adminRegistrationRequest(t, srv, "/admin/agents/computer", nil)
	if page.Code != http.StatusOK || strings.Contains(page.Body.String(), "OAuth Client ID") || strings.Contains(page.Body.String(), "rotate-oauth-secret") {
		t.Fatal("worker admin card contains MCP registration")
	}

	created = adminRegistrationRequest(t, srv, "/admin/mcp-clients", url.Values{"csrf": {srv.adminCSRF}, "id": {"shared"}, "name": {"Shared MCP"}})
	if created.Code != http.StatusOK || strings.Contains(created.Body.String(), `id="val-agent-token"`) {
		t.Fatalf("MCP registration: %d %s", created.Code, created.Body.String())
	}
	secret := regexp.MustCompile(`id="val-client-secret">([^<]+)`).FindStringSubmatch(created.Body.String())
	if len(secret) != 2 {
		t.Fatal("OAuth secret missing")
	}
	client, err := srv.store.GetMCPClient(ctx, "shared")
	if err != nil {
		t.Fatal(err)
	}
	token := exchangeOAuthToken(t, srv, client.OAuthClientID, secret[1], "https://chatgpt.com/oauth/callback")
	if len(groupMCPRequest(t, srv, token, "tools/call", "list_agents", map[string]any{})["result"].(map[string]any)["structuredContent"].(map[string]any)["agents"].([]any)) != 0 {
		t.Fatal("new MCP was granted computers implicitly")
	}
	grant := adminRegistrationRequest(t, srv, "/admin/mcp-clients/shared/agents", url.Values{"csrf": {srv.adminCSRF}, "agent_id": {"computer"}})
	if grant.Code != http.StatusSeeOther {
		t.Fatalf("grant status: %d", grant.Code)
	}
	if len(groupMCPRequest(t, srv, token, "tools/call", "list_agents", map[string]any{})["result"].(map[string]any)["structuredContent"].(map[string]any)["agents"].([]any)) != 1 {
		t.Fatal("MCP failed to list granted computer")
	}

	workerReq := httptest.NewRequest(http.MethodGet, "/agent/config", nil)
	workerReq.Header.Set("Authorization", "Bearer "+workerToken[1])
	if err := srv.store.SetAgentEnabled(ctx, "computer", false); err != nil {
		t.Fatal(err)
	}
	mcpReq := httptest.NewRequest(http.MethodPost, "/mcp/v5", nil)
	mcpReq.Header.Set("Authorization", "Bearer "+token)
	if _, err := srv.authenticateMCP(mcpReq); err != nil {
		t.Fatalf("worker disable disabled MCP: %v", err)
	}
	if err := srv.store.SetAgentEnabled(ctx, "computer", true); err != nil {
		t.Fatal(err)
	}
	if rec := adminRegistrationRequest(t, srv, "/admin/mcp-clients/shared/toggle", url.Values{"csrf": {srv.adminCSRF}}); rec.Code != http.StatusSeeOther {
		t.Fatal("MCP toggle failed")
	}
	if _, err := srv.authenticateMCP(mcpReq); err == nil {
		t.Fatal("disabled MCP token accepted")
	}
	if _, _, err := srv.authenticateAgent(workerReq); err != nil {
		t.Fatalf("MCP disable disabled worker: %v", err)
	}
	if rec := adminRegistrationRequest(t, srv, "/admin/mcp-clients/shared/toggle", url.Values{"csrf": {srv.adminCSRF}}); rec.Code != http.StatusSeeOther {
		t.Fatal("MCP enable failed")
	}
	if rec := adminRegistrationRequest(t, srv, "/admin/mcp-clients/shared/rotate-oauth-secret", url.Values{"csrf": {srv.adminCSRF}}); rec.Code != http.StatusOK {
		t.Fatal("MCP rotation failed")
	}
	if _, err := srv.authenticateMCP(mcpReq); err == nil {
		t.Fatal("OAuth rotation retained access token")
	}
	if _, _, err := srv.authenticateAgent(workerReq); err != nil {
		t.Fatalf("OAuth rotation broke worker token: %v", err)
	}
	if rec := adminRegistrationRequest(t, srv, "/admin/mcp-clients/shared/delete", url.Values{"csrf": {srv.adminCSRF}}); rec.Code != http.StatusSeeOther {
		t.Fatal("MCP deletion failed")
	}
	if _, _, err := srv.authenticateAgent(workerReq); err != nil {
		t.Fatalf("MCP deletion removed worker: %v", err)
	}
}

func TestWorkerConfigAndAllowedRootsValidation(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	createMCPGroupAgents(t, srv)
	for _, test := range []struct {
		roots string
		valid bool
	}{
		{`C:\projects;W:\`, true}, {`C:/projects;W:/`, true}, {`/srv/projects:/opt/work`, true}, {`\\server\share`, true}, {`*`, true},
		{`relative`, false}, {`C:projects`, false}, {``, false}, {"C:\\projects\nset X=1", false}, {`C:\projects" & echo bad`, false},
	} {
		if validAllowedRoots(test.roots) != test.valid {
			t.Fatalf("roots validation %q", test.roots)
		}
	}
	for _, token := range []string{"worker-home", "worker-work", "invalid"} {
		req := httptest.NewRequest(http.MethodGet, "/agent/config", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		if token == "invalid" {
			if rec.Code != http.StatusUnauthorized {
				t.Fatal("config accepted invalid worker token")
			}
		} else if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"allowed_roots":"C:\\projects;W:\\"`) {
			t.Fatalf("config: %d %s", rec.Code, rec.Body.String())
		}
	}
	roots := `C:\custom;W:\`
	for _, test := range []struct {
		csrf, value string
		status      int
	}{
		{"bad", roots, http.StatusForbidden}, {srv.adminCSRF, "relative", http.StatusBadRequest}, {srv.adminCSRF, roots, http.StatusSeeOther},
	} {
		rec := adminRegistrationRequest(t, srv, "/admin/agents/home/roots", url.Values{"csrf": {test.csrf}, "allowed_roots": {test.value}})
		if rec.Code != test.status {
			t.Fatalf("roots update: %d %s", rec.Code, rec.Body.String())
		}
	}
	agent, err := srv.store.GetAgent(context.Background(), "home")
	if err != nil || agent.AllowedRoots != roots {
		t.Fatalf("saved roots: %+v %v", agent, err)
	}
	rotated := adminRegistrationRequest(t, srv, "/admin/agents/home/rotate-agent-token", url.Values{"csrf": {srv.adminCSRF}})
	if rotated.Code != http.StatusOK || !strings.Contains(rotated.Body.String(), `const allowedRoots = "C:\\custom;W:\\"`) {
		t.Fatalf("downloaded script ignored saved roots: %d %s", rotated.Code, rotated.Body.String())
	}
}
