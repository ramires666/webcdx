package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"webcodex/internal/protocol"
)

func TestLegacyMCPMigrationPreservesCredentialsAndTokens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE agents (id TEXT PRIMARY KEY, name TEXT NOT NULL, enabled INTEGER NOT NULL,
		 agent_token_hash TEXT NOT NULL UNIQUE, oauth_client_id TEXT NOT NULL UNIQUE, oauth_client_secret_hash TEXT NOT NULL,
		 allowed_tools TEXT NOT NULL, denied_tools TEXT NOT NULL, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, last_seen_at DATETIME)`,
		`CREATE TABLE access_tokens (token_hash TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE, expires_at DATETIME NOT NULL, created_at DATETIME NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO agents VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)`, "home", "Home", hashSecret("worker"), "old-oauth-id", hashSecret("old-secret"), "read_file", "write_file", now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO access_tokens VALUES (?, ?, ?, ?)`, hashSecret("live-token"), "home", time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), now); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := newStore(path)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.FindAgentByAgentTokenHash(ctx, hashSecret("worker"))
	if err != nil || agent.AllowedRoots != protocol.DefaultAllowedRoots || agent.AllowedTools != "read_file" || agent.DeniedTools != "write_file" || agent.LastSeenAt == nil {
		t.Fatalf("worker migration: %+v %v", agent, err)
	}
	client, token, err := store.FindMCPClientByAccessTokenHash(ctx, hashSecret("live-token"))
	if err != nil || client.OAuthClientID != "old-oauth-id" || client.OAuthClientSecretHash != hashSecret("old-secret") || token.ClientID != "home" {
		t.Fatalf("MCP migration: %+v %+v %v", client, token, err)
	}
	ids, err := store.ListMCPAgentIDs(ctx, client.ID)
	if err != nil || !reflect.DeepEqual(ids, []string{"home"}) {
		t.Fatalf("migrated grants: %v %v", ids, err)
	}
	if err := store.RevokeAccessTokens(ctx, "home"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = newStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.FindMCPClientByAccessTokenHash(ctx, hashSecret("live-token")); err == nil {
		t.Fatal("restart resurrected revoked legacy token")
	}
	clients, err := store.ListMCPClients(ctx)
	if err != nil || len(clients) != 1 {
		t.Fatalf("migration duplicated clients: %v %v", clients, err)
	}
}
