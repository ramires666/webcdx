package main

import "context"

// Existing fixtures model a worker and its now-independent MCP registration.
func createTestAgent(s *store, ctx context.Context, agent Agent, oauthID, secretHash string) error {
	if err := s.CreateAgent(ctx, agent); err != nil {
		return err
	}
	if err := s.CreateMCPClient(ctx, MCPClient{ID: agent.ID, Name: agent.Name, Enabled: agent.Enabled, OAuthClientID: oauthID, OAuthClientSecretHash: secretHash}); err != nil {
		return err
	}
	return s.SetMCPAgents(ctx, agent.ID, []string{agent.ID})
}
