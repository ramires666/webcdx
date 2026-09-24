package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"webcodex/internal/protocol"
)

const mcpProtocolVersion = "2025-06-18"

const maxMCPRequestBytes = 1 << 20

type jsonrpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
}

// handleMCP implements the MCP Streamable HTTP entry point and routes calls to the authenticated agent.
func (s *server) handleMCP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	agent, rt, authErr := s.authenticateMCP(r)
	authorized := authErr == nil

	agentLabel := "unauthorized"
	if authorized {
		agentLabel = agent.ID
	}

	log.Printf(
		"mcp %s %s agent=%s ua=%q auth=%t accept=%q content_type=%q session=%q protocol=%q",
		r.Method,
		r.URL.Path,
		agentLabel,
		r.UserAgent(),
		authorized,
		r.Header.Get("Accept"),
		r.Header.Get("Content-Type"),
		r.Header.Get("Mcp-Session-Id"),
		r.Header.Get("Mcp-Protocol-Version"),
	)
	w.Header().Set("Mcp-Protocol-Version", mcpProtocolVersion)

	if r.Method == http.MethodGet {
		s.handleMCPStream(w, r, authorized)
		return
	}
	if r.Method == http.MethodDelete {
		if !authorized {
			s.writeMCPUnauthorized(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !authorized {
		if !s.allowRequest("mcp-auth:"+remoteHost(r), 60, time.Minute) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		s.writeMCPUnauthorized(w, r)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxMCPRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		writeRPCError(w, nil, -32700, "read request")
		return
	}
	var msg jsonrpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		log.Printf("mcp invalid json bytes=%d agent=%s", len(body), agent.ID)
		writeRPCError(w, nil, -32700, "invalid json")
		return
	}

	log.Printf("mcp request agent=%s method=%q id=%s bytes=%d", agent.ID, msg.Method, string(msg.ID), len(body))

	if msg.Method == "initialize" {
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      msg.ID,
			"result": map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities": map[string]any{
					"tools": map[string]any{
						"listChanged": false,
					},
				},
				"serverInfo": map[string]string{
					"name":    "local-workspace",
					"title":   "Local Workspace",
					"version": "1.0.0",
				},
				"instructions": "Use explicit file paths and command working directories. Long commands continue through process sessions and logs.",
			},
		})
		log.Printf("mcp response ok agent=%s method=%q id=%s local=true elapsed=%s", agent.ID, msg.Method, string(msg.ID), time.Since(started))
		return
	}

	if msg.Method == "ping" {
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0",
			"id":      msg.ID,
			"result":  map[string]any{},
		})
		log.Printf("mcp response ok agent=%s method=%q id=%s local=true elapsed=%s", agent.ID, msg.Method, string(msg.ID), time.Since(started))
		return
	}

	if len(msg.ID) == 0 {
		if err := rt.enqueue(r.Context(), protocol.AgentRequest{ID: "", Request: body, Deadline: time.Now().Add(s.timeout)}); err != nil {
			log.Printf("mcp notification enqueue error agent=%s method=%q error=%v", agent.ID, msg.Method, err)
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		log.Printf("mcp notification accepted agent=%s method=%q elapsed=%s", agent.ID, msg.Method, time.Since(started))
		w.WriteHeader(http.StatusAccepted)
		return
	}

	policy := newToolPolicy(agent.AllowedTools, agent.DeniedTools)
	if msg.Method == "tools/call" {
		call, err := parseToolCall(body)
		if err != nil {
			writeToolCallError(w, msg.ID, err.Error())
			return
		}
		if !policy.allows(call.Name) {
			log.Printf("mcp tool denied by policy agent=%s tool=%q", agent.ID, call.Name)
			writeToolCallError(w, msg.ID, fmt.Sprintf("tool not allowed: %q", call.Name))
			return
		}
	}

	agentRequest := body
	if r.URL.Path == "/mcp/v4" {
		agentRequest, err = markContract(body, "v4")
		if err != nil {
			writeRPCError(w, msg.ID, -32602, err.Error())
			return
		}
	}
	resp, err := rt.callAgent(r.Context(), agentRequest, s.timeout)
	if err != nil {
		log.Printf(
			"mcp response error agent=%s method=%q id=%s error=%v elapsed=%s",
			agent.ID,
			msg.Method,
			string(msg.ID),
			err,
			time.Since(started),
		)
		if msg.Method == "tools/call" {
			writeToolCallError(w, msg.ID, fmt.Sprintf("execution error: %v", err))
			return
		}
		writeRPCError(w, msg.ID, -32000, err.Error())
		return
	}

	if msg.Method == "tools/list" {
		response, err := filterToolsList(resp.Response, policy)
		if err != nil {
			log.Printf(
				"mcp filter tools error agent=%s method=%q id=%s error=%v elapsed=%s",
				agent.ID,
				msg.Method,
				string(msg.ID),
				err,
				time.Since(started),
			)
			writeRPCError(w, msg.ID, -32000, err.Error())
			return
		}
		resp.Response = response
	}

	if msg.Method == "tools/call" {
		resp.Response = ensureToolCallResult(resp.Response, msg.ID)
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(resp.Response); err != nil {
		log.Printf("write mcp response agent=%s error=%v", agent.ID, err)
	}
	log.Printf(
		"mcp response ok agent=%s method=%q id=%s bytes=%d elapsed=%s",
		agent.ID,
		msg.Method,
		string(msg.ID),
		len(resp.Response),
		time.Since(started),
	)
}

// handleMCPStream answers the optional MCP GET transport with an SSE stream readiness message.
func (s *server) handleMCPStream(w http.ResponseWriter, r *http.Request, authorized bool) {
	if !authorized {
		s.writeMCPUnauthorized(w, r)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := fmt.Fprint(w, ": local workspace stream ready\n\n"); err != nil {
		log.Printf("write mcp stream: %v", err)
	}
}

func (s *server) writeMCPUnauthorized(w http.ResponseWriter, r *http.Request) {
	mcpPath := "/mcp"
	if r.URL.Path == "/mcp/v2" || r.URL.Path == "/mcp/v3" || r.URL.Path == "/mcp/v4" {
		mcpPath = r.URL.Path
	}
	w.Header().Set(
		"WWW-Authenticate",
		fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource%s", scope="mcp"`, s.publicURL, mcpPath),
	)
}

func markContract(request json.RawMessage, version string) (json.RawMessage, error) {
	var message map[string]any
	if err := json.Unmarshal(request, &message); err != nil {
		return nil, fmt.Errorf("parse request contract: %w", err)
	}
	params, _ := message["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
		message["params"] = params
	}
	params["_webcodex_contract"] = version
	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode request contract: %w", err)
	}
	return encoded, nil
}

type toolCall struct {
	Name      string
	Arguments map[string]any
}

func parseToolCall(request json.RawMessage) (toolCall, error) {
	var msg struct {
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(request, &msg); err != nil {
		return toolCall{}, fmt.Errorf("parse tool call: %w", err)
	}
	if msg.Params.Name == "" {
		return toolCall{}, errors.New("missing tool name")
	}
	if msg.Params.Arguments == nil {
		msg.Params.Arguments = map[string]any{}
	}
	return toolCall{Name: msg.Params.Name, Arguments: msg.Params.Arguments}, nil
}

func ensureToolCallResult(response json.RawMessage, id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	var obj map[string]any
	if err := json.Unmarshal(response, &obj); err != nil {
		out, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"isError": true,
				"content": []map[string]any{
					{"type": "text", "text": string(response)},
				},
			},
		})
		return out
	}

	// Convert a local JSON-RPC error to an MCP tool result with isError: true.
	if errObj, ok := obj["error"]; ok && errObj != nil {
		errMsg := "unknown tool error"
		if errMap, ok := errObj.(map[string]any); ok {
			if msg, ok := errMap["message"].(string); ok && msg != "" {
				errMsg = msg
			}
		} else if str, ok := errObj.(string); ok {
			errMsg = str
		}
		out, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"isError": true,
				"content": []map[string]any{
					{"type": "text", "text": errMsg},
				},
			},
		})
		return out
	}

	return response
}

func writeToolCallError(w http.ResponseWriter, id json.RawMessage, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	writeJSON(w, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]any{
			"isError": true,
			"content": []map[string]any{
				{
					"type": "text",
					"text": message,
				},
			},
		},
	})
}
