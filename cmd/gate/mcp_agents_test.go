package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"webcodex/internal/protocol"
)

func createMCPGroupAgents(t *testing.T, srv *server) {
	t.Helper()
	for _, id := range []string{"home", "work", "private"} {
		if err := createTestAgent(srv.store, context.Background(), Agent{
			ID: id, Name: id, Enabled: true, AgentTokenHash: hashSecret("worker-" + id),
		}, "client-"+id, hashSecret("secret-"+id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.store.SetMCPAgents(context.Background(), "home", []string{"home", "work"}); err != nil {
		t.Fatal(err)
	}
}

func connectMCPGroupWorker(t *testing.T, srv *server, id string, incompatible ...bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/agent/stream", nil)
	req.Header.Set("Authorization", "Bearer worker-"+id)
	_, rt, err := srv.authenticateAgent(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stream, _ := rt.activateAgentStream(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { rt.deactivateAgentStream(stream); <-done })
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case request := <-rt.queue:
				var msg struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
						Contract  string         `json:"_webcodex_contract"`
					} `json:"params"`
				}
				if err := json.Unmarshal(request.Request, &msg); err != nil {
					t.Error(err)
					return
				}
				if _, present := msg.Params.Arguments["agent_id"]; present {
					t.Error("routing argument reached local worker")
				}
				if msg.Params.Contract != "v5" {
					t.Errorf("worker contract = %q", msg.Params.Contract)
				}
				var result any
				if msg.Method == "tools/list" {
					tools := []map[string]any{}
					for _, name := range []string{"read_file", "write_file", "read_command_output"} {
						tools = append(tools, map[string]any{
							"name": name, "description": name,
							"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}},
						})
					}
					if len(incompatible) != 0 {
						tools[0]["inputSchema"].(map[string]any)["required"] = []string{"different"}
					}
					result = map[string]any{"tools": tools}
				} else {
					data := map[string]any{"computer": id, "arguments": msg.Params.Arguments}
					text, _ := json.Marshal(data)
					result = map[string]any{"structuredContent": data, "content": []map[string]any{{"type": "text", "text": string(text)}}}
				}
				response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
				payload, _ := json.Marshal(protocol.AgentResponse{ID: request.ID, Response: response})
				resultReq := httptest.NewRequest(http.MethodPost, "/agent/result", strings.NewReader(string(payload)))
				resultReq.Header.Set("Authorization", "Bearer worker-"+id)
				rec := httptest.NewRecorder()
				srv.handleAgentResult(rec, resultReq)
				if rec.Code != http.StatusNoContent {
					t.Errorf("worker result: %d %s", rec.Code, rec.Body.String())
				}
			}
		}
	}()
}

