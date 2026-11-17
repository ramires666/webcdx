package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func gateOperationMethod(raw json.RawMessage) bool {
	var request struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(raw, &request)
	return strings.HasPrefix(request.Method, "bridge/operations/")
}

func (e *nativeExecutor) callGateOperation(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	respond := func(value any, failure *studioRPCError) (json.RawMessage, error) {
		if failure != nil {
			return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": failure})
		}
		return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": value})
	}
	if e.gateOperationDir == "" {
		return respond(nil, &studioRPCError{-32601, "Gate operation journal unavailable"})
	}
	if request.Method != "bridge/operations/create" && request.Method != "bridge/operations/lookup" {
		return respond(nil, &studioRPCError{-32601, "method not found"})
	}
	var params struct {
		ClientID  string         `json:"client_id"`
		AgentID   string         `json:"agent_id"`
		Key       string         `json:"key"`
		Name      string         `json:"name,omitempty"`
		Arguments map[string]any `json:"arguments,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(request.Params))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil || decoder.Decode(new(any)) != io.EOF ||
		strings.TrimSpace(params.ClientID) == "" || strings.TrimSpace(params.AgentID) == "" || strings.TrimSpace(params.Key) == "" {
		return respond(nil, &studioRPCError{-32602, "client_id, agent_id and key are required"})
	}
	if request.Method == "bridge/operations/lookup" && (params.Name != "" || params.Arguments != nil) {
		return respond(nil, &studioRPCError{-32602, "lookup accepts identity fields only"})
	}
	scope := commandHash(map[string]string{"source": "gate", "client": params.ClientID, "agent": params.AgentID})
	stored, ok := e.gateOperations.Load(scope)
	if !ok {
		directory := filepath.Join(e.gateOperationDir, scope)
		journal, err := newStudioOperationsAt(directory)
		if err != nil {
			return respond(nil, &studioRPCError{-32000, "cannot initialize Gate journal"})
		}
		journal.scope = scope
		stored, _ = e.gateOperations.LoadOrStore(scope, journal)
	}
	journal := stored.(*studioOperations)
	if request.Method == "bridge/operations/lookup" {
		value, err := journal.get(studioOperationID(params.Key))
		if errors.Is(err, os.ErrNotExist) {
			status := "not_found"
			if studioOperationExpired(params.Key) {
				status = "expired"
			}
			return respond(map[string]any{"status": status}, nil)
		}
		if err != nil {
			return respond(nil, &studioRPCError{-32000, "cannot read Gate journal"})
		}
		return respond(value, nil)
	}
	if params.Name == "" || params.Arguments == nil {
		return respond(nil, &studioRPCError{-32602, "name and object arguments are required"})
	}
	if _, exists := params.Arguments["agent_id"]; exists {
		return respond(nil, &studioRPCError{-32602, "agent_id is reserved"})
	}
	if _, exists := params.Arguments["agentId"]; exists {
		return respond(nil, &studioRPCError{-32602, "agentId is reserved"})
	}
	if err := os.MkdirAll(journal.dir, 0o700); err != nil {
		return respond(nil, &studioRPCError{-32000, "cannot create Gate journal"})
	}
	// Execution survives stream loss once the durable reservation has been accepted.
	callCtx := withTaskOwner(context.WithoutCancel(ctx), e.gateTaskOwner(params.ClientID, params.AgentID))
	value, err := journal.create(callCtx, e, params.Key, params.Name, params.Arguments, "local")
	if err != nil {
		return respond(nil, &studioRPCError{-32000, err.Error()})
	}
	return respond(value, nil)
}
