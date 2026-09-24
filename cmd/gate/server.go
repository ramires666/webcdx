package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// server owns the multi-agent routing, persistence store, OAuth registry, and admin handlers.
type server struct {
	publicURL string
	timeout   time.Duration
	toolCards bool

	store *store

	runtimeMu sync.RWMutex
	runtimes  map[string]*agentRuntime

	adminUser     string
	adminPassword string
	adminCSRF     string

	oauthMu    sync.Mutex
	oauthCodes map[string]oauthCode
}

// newServer builds the gate server from environment variables and initializes SQLite store.
func newServer() (*server, error) {
	dbPath := env("WEBCODEX_DB_PATH", "/data/webcodex.db")
	db, err := newStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("initialize database: %w", err)
	}

	csrf, err := generateSecret("csrf_")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("generate csrf secret: %w", err)
	}

	adminUser := env("WEBCODEX_ADMIN_USER", "admin")
	adminPass := env("WEBCODEX_ADMIN_PASSWORD", "")
	if adminPass == "" {
		_ = db.Close()
		return nil, errors.New("WEBCODEX_ADMIN_PASSWORD is required")
	}

	publicURL := strings.TrimRight(env("WEBCODEX_PUBLIC_URL", "https://codex.grom.world"), "/")
	if publicURL == "" {
		publicURL = "http://" + env("WEBCODEX_ADDR", "127.0.0.1:8080")
	}

	srv := &server{
		publicURL:     publicURL,
		timeout:       durationEnv("WEBCODEX_CALL_TIMEOUT", 20*time.Minute),
		toolCards:     boolEnv("WEBCODEX_TOOL_CARDS", false),
		store:         db,
		runtimes:      make(map[string]*agentRuntime),
		adminUser:     adminUser,
		adminPassword: adminPass,
		adminCSRF:     csrf,
		oauthCodes:    make(map[string]oauthCode),
	}

	// Clean up expired tokens on startup
	go func() {
		_ = srv.store.DeleteExpiredAccessTokens(context.Background(), time.Now().UTC())
	}()

	return srv, nil
}

func (s *server) runtimeFor(agentID string) *agentRuntime {
	s.runtimeMu.RLock()
	rt := s.runtimes[agentID]
	s.runtimeMu.RUnlock()

	if rt != nil {
		return rt
	}

	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	if rt = s.runtimes[agentID]; rt != nil {
		return rt
	}

	rt = newAgentRuntime(agentID)
	s.runtimes[agentID] = rt
	return rt
}

func (s *server) existingRuntime(agentID string) *agentRuntime {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.runtimes[agentID]
}

// withCORS adds Cross-Origin Resource Sharing headers required for ChatGPT Web and MCP discovery.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Mcp-Session-Id, Mcp-Protocol-Version, X-Requested-With, Origin")
		w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate, Mcp-Protocol-Version, Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// routes declares every public and administrative endpoint served by the gate.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	// Root endpoint
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"service": "WebCodex Gate",
			"status":  "ok",
			"admin":   "/admin",
			"mcp":     "/mcp",
		})
	})

	// MCP endpoints
	mux.HandleFunc("GET /mcp", s.handleMCP)
	mux.HandleFunc("POST /mcp", s.handleMCP)
	mux.HandleFunc("DELETE /mcp", s.handleMCP)
	mux.HandleFunc("HEAD /mcp", s.handleMCP)
	mux.HandleFunc("GET /mcp/v2", s.handleMCP)
	mux.HandleFunc("POST /mcp/v2", s.handleMCP)
	mux.HandleFunc("DELETE /mcp/v2", s.handleMCP)
	mux.HandleFunc("HEAD /mcp/v2", s.handleMCP)

	// OAuth discovery & metadata
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.handleProtectedResource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.handleProtectedResource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp/v2", s.handleProtectedResource)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleOAuthServer)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/mcp", s.handleOAuthServer)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/mcp/v2", s.handleOAuthServer)
	mux.HandleFunc("GET /.well-known/openid-configuration", s.handleOAuthServer)

	// OAuth authorization & token exchange
	mux.HandleFunc("GET /oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("POST /oauth/token", s.handleToken)

	// Agent connection endpoints
	mux.HandleFunc("GET /agent/stream", s.handleAgentStream)
	mux.HandleFunc("POST /agent/result", s.handleAgentResult)

	// Health check (checks SQLite availability)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := s.store.db.PingContext(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Admin Web UI (protected by Basic Auth)
	mux.HandleFunc("GET /admin", s.adminAuth(s.handleAdminIndex))
	mux.HandleFunc("POST /admin/agents", s.adminAuth(s.handleAdminCreateAgent))
	mux.HandleFunc("GET /admin/agents/{id}", s.adminAuth(s.handleAdminAgent))
	mux.HandleFunc("POST /admin/agents/{id}/toggle", s.adminAuth(s.handleAdminToggleAgent))
	mux.HandleFunc("POST /admin/agents/{id}/rotate-agent-token", s.adminAuth(s.handleAdminRotateAgentToken))
	mux.HandleFunc("POST /admin/agents/{id}/rotate-oauth-secret", s.adminAuth(s.handleAdminRotateOAuthSecret))
	mux.HandleFunc("POST /admin/agents/{id}/revoke-access", s.adminAuth(s.handleAdminRevokeAccess))
	mux.HandleFunc("POST /admin/agents/{id}/policy", s.adminAuth(s.handleAdminPolicy))
	mux.HandleFunc("POST /admin/agents/{id}/delete", s.adminAuth(s.handleAdminDeleteAgent))

	return withCORS(mux)
}
