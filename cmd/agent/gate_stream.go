package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"webcodex/internal/protocol"
)

// mcpRunner represents an entity that can execute an MCP JSON-RPC call (native or legacy).
type mcpRunner interface {
	call(ctx context.Context, request json.RawMessage) (json.RawMessage, error)
}

// streamOnce holds one outbound NDJSON connection and dispatches gate requests concurrently.
func streamOnce(ctx context.Context, client *http.Client, gateURL, token string, runner mcpRunner) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gateURL+"/agent/stream", nil)
	if err != nil {
		return fmt.Errorf("create stream request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream status %s", resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var request protocol.AgentRequest
		if err := json.Unmarshal(line, &request); err != nil {
			log.Printf("bad stream json: %v", err)
			continue
		}
		go handleRequest(ctx, client, gateURL, token, runner, request)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}
	return errors.New("stream closed")
}

// handleRequest forwards one gate request to the MCP runner and posts the correlated result.
func handleRequest(
	ctx context.Context,
	client *http.Client,
	gateURL string,
	token string,
	runner mcpRunner,
	request protocol.AgentRequest,
) {
	started := time.Now()
	actionDesc := formatActionRequest(request.Request)
	log.Printf("▶ %s (id=%s, bytes=%d)", actionDesc, request.ID, len(request.Request))

	callCtx, cancel := context.WithTimeout(ctx, durationEnv("WEBCODEX_MCP_CALL_TIMEOUT", 20*time.Minute))
	defer cancel()

	response, err := runner.call(callCtx, request.Request)
	actionResult := formatActionResponse(response, err, time.Since(started))
	log.Printf("◀ %s (id=%s)", actionResult, request.ID)
	result := protocol.AgentResponse{ID: request.ID, Response: response}
	if err != nil {
		result.Error = err.Error()
	}
	if request.ID == "" {
		return
	}
	if err := sendResult(ctx, client, gateURL, token, result); err != nil {
		log.Printf("send result: %v", err)
	}
}

// sendResult returns a completed Codex MCP call to the gate.
func sendResult(
	ctx context.Context,
	client *http.Client,
	gateURL string,
	token string,
	result protocol.AgentResponse,
) error {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(result); err != nil {
		return fmt.Errorf("encode result: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, gateURL+"/agent/result", &body)
	if err != nil {
		return fmt.Errorf("create result request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post result: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("result status %s", resp.Status)
	}
	return nil
}
