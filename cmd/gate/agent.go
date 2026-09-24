package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"webcodex/internal/protocol"
)

const maxAgentResultBytes = 2 << 20

// handleAgentStream manages the long-lived outbound NDJSON stream for a connected agent worker.
func (s *server) handleAgentStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	agent, rt, err := s.authenticateAgent(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")

	if _, err := fmt.Fprintln(w); err != nil {
		return
	}
	flusher.Flush()

	streamCtx, stream, replaced := rt.activateAgentStream(r.Context())
	log.Printf("agent stream connected agent=%s replaced=%t", agent.ID, replaced)
	go func(id string) {
		_ = s.store.UpdateLastSeen(context.Background(), id, time.Now().UTC())
	}(agent.ID)

	defer func() {
		rt.deactivateAgentStream(stream)
		log.Printf("agent stream disconnected agent=%s", agent.ID)
		go func(id string) {
			_ = s.store.UpdateLastSeen(context.Background(), id, time.Now().UTC())
		}(agent.ID)
	}()

	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()

	enc := json.NewEncoder(w)
	for {
		select {
		case request := <-rt.queue:
			if !request.Deadline.IsZero() && !time.Now().Before(request.Deadline) {
				log.Printf("agent stream rejected expired request agent=%s id=%s", agent.ID, request.ID)
				continue
			}
			log.Printf("agent stream dispatch agent=%s id=%s bytes=%d", agent.ID, request.ID, len(request.Request))
			if err := enc.Encode(request); err != nil {
				return
			}
			flusher.Flush()
			rt.touch()
		case <-heartbeat.C:
			if _, err := fmt.Fprintln(w); err != nil {
				return
			}
			flusher.Flush()
		case <-streamCtx.Done():
			return
		}
	}
}

// handleAgentResult completes the pending MCP call waiting for this agent's response.
func (s *server) handleAgentResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	agent, rt, err := s.authenticateAgent(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAgentResultBytes)
	var result protocol.AgentResponse
	if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	rt.mu.Lock()
	resultCh := rt.pending[result.ID]
	rt.mu.Unlock()

	if resultCh == nil {
		http.Error(w, "unknown request id", http.StatusNotFound)
		return
	}

	log.Printf("agent result received agent=%s id=%s", agent.ID, result.ID)
	rt.touch()
	resultCh <- result
	w.WriteHeader(http.StatusNoContent)
}
