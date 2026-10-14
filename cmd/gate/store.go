package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"webcodex/internal/protocol"

	_ "modernc.org/sqlite"
)

var (
	errAgentNotFound       = errors.New("agent not found")
	errAccessTokenNotFound = errors.New("access token not found")
)

type store struct {
	db *sql.DB
}

func newStore(path string) (*store, error) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Keep foreign-key enforcement and other connection-local PRAGMAs on every query.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	s := &store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

func (s *store) Close() error {
	return s.db.Close()
}

func (s *store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON;`); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode = WAL;`); err != nil {
		return fmt.Errorf("set wal mode: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout = 5000;`); err != nil {
		return fmt.Errorf("set busy timeout: %w", err)
	}

	statements := []string{
		`CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			agent_token_hash TEXT NOT NULL UNIQUE,
			allowed_roots TEXT NOT NULL DEFAULT 'C:\projects;W:\',
			allowed_tools TEXT NOT NULL DEFAULT '',
			denied_tools TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			last_seen_at DATETIME
		);`,
		`CREATE TABLE IF NOT EXISTS mcp_clients (
            id TEXT PRIMARY KEY, name TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
            oauth_client_id TEXT NOT NULL UNIQUE, oauth_client_secret_hash TEXT NOT NULL,
            created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL
        );`,
		`CREATE TABLE IF NOT EXISTS mcp_access_tokens (
            token_hash TEXT PRIMARY KEY, client_id TEXT NOT NULL REFERENCES mcp_clients(id) ON DELETE CASCADE,
            expires_at DATETIME NOT NULL, created_at DATETIME NOT NULL
        );`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_tokens_client ON mcp_access_tokens(client_id);`,
		`CREATE INDEX IF NOT EXISTS idx_mcp_tokens_expires ON mcp_access_tokens(expires_at);`,
		`CREATE TABLE IF NOT EXISTS mcp_client_agents (
            client_id TEXT NOT NULL REFERENCES mcp_clients(id) ON DELETE CASCADE,
            agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
            PRIMARY KEY (client_id, agent_id)
        );`,
	}

	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("database migration failed: %w", err)
		}
	}
	return s.migrateLegacyClients(ctx)
}

func (s *store) CreateAgent(ctx context.Context, agent Agent) error {
	now := time.Now().UTC()
	if agent.AllowedRoots == "" {
		agent.AllowedRoots = protocol.DefaultAllowedRoots
	}
	if agent.CreatedAt.IsZero() {
		agent.CreatedAt = now
	}
	if agent.UpdatedAt.IsZero() {
		agent.UpdatedAt = now
	}

	enabledInt := 0
	if agent.Enabled {
		enabledInt = 1
	}

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO agents (
			id, name, enabled, agent_token_hash,
			allowed_roots,
			allowed_tools, denied_tools, created_at, updated_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agent.ID,
		agent.Name,
		enabledInt,
		agent.AgentTokenHash,
		agent.AllowedRoots,
		agent.AllowedTools,
		agent.DeniedTools,
		agent.CreatedAt.UTC().Format(time.RFC3339Nano),
		agent.UpdatedAt.UTC().Format(time.RFC3339Nano),
		formatNullableTime(agent.LastSeenAt),
	)
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}
	return nil
}

func (s *store) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, name, enabled, agent_token_hash,
		        allowed_roots,
		        allowed_tools, denied_tools, created_at, updated_at, last_seen_at
		 FROM agents ORDER BY name ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()

	var agents []Agent
	for rows.Next() {
		var a Agent
		var enabledInt int
		var createdAtStr, updatedAtStr string
		var lastSeenStr sql.NullString

		if err := rows.Scan(
			&a.ID,
			&a.Name,
			&enabledInt,
			&a.AgentTokenHash,
			&a.AllowedRoots,
			&a.AllowedTools,
			&a.DeniedTools,
			&createdAtStr,
			&updatedAtStr,
			&lastSeenStr,
		); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		a.Enabled = enabledInt == 1
		a.CreatedAt = parseDBTime(createdAtStr)
		a.UpdatedAt = parseDBTime(updatedAtStr)
		if lastSeenStr.Valid && lastSeenStr.String != "" {
			t := parseDBTime(lastSeenStr.String)
			a.LastSeenAt = &t
		}
		agents = append(agents, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return agents, nil
}

func (s *store) GetAgent(ctx context.Context, id string) (*Agent, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, name, enabled, agent_token_hash,
		        allowed_roots,
		        allowed_tools, denied_tools, created_at, updated_at, last_seen_at
		 FROM agents WHERE id = ? LIMIT 1`,
		id,
	)
	return scanAgent(row)
}