func groupMCPRequest(t *testing.T, srv *server, token, method, name string, arguments map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": map[string]any{"name": name, "arguments": arguments}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp/v5", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("MCP request: %d %s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestMCPGroupOAuthRoutingAndIsolation(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	createMCPGroupAgents(t, srv)
	connectMCPGroupWorker(t, srv, "home")
	connectMCPGroupWorker(t, srv, "work")
	token := exchangeOAuthToken(t, srv, "client-home", "secret-home", "https://oauth-redirect.googleusercontent.com/r/user_bound_custom-mcp-test")

	initialized := groupMCPRequest(t, srv, token, "initialize", "", nil)
	if !strings.Contains(initialized["result"].(map[string]any)["instructions"].(string), "agent_id") {
		t.Fatal("group routing instructions missing")
	}
	listed := groupMCPRequest(t, srv, token, "tools/list", "", nil)
	tools := listed["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 4 || tools[0].(map[string]any)["name"] != "list_agents" {
		t.Fatalf("group tools: %v", tools)
	}
	for _, item := range tools[1:] {
		schema := item.(map[string]any)["inputSchema"].(map[string]any)
		if !reflect.DeepEqual(schema["required"], []any{"path", "agent_id"}) {
			t.Fatalf("routing not required: %v", schema)
		}
	}
	agents := groupMCPRequest(t, srv, token, "tools/call", "list_agents", map[string]any{})["result"].(map[string]any)["structuredContent"].(map[string]any)["agents"].([]any)
	if len(agents) != 2 || agents[0].(map[string]any)["id"] != "home" || agents[1].(map[string]any)["id"] != "work" {
		t.Fatalf("agents leaked or missing: %v", agents)
	}
	for _, id := range []string{"home", "work"} {
		response := groupMCPRequest(t, srv, token, "tools/call", "read_command_output", map[string]any{"agent_id": id, "session_id": "same-session-id"})
		data := response["result"].(map[string]any)["structuredContent"].(map[string]any)
		if data["computer"] != id || data["arguments"].(map[string]any)["session_id"] != "same-session-id" {
			t.Fatalf("wrong computer or changed session: %v", data)
		}
	}
	for _, args := range []map[string]any{{}, {"agent_id": "private"}, {"agent_id": ""}, {"agent_id": 1}} {
		response := groupMCPRequest(t, srv, token, "tools/call", "read_file", args)
		if response["result"].(map[string]any)["isError"] != true {
			t.Fatalf("unsafe routing accepted: %v", response)
		}
	}
	if err := srv.store.UpdateToolPolicy(context.Background(), "work", "", "write_file"); err != nil {
		t.Fatal(err)
	}
	denied := groupMCPRequest(t, srv, token, "tools/call", "write_file", map[string]any{"agent_id": "work"})
	if denied["result"].(map[string]any)["isError"] != true {
		t.Fatalf("target policy bypassed: %v", denied)
	}
	allowed := groupMCPRequest(t, srv, token, "tools/call", "write_file", map[string]any{"agent_id": "home"})
	if allowed["result"].(map[string]any)["isError"] == true {
		t.Fatalf("wrong policy applied: %v", allowed)
	}
	for _, item := range groupMCPRequest(t, srv, token, "tools/list", "", nil)["result"].(map[string]any)["tools"].([]any) {
		tool := item.(map[string]any)
		if tool["name"] == "write_file" {
			ids := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)["agent_id"].(map[string]any)["enum"]
			if !reflect.DeepEqual(ids, []any{"home"}) {
				t.Fatalf("denied target in tool schema: %v", ids)
			}
		}
	}

	otherToken := exchangeOAuthToken(t, srv, "client-work", "secret-work", "https://chatgpt.com/oauth/callback")
	isolated := groupMCPRequest(t, srv, otherToken, "tools/call", "read_file", map[string]any{"agent_id": "home"})
	if isolated["result"].(map[string]any)["isError"] != true {
		t.Fatal("grants leaked to another OAuth client")
	}

	if err := srv.store.SetAgentEnabled(context.Background(), "work", false); err != nil {
		t.Fatal(err)
	}
	if groupMCPRequest(t, srv, token, "tools/call", "read_file", map[string]any{"agent_id": "work"})["result"].(map[string]any)["isError"] != true {
		t.Fatal("disabled agent accepted")
	}
	if groupMCPRequest(t, srv, token, "tools/call", "read_file", map[string]any{})["result"].(map[string]any)["isError"] != true {
		t.Fatal("disabled target caused implicit fallback")
	}

	if err := srv.store.SetMCPAgents(context.Background(), "home", []string{"home"}); err != nil {
		t.Fatal(err)
	}
	if groupMCPRequest(t, srv, token, "tools/call", "read_file", map[string]any{"agent_id": "work"})["result"].(map[string]any)["isError"] != true {
		t.Fatal("revoked grant still works with existing token")
	}
	legacy := groupMCPRequest(t, srv, token, "tools/call", "read_file", map[string]any{"path": "file"})
	if legacy["result"].(map[string]any)["structuredContent"].(map[string]any)["computer"] != "home" {
		t.Fatal("legacy single-agent call broken")
	}
}

