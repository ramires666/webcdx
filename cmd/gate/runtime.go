package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"webcodex/internal/protocol"
)

type activeAgentStream struct {
	cancel context.CancelFunc
}

// agentRuntime manages the in-memory communication state (stream, queue, pending requests) for a single agent.
type agentRuntime struct {
	id string

	queue chan protocol.AgentRequest

	mu       sync.Mutex
	pending  map[string]chan protocol.AgentResponse
	stream   *activeAgentStream
	lastSeen time.Time
}

func newAgentRuntime(id string) *agentRuntime {
	return &agentRuntime{
		id:      id,
		queue:   make(chan protocol.AgentRequest, 128),
		pending: make(map[string]chan protocol.AgentResponse),
	}
}

// activateAgentStream registers an incoming agent stream connection.
// If an active stream already exists for this agent, it is cancelled and replaced.
func (rt *agentRuntime) activateAgentStream(parent context.Context) (context.Context, *activeAgentStream, bool) {
	streamCtx, cancel := context.WithCancel(parent)
	stream := &activeAgentStream{
		cancel: cancel,
	}

	rt.mu.Lock()
	previous := rt.stream
	rt.stream = stream
	rt.lastSeen = time.Now()
	rt.mu.Unlock()

	if previous != nil {
		previous.cancel()
	}

	return streamCtx, stream, previous != nil
}

// deactivateAgentStream cleans up the active stream when the connection terminates.
func (rt *agentRuntime) deactivateAgentStream(stream *activeAgentStream) {
	stream.cancel()
	rt.mu.Lock()
	if rt.stream == stream {
		rt.stream = nil
	}
	rt.lastSeen = time.Now()
	rt.mu.Unlock()
}

// disconnect forcefully disconnects any active stream for this agent (e.g. on disable or token rotation).
func (rt *agentRuntime) disconnect() {
	rt.mu.Lock()
	stream := rt.stream
	rt.stream = nil
	rt.mu.Unlock()

	if stream != nil {
		stream.cancel()
	}
}

// isOnline returns whether the agent has an active connected stream.
func (rt *agentRuntime) isOnline() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.stream != nil
}

// status returns the online status and the timestamp of last activity.
func (rt *agentRuntime) status() (bool, time.Time) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.stream != nil, rt.lastSeen
}

// touch updates the last activity timestamp in memory.
func (rt *agentRuntime) touch() {
	rt.mu.Lock()
	rt.lastSeen = time.Now()
	rt.mu.Unlock()
}

// enqueue places a request into the agent's work queue.
func (rt *agentRuntime) enqueue(ctx context.Context, request protocol.AgentRequest) error {
	if !request.Deadline.IsZero() && !time.Now().Before(request.Deadline) {
		return errors.New("request deadline expired")
	}
	rt.mu.Lock()
	online := rt.stream != nil
	rt.mu.Unlock()

	if !online {
		return errors.New("agent is offline")
	}

	select {
	case rt.queue <- request:
		return nil
	case <-ctx.Done():
		return errors.New("request cancelled")
	default:
		return errors.New("agent queue is full")
	}
}

// callAgent forwards an MCP request to the connected agent and waits for the correlated result.
func (rt *agentRuntime) callAgent(ctx context.Context, request json.RawMessage, timeout time.Duration) (protocol.AgentResponse, error) {
	id, err := randomID()
	if err != nil {
		return protocol.AgentResponse{}, fmt.Errorf("create request id: %w", err)
	}

	resultCh := make(chan protocol.AgentResponse, 1)

	rt.mu.Lock()
	rt.pending[id] = resultCh
	rt.mu.Unlock()

	defer rt.forget(id)

	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := rt.enqueue(ctx, protocol.AgentRequest{ID: id, Request: request, Deadline: deadline}); err != nil {
		return protocol.AgentResponse{}, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case result := <-resultCh:
		if result.Error != "" {
			return protocol.AgentResponse{}, errors.New(result.Error)
		}
		return result, nil
	case <-timer.C:
		return protocol.AgentResponse{}, errors.New("agent call timed out")
	case <-ctx.Done():
		return protocol.AgentResponse{}, ctx.Err()
	}
}

func (rt *agentRuntime) forget(id string) {
	rt.mu.Lock()
	delete(rt.pending, id)
	rt.mu.Unlock()
}
