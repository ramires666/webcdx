package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// initializeRequest stays on one line because Codex MCP uses newline-delimited JSON-RPC over stdio.
const initializeRequest = `{"jsonrpc":"2.0","id":"webcodex-init",` +
	`"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
	`"capabilities":{},"clientInfo":{"name":"webcodex-agent","version":"0.1.0"}}}`

type jsonrpcMessage struct {
	ID json.RawMessage `json:"id,omitempty"`
}

// mcpClient serializes writes to Codex MCP and dispatches responses by an
// internal per-call JSON-RPC ID. Public callers are allowed to reuse the same
// JSON-RPC IDs, so forwarding their IDs directly would let concurrent calls
// overwrite each other in pending.
type mcpClient struct {
	stdin io.WriteCloser
	mu    sync.Mutex

	nextID    atomic.Uint64
	pendingMu sync.Mutex
	pending   map[string]chan json.RawMessage
}

func (c *mcpClient) initialize(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	response, err := c.call(ctx, json.RawMessage(initializeRequest))
	if err != nil {
		return fmt.Errorf("initialize Codex MCP: %w", err)
	}

	var msg struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response, &msg); err != nil {
		return fmt.Errorf("parse initialize response: %w", err)
	}
	if msg.Error != nil {
		return errors.New(msg.Error.Message)
	}
	if _, err := c.stdin.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")); err != nil {
		return fmt.Errorf("write initialized notification: %w", err)
	}
	return nil
}

// startMCP launches the configured Codex MCP server over stdio.
func startMCP(ctx context.Context, binary string, args []string) (*mcpClient, error) {
	cmd := exec.CommandContext(ctx, binary, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start command (%s): %w", binary, err)
	}

	client := &mcpClient{
		stdin:   stdin,
		pending: make(map[string]chan json.RawMessage),
	}
	go client.readLoop(stdout)
	go logPipe("codex-mcp", stderr)
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("codex mcp exited: %v", err)
		}
	}()
	return client, nil
}

func (c *mcpClient) call(ctx context.Context, request json.RawMessage) (json.RawMessage, error) {
	var msg jsonrpcMessage
	if err := json.Unmarshal(request, &msg); err != nil {
		return nil, fmt.Errorf("parse jsonrpc request: %w", err)
	}

	if len(msg.ID) == 0 {
		return nil, c.write(request)
	}

	internalID := fmt.Sprintf("webcodex-call-%d", c.nextID.Add(1))
	forwarded, err := replaceJSONRPCID(request, internalID)
	if err != nil {
		return nil, fmt.Errorf("rewrite jsonrpc request id: %w", err)
	}

	respCh := make(chan json.RawMessage, 1)
	c.pendingMu.Lock()
	c.pending[internalID] = respCh
	c.pendingMu.Unlock()
	defer c.forget(internalID)

	if err := c.write(forwarded); err != nil {
		return nil, err
	}

	select {
	case response := <-respCh:
		restored, err := replaceJSONRPCIDRaw(response, msg.ID)
		if err != nil {
			return nil, fmt.Errorf("restore jsonrpc response id: %w", err)
		}
		return sanitizeToolsListDescriptions(request, restored), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func sanitizeToolsListDescriptions(request json.RawMessage, response json.RawMessage) json.RawMessage {
	var req struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(request, &req)
	if req.Method != "tools/list" {
		return response
	}

	var msg map[string]any
	if err := json.Unmarshal(response, &msg); err != nil {
		return response
	}
	result, ok := msg["result"].(map[string]any)
	if !ok {
		return response
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		return response
	}

	for _, t := range tools {
		toolMap, ok := t.(map[string]any)
		if !ok {
			continue
		}
		name, _ := toolMap["name"].(string)
		if name == "codex" || name == "codex-reply" {
			toolMap["title"] = "Работа с файлами и консолью"
			if name == "codex" {
				toolMap["description"] = "Работа с файлами и выполнение команд в рабочей директории проекта. Создание, запись, чтение, проверка файлов и запуск команд терминала."
			} else {
				toolMap["description"] = "Продолжение работы с файлами и выполнения команд в рабочей директории проекта."
			}
			if schema, ok := toolMap["inputSchema"].(map[string]any); ok {
				if props, ok := schema["properties"].(map[string]any); ok {
					if promptProp, ok := props["prompt"].(map[string]any); ok {
						promptProp["description"] = "Инструкция или задача: работа с файлами (создание, чтение, запись) или запуск команды в папке проекта."
					}
					if cwdProp, ok := props["cwd"].(map[string]any); ok {
						cwdProp["description"] = "Рабочая папка проекта для выполнения операций с файлами и командами."
					}
				}
			}
		}
	}

	sanitized, err := json.Marshal(msg)
	if err != nil {
		return response
	}
	return sanitized
}

func replaceJSONRPCID(message json.RawMessage, id string) (json.RawMessage, error) {
	encodedID, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	return replaceJSONRPCIDRaw(message, encodedID)
}

func replaceJSONRPCIDRaw(message json.RawMessage, id json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(message, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("jsonrpc message must be an object")
	}
	object["id"] = append(json.RawMessage(nil), id...)
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func (c *mcpClient) write(request json.RawMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, err := c.stdin.Write(request); err != nil {
		return fmt.Errorf("write request: %w", err)
	}
	if _, err := c.stdin.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write newline: %w", err)
	}
	return nil
}

// readLoop dispatches each Codex MCP response to the call waiting on its internal JSON-RPC ID.
func (c *mcpClient) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var msg jsonrpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			log.Printf("codex mcp bad json: %v", err)
			continue
		}
		if len(msg.ID) == 0 {
			continue
		}

		var internalID string
		if err := json.Unmarshal(msg.ID, &internalID); err != nil || internalID == "" {
			log.Printf("codex mcp response has invalid internal id %s", string(msg.ID))
			continue
		}

		c.pendingMu.Lock()
		respCh := c.pending[internalID]
		c.pendingMu.Unlock()
		if respCh == nil {
			log.Printf("codex mcp response for unknown id %q", internalID)
			continue
		}

		response := append(json.RawMessage(nil), line...)
		respCh <- response
	}
	if err := scanner.Err(); err != nil {
		log.Printf("codex mcp stdout: %v", err)
	}
}

func (c *mcpClient) forget(key string) {
	c.pendingMu.Lock()
	delete(c.pending, key)
	c.pendingMu.Unlock()
}

func logPipe(name string, reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		log.Printf("%s: %s", name, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		log.Printf("%s: %v", name, err)
	}
}
