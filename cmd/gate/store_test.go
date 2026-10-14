package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreCRUDAndRelations(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_webcodex.db")

	s, err := newStore(dbPath)
	if err != nil {
		t.Fatalf("newStore failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// 1. Create Agent
	agent := Agent{
		ID:             "home",
		Name:           "Home PC",
		Enabled:        true,
		AgentTokenHash: hashSecret("token_home"),
		AllowedTools:   "exec_command,read_file",
		DeniedTools:    "exec_command",
	}

	if err := createTestAgent(s, ctx, agent, "client_home", hashSecret("secret_home")); err != nil {
		t.Fatalf("CreateAgent failed: %v", err)
	}

	// 2. GetAgent
	fetched, err := s.GetAgent(ctx, "home")
	if err != nil {
		t.Fatalf("GetAgent failed: %v", err)
	}
	if fetched.Name != "Home PC" || !fetched.Enabled || fetched.AllowedTools != "exec_command,read_file" {
		t.Fatalf("unexpected fetched agent: %+v", fetched)
	}

	// 3. FindAgentByAgentTokenHash
	byToken, err := s.FindAgentByAgentTokenHash(ctx, hashSecret("token_home"))
	if err != nil {
		t.Fatalf("FindAgentByAgentTokenHash failed: %v", err)
	}
	if byToken.ID != "home" {
		t.Fatalf("byToken.ID = %q, want 'home'", byToken.ID)
	}

	// 4. FindMCPClientByOAuthClientID
	byClient, err := s.FindMCPClientByOAuthClientID(ctx, "client_home")
	if err != nil {
		t.Fatalf("FindMCPClientByOAuthClientID failed: %v", err)
	}
	if byClient.ID != "home" {
		t.Fatalf("byClient.ID = %q, want 'home'", byClient.ID)
	}

	// 5. Create AccessToken
	accessTokenHash := hashSecret("mcp_token_123")
	expiresAt := time.Now().Add(24 * time.Hour)
	if err := s.CreateAccessToken(ctx, AccessToken{
		TokenHash: accessTokenHash,
		ClientID:  "home",
		ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatalf("CreateAccessToken failed: %v", err)
	}

	// 6. FindMCPClientByAccessTokenHash
	agentWithToken, token, err := s.FindMCPClientByAccessTokenHash(ctx, accessTokenHash)
	if err != nil {
		t.Fatalf("FindMCPClientByAccessTokenHash failed: %v", err)
	}
	if agentWithToken.ID != "home" || token.ClientID != "home" {
		t.Fatalf("unexpected result: agent=%+v token=%+v", agentWithToken, token)
	}

	// 7. Update Agent Policy
	if err := s.UpdateToolPolicy(ctx, "home", "read_file", "write_file"); err != nil {
		t.Fatalf("UpdateToolPolicy failed: %v", err)
	}
	updated, _ := s.GetAgent(ctx, "home")
	if updated.AllowedTools != "read_file" || updated.DeniedTools != "write_file" {
		t.Fatalf("unexpected updated policy: %+v", updated)
	}

	// 8. Update Last Seen
	now := time.Now().UTC()
	if err := s.UpdateLastSeen(ctx, "home", now); err != nil {
		t.Fatalf("UpdateLastSeen failed: %v", err)
	}
	seenAgent, _ := s.GetAgent(ctx, "home")
	if seenAgent.LastSeenAt == nil {
		t.Fatalf("lastSeenAt is nil")
	}

	// 9. Revoke Access Tokens
	if err := s.RevokeAccessTokens(ctx, "home"); err != nil {
		t.Fatalf("RevokeAccessTokens failed: %v", err)
	}
	_, _, err = s.FindMCPClientByAccessTokenHash(ctx, accessTokenHash)
	if err == nil {
		t.Fatalf("expected error finding revoked access token")
	}

	// 10. Deleting a worker preserves the independent MCP registration and token.
	if err := s.CreateAccessToken(ctx, AccessToken{
		TokenHash: accessTokenHash,
		ClientID:  "home",
		ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatalf("CreateAccessToken failed: %v", err)
	}
	if err := s.DeleteAgent(ctx, "home"); err != nil {
		t.Fatalf("DeleteAgent failed: %v", err)
	}
	_, err = s.GetAgent(ctx, "home")
	if err == nil {
		t.Fatalf("expected agent to be deleted")
	}
	_, _, err = s.FindMCPClientByAccessTokenHash(ctx, accessTokenHash)
	if err != nil {
		t.Fatalf("deleting worker revoked MCP token: %v", err)
	}
	if err := s.DeleteMCPClient(ctx, "home"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FindMCPClientByAccessTokenHash(ctx, accessTokenHash); err == nil {
		t.Fatal("deleted MCP retained access token")
	}
}
