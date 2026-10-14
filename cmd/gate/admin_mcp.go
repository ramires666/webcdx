package main

import (
	"log"
	"net/http"
	"regexp"
	"strings"
)

type adminMCPData struct {
	PublicURL string
	CSRF      string
	Client    MCPClient
	Agents    []adminMCPAgentView
}

var windowsRootRegex = regexp.MustCompile(`^[a-zA-Z]:[\\/]`)

func validAllowedRoots(roots string) bool {
	if roots == "" || strings.ContainsAny(roots, "\r\n\x00\"") {
		return false
	}
	separator := ";"
	if strings.HasPrefix(roots, "/") {
		separator = ":"
	}
	for _, root := range strings.Split(roots, separator) {
		root = strings.TrimSpace(root)
		if root != "*" && !strings.HasPrefix(root, "/") && !strings.HasPrefix(root, `\\`) && !windowsRootRegex.MatchString(root) {
			return false
		}
	}
	return true
}

func (s *server) handleAdminCreateMCPClient(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, name := strings.TrimSpace(r.FormValue("id")), strings.TrimSpace(r.FormValue("name"))
	if !agentIDRegex.MatchString(id) {
		http.Error(w, "invalid MCP ID", http.StatusBadRequest)
		return
	}
	if name == "" {
		name = id
	}
	oauthID, err := generateSecret("wc_client_")
	if err != nil {
		http.Error(w, "generate client ID error", http.StatusInternalServerError)
		return
	}
	secret, err := generateSecret("wc_oauth_")
	if err != nil {
		http.Error(w, "generate secret error", http.StatusInternalServerError)
		return
	}
	client := MCPClient{ID: id, Name: name, Enabled: true, OAuthClientID: oauthID, OAuthClientSecretHash: hashSecret(secret)}
	if err := s.store.CreateMCPClient(r.Context(), client); err != nil {
		http.Error(w, "MCP ID already exists", http.StatusConflict)
		return
	}
	data := adminCreatedData{PublicURL: s.publicURL, CSRF: s.adminCSRF, Client: client, OAuthClientID: oauthID, OAuthClientSecret: secret}
	if err := renderAdminTemplate(w, "admin-created.html", data); err != nil {
		log.Printf("render MCP credentials: %v", err)
	}
}

func (s *server) handleAdminMCPClient(w http.ResponseWriter, r *http.Request) {
	client, err := s.store.GetMCPClient(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "MCP registration not found", http.StatusNotFound)
		return
	}
	agents, err := s.store.ListAgents(r.Context())
	if err != nil {
		http.Error(w, "failed to load agents", http.StatusInternalServerError)
		return
	}
	ids, err := s.store.ListMCPAgentIDs(r.Context(), client.ID)
	if err != nil {
		http.Error(w, "failed to load MCP access", http.StatusInternalServerError)
		return
	}
	granted := map[string]bool{}
	for _, id := range ids {
		granted[id] = true
	}
	data := adminMCPData{PublicURL: s.publicURL, CSRF: s.adminCSRF, Client: *client}
	for _, agent := range agents {
		data.Agents = append(data.Agents, adminMCPAgentView{Agent: agent, Granted: granted[agent.ID]})
	}
	if err := renderAdminTemplate(w, "admin-mcp.html", data); err != nil {
		log.Printf("render MCP card: %v", err)
	}
}

func (s *server) handleAdminMCPAgents(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if err := s.store.SetMCPAgents(r.Context(), id, r.PostForm["agent_id"]); err != nil {
		if err == errAgentNotFound {
			http.Error(w, "MCP registration or agent not found", http.StatusBadRequest)
		} else {
			log.Printf("update MCP access: %v", err)
			http.Error(w, "failed to update access", http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/admin/mcp-clients/"+id, http.StatusSeeOther)
}

func (s *server) handleAdminToggleMCPClient(w http.ResponseWriter, r *http.Request) {
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	client, err := s.store.GetMCPClient(r.Context(), id)
	if err != nil {
		http.Error(w, "MCP registration not found", http.StatusNotFound)
		return
	}
	if err := s.store.SetMCPClientEnabled(r.Context(), id, !client.Enabled); err != nil {
		http.Error(w, "failed to update MCP state", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/mcp-clients/"+id, http.StatusSeeOther)
}

func (s *server) handleAdminRotateOAuthSecret(w http.ResponseWriter, r *http.Request) {
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	client, err := s.store.GetMCPClient(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "MCP registration not found", http.StatusNotFound)
		return
	}
	secret, err := generateSecret("wc_oauth_")
	if err != nil {
		http.Error(w, "generate secret error", http.StatusInternalServerError)
		return
	}
	if err := s.store.UpdateOAuthSecretHash(r.Context(), client.ID, hashSecret(secret)); err != nil {
		http.Error(w, "failed to rotate secret", http.StatusInternalServerError)
		return
	}
	data := adminCreatedData{PublicURL: s.publicURL, CSRF: s.adminCSRF, Client: *client, OAuthClientID: client.OAuthClientID, OAuthClientSecret: secret}
	if err := renderAdminTemplate(w, "admin-created.html", data); err != nil {
		log.Printf("render MCP credentials: %v", err)
	}
}

func (s *server) handleAdminRevokeAccess(w http.ResponseWriter, r *http.Request) {
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if err := s.store.RevokeAccessTokens(r.Context(), id); err != nil {
		http.Error(w, "failed to revoke access", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/mcp-clients/"+id, http.StatusSeeOther)
}

func (s *server) handleAdminDeleteMCPClient(w http.ResponseWriter, r *http.Request) {
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := s.store.DeleteMCPClient(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, "failed to delete MCP registration", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *server) handleAdminRoots(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	roots := strings.TrimSpace(r.FormValue("allowed_roots"))
	if !validAllowedRoots(roots) {
		http.Error(w, "allowed roots must be absolute paths without quotes or newlines", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	if err := s.store.UpdateAllowedRoots(r.Context(), id, roots); err != nil {
		http.Error(w, "failed to save roots", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/agents/"+id, http.StatusSeeOther)
}
