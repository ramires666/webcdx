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
	"strings"
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
	log.Printf("Агент подключён. Жду действий модели.")

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var request protocol.AgentRequest
		if err := json.Unmarshal(line, &request); err != nil {
			log.Printf("Ошибка: Gate прислал некорректный запрос")
			continue
		}
		if !request.Deadline.IsZero() && !time.Now().Before(request.Deadline) {
			log.Printf("Действие пропущено: время ожидания истекло")
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
	if !request.Deadline.IsZero() && !started.Before(request.Deadline) {
		log.Printf("Действие пропущено: время ожидания истекло")
		return
	}
	actionDesc := formatActionRequest(request.Request)
	executor, _ := runner.(*nativeExecutor)
	debug := executor != nil && executor.verbose
	browserLog := executor != nil && browserConsoleRequest(request.Request)
	if debug {
		log.Printf("▶ %s (id=%s, bytes=%d)", actionDesc, request.ID, len(request.Request))
	} else if actionDesc != "" && !browserLog {
		log.Print("▶ " + strings.TrimSpace(actionDesc))
	}

	callCtx := ctx
	cancel := func() {}
	if !request.Deadline.IsZero() {
		callCtx, cancel = context.WithDeadline(ctx, request.Deadline)
	}
	defer cancel()

	callCtx = context.WithValue(callCtx, commandRequestID{}, request.ID)
	response, err := runner.call(callCtx, request.Request)
	actionResult := formatActionResponse(response, err)
	if debug {
		log.Printf("◀ %s (id=%s, elapsed=%s)", actionResult, request.ID, time.Since(started).Round(time.Millisecond))
	} else if actionResult != "" && !browserLog {
		if actionDesc == "" {
			log.Printf("⚠ %s", actionResult)
		} else {
			log.Printf("⚠ %s — %s", actionResult, strings.TrimSpace(actionDesc))
		}
	}
	result := protocol.AgentResponse{ID: request.ID, Response: response}
	if err != nil {
		result.Error = err.Error()
	}
	if request.ID == "" {
		return
	}
	if err := sendResult(ctx, client, gateURL, token, result); err != nil {
		log.Printf("Не удалось передать результат модели: %s", compactString(err.Error(), 180))
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
