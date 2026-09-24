package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *server) handleProtectedResource(w http.ResponseWriter, r *http.Request) {
	log.Printf("oauth protected-resource %s %s ua=%q", r.Method, r.URL.Path, r.UserAgent())
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mcpPath := "/mcp"
	if strings.HasSuffix(r.URL.Path, "/mcp/v2") {
		mcpPath = "/mcp/v2"
	} else if strings.HasSuffix(r.URL.Path, "/mcp/v3") {
		mcpPath = "/mcp/v3"
	} else if strings.HasSuffix(r.URL.Path, "/mcp/v4") {
		mcpPath = "/mcp/v4"
	}
	writeJSON(w, map[string]any{
		"resource":              s.publicURL + mcpPath,
		"authorization_servers": []string{s.publicURL},
		"scopes_supported":      []string{"mcp"},
	})
}

func (s *server) handleOAuthServer(w http.ResponseWriter, r *http.Request) {
	log.Printf("oauth server-metadata %s %s ua=%q", r.Method, r.URL.Path, r.UserAgent())
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{
		"issuer": s.publicURL,
		"authorization_response_iss_parameter_supported": true,
		"authorization_endpoint":                         s.publicURL + "/oauth/authorize",
		"token_endpoint":                                 s.publicURL + "/oauth/token",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"client_secret_post", "client_secret_basic"},
		"scopes_supported":                               []string{"mcp"},
	})
}

