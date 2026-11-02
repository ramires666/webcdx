package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const remoteBrowserMaxBody = 2 << 20
const remoteBrowserProtocolVersion = "2025-03-26"

var remoteBrowserToolNames = map[string]bool{
	"browser_task_start": true, "browser_task_status": true, "browser_task_events": true,
	"browser_task_result": true, "browser_task_pause": true, "browser_task_resume": true,
	"browser_task_cancel": true,
}

type remoteBrowserMCPConfig struct {
	URL       string
	TokenFile string
	Timeout   time.Duration
}

type remoteBrowserMCP struct {
	endpoint  *url.URL
	tokenFile string
	client    *http.Client
}

func isRemoteBrowserTool(name string) bool { return remoteBrowserToolNames[name] }

func newRemoteBrowserMCP(config remoteBrowserMCPConfig) (*remoteBrowserMCP, error) {
	u, err := url.Parse(strings.TrimSpace(config.URL))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("remote browser MCP URL must be an absolute HTTP(S) URL without credentials or fragment")
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}
	return &remoteBrowserMCP{endpoint: u, tokenFile: config.TokenFile, client: &http.Client{
		Timeout:       config.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *remoteBrowserMCP) token() (string, error) {
	if c.tokenFile == "" {
		return "", errors.New("remote browser MCP token file is not configured")
	}
	b, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return "", fmt.Errorf("read remote browser MCP token file: %w", err)
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", errors.New("remote browser MCP token file is empty")
	}
	return t, nil
}

func (c *remoteBrowserMCP) request(ctx context.Context, method, protocolVersion string, params any, session string, notification bool) (json.RawMessage, string, error) {
	token, err := c.token()
	if err != nil {
		return nil, session, err
	}
	var id json.RawMessage
	if !notification {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, session, fmt.Errorf("create MCP request id: %w", err)
		}
		id, _ = json.Marshal(hex.EncodeToString(b[:]))
	}
	reqObj := map[string]any{"jsonrpc": "2.0", "method": method}
	if !notification {
		reqObj["id"] = id
	}
	if params != nil {
		reqObj["params"] = params
	}
	body, err := json.Marshal(reqObj)
	if err != nil {
		return nil, session, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, session, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if protocolVersion != "" {
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
	}
	if session != "" {
		req.Header.Set("MCP-Session-Id", session)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, session, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("MCP-Session-Id"); sid != "" {
		session = sid
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, session, fmt.Errorf("remote browser MCP redirect rejected (HTTP %d)", resp.StatusCode)
	}
	if notification {
		if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			return nil, session, fmt.Errorf("remote browser MCP notification failed (HTTP %d)", resp.StatusCode)
		}
		return nil, session, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, session, fmt.Errorf("remote browser MCP request failed (HTTP %d)", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, remoteBrowserMaxBody+1))
	if err != nil {
		return nil, session, fmt.Errorf("read remote browser MCP response: %w", err)
	}
	if len(b) > remoteBrowserMaxBody {
		return nil, session, errors.New("remote browser MCP response exceeds size limit")
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		b, err = remoteBrowserSSEData(b)
		if err != nil {
			return nil, session, err
		}
	}
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		return nil, session, fmt.Errorf("remote browser MCP returned unsupported JSON/SSE response: %w", err)
	}
	if !bytes.Equal(envelope.ID, id) {
		return nil, session, errors.New("remote browser MCP response id mismatch")
	}
	if envelope.Error != nil {
		return nil, session, fmt.Errorf("remote browser MCP error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil, session, errors.New("remote browser MCP response has no result")
	}
	return envelope.Result, session, nil
}

func remoteBrowserSSEData(b []byte) ([]byte, error) {
	var data []string
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if len(data) == 0 {
		return nil, errors.New("remote browser MCP SSE response contained no data event")
	}
	joined := []byte(strings.Join(data, "\n"))
	var probe map[string]json.RawMessage
	if json.Unmarshal(joined, &probe) != nil {
		return nil, errors.New("remote browser MCP SSE response did not contain a JSON-RPC message")
	}
	return joined, nil
}

func (c *remoteBrowserMCP) initialized(ctx context.Context, session string) (string, string, error) {
	var initResult json.RawMessage
	var err error
	initResult, session, err = c.request(ctx, "initialize", "", map[string]any{
		"protocolVersion": remoteBrowserProtocolVersion, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "webcdx-remote-browser", "version": "1"},
	}, session, false)
	if err != nil {
		return session, "", err
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(initResult, &init); err != nil || init.ProtocolVersion != remoteBrowserProtocolVersion {
		return session, "", fmt.Errorf("remote browser MCP negotiated unsupported protocol version %q", init.ProtocolVersion)
	}
	_, session, err = c.request(ctx, "notifications/initialized", init.ProtocolVersion, nil, session, true)
	return session, init.ProtocolVersion, err
}

func (c *remoteBrowserMCP) Tools(ctx context.Context) ([]mcpToolDefinition, error) {
	session, protocolVersion, err := c.initialized(ctx, "")
	if err != nil {
		return nil, err
	}
	raw, _, err := c.request(ctx, "tools/list", protocolVersion, map[string]any{}, session, false)
	if err != nil {
		return nil, err
	}
	var response struct {
		Tools []mcpToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode remote browser MCP tools: %w", err)
	}
	allowed := make([]mcpToolDefinition, 0, len(remoteBrowserToolNames))
	seen := map[string]bool{}
	for _, tool := range response.Tools {
		if !isRemoteBrowserTool(tool.Name) {
			continue
		}
		if seen[tool.Name] {
			return nil, fmt.Errorf("remote browser MCP returned duplicate tool %q", tool.Name)
		}
		seen[tool.Name] = true
		if tool.InputSchema == nil {
			tool.InputSchema = map[string]any{"type": "object"}
		}
		tool.Description = strings.TrimSpace(tool.Description + " uebagent runs a bounded task in its Docker Camoufox browser. Typical flow: start, poll status/events, read result; task execution uses its separately configured model endpoint.")
		if tool.Name == "browser_task_start" {
			props, _ := tool.InputSchema["properties"].(map[string]any)
			if props == nil {
				props = map[string]any{}
				tool.InputSchema["properties"] = props
			}
			cookie, _ := props["sync_cookies"].(map[string]any)
			if cookie == nil {
				cookie = map[string]any{"type": "boolean"}
				props["sync_cookies"] = cookie
			}
			cookie["default"] = false
		}
		allowed = append(allowed, tool)
	}
	for name := range remoteBrowserToolNames {
		if !seen[name] {
			return nil, fmt.Errorf("remote browser MCP is missing required tool %q", name)
		}
	}
	return allowed, nil
}

func (c *remoteBrowserMCP) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	if !isRemoteBrowserTool(name) {
		return nil, fmt.Errorf("unsupported remote browser MCP tool %q", name)
	}
	argsCopy := make(map[string]any, len(args))
	for key, value := range args {
		argsCopy[key] = value
	}
	if name == "browser_task_start" {
		if _, ok := argsCopy["sync_cookies"]; !ok {
			argsCopy["sync_cookies"] = false
		}
	}
	session, protocolVersion, err := c.initialized(ctx, "")
	if err != nil {
		return nil, err
	}
	raw, _, err := c.request(ctx, "tools/call", protocolVersion, map[string]any{"name": name, "arguments": argsCopy}, session, false)
	if err != nil {
		return nil, err
	}
	return raw, nil
}
