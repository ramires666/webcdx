package main

import (
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var agentIDRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type adminAgentView struct {
	Agent              Agent
	Online             bool
	LastSeenFormatted  string
	CreatedAtFormatted string
}

type adminIndexData struct {
	PublicURL string
	CSRF      string
	Agents    []adminAgentView
}

type adminAgentData struct {
	PublicURL          string
	CSRF               string
	Agent              Agent
	Online             bool
	LastSeenFormatted  string
	CreatedAtFormatted string
}

type adminCreatedData struct {
	PublicURL         string
	CSRF              string
	Agent             Agent
	AgentToken        string
	OAuthClientID     string
	OAuthClientSecret string
}

func (s *server) verifyCSRF(r *http.Request) bool {
	token := r.FormValue("csrf")
	return token != "" && secureCompare(token, s.adminCSRF)
}

func (s *server) handleAdminIndex(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgents(r.Context())
	if err != nil {
		log.Printf("admin list agents error: %v", err)
		http.Error(w, "failed to load agents", http.StatusInternalServerError)
		return
	}

	views := make([]adminAgentView, 0, len(agents))
	for _, a := range agents {
		online := false
		var lastSeen time.Time
		if a.LastSeenAt != nil {
			lastSeen = *a.LastSeenAt
		}

		if rt := s.existingRuntime(a.ID); rt != nil {
			rtOnline, rtLastSeen := rt.status()
			if rtOnline {
				online = true
			}
			if !rtLastSeen.IsZero() && (lastSeen.IsZero() || rtLastSeen.After(lastSeen)) {
				lastSeen = rtLastSeen
			}
		}

		views = append(views, adminAgentView{
			Agent:              a,
			Online:             online,
			LastSeenFormatted:  formatTimeRelative(lastSeen),
			CreatedAtFormatted: a.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	data := adminIndexData{
		PublicURL: s.publicURL,
		CSRF:      s.adminCSRF,
		Agents:    views,
	}

	if err := renderAdminTemplate(w, "admin-index.html", data); err != nil {
		log.Printf("render admin index error: %v", err)
		http.Error(w, "internal template error", http.StatusInternalServerError)
	}
}

func (s *server) handleAdminCreateAgent(w http.ResponseWriter, r *http.Request) {
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}

	id := strings.TrimSpace(r.FormValue("id"))
	name := strings.TrimSpace(r.FormValue("name"))

	if !agentIDRegex.MatchString(id) {
		http.Error(w, "invalid agent ID (must match ^[a-z0-9][a-z0-9_-]{0,63}$)", http.StatusBadRequest)
		return
	}
	if name == "" {
		name = id
	}

	rawAgentToken, err := generateSecret("wc_agent_")
	if err != nil {
		http.Error(w, "generate secret error", http.StatusInternalServerError)
		return
	}

	rawClientID, err := generateSecret("wc_client_")
	if err != nil {
		http.Error(w, "generate secret error", http.StatusInternalServerError)
		return
	}

	rawOAuthSecret, err := generateSecret("wc_oauth_")
	if err != nil {
		http.Error(w, "generate secret error", http.StatusInternalServerError)
		return
	}

	agent := Agent{
		ID:                    id,
		Name:                  name,
		Enabled:               true,
		AgentTokenHash:        hashSecret(rawAgentToken),
		OAuthClientID:         rawClientID,
		OAuthClientSecretHash: hashSecret(rawOAuthSecret),
		AllowedTools:          "",
		DeniedTools:           "edit_file,move_path,delete_path",
	}

	if err := s.store.CreateAgent(r.Context(), agent); err != nil {
		log.Printf("create agent in store error: %v", err)
		http.Error(w, "agent ID or client ID already exists", http.StatusConflict)
		return
	}

	data := adminCreatedData{
		PublicURL:         s.publicURL,
		CSRF:              s.adminCSRF,
		Agent:             agent,
		AgentToken:        rawAgentToken,
		OAuthClientID:     rawClientID,
		OAuthClientSecret: rawOAuthSecret,
	}

	if err := renderAdminTemplate(w, "admin-created.html", data); err != nil {
		log.Printf("render admin created error: %v", err)
		http.Error(w, "internal template error", http.StatusInternalServerError)
	}
}

func (s *server) handleAdminAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, err := s.store.GetAgent(r.Context(), id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	online := false
	var lastSeen time.Time
	if agent.LastSeenAt != nil {
		lastSeen = *agent.LastSeenAt
	}
	if rt := s.existingRuntime(agent.ID); rt != nil {
		rtOnline, rtLastSeen := rt.status()
		if rtOnline {
			online = true
		}
		if !rtLastSeen.IsZero() && (lastSeen.IsZero() || rtLastSeen.After(lastSeen)) {
			lastSeen = rtLastSeen
		}
	}

	data := adminAgentData{
		PublicURL:          s.publicURL,
		CSRF:               s.adminCSRF,
		Agent:              *agent,
		Online:             online,
		LastSeenFormatted:  formatTimeRelative(lastSeen),
		CreatedAtFormatted: agent.CreatedAt.Format("2006-01-02 15:04:05"),
	}

	if err := renderAdminTemplate(w, "admin-agent.html", data); err != nil {
		log.Printf("render admin agent error: %v", err)
		http.Error(w, "internal template error", http.StatusInternalServerError)
	}
}

func (s *server) handleAdminToggleAgent(w http.ResponseWriter, r *http.Request) {
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	agent, err := s.store.GetAgent(r.Context(), id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	newEnabled := !agent.Enabled
	if err := s.store.SetAgentEnabled(r.Context(), id, newEnabled); err != nil {
		log.Printf("toggle agent error: %v", err)
		http.Error(w, "failed to update agent state", http.StatusInternalServerError)
		return
	}

	if !newEnabled {
		if rt := s.existingRuntime(id); rt != nil {
			rt.disconnect()
		}
	}

	http.Redirect(w, r, "/admin/agents/"+id, http.StatusSeeOther)
}

func (s *server) handleAdminRotateAgentToken(w http.ResponseWriter, r *http.Request) {
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	agent, err := s.store.GetAgent(r.Context(), id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	rawAgentToken, err := generateSecret("wc_agent_")
	if err != nil {
		http.Error(w, "generate secret error", http.StatusInternalServerError)
		return
	}

	if err := s.store.UpdateAgentTokenHash(r.Context(), id, hashSecret(rawAgentToken)); err != nil {
		log.Printf("update agent token hash error: %v", err)
		http.Error(w, "failed to rotate agent token", http.StatusInternalServerError)
		return
	}

	if rt := s.existingRuntime(id); rt != nil {
		rt.disconnect()
	}

	data := adminCreatedData{
		PublicURL:  s.publicURL,
		CSRF:       s.adminCSRF,
		Agent:      *agent,
		AgentToken: rawAgentToken,
	}

	if err := renderAdminTemplate(w, "admin-created.html", data); err != nil {
		log.Printf("render admin created error: %v", err)
		http.Error(w, "internal template error", http.StatusInternalServerError)
	}
}

func (s *server) handleAdminRotateOAuthSecret(w http.ResponseWriter, r *http.Request) {
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	agent, err := s.store.GetAgent(r.Context(), id)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	rawOAuthSecret, err := generateSecret("wc_oauth_")
	if err != nil {
		http.Error(w, "generate secret error", http.StatusInternalServerError)
		return
	}

	if err := s.store.UpdateOAuthSecretHash(r.Context(), id, hashSecret(rawOAuthSecret)); err != nil {
		log.Printf("update oauth secret hash error: %v", err)
		http.Error(w, "failed to rotate oauth secret", http.StatusInternalServerError)
		return
	}

	// Revoke existing access tokens for this agent
	if err := s.store.RevokeAccessTokens(r.Context(), id); err != nil {
		log.Printf("revoke access tokens on secret rotation error: %v", err)
	}

	data := adminCreatedData{
		PublicURL:         s.publicURL,
		CSRF:              s.adminCSRF,
		Agent:             *agent,
		OAuthClientID:     agent.OAuthClientID,
		OAuthClientSecret: rawOAuthSecret,
	}

	if err := renderAdminTemplate(w, "admin-created.html", data); err != nil {
		log.Printf("render admin created error: %v", err)
		http.Error(w, "internal template error", http.StatusInternalServerError)
	}
}

func (s *server) handleAdminRevokeAccess(w http.ResponseWriter, r *http.Request) {
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	if err := s.store.RevokeAccessTokens(r.Context(), id); err != nil {
		log.Printf("revoke access tokens error: %v", err)
		http.Error(w, "failed to revoke access tokens", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/agents/"+id, http.StatusSeeOther)
}

func (s *server) handleAdminPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	allowed := strings.TrimSpace(r.FormValue("allowed_tools"))
	denied := strings.TrimSpace(r.FormValue("denied_tools"))

	if err := s.store.UpdateToolPolicy(r.Context(), id, allowed, denied); err != nil {
		log.Printf("update tool policy error: %v", err)
		http.Error(w, "failed to update tool policy", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/agents/"+id, http.StatusSeeOther)
}

func (s *server) handleAdminDeleteAgent(w http.ResponseWriter, r *http.Request) {
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	if rt := s.existingRuntime(id); rt != nil {
		rt.disconnect()
	}

	if err := s.store.DeleteAgent(r.Context(), id); err != nil {
		log.Printf("delete agent error: %v", err)
		http.Error(w, "failed to delete agent", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func formatTimeRelative(t time.Time) string {
	if t.IsZero() {
		return "никогда"
	}
	diff := time.Since(t)
	if diff < 10*time.Second {
		return "только что"
	}
	if diff < time.Minute {
		return fmt.Sprintf("%d сек. назад", int(diff.Seconds()))
	}
	if diff < time.Hour {
		return fmt.Sprintf("%d мин. назад", int(diff.Minutes()))
	}
	if diff < 24*time.Hour {
		return fmt.Sprintf("%d ч. назад", int(diff.Hours()))
	}
	return t.Format("2006-01-02 15:04")
}
