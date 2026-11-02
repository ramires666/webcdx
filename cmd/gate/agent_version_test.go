package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"webcodex/internal/buildinfo"
)

func TestAgentStreamVersionHeaderAndLegacyQuery(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	if err := createTestAgent(srv.store, context.Background(), Agent{
		ID: "version-agent", Name: "Version Agent", Enabled: true,
		AgentTokenHash: hashSecret("version-token"),
	}, "version-client", ""); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		query, want string
	}{{"?version=agent-1", "agent-1"}, {"", "версия не передана"}} {
		httpServer := httptest.NewServer(http.HandlerFunc(srv.handleAgentStream))
		req, err := http.NewRequest("GET", httpServer.URL+"/agent/stream"+tc.query, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer version-token")
		resp, err := httpServer.Client().Do(req)
		if err != nil {
			httpServer.Close()
			t.Fatal(err)
		}
		if got := resp.Header.Get("X-WebCodex-Version"); got != buildinfo.Version {
			t.Errorf("Gate version header = %q", got)
		}
		if got := buildinfo.DisplayPeer(req.URL.Query().Get("version")); got != tc.want {
			t.Errorf("agent version display = %q, want %q", got, tc.want)
		}
		resp.Body.Close()
		httpServer.Close()
	}
}
