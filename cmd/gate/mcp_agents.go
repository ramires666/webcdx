package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
)

// MCP grants extend an existing OAuth client's access without sharing worker tokens.
func (s *store) ListMCPAgentIDs(ctx context.Context, clientID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM mcp_client_agents WHERE client_id = ? ORDER BY agent_id`, clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *store) SetMCPAgents(ctx context.Context, clientID string, targetIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM mcp_clients WHERE id = ?`, clientID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errAgentNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mcp_client_agents WHERE client_id = ?`, clientID); err != nil {
		return err
	}
	for _, id := range targetIDs {
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, id).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errAgentNotFound
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO mcp_client_agents VALUES (?, ?)`, clientID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *server) mcpAgents(ctx context.Context, owner *MCPClient) ([]Agent, error) {
	ids, err := s.store.ListMCPAgentIDs(ctx, owner.ID)
	if err != nil {
		return nil, err
	}
	agents := make([]Agent, 0, len(ids))
	for _, id := range ids {
		a, err := s.store.GetAgent(ctx, id)
		if err != nil {
			return nil, err
		}
		agents = append(agents, *a)
	}
	return agents, nil
}

func listAgentsTool() map[string]any {
	return map[string]any{
		"name": "list_agents", "description": "List computers available to this MCP connection and their online status.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
	}
}

func (s *server) writeMCPAgents(w http.ResponseWriter, id json.RawMessage, agents []Agent) {
	items := []map[string]any{}
	for _, agent := range agents {
		if !agent.Enabled {
			continue
		}
		online := false
		if rt := s.existingRuntime(agent.ID); rt != nil {
			online = rt.isOnline()
		}
		items = append(items, map[string]any{"id": agent.ID, "name": agent.Name, "online": online})
	}
	data := map[string]any{"agents": items}
	encoded, _ := json.Marshal(data)
	writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
		"structuredContent": data, "content": []map[string]any{{"type": "text", "text": string(encoded)}},
	}})
}

// Strip the routing argument before forwarding to unchanged local agents.
func selectMCPAgent(body json.RawMessage, call toolCall, agents []Agent) (*Agent, json.RawMessage, error) {
	value, provided := call.Arguments["agent_id"]
	id, valid := value.(string)
	if provided && (!valid || id == "") {
		return nil, nil, errors.New("agent_id must be a non-empty string")
	}
	if !provided {
		if len(agents) != 1 {
			return nil, nil, errors.New("agent_id is required; use list_agents to choose a computer")
		}
		id = agents[0].ID
	}
	for _, agent := range agents {
		if agent.ID != id {
			continue
		}
		if !agent.Enabled {
			return nil, nil, errors.New("agent disabled")
		}
		if !provided {
			return &agent, body, nil
		}
		var message map[string]any
		if err := json.Unmarshal(body, &message); err != nil {
			return nil, nil, err
		}
		delete(call.Arguments, "agent_id")
		message["params"].(map[string]any)["arguments"] = call.Arguments
		encoded, err := json.Marshal(message)
		return &agent, encoded, err
	}
	return nil, nil, errors.New("agent is not available to this MCP connection")
}

func (s *server) groupedTools(ctx context.Context, agents []Agent, request json.RawMessage) ([]map[string]any, error) {
	tools := []map[string]any{listAgentsTool()}
	byName := map[string]map[string]any{}
	computers := map[string][]string{}
	for _, agent := range agents {
		rt := s.existingRuntime(agent.ID)
		if !agent.Enabled || rt == nil || !rt.isOnline() {
			continue
		}
		resp, err := rt.callAgent(ctx, request, s.timeout)
		if err != nil {
			return nil, fmt.Errorf("list tools for %s: %w", agent.ID, err)
		}
		resp.Response, err = filterToolsList(resp.Response, newToolPolicy(agent.AllowedTools, agent.DeniedTools))
		if err != nil {
			return nil, fmt.Errorf("filter tools for %s: %w", agent.ID, err)
		}
		var msg struct {
			Result *struct {
				Tools []map[string]any `json:"tools"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(resp.Response, &msg); err != nil {
			return nil, err
		}
		if len(msg.Error) != 0 && string(msg.Error) != "null" {
			return nil, fmt.Errorf("list tools for %s: %s", agent.ID, msg.Error)
		}
		if msg.Result == nil || msg.Result.Tools == nil {
			return nil, fmt.Errorf("invalid tools/list response from %s", agent.ID)
		}
		for _, tool := range msg.Result.Tools {
			name, ok := tool["name"].(string)
			if !ok || name == "list_agents" {
				continue
			}
			if previous, ok := byName[name]; ok {
				if !reflect.DeepEqual(previous["inputSchema"], tool["inputSchema"]) || !reflect.DeepEqual(previous["outputSchema"], tool["outputSchema"]) {
					return nil, fmt.Errorf("incompatible schemas for %s; update grouped agents to the same version", name)
				}
				computers[name] = append(computers[name], agent.ID)
				continue
			}
			byName[name] = tool
			computers[name] = []string{agent.ID}
			tools = append(tools, tool)
		}
	}
	for _, tool := range tools[1:] {
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			return nil, errors.New("missing tool input schema")
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			return nil, errors.New("missing tool input properties")
		}
		if _, exists := properties["agent_id"]; exists {
			return nil, errors.New("agent_id is reserved for MCP routing")
		}
		properties["agent_id"] = map[string]any{"type": "string", "enum": computers[tool["name"].(string)], "description": "Computer ID returned by list_agents. Use the same ID for all operations on its files, browser and command sessions."}
		required, _ := schema["required"].([]any)
		if len(agents) != 1 {
			schema["required"] = append(required, "agent_id")
		}
	}
	return tools, nil
}
