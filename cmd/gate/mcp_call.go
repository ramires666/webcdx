package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// executeMCPTool keeps direct MCP calls on the same tool and agent policy path as before.
func (s *server) executeMCPTool(ctx context.Context, client *MCPClient, request json.RawMessage, version string) (json.RawMessage, error) {
	call, err := parseToolCall(request)
	if err != nil {
		return nil, err
	}
	if !bridgeAllows(client, call.Name) {
		return nil, fmt.Errorf("tool not allowed: %q", call.Name)
	}
	agents, err := s.mcpAgents(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("failed to load available agents: %w", err)
	}
	if call.Name == "list_agents" {
		if len(call.Arguments) != 0 {
			return nil, fmt.Errorf("list_agents takes no arguments")
		}
		return json.Marshal(s.mcpAgentsResponse(rpcID(request), agents))
	}
	agent, body, err := selectMCPAgent(request, call, agents)
	if err != nil {
		return nil, err
	}
	if !newToolPolicy(agent.AllowedTools, agent.DeniedTools).allows(call.Name) {
		return nil, fmt.Errorf("tool not allowed: %q", call.Name)
	}
	body, err = markContract(body, version)
	if err != nil {
		return nil, err
	}
	resp, err := s.runtimeFor(agent.ID).callAgent(ctx, body, s.timeout)
	if err != nil {
		return nil, fmt.Errorf("execution error: %w", err)
	}
	return ensureToolCallResult(resp.Response, rpcID(request)), nil
}

func rpcID(request json.RawMessage) json.RawMessage {
	var message struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(request, &message)
	return message.ID
}
