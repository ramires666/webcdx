package main

import "time"

// Agent represents a registered execution agent (e.g. Windows PC, Linux VPS).
type Agent struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Enabled        bool       `json:"enabled"`
	AgentTokenHash string     `json:"-"`
	AllowedRoots   string     `json:"allowed_roots"`
	AllowedTools   string     `json:"allowed_tools"`
	DeniedTools    string     `json:"denied_tools"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastSeenAt     *time.Time `json:"last_seen_at,omitempty"`
}

type MCPClient struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	Enabled               bool      `json:"enabled"`
	OAuthClientID         string    `json:"oauth_client_id"`
	OAuthClientSecretHash string    `json:"-"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// AccessToken represents an active issued bearer token for MCP clients (ChatGPT).
type AccessToken struct {
	TokenHash string    `json:"token_hash"`
	ClientID  string    `json:"client_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// oauthCode represents a temporary single-use authorization code during OAuth flow.
type oauthCode struct {
	MCPClientID         string
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	ExpiresAt           time.Time
}
