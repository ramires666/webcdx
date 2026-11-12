package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var bridgeOperationKey = regexp.MustCompile(`^[0-9]{1,16}-[0-9a-fA-F-]{36}$`)

type bridgeOperation struct {
	ID      string          `json:"id"`
	Key     string          `json:"key"`
	Status  string          `json:"status"`
	Name    string          `json:"name"`
	AgentID string          `json:"agent_id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
}

func (s *store) initBridgeOperations(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS bridge_operations (
		id TEXT PRIMARY KEY, client_id TEXT NOT NULL REFERENCES mcp_clients(id) ON DELETE CASCADE,
		key TEXT NOT NULL, name TEXT NOT NULL, agent_id TEXT NOT NULL, args_hash TEXT NOT NULL,
		status TEXT NOT NULL, result BLOB, error TEXT, created_at DATETIME NOT NULL, expires_at DATETIME NOT NULL,
		UNIQUE(client_id,key))`); err != nil {
		return fmt.Errorf("initialize bridge operations: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_bridge_operations_expiry ON bridge_operations(expires_at)`); err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE bridge_operations SET result=NULL,error=NULL WHERE expires_at<=?`, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM bridge_operations WHERE created_at<=?`, now.Add(-48*time.Hour).Format(time.RFC3339Nano)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE bridge_operations SET status='outcome_unknown', result=NULL, error='server restarted during execution' WHERE status IN ('accepted','dispatching')`)
	return err
}

type bridgeOperationRequest struct {
	Key       string         `json:"key"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	AgentID   string         `json:"agent_id"`
}

func (s *server) handleBridgeCreateOperation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	client, err := s.authenticateMCP(r)
	if err != nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	if !bridgeOperationOriginAllowed(client, r) {
		http.Error(w, "origin denied", 403)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid request body", 400)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req bridgeOperationRequest
	if err = dec.Decode(&req); err != nil || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request", 400)
		return
	}
	if len(req.Key) > 128 || !bridgeOperationKey.MatchString(req.Key) || req.Name == "" || req.AgentID == "" || req.Arguments == nil {
		http.Error(w, "key, name, object arguments, and agent_id are required", 400)
		return
	}
	keyParts := strings.SplitN(req.Key, "-", 2)
	keyTime, e := strconv.ParseInt(keyParts[0], 10, 64)
	if e != nil || uuid.Validate(keyParts[1]) != nil {
		http.Error(w, "invalid key", 400)
		return
	}
	now := time.Now()
	kt := time.UnixMilli(keyTime)
	if strings.HasPrefix(req.Name, "browser_") {
		http.Error(w, "browser tools are not supported", 400)
		return
	}
	if _, exists := req.Arguments["agent_id"]; exists {
		http.Error(w, "agent_id is reserved", 400)
		return
	}
	if kt.After(now.Add(time.Minute)) {
		http.Error(w, "invalid key timestamp", 400)
		return
	}
	if !bridgeAllows(client, req.Name) {
		http.Error(w, "tool not allowed", 403)
		return
	}
	agents, err := s.mcpAgents(r.Context(), client)
	if err != nil {
		http.Error(w, "failed to load agents", 500)
		return
	}
	var agent *Agent
	for i := range agents {
		if agents[i].ID == req.AgentID && agents[i].Enabled {
			agent = &agents[i]
			break
		}
	}
	if agent == nil || !newToolPolicy(agent.AllowedTools, agent.DeniedTools).allows(req.Name) {
		http.Error(w, "agent or tool not available", 403)
		return
	}
	canonicalRequest, err := json.Marshal(map[string]any{"name": req.Name, "agent_id": req.AgentID, "arguments": req.Arguments})
	if err != nil {
		http.Error(w, "invalid arguments", 400)
		return
	}
	h := sha256.Sum256(canonicalRequest)
	argsHash := hex.EncodeToString(h[:])
	op, err := s.store.acceptBridgeOperation(r.Context(), client.ID, req, argsHash, now)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "operation key expired", 410)
		return
	}
	if errors.Is(err, errBridgeOperationStaleKey) {
		http.Error(w, "stale key", 400)
		return
	}
	if errors.Is(err, errBridgeOperationConflict) {
		http.Error(w, "operation key reused with different arguments", 409)
		return
	}
	if errors.Is(err, errBridgeOperationLimit) {
		http.Error(w, "operation limit reached", 429)
		return
	}
	if err != nil {
		http.Error(w, "failed to accept operation", 500)
		return
	}
	if op.Status == "accepted" {
		claimed, claimErr := s.store.claimBridgeOperation(r.Context(), op.ID)
		if claimErr != nil {
			http.Error(w, "failed to dispatch operation", http.StatusInternalServerError)
			return
		}
		if claimed {
			op.Status = "dispatching"
			arguments, _ := json.Marshal(req.Arguments)
			go s.runBridgeOperation(bearerToken(r), op.ID, req, arguments)
		} else {
			_ = s.store.db.QueryRowContext(r.Context(), `SELECT status FROM bridge_operations WHERE id=?`, op.ID).Scan(&op.Status)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(op)
}

func (s *store) claimBridgeOperation(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE bridge_operations SET status='dispatching' WHERE id=? AND status='accepted'`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

var errBridgeOperationConflict = errors.New("bridge operation key conflict")
var errBridgeOperationLimit = errors.New("bridge operation limit reached")
var errBridgeOperationStaleKey = errors.New("stale bridge operation key")

func (s *store) acceptBridgeOperation(ctx context.Context, clientID string, req bridgeOperationRequest, argsHash string, now time.Time) (bridgeOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return bridgeOperation{}, err
	}
	defer tx.Rollback()
	var op bridgeOperation
	var result []byte
	var e sql.NullString
	var expiry, existingHash string
	err = tx.QueryRowContext(ctx, `SELECT id,key,name,agent_id,args_hash,status,result,error,expires_at FROM bridge_operations WHERE client_id=? AND key=?`, clientID, req.Key).Scan(&op.ID, &op.Key, &op.Name, &op.AgentID, &existingHash, &op.Status, &result, &e, &expiry)
	if err == nil {
		if argsHash != existingHash {
			return bridgeOperation{}, errBridgeOperationConflict
		}
		if !parseDBTime(expiry).After(now) {
			return bridgeOperation{}, sql.ErrNoRows
		}
		op.Result = result
		if e.Valid {
			op.Error = e.String
		}
		return op, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return bridgeOperation{}, err
	}
	parts := strings.SplitN(req.Key, "-", 2)
	keyMillis, _ := strconv.ParseInt(parts[0], 10, 64)
	if time.UnixMilli(keyMillis).Before(now.Add(-5 * time.Minute)) {
		return bridgeOperation{}, errBridgeOperationStaleKey
	}
	if _, err = tx.ExecContext(ctx, `UPDATE bridge_operations SET result=NULL,error=NULL WHERE expires_at<=?`, now.UTC().Format(time.RFC3339Nano)); err != nil {
		return bridgeOperation{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM bridge_operations WHERE created_at<=?`, now.UTC().Add(-48*time.Hour).Format(time.RFC3339Nano)); err != nil {
		return bridgeOperation{}, err
	}
	var count, active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN status IN ('accepted','dispatching') THEN 1 ELSE 0 END),0) FROM bridge_operations WHERE client_id=? AND created_at>?`, clientID, now.UTC().Add(-24*time.Hour).Format(time.RFC3339Nano)).Scan(&count, &active); err != nil {
		return bridgeOperation{}, err
	}
	if count >= 1000 || active >= 32 {
		return bridgeOperation{}, errBridgeOperationLimit
	}
	op = bridgeOperation{ID: uuid.NewString(), Key: req.Key, Name: req.Name, AgentID: req.AgentID, Status: "accepted"}
	_, err = tx.ExecContext(ctx, `INSERT INTO bridge_operations(id,client_id,key,name,agent_id,args_hash,status,created_at,expires_at) VALUES(?,?,?,?,?,?, 'accepted',?,?)`, op.ID, clientID, req.Key, req.Name, req.AgentID, argsHash, now.UTC().Format(time.RFC3339Nano), now.Add(24*time.Hour).UTC().Format(time.RFC3339Nano))
	if err != nil {
		return bridgeOperation{}, err
	}
	return op, tx.Commit()
}

func (s *server) runBridgeOperation(token, id string, req bridgeOperationRequest, args json.RawMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	client, err := s.authenticateMCP(r)
	if err != nil {
		s.finishBridgeOperation(ctx, id, "failed", nil, err.Error())
		return
	}
	if !bridgeAllows(client, req.Name) {
		s.finishBridgeOperation(ctx, id, "failed", nil, "tool not allowed")
		return
	}
	agents, err := s.mcpAgents(ctx, client)
	if err != nil {
		s.finishBridgeOperation(ctx, id, "failed", nil, "failed to load agent")
		return
	}
	var found *Agent
	for i := range agents {
		if agents[i].ID == req.AgentID && agents[i].Enabled {
			found = &agents[i]
			break
		}
	}
	if found == nil || !newToolPolicy(found.AllowedTools, found.DeniedTools).allows(req.Name) {
		s.finishBridgeOperation(ctx, id, "failed", nil, "agent or tool no longer available")
		return
	}
	var argv map[string]any
	if json.Unmarshal(args, &argv) != nil {
		s.finishBridgeOperation(ctx, id, "failed", nil, "invalid stored arguments")
		return
	}
	if req.Name != "list_agents" {
		argv["agent_id"] = req.AgentID
	}
	call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": req.Name, "arguments": argv}})
	response, err := s.executeMCPTool(ctx, client, call, "v5")
	if err != nil {
		s.finishBridgeOperation(ctx, id, "outcome_unknown", nil, "execution outcome unknown")
		return
	}
	var envelope struct {
		Error  json.RawMessage `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(response, &envelope) != nil {
		s.finishBridgeOperation(ctx, id, "outcome_unknown", nil, "execution outcome unknown")
		return
	}
	message := ""
	status := "completed"
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		status = "failed"
	}
	var toolResult struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(envelope.Result, &toolResult) == nil && toolResult.IsError {
		status = "failed"
		if len(toolResult.Content) > 0 {
			message = toolResult.Content[0].Text
		}
	}
	result := envelope.Result
	if status == "failed" {
		var rpcError struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(envelope.Error, &rpcError)
		message = rpcError.Message
		if message == "" {
			message = "tool execution failed"
		}
	}
	s.finishBridgeOperation(ctx, id, status, result, message)
}

func (s *server) finishBridgeOperation(ctx context.Context, id, status string, result json.RawMessage, message string) {
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := s.store.db.ExecContext(writeCtx, `UPDATE bridge_operations SET status=?,result=?,error=? WHERE id=? AND status='dispatching'`, status, nullBytes(result), nullString(message), id)
	if err != nil {
		return
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return
	}
}
func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func bridgeOperationOriginAllowed(client *MCPClient, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if client.BridgeGrant == nil {
		return false
	}
	return origin == "chrome-extension://"+client.BridgeGrant.ExtensionID
}

func (s *server) handleBridgeGetOperation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	client, err := s.authenticateMCP(r)
	if err != nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	if !bridgeOperationOriginAllowed(client, r) {
		http.Error(w, "origin denied", 403)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/bridge/v1/operations/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "not found", 404)
		return
	}
	var op bridgeOperation
	var result []byte
	var e sql.NullString
	var expiry string
	err = s.store.db.QueryRowContext(r.Context(), `SELECT id,key,name,agent_id,status,result,error,expires_at FROM bridge_operations WHERE id=? AND client_id=?`, id, client.ID).Scan(&op.ID, &op.Key, &op.Name, &op.AgentID, &op.Status, &result, &e, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", 404)
		return
	}
	if err != nil {
		http.Error(w, "failed to load operation", 500)
		return
	}
	if !parseDBTime(expiry).After(time.Now()) {
		_, _ = s.store.db.ExecContext(r.Context(), `UPDATE bridge_operations SET result=NULL,error=NULL WHERE id=?`, id)
		http.Error(w, "operation expired", 410)
		return
	}
	agents, err := s.mcpAgents(r.Context(), client)
	if err != nil {
		http.Error(w, "failed to load agent", 500)
		return
	}
	var agent *Agent
	for i := range agents {
		if agents[i].ID == op.AgentID && agents[i].Enabled {
			agent = &agents[i]
			break
		}
	}
	if agent == nil || !bridgeAllows(client, op.Name) || !newToolPolicy(agent.AllowedTools, agent.DeniedTools).allows(op.Name) {
		http.Error(w, "operation access revoked", 403)
		return
	}
	op.Result = result
	if e.Valid {
		op.Error = e.String
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(op)
}