func (s *store) FindAgentByAgentTokenHash(ctx context.Context, hash string) (*Agent, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, name, enabled, agent_token_hash,
		        allowed_roots,
		        allowed_tools, denied_tools, created_at, updated_at, last_seen_at
		 FROM agents WHERE agent_token_hash = ? LIMIT 1`,
		hash,
	)
	return scanAgent(row)
}

func (s *store) SetAgentEnabled(ctx context.Context, id string, enabled bool) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE agents SET enabled = ?, updated_at = ? WHERE id = ?`,
		enabledInt,
		nowStr,
		id,
	)
	if err != nil {
		return fmt.Errorf("set agent enabled: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return errAgentNotFound
	}
	return nil
}

func (s *store) UpdateAgentTokenHash(ctx context.Context, id string, tokenHash string) error {
	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE agents SET agent_token_hash = ?, updated_at = ? WHERE id = ?`,
		tokenHash,
		nowStr,
		id,
	)
	if err != nil {
		return fmt.Errorf("update agent token hash: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return errAgentNotFound
	}
	return nil
}

func (s *store) UpdateToolPolicy(ctx context.Context, id string, allowedTools, deniedTools string) error {
	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE agents SET allowed_tools = ?, denied_tools = ?, updated_at = ? WHERE id = ?`,
		allowedTools,
		deniedTools,
		nowStr,
		id,
	)
	if err != nil {
		return fmt.Errorf("update tool policy: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return errAgentNotFound
	}
	return nil
}

func (s *store) DeleteAgent(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return errAgentNotFound
	}
	return nil
}

func (s *store) CreateAccessToken(ctx context.Context, token AccessToken) error {
	now := time.Now().UTC()
	if token.CreatedAt.IsZero() {
		token.CreatedAt = now
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO mcp_access_tokens (token_hash, client_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		token.TokenHash,
		token.ClientID,
		token.ExpiresAt.UTC().Format(time.RFC3339Nano),
		token.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("create access token: %w", err)
	}
	return nil
}

func (s *store) RevokeAccessTokens(ctx context.Context, clientID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mcp_access_tokens WHERE client_id = ?`, clientID)
	if err != nil {
		return fmt.Errorf("revoke access tokens: %w", err)
	}
	return nil
}

func (s *store) DeleteExpiredAccessTokens(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(
		ctx,
		`DELETE FROM mcp_access_tokens WHERE expires_at <= ?`,
		now.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("delete expired access tokens: %w", err)
	}
	return nil
}

func (s *store) UpdateLastSeen(ctx context.Context, agentID string, seenAt time.Time) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE agents SET last_seen_at = ? WHERE id = ?`,
		seenAt.UTC().Format(time.RFC3339Nano),
		agentID,
	)
	if err != nil {
		return fmt.Errorf("update last seen: %w", err)
	}
	return nil
}

func scanAgent(row *sql.Row) (*Agent, error) {
	var a Agent
	var enabledInt int
	var createdAtStr, updatedAtStr string
	var lastSeenStr sql.NullString

	if err := row.Scan(
		&a.ID,
		&a.Name,
		&enabledInt,
		&a.AgentTokenHash,
		&a.AllowedRoots,
		&a.AllowedTools,
		&a.DeniedTools,
		&createdAtStr,
		&updatedAtStr,
		&lastSeenStr,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errAgentNotFound
		}
		return nil, fmt.Errorf("scan agent: %w", err)
	}

	a.Enabled = enabledInt == 1
	a.CreatedAt = parseDBTime(createdAtStr)
	a.UpdatedAt = parseDBTime(updatedAtStr)
	if lastSeenStr.Valid && lastSeenStr.String != "" {
		t := parseDBTime(lastSeenStr.String)
		a.LastSeenAt = &t
	}
	return &a, nil
}

func formatNullableTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseDBTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
