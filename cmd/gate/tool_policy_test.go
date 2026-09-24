package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestToolPolicyFiltersToolsList(t *testing.T) {
	policy := newToolPolicy("exec_command,write_file", "write_file")
	response := json.RawMessage(`{
		"jsonrpc":"2.0",
		"id":1,
		"result":{
			"tools":[
				{"name":"exec_command"},
				{"name":"write_file"},
				{"name":"read_mcp_resource"}
			]
		}
	}`)

	filtered, err := filterToolsList(response, policy)
	if err != nil {
		t.Fatalf("filter tools list: %v", err)
	}

	var msg struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(filtered, &msg); err != nil {
		t.Fatalf("parse filtered response: %v", err)
	}
	if len(msg.Result.Tools) != 1 {
		t.Fatalf("tool count = %d, want 1", len(msg.Result.Tools))
	}
	if msg.Result.Tools[0].Name != "exec_command" {
		t.Fatalf("tool = %q, want exec_command", msg.Result.Tools[0].Name)
	}
}

func TestNewAdminAgentDeniesDestructiveV4Tools(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	form := url.Values{"csrf": {srv.adminCSRF}, "id": {"policy-default"}, "name": {"Policy Default"}}
	request := httptest.NewRequest(http.MethodPost, "/admin/agents", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	srv.handleAdminCreateAgent(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	agent, err := srv.store.GetAgent(context.Background(), "policy-default")
	if err != nil {
		t.Fatal(err)
	}
	policy := newToolPolicy(agent.AllowedTools, agent.DeniedTools)
	for _, name := range []string{"edit_file", "move_path", "delete_path"} {
		if policy.allows(name) {
			t.Fatalf("%s is allowed by default: %+v", name, agent)
		}
	}
	if !policy.allows("find_files") {
		t.Fatalf("find_files is denied by default: %+v", agent)
	}
}
