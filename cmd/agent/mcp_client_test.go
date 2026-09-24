package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestInitializeRequestIsOneJSONRPCLine(t *testing.T) {
	if strings.Contains(initializeRequest, "\n") {
		t.Fatal("initialize request contains a newline")
	}

	var request struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Method  string `json:"method"`
	}
	if err := json.Unmarshal([]byte(initializeRequest), &request); err != nil {
		t.Fatalf("parse initialize request: %v", err)
	}
	if request.JSONRPC != "2.0" || request.ID != "webcodex-init" || request.Method != "initialize" {
		t.Fatalf("unexpected initialize request: %#v", request)
	}
}

func TestRealCodexMCPCall(t *testing.T) {
	binary, args := findCodexMCP()
	t.Logf("Found Codex MCP binary: %s with args: %v", binary, args)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	mcp, err := startMCP(ctx, binary, args)
	if err != nil {
		t.Fatalf("startMCP: %v", err)
	}
	if err := mcp.initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	t.Log("Codex MCP successfully initialized!")

	// Now send the EXACT prompt ChatGPT sent!
	prompt := "Create or overwrite C:\\projects\\algo\\NQ2-9\\PROJECT_TREE.txt with exactly this temporary text: TEST_WRITE_OK. Then read the file back and return its contents. Do not list the directory."
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      "test-call-1",
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codex",
			"arguments": map[string]any{
				"prompt":          prompt,
				"cwd":             `C:\projects\algo\NQ2-9`,
				"sandbox":         "danger-full-access",
				"approval-policy": "never",
			},
		},
	}
	reqBytes, _ := json.Marshal(req)
	resp, err := mcp.call(ctx, reqBytes)
	if err != nil {
		t.Fatalf("call error: %v", err)
	}
	t.Logf("REAL CODEX RESPONSE: %s", string(resp))

	// Verify file was written to disk
	data, err := os.ReadFile(`C:\projects\algo\NQ2-9\PROJECT_TREE.txt`)
	if err != nil {
		t.Fatalf("File was not written to disk: %v", err)
	}
	t.Logf("VERIFIED CONTENT ON DISK: %s", string(data))
	if !strings.Contains(string(data), "TEST_WRITE_OK") {
		t.Fatalf("File content does not contain TEST_WRITE_OK: %s", string(data))
	}
	// Clean up after test
	_ = os.Remove(`C:\projects\algo\NQ2-9\PROJECT_TREE.txt`)
	fmt.Println("SUCCESSFULLY VERIFIED FULL CODEX MCP EXECUTION!")
}

func TestSanitizeToolsListDescriptions(t *testing.T) {
	req := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	rawResp := json.RawMessage(`{
		"jsonrpc":"2.0",
		"id":1,
		"result":{
			"tools":[
				{
					"name":"codex",
					"title":"Codex",
					"description":"Run a Codex session. Accepts configuration parameters matching the Codex Config struct.",
					"inputSchema":{
						"type":"object",
						"properties":{
							"prompt":{"type":"string","description":"The initial user prompt to start the Codex conversation."},
							"model":{"type":"string","description":"Model override"}
						},
						"required":["prompt","model"]
					}
				},
				{
					"name":"codex-reply",
					"title":"Codex Reply",
					"description":"Continue a Codex conversation by providing the thread id and prompt.",
					"inputSchema":{
						"type":"object",
						"properties":{
							"prompt":{"type":"string","description":"The next user prompt to continue the Codex conversation."},
							"model":{"type":"string","description":"Model override"}
						},
						"required":["prompt","model"]
					}
				}
			]
		}
	}`)

	sanitized := sanitizeToolsListDescriptions(req, rawResp)

	var msg struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Title       string `json:"title"`
				Description string `json:"description"`
				InputSchema struct {
					Properties map[string]struct {
						Description string `json:"description"`
					} `json:"properties"`
					Required []string `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(sanitized, &msg); err != nil {
		t.Fatalf("unmarshal sanitized response: %v", err)
	}

	for _, tool := range msg.Result.Tools {
		lowerDesc := strings.ToLower(tool.Description)
		lowerTitle := strings.ToLower(tool.Title)
		if strings.Contains(lowerDesc, "codex") || strings.Contains(lowerDesc, "кодекс") {
			t.Errorf("tool %q description must not contain 'codex' or 'кодекс', got: %q", tool.Name, tool.Description)
		}
		if strings.Contains(lowerTitle, "codex") || strings.Contains(lowerTitle, "кодекс") {
			t.Errorf("tool %q title must not contain 'codex' or 'кодекс', got: %q", tool.Name, tool.Title)
		}
		if !strings.Contains(tool.Description, "файлами") {
			t.Errorf("tool %q description should mention 'файлами', got: %q", tool.Name, tool.Description)
		}
		if !strings.Contains(tool.Description, "20 минут") {
			t.Errorf("tool %q description should mention '20 минут', got: %q", tool.Name, tool.Description)
		}
		if !strings.Contains(tool.Description, "gpt-5.6-sol") {
			t.Errorf("tool %q description should mention 'gpt-5.6-sol', got: %q", tool.Name, tool.Description)
		}
		if !strings.Contains(tool.Description, "thinking: high") {
			t.Errorf("tool %q description should mention 'thinking: high', got: %q", tool.Name, tool.Description)
		}
		if _, hasModel := tool.InputSchema.Properties["model"]; hasModel {
			t.Errorf("tool %q properties must not contain 'model'", tool.Name)
		}
		for _, req := range tool.InputSchema.Required {
			if req == "model" {
				t.Errorf("tool %q required list must not contain 'model'", tool.Name)
			}
		}
		promptDesc := tool.InputSchema.Properties["prompt"].Description
		if strings.Contains(strings.ToLower(promptDesc), "codex") {
			t.Errorf("prompt parameter description must not contain 'codex', got: %q", promptDesc)
		}
	}
}

func TestEnforceDefaultModelAndReasoning(t *testing.T) {
	req := json.RawMessage(`{
		"jsonrpc":"2.0",
		"id":"test-1",
		"method":"tools/call",
		"params":{
			"name":"codex",
			"arguments":{
				"prompt":"do something",
				"model":"o3",
				"cwd":"C:\\test"
			}
		}
	}`)

	enforced := enforceDefaultModelAndReasoning(req)

	var msg struct {
		Params struct {
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(enforced, &msg); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if msg.Params.Arguments["model"] != "gpt-5.6-sol" {
		t.Errorf("expected 'model' to be 'gpt-5.6-sol', got: %v", msg.Params.Arguments["model"])
	}
	cfg, ok := msg.Params.Arguments["config"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'config' object, got: %v", msg.Params.Arguments["config"])
	}
	if cfg["model"] != "gpt-5.6-sol" {
		t.Errorf("expected config.model to be 'gpt-5.6-sol', got: %v", cfg["model"])
	}
	if cfg["model_reasoning_effort"] != "high" {
		t.Errorf("expected config.model_reasoning_effort to be 'high', got: %v", cfg["model_reasoning_effort"])
	}
	if msg.Params.Arguments["prompt"] != "do something" {
		t.Errorf("expected prompt to remain, got: %v", msg.Params.Arguments["prompt"])
	}
	if msg.Params.Arguments["cwd"] != "C:\\test" {
		t.Errorf("expected cwd to remain, got: %v", msg.Params.Arguments["cwd"])
	}
}
