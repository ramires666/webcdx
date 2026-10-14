package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleMCPStream(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	rawToken := "wc_mcp_test_stream"
	_ = createTestAgent(srv.store, context.Background(), Agent{
		ID:             "stream-agent",
		Name:           "Stream Agent",
		Enabled:        true,
		AgentTokenHash: hashSecret("agent_tok"),
	}, "client_tok", "")
	_ = srv.store.CreateAccessToken(context.Background(), AccessToken{
		TokenHash: hashSecret(rawToken),
		ClientID:  "stream-agent",
		ExpiresAt: time.Now().Add(time.Hour),
	})

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	srv.handleMCP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q, want text/event-stream", rec.Header().Get("Content-Type"))
	}
}