func TestMCPGroupOfflineDiscoveryAndSchemaMismatch(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	createMCPGroupAgents(t, srv)
	token := exchangeOAuthToken(t, srv, "client-home", "secret-home", "https://chatgpt.com/oauth/callback")
	listed := groupMCPRequest(t, srv, token, "tools/list", "", nil)
	if len(listed["result"].(map[string]any)["tools"].([]any)) != 1 {
		t.Fatal("offline discovery failed")
	}
	if groupMCPRequest(t, srv, token, "tools/call", "read_file", map[string]any{"agent_id": "work"})["result"].(map[string]any)["isError"] != true {
		t.Fatal("offline execution accepted")
	}
	connectMCPGroupWorker(t, srv, "work")
	if groupMCPRequest(t, srv, token, "tools/call", "read_file", map[string]any{"agent_id": "work"})["result"].(map[string]any)["structuredContent"].(map[string]any)["computer"] != "work" {
		t.Fatal("offline primary blocked other worker")
	}
	for _, agent := range groupMCPRequest(t, srv, token, "tools/call", "list_agents", map[string]any{})["result"].(map[string]any)["structuredContent"].(map[string]any)["agents"].([]any) {
		item := agent.(map[string]any)
		if item["online"] != (item["id"] == "work") {
			t.Fatalf("wrong online status: %v", item)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp/v5", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, request)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("offline primary initialization: %d", rec.Code)
	}
	connectMCPGroupWorker(t, srv, "home", true)
	mismatch := groupMCPRequest(t, srv, token, "tools/list", "", nil)
	if !strings.Contains(mismatch["error"].(map[string]any)["message"].(string), "incompatible schemas") {
		t.Fatalf("incompatible schemas silently merged: %v", mismatch)
	}
}

func TestMCPGrantsAdminPersistenceAndValidation(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	createMCPGroupAgents(t, srv)
	for _, test := range []struct {
		csrf    string
		targets []string
		status  int
	}{
		{"bad", []string{"private"}, http.StatusForbidden},
		{srv.adminCSRF, []string{"private", "missing"}, http.StatusBadRequest},
		{srv.adminCSRF, []string{"home", "work", "work"}, http.StatusSeeOther},
	} {
		form := url.Values{"csrf": {test.csrf}, "agent_id": test.targets}
		req := httptest.NewRequest(http.MethodPost, "/admin/mcp-clients/home/agents", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth(srv.adminUser, srv.adminPassword)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		if rec.Code != test.status {
			t.Fatalf("admin status %d: %s", rec.Code, rec.Body.String())
		}
		ids, err := srv.store.ListMCPAgentIDs(context.Background(), "home")
		if err != nil || !reflect.DeepEqual(ids, []string{"home", "work"}) {
			t.Fatalf("grants changed unexpectedly: %v %v", ids, err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/mcp-clients/home", nil)
	req.SetBasicAuth(srv.adminUser, srv.adminPassword)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `value="work" checked`) || strings.Contains(rec.Body.String(), `value="private" checked`) {
		t.Fatalf("admin grant choices: %d %s", rec.Code, rec.Body.String())
	}

	path := filepath.Join(t.TempDir(), "persist.db")
	persist, err := newStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := createTestAgent(persist, context.Background(), Agent{ID: id, Name: id, Enabled: true, AgentTokenHash: id}, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := persist.SetMCPAgents(context.Background(), "a", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := persist.Close(); err != nil {
		t.Fatal(err)
	}
	persist, err = newStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer persist.Close()
	ids, err := persist.ListMCPAgentIDs(context.Background(), "a")
	if err != nil || !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("grants lost on restart: %v %v", ids, err)
	}
	if err := persist.DeleteAgent(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	ids, err = persist.ListMCPAgentIDs(context.Background(), "a")
	if err != nil || !reflect.DeepEqual(ids, []string{"a"}) {
		t.Fatalf("deleted agent grant remains: %v %v", ids, err)
	}
}
