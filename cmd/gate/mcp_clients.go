package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Move legacy per-agent OAuth registrations and live tokens without changing credentials.
func (s *store) migrateLegacyClients(ctx context.Context) error {
	var legacy int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('agents') WHERE name = 'oauth_client_id'`).Scan(&legacy); err != nil {
		return err
	}
	if legacy == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`INSERT INTO mcp_clients SELECT id, name, enabled, oauth_client_id, oauth_client_secret_hash, created_at, updated_at FROM agents`,
		`INSERT INTO mcp_access_tokens SELECT token_hash, agent_id, expires_at, created_at FROM access_tokens`,
		`DROP TABLE access_tokens`,
		`CREATE TABLE agents_v2 (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
			agent_token_hash TEXT NOT NULL UNIQUE, allowed_roots TEXT NOT NULL DEFAULT 'C:\projects;W:\',
			allowed_tools TEXT NOT NULL DEFAULT '', denied_tools TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, last_seen_at DATETIME
		)`,
		`INSERT INTO agents_v2 (id, name, enabled, agent_token_hash, allowed_tools, denied_tools, created_at, updated_at, last_seen_at)
			SELECT id, name, enabled, agent_token_hash, allowed_tools, denied_tools, created_at, updated_at, last_seen_at FROM agents`,
		`DROP TABLE agents`,
		`ALTER TABLE agents_v2 RENAME TO agents`,
		`INSERT INTO mcp_client_agents SELECT id, id FROM agents`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate MCP registrations: %w", err)
		}
	}
	return tx.Commit()
}

const mcpClientColumns = `id, name, enabled, oauth_client_id, oauth_client_secret_hash, created_at, updated_at`

func scanMCPClient(row interface{ Scan(...any) error }) (*MCPClient, error) {
	var client MCPClient
	var enabled int
	var created, updated string
	if err := row.Scan(&client.ID, &client.Name, &enabled, &client.OAuthClientID, &client.OAuthClientSecretHash, &created, &updated); err != nil {
		return nil, err
	}
	client.Enabled = enabled != 0
	client.CreatedAt, client.UpdatedAt = parseDBTime(created), parseDBTime(updated)
	return &client, nil
}

func (s *store) CreateMCPClient(ctx context.Context, client MCPClient) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO mcp_clients (`+mcpClientColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		client.ID, client.Name, client.Enabled, client.OAuthClientID, client.OAuthClientSecretHash, now, now)
	return err
}

func (s *store) GetMCPClient(ctx context.Context, id string) (*MCPClient, error) {
	return scanMCPClient(s.db.QueryRowContext(ctx, `SELECT `+mcpClientColumns+` FROM mcp_clients WHERE id = ?`, id))
}

func (s *store) FindMCPClientByOAuthClientID(ctx context.Context, id string) (*MCPClient, error) {
	return scanMCPClient(s.db.QueryRowContext(ctx, `SELECT `+mcpClientColumns+` FROM mcp_clients WHERE oauth_client_id = ?`, id))
}

func (s *store) ListMCPClients(ctx context.Context) ([]MCPClient, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+mcpClientColumns+` FROM mcp_clients ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clients := []MCPClient{}
	for rows.Next() {
		client, err := scanMCPClient(rows)
		if err != nil {
			return nil, err
		}
		clients = append(clients, *client)
	}
	return clients, rows.Err()
}

func (s *store) FindMCPClientByAccessTokenHash(ctx context.Context, hash string) (*MCPClient, *AccessToken, error) {
	var token AccessToken
	var expiry, created string
	if err := s.db.QueryRowContext(ctx, `SELECT token_hash, client_id, expires_at, created_at FROM mcp_access_tokens WHERE token_hash = ?`, hash).
		Scan(&token.TokenHash, &token.ClientID, &expiry, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, errAccessTokenNotFound
		}
		return nil, nil, err
	}
	token.ExpiresAt, token.CreatedAt = parseDBTime(expiry), parseDBTime(created)
	client, err := s.GetMCPClient(ctx, token.ClientID)
	return client, &token, err
}

func (s *store) SetMCPClientEnabled(ctx context.Context, id string, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mcp_clients SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *store) UpdateOAuthSecretHash(ctx context.Context, id, hash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE mcp_clients SET oauth_client_secret_hash = ?, updated_at = ? WHERE id = ?`, hash, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mcp_access_tokens WHERE client_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *store) DeleteMCPClient(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mcp_clients WHERE id = ?`, id)
	return err
}

func (s *store) UpdateAllowedRoots(ctx context.Context, id, roots string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET allowed_roots = ?, updated_at = ? WHERE id = ?`, roots, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}
