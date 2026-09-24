package main

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
)

// bearerToken extracts the token from the standard Authorization: Bearer <token> header.
func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, prefix))
}

// authenticateAgent authenticates an agent worker connection by its Bearer token.
func (s *server) authenticateAgent(r *http.Request) (*Agent, *agentRuntime, error) {
	token := bearerToken(r)
	if token == "" {
		return nil, nil, errors.New("missing bearer token")
	}

	hash := hashSecret(token)
	agent, err := s.store.FindAgentByAgentTokenHash(r.Context(), hash)
	if err != nil {
		return nil, nil, errors.New("invalid agent token")
	}

	if !agent.Enabled {
		return nil, nil, errors.New("agent disabled")
	}

	rt := s.runtimeFor(agent.ID)
	return agent, rt, nil
}

// authenticateMCP authenticates an incoming MCP client request by its issued access token.
func (s *server) authenticateMCP(r *http.Request) (*Agent, *agentRuntime, error) {
	token := bearerToken(r)
	if token == "" {
		return nil, nil, errors.New("missing bearer token")
	}

	hash := hashSecret(token)
	agent, accessToken, err := s.store.FindAgentByAccessTokenHash(r.Context(), hash)
	if err != nil {
		return nil, nil, errors.New("invalid access token")
	}

	if time.Now().UTC().After(accessToken.ExpiresAt) {
		return nil, nil, errors.New("access token expired")
	}

	if !agent.Enabled {
		return nil, nil, errors.New("agent disabled")
	}

	rt := s.runtimeFor(agent.ID)
	return agent, rt, nil
}

// adminAuth guards admin endpoints using HTTP Basic Authentication.
func (s *server) adminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != s.adminUser || !secureCompare(pass, s.adminPassword) {
			w.Header().Set("WWW-Authenticate", `Basic realm="WebCodex Admin"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// verifyPKCE validates an OAuth PKCE code verifier against the challenge.
func verifyPKCE(verifier, challenge, method string) bool {
	if verifier == "" || challenge == "" || method != "S256" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	calculated := base64.RawURLEncoding.EncodeToString(sum[:])
	return calculated == challenge
}
