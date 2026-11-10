package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
	"webcodex/internal/protocol"
)

func TestBridgeOperationAcceptanceIsIdempotentAndConflictsOnChangedArgs(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	ctx := context.Background()
	if err := srv.store.initBridgeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateAgent(ctx, Agent{ID: "op-agent", Name: "Agent", Enabled: true, AgentTokenHash: hashSecret("agent")}); err != nil {
		t.Fatal(err)
	}
	req := bridgeOperationRequest{Key: fmt.Sprintf("%d-550e8400-e29b-41d4-a716-446655440000", time.Now().UnixMilli()), Name: "read_file", AgentID: "op-agent", Arguments: map[string]any{"path": "readme"}}
	canonical, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "tools/call", "params": map[string]any{"name": "read_file", "arguments": map[string]any{"path": "readme", "agent_id": "op-agent"}}})
	digest := sha256.Sum256(canonical)
	if err := srv.store.CreateMCPClient(ctx, MCPClient{ID: "op-client", Name: "Client", Enabled: true, OAuthClientID: "op-oauth", OAuthClientSecretHash: hashSecret("secret")}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.SetMCPAgents(ctx, "op-client", []string{"op-agent"}); err != nil {
		t.Fatal(err)
	}
	first, err := srv.store.acceptBridgeOperation(ctx, "op-client", req, hex.EncodeToString(digest[:]), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := srv.store.acceptBridgeOperation(ctx, "op-client", req, hex.EncodeToString(digest[:]), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Status != "accepted" || second.Status != "accepted" {
		t.Fatalf("not idempotent: first=%+v second=%+v", first, second)
	}
	if _, err = srv.store.acceptBridgeOperation(ctx, "op-client", req, "different", time.Now()); !errors.Is(err, errBridgeOperationConflict) {
		t.Fatalf("changed arguments error = %v", err)
	}
	if _, err = srv.store.acceptBridgeOperation(ctx, "other-client", req, hex.EncodeToString(digest[:]), time.Now()); err == nil {
		t.Fatal("foreign client unexpectedly accepted without an MCP client")
	}
}

func TestBridgeOperationRestartMarksInFlightUnknown(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	ctx := context.Background()
	if err := srv.store.initBridgeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := createTestAgent(srv.store, ctx, Agent{ID: "restart-client", Name: "Agent", Enabled: true, AgentTokenHash: hashSecret("agent")}, "restart-client", ""); err != nil {
		t.Fatal(err)
	}
	_, err := srv.store.db.ExecContext(ctx, `INSERT INTO bridge_operations(id,client_id,key,name,agent_id,args_hash,status,created_at,expires_at) VALUES('op','restart-client','1-550e8400-e29b-41d4-a716-446655440000','read_file','restart-client','hash','dispatching',?,?)`, time.Now().Format(time.RFC3339Nano), time.Now().Add(time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if err = srv.store.initBridgeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = srv.store.db.QueryRowContext(ctx, `SELECT status FROM bridge_operations WHERE id='op'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "outcome_unknown" {
		t.Fatalf("status=%q", status)
	}
}

func TestBridgeOperationClaimDispatchesOnlyOnce(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	ctx := context.Background()
	if err := srv.store.initBridgeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := createTestAgent(srv.store, ctx, Agent{ID: "claim-agent", Name: "Agent", Enabled: true, AgentTokenHash: hashSecret("agent")}, "claim-agent", ""); err != nil {
		t.Fatal(err)
	}
	_, err := srv.store.db.ExecContext(ctx, `INSERT INTO bridge_operations(id,client_id,key,name,agent_id,args_hash,status,created_at,expires_at) VALUES('claim-op','claim-agent','1-550e8400-e29b-41d4-a716-446655440000','read_file','claim-agent','hash','accepted',?,?)`, time.Now().Format(time.RFC3339Nano), time.Now().Add(time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claimed := make(chan bool, 16)
	for i := 0; i < cap(claimed); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, e := srv.store.claimBridgeOperation(ctx, "claim-op")
			if e != nil {
				t.Errorf("claim: %v", e)
			}
			claimed <- ok
		}()
	}
	wg.Wait()
	close(claimed)
	n := 0
	for ok := range claimed {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("successful claims=%d, want 1", n)
	}
}

func TestBridgeOperationConcurrentCreateIsIdempotent(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	ctx := context.Background()
	if err := srv.store.initBridgeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := createTestAgent(srv.store, ctx, Agent{ID: "http-op-agent", Name: "Agent", Enabled: true, AgentTokenHash: hashSecret("agent")}, "http-op-agent", ""); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateAccessToken(ctx, AccessToken{TokenHash: hashSecret("http-op-token"), ClientID: "http-op-agent", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	body := []byte(fmt.Sprintf(`{"key":"%d-550e8400-e29b-41d4-a716-446655440000","name":"list_agents","agent_id":"http-op-agent","arguments":{}}`, time.Now().UnixMilli()))
	const callers = 12
	ids := make(chan string, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/bridge/v1/operations", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer http-op-token")
			rec := httptest.NewRecorder()
			srv.handleBridgeCreateOperation(rec, req)
			if rec.Code != http.StatusAccepted {
				t.Errorf("status=%d body=%s", rec.Code, rec.Body.String())
				return
			}
			var op bridgeOperation
			if err := json.Unmarshal(rec.Body.Bytes(), &op); err != nil {
				t.Errorf("decode: %v", err)
				return
			}
			ids <- op.ID
		}()
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("duplicate request returned IDs %q and %q", first, id)
		}
	}
	var count int
	if err := srv.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bridge_operations WHERE client_id='http-op-agent'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored operations=%d, want 1", count)
	}
	var status string
	var result []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = srv.store.db.QueryRowContext(ctx, `SELECT status,result FROM bridge_operations WHERE id=?`, first).Scan(&status, &result)
		if status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "completed" {
		t.Fatalf("list_agents operation status=%q", status)
	}
	var response struct {
		StructuredContent struct {
			Agents []struct {
				ID string `json:"id"`
			} `json:"agents"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatalf("decode completed result %s: %v", result, err)
	}
	if len(response.StructuredContent.Agents) != 1 || response.StructuredContent.Agents[0].ID != "http-op-agent" {
		t.Fatalf("unexpected completed list_agents result: %s", result)
	}
}

func TestBridgeOperationReadFileDispatchUsesArguments(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	ctx := context.Background()
	if err := srv.store.initBridgeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := createTestAgent(srv.store, ctx, Agent{ID: "read-agent", Name: "Agent", Enabled: true, AgentTokenHash: hashSecret("agent"), AllowedTools: "read_file"}, "read-agent", ""); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateAccessToken(ctx, AccessToken{TokenHash: hashSecret("read-token"), ClientID: "read-agent", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	_, err := srv.store.db.ExecContext(ctx, `INSERT INTO bridge_operations(id,client_id,key,name,agent_id,args_hash,status,created_at,expires_at) VALUES('read-op','read-agent','1-550e8400-e29b-41d4-a716-446655440000','read_file','read-agent','hash','dispatching',?,?)`, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	rt := newAgentRuntime("read-agent")
	srv.runtimes["read-agent"] = rt
	_, stream, _ := rt.activateAgentStream(ctx)
	done := make(chan struct{})
	go func() {
		srv.runBridgeOperation("read-token", "read-op", bridgeOperationRequest{Name: "read_file", AgentID: "read-agent"}, json.RawMessage(`{"path":"docs/guide.txt"}`))
		close(done)
	}()
	select {
	case call := <-rt.queue:
		var msg struct {
			ID     string `json:"id"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(call.Request, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Params.Name != "read_file" || msg.Params.Arguments["path"] != "docs/guide.txt" || msg.Params.Arguments["agent_id"] != nil {
			t.Fatalf("unexpected dispatched call: %s", call.Request)
		}
		resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}})
		rt.mu.Lock()
		pending := rt.pending[call.ID]
		rt.mu.Unlock()
		if pending == nil {
			t.Fatal("missing pending result channel")
		}
		pending <- protocol.AgentResponse{ID: call.ID, Response: resp}
	case <-time.After(time.Second):
		t.Fatal("read_file call was not dispatched")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("operation did not finish")
	}
	rt.deactivateAgentStream(stream)
	var status string
	var result []byte
	if err := srv.store.db.QueryRowContext(ctx, `SELECT status,result FROM bridge_operations WHERE id='read-op'`).Scan(&status, &result); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("status=%s result=%s", status, result)
	}
}