// handleAuthorize validates the client ID, produces a single-use authorization code, and redirects.
func (s *server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.allowRequest("authorize:"+remoteHost(r), 30, time.Minute) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	clientID := r.URL.Query().Get("client_id")
	redirectURI := r.URL.Query().Get("redirect_uri")

	log.Printf(
		"oauth authorize client_id=%q redirect_uri=%q ua=%q",
		clientID,
		redirectURI,
		r.UserAgent(),
	)

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if clientID == "" {
		http.Error(w, "missing client_id", http.StatusBadRequest)
		return
	}

	agent, err := s.store.FindAgentByOAuthClientID(r.Context(), clientID)
	if err != nil || !agent.Enabled {
		http.Error(w, "unknown or disabled client", http.StatusUnauthorized)
		return
	}

	if redirectURI == "" {
		http.Error(w, "missing redirect_uri", http.StatusBadRequest)
		return
	}
	if !allowedOAuthRedirect(redirectURI) {
		http.Error(w, "redirect_uri is not allowed", http.StatusBadRequest)
		return
	}

	target, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("response_type") != "code" {
		s.redirectOAuthError(w, r, target, "unsupported_response_type", "response_type must be code")
		return
	}
	challenge := r.URL.Query().Get("code_challenge")
	if challenge == "" || r.URL.Query().Get("code_challenge_method") != "S256" {
		s.redirectOAuthError(w, r, target, "invalid_request", "PKCE S256 is required")
		return
	}

	code, err := generateSecret("wc_code_")
	if err != nil {
		s.redirectOAuthError(w, r, target, "server_error", "generate code error")
		return
	}

	entry := oauthCode{
		AgentID:             agent.ID,
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		ExpiresAt:           time.Now().Add(5 * time.Minute),
	}

	s.oauthMu.Lock()
	s.oauthCodes[code] = entry
	s.oauthMu.Unlock()

	query := target.Query()
	query.Set("code", code)
	query.Set("iss", s.publicURL)
	if state := r.URL.Query().Get("state"); state != "" {
		query.Set("state", state)
	}
	target.RawQuery = query.Encode()

	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (s *server) redirectOAuthError(w http.ResponseWriter, r *http.Request, target *url.URL, code, description string) {
	query := target.Query()
	query.Set("error", code)
	query.Set("error_description", description)
	query.Set("iss", s.publicURL)
	if state := r.URL.Query().Get("state"); state != "" {
		query.Set("state", state)
	}
	target.RawQuery = query.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

// handleToken exchanges an authorization code for a long-lived MCP Bearer token.
func (s *server) handleToken(w http.ResponseWriter, r *http.Request) {
	if !s.allowRequest("token:"+remoteHost(r), 30, time.Minute) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	log.Printf("oauth token %s content_type=%q ua=%q", r.Method, r.Header.Get("Content-Type"), r.UserAgent())
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)

	params, err := tokenParams(r)
	if err != nil {
		http.Error(w, "bad token request", http.StatusBadRequest)
		return
	}

	clientID, clientSecret := params["client_id"], params["client_secret"]
	if authID, authSecret, ok := r.BasicAuth(); ok {
		clientID, clientSecret = authID, authSecret
	}

	if clientID == "" || clientSecret == "" {
		http.Error(w, "missing client credentials", http.StatusUnauthorized)
		return
	}

	agent, err := s.store.FindAgentByOAuthClientID(r.Context(), clientID)
	if err != nil || !agent.Enabled {
		http.Error(w, "unknown or disabled client", http.StatusUnauthorized)
		return
	}

	secretHash := hashSecret(clientSecret)
	if !secureCompare(secretHash, agent.OAuthClientSecretHash) {
		http.Error(w, "bad client secret", http.StatusUnauthorized)
		return
	}

	if params["grant_type"] != "authorization_code" {
		http.Error(w, "unsupported grant_type", http.StatusBadRequest)
		return
	}

	codeVal := params["code"]
	if codeVal == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	s.oauthMu.Lock()
	entry, exists := s.oauthCodes[codeVal]
	delete(s.oauthCodes, codeVal)
	s.oauthMu.Unlock()

	if !exists || time.Now().After(entry.ExpiresAt) || entry.ClientID != clientID || entry.AgentID != agent.ID {
		http.Error(w, "invalid or expired authorization code", http.StatusBadRequest)
		return
	}

	if params["redirect_uri"] == "" || params["redirect_uri"] != entry.RedirectURI {
		http.Error(w, "redirect_uri mismatch", http.StatusBadRequest)
		return
	}

	verifier := params["code_verifier"]
	if !verifyPKCE(verifier, entry.CodeChallenge, entry.CodeChallengeMethod) {
		http.Error(w, "invalid code_verifier", http.StatusBadRequest)
		return
	}

	rawToken, err := generateSecret("wc_mcp_")
	if err != nil {
		http.Error(w, "generate access token error", http.StatusInternalServerError)
		return
	}

	tokenHash := hashSecret(rawToken)
	tokenTTL := durationEnv("WEBCODEX_ACCESS_TOKEN_TTL", 24*time.Hour)
	expiresAt := time.Now().Add(tokenTTL)

	if err := s.store.CreateAccessToken(r.Context(), AccessToken{
		TokenHash: tokenHash,
		AgentID:   agent.ID,
		ExpiresAt: expiresAt,
	}); err != nil {
		log.Printf("create access token in store error: %v", err)
		http.Error(w, "failed to persist access token", http.StatusInternalServerError)
		return
	}

	log.Printf("oauth token issued agent=%s client_id=%s", agent.ID, clientID)
	writeJSON(w, map[string]any{
		"access_token": rawToken,
		"token_type":   "Bearer",
		"expires_in":   int(tokenTTL.Seconds()),
		"scope":        "mcp",
	})
}

func allowedOAuthRedirect(value string) bool {
	switch value {
	case "https://chatgpt.com/oauth/callback", "https://chatgpt.com/connector_platform_oauth_redirect":
		return true
	}
	target, err := url.Parse(value)
	if err != nil || target.Scheme != "https" || !strings.EqualFold(target.Host, "chatgpt.com") || target.RawQuery != "" || target.Fragment != "" {
		return false
	}
	callbackID := strings.TrimPrefix(target.Path, "/connector/oauth/")
	return callbackID != target.Path && callbackID != "" && !strings.Contains(callbackID, "/")
}

func tokenParams(r *http.Request) (map[string]string, error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var values map[string]string
		if err := json.NewDecoder(r.Body).Decode(&values); err != nil {
			return nil, fmt.Errorf("decode token request: %w", err)
		}
		return values, nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("parse token form: %w", err)
	}
	values := make(map[string]string, len(r.Form))
	for key := range r.Form {
		values[key] = r.Form.Get(key)
	}
	return values, nil
}
