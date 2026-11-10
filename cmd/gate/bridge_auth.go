package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type bridgeGrant struct {
	ExtensionID  string
	AllowedTools []string
}

var (
	bridgeExtensionID  = regexp.MustCompile(`^[a-p]{32}$`)
	bridgeToolName     = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	bridgeDefaultTools = []string{"list_allowed_roots", "list_directory", "read_file", "search_files", "find_files", "list_agents"}
	bridgeToolNames    = map[string]bool{"list_allowed_roots": true, "list_directory": true, "read_file": true, "search_files": true, "find_files": true, "list_agents": true, "exec_command": true, "poll_command": true, "cancel_command": true, "list_commands": true, "write_file": true, "edit_file": true, "move_path": true, "delete_path": true}
)

func (s *store) initBridgeAuth(ctx context.Context) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS bridge_clients (client_id TEXT PRIMARY KEY REFERENCES mcp_clients(id) ON DELETE CASCADE, extension_id TEXT NOT NULL, allowed_tools TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS bridge_codes (code_hash TEXT PRIMARY KEY, client_id TEXT NOT NULL REFERENCES mcp_clients(id) ON DELETE CASCADE, redirect_uri TEXT NOT NULL, challenge TEXT NOT NULL, config_hash TEXT NOT NULL, expires_at DATETIME NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS bridge_token_grants (token_hash TEXT PRIMARY KEY REFERENCES mcp_access_tokens(token_hash) ON DELETE CASCADE, client_id TEXT NOT NULL REFERENCES mcp_clients(id) ON DELETE CASCADE, extension_id TEXT NOT NULL, allowed_tools TEXT NOT NULL)`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("initialize bridge auth: %w", err)
		}
	}
	return nil
}

func (s *store) loadBridgeGrant(ctx context.Context, rawToken string) (*bridgeGrant, error) {
	var grant bridgeGrant
	var rawTools, configured string
	err := s.db.QueryRowContext(ctx, `SELECT g.extension_id,g.allowed_tools,b.extension_id FROM bridge_token_grants g JOIN bridge_clients b ON b.client_id=g.client_id JOIN mcp_access_tokens t ON t.token_hash=g.token_hash WHERE g.token_hash=? AND t.expires_at>?`, hashSecret(rawToken), time.Now().UTC().Format(time.RFC3339Nano)).Scan(&grant.ExtensionID, &rawTools, &configured)
	if err != nil {
		return nil, err
	}
	if grant.ExtensionID != configured {
		return nil, sql.ErrNoRows
	}
	if err := json.Unmarshal([]byte(rawTools), &grant.AllowedTools); err != nil {
		return nil, err
	}
	return &grant, nil
}

func bridgeAllows(client *MCPClient, name string) bool {
	if client.BridgeGrant == nil {
		return true
	}
	for _, allowed := range client.BridgeGrant.AllowedTools {
		if name == allowed {
			return true
		}
	}
	return false
}

func (s *server) handleAdminBridgeConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	client, err := s.store.GetMCPClient(r.Context(), id)
	if err != nil {
		http.Error(w, "MCP registration not found", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		if !s.verifyCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		extension := strings.TrimSpace(r.FormValue("extension_id"))
		tools := strings.FieldsFunc(r.FormValue("allowed_tools"), func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' })
		seen := map[string]bool{}
		if extension != "" && !bridgeExtensionID.MatchString(extension) {
			http.Error(w, "invalid extension ID", http.StatusBadRequest)
			return
		}
		for _, tool := range tools {
			if !bridgeToolName.MatchString(tool) || !bridgeToolNames[tool] || seen[tool] {
				http.Error(w, "invalid tool list", http.StatusBadRequest)
				return
			}
			seen[tool] = true
		}
		if extension != "" && len(tools) == 0 {
			http.Error(w, "select at least one tool", http.StatusBadRequest)
			return
		}
		encoded, _ := json.Marshal(tools)
		tx, err := s.store.db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "failed to save configuration", 500)
			return
		}
		defer tx.Rollback()
		if extension == "" {
			_, err = tx.ExecContext(r.Context(), `DELETE FROM bridge_clients WHERE client_id=?`, id)
		} else {
			_, err = tx.ExecContext(r.Context(), `INSERT INTO bridge_clients(client_id,extension_id,allowed_tools) VALUES(?,?,?) ON CONFLICT(client_id) DO UPDATE SET extension_id=excluded.extension_id,allowed_tools=excluded.allowed_tools`, id, extension, string(encoded))
		}
		if err == nil {
			_, err = tx.ExecContext(r.Context(), `DELETE FROM mcp_access_tokens WHERE client_id=? AND token_hash IN (SELECT token_hash FROM bridge_token_grants WHERE client_id=?)`, id, id)
		}
		if err == nil {
			_, err = tx.ExecContext(r.Context(), `DELETE FROM bridge_codes WHERE client_id=?`, id)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			http.Error(w, "failed to save configuration", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/mcp-clients/"+url.PathEscape(id)+"/bridge", http.StatusSeeOther)
		return
	}
	var extension, toolsJSON string
	err = s.store.db.QueryRowContext(r.Context(), `SELECT extension_id,allowed_tools FROM bridge_clients WHERE client_id=?`, id).Scan(&extension, &toolsJSON)
	tools := strings.Join(bridgeDefaultTools, ",")
	if err == nil {
		var configured []string
		_ = json.Unmarshal([]byte(toolsJSON), &configured)
		tools = strings.Join(configured, ",")
	} else if err != sql.ErrNoRows {
		http.Error(w, "failed to load configuration", 500)
		return
	}
	if err := renderAdminTemplate(w, "admin-bridge.html", map[string]any{"Client": client, "ExtensionID": extension, "AllowedTools": tools, "RedirectURI": bridgeRedirect(extension), "CSRF": s.adminCSRF}); err != nil {
		http.Error(w, "internal template error", 500)
	}
}

func bridgeRedirect(extension string) string {
	if extension == "" {
		return ""
	}
	return "https://" + extension + ".chromiumapp.org/webcodex"
}

func bridgeConfigHash(extension, toolsJSON string) string {
	sum := sha256.Sum256([]byte(extension + "\x00" + toolsJSON))
	return hex.EncodeToString(sum[:])
}

func (s *server) handleBridgeAuthorize(w http.ResponseWriter, r *http.Request) {
	bridgeNoStore(w)
	if !s.allowRequest("bridge-authorize:"+remoteHost(r), 30, time.Minute) {
		http.Error(w, "too many requests", 429)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	params := r.URL.Query()
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		params = r.Form
	}
	client, err := s.store.FindMCPClientByOAuthClientID(r.Context(), params.Get("client_id"))
	if err != nil || !client.Enabled {
		http.Error(w, "unknown or disabled client", 401)
		return
	}
	var extension, toolsJSON string
	if err = s.store.db.QueryRowContext(r.Context(), `SELECT extension_id,allowed_tools FROM bridge_clients WHERE client_id=?`, client.ID).Scan(&extension, &toolsJSON); err != nil {
		http.Error(w, "bridge is not configured", 400)
		return
	}
	redirect := bridgeRedirect(extension)
	if params.Get("redirect_uri") != redirect || params.Get("response_type") != "code" {
		http.Error(w, "invalid bridge authorization request", 400)
		return
	}
	challenge, state := params.Get("code_challenge"), params.Get("state")
	decodedChallenge, challengeErr := base64.RawURLEncoding.DecodeString(challenge)
	if params.Get("code_challenge_method") != "S256" || challengeErr != nil || len(decodedChallenge) != 32 || len(challenge) != 43 || len(state) == 0 || len(state) > 1024 {
		http.Error(w, "PKCE S256 and state are required", 400)
		return
	}
	if r.Method == http.MethodGet {
		var tools []string
		_ = json.Unmarshal([]byte(toolsJSON), &tools)
		if err := renderAdminTemplate(w, "admin-bridge-consent.html", map[string]any{"Client": client, "ExtensionID": extension, "AllowedTools": tools, "CSRF": s.adminCSRF, "ClientID": client.OAuthClientID, "RedirectURI": redirect, "Challenge": challenge, "State": state, "ConfigHash": bridgeConfigHash(extension, toolsJSON)}); err != nil {
			http.Error(w, "internal template error", 500)
		}
		return
	}
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	configHash := bridgeConfigHash(extension, toolsJSON)
	if params.Get("config_hash") != configHash {
		http.Error(w, "bridge configuration changed; restart authorization", http.StatusBadRequest)
		return
	}
	target, _ := url.Parse(redirect)
	q := target.Query()
	q.Set("state", state)
	if params.Get("decision") == "deny" {
		q.Set("error", "access_denied")
	} else if params.Get("decision") == "allow" {
		code, e := generateSecret("wc_bridge_code_")
		if e != nil {
			http.Error(w, "failed to create authorization code", 500)
			return
		}
		tx, beginErr := s.store.db.BeginTx(r.Context(), nil)
		if beginErr != nil {
			http.Error(w, "failed to create authorization code", 500)
			return
		}
		defer tx.Rollback()
		var latestExtension, latestTools string
		if e = tx.QueryRowContext(r.Context(), `SELECT extension_id,allowed_tools FROM bridge_clients WHERE client_id=?`, client.ID).Scan(&latestExtension, &latestTools); e != nil || bridgeConfigHash(latestExtension, latestTools) != configHash {
			http.Error(w, "bridge configuration changed; restart authorization", 400)
			return
		}
		_, e = tx.ExecContext(r.Context(), `DELETE FROM bridge_codes WHERE expires_at<=?`, time.Now().UTC().Format(time.RFC3339Nano))
		if e == nil {
			_, e = tx.ExecContext(r.Context(), `INSERT INTO bridge_codes(code_hash,client_id,redirect_uri,challenge,config_hash,expires_at) VALUES(?,?,?,?,?,?)`, hashSecret(code), client.ID, redirect, challenge, configHash, time.Now().Add(5*time.Minute).UTC().Format(time.RFC3339Nano))
		}
		if e == nil {
			e = tx.Commit()
		}
		if e != nil {
			http.Error(w, "failed to create authorization code", 500)
			return
		}
		q.Set("code", code)
	} else {
		http.Error(w, "invalid consent decision", 400)
		return
	}
	q.Set("iss", s.publicURL)
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (s *server) handleBridgeToken(w http.ResponseWriter, r *http.Request) {
	bridgeNoStore(w)
	if !s.allowRequest("bridge-token:"+remoteHost(r), 30, time.Minute) {
		http.Error(w, "too many requests", 429)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	p, err := tokenParams(r)
	if err != nil || p["grant_type"] != "authorization_code" {
		http.Error(w, "bad token request", 400)
		return
	}
	client, err := s.store.FindMCPClientByOAuthClientID(r.Context(), p["client_id"])
	if err != nil || !client.Enabled {
		http.Error(w, "unknown or disabled client", 401)
		return
	}
	var extension, toolsJSON string
	if err = s.store.db.QueryRowContext(r.Context(), `SELECT extension_id,allowed_tools FROM bridge_clients WHERE client_id=?`, client.ID).Scan(&extension, &toolsJSON); err != nil {
		http.Error(w, "bridge is not configured", 400)
		return
	}
	redirect := bridgeRedirect(extension)
	if p["redirect_uri"] != redirect {
		http.Error(w, "redirect_uri mismatch", 400)
		return
	}
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "token exchange failed", 500)
		return
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(r.Context(), `SELECT extension_id,allowed_tools FROM bridge_clients WHERE client_id=?`, client.ID).Scan(&extension, &toolsJSON); err != nil || bridgeRedirect(extension) != redirect {
		http.Error(w, "bridge configuration changed", http.StatusBadRequest)
		return
	}
	var codeClient, challenge, codeRedirect, codeConfigHash string
	var expiry string
	err = tx.QueryRowContext(r.Context(), `SELECT client_id,redirect_uri,challenge,config_hash,expires_at FROM bridge_codes WHERE code_hash=?`, hashSecret(p["code"])).Scan(&codeClient, &codeRedirect, &challenge, &codeConfigHash, &expiry)
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `DELETE FROM bridge_codes WHERE code_hash=?`, hashSecret(p["code"]))
	}
	if err != nil || codeClient != client.ID || codeRedirect != redirect || codeConfigHash != bridgeConfigHash(extension, toolsJSON) || !parseDBTime(expiry).After(time.Now()) || !verifyPKCE(p["code_verifier"], challenge, "S256") {
		http.Error(w, "invalid authorization code", 400)
		return
	}
	raw, err := generateSecret("wc_bridge_")
	if err != nil {
		http.Error(w, "token exchange failed", 500)
		return
	}
	var tools []string
	if err = json.Unmarshal([]byte(toolsJSON), &tools); err != nil {
		http.Error(w, "token exchange failed", 500)
		return
	}
	encoded, _ := json.Marshal(tools)
	ttl := durationEnv("WEBCODEX_ACCESS_TOKEN_TTL", 24*time.Hour)
	if ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	now := time.Now().UTC()
	hash := hashSecret(raw)
	_, err = tx.ExecContext(r.Context(), `INSERT INTO mcp_access_tokens(token_hash,client_id,expires_at,created_at) VALUES(?,?,?,?)`, hash, client.ID, now.Add(ttl).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO bridge_token_grants(token_hash,client_id,extension_id,allowed_tools) VALUES(?,?,?,?)`, hash, client.ID, extension, string(encoded))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		http.Error(w, "token exchange failed", 500)
		return
	}
	writeJSON(w, map[string]any{"access_token": raw, "token_type": "Bearer", "expires_in": int(ttl.Seconds()), "scope": "mcp"})
}

func bridgeNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
}
