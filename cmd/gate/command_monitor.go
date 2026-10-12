package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

func (s *server) commandMonitorAgent(w http.ResponseWriter, r *http.Request, tool string) (*Agent, bool) {
	w.Header().Set("Cache-Control", "no-store")
	agent, err := s.store.GetAgent(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return nil, false
	}
	if !agent.Enabled || !newToolPolicy(agent.AllowedTools, agent.DeniedTools).allows(tool) {
		http.Error(w, "tool not allowed", http.StatusForbidden)
		return nil, false
	}
	return agent, true
}

func (s *server) monitorCall(r *http.Request, agentID, tool string, args map[string]any) (map[string]any, error) {
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "monitor", "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args, "_webcodex_contract": "v5"},
	})
	if err != nil {
		return nil, err
	}
	response, err := s.runtimeFor(agentID).callAgent(r.Context(), request, s.timeout)
	if err != nil {
		return nil, err
	}
	var message struct {
		Result struct {
			IsError    bool           `json:"isError"`
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Response, &message); err != nil {
		return nil, err
	}
	if message.Result.IsError || message.Result.Structured == nil {
		return nil, fmt.Errorf("agent rejected %s", tool)
	}
	return message.Result.Structured, nil
}

func (s *server) handleCommandMonitor(w http.ResponseWriter, r *http.Request) {
	agent, ok := s.commandMonitorAgent(w, r, "list_commands")
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	if err := renderAdminTemplate(w, "admin-commands.html", map[string]any{"Agent": agent, "CSRF": s.adminCSRF}); err != nil {
		http.Error(w, "render monitor", http.StatusInternalServerError)
	}
}

func (s *server) handleCommandData(w http.ResponseWriter, r *http.Request) {
	tool, args := "list_commands", map[string]any{"limit": 500}
	if id := r.URL.Query().Get("session_id"); id != "" {
		tool, args = "poll_command", map[string]any{"session_id": id, "max_bytes": 65536}
		for _, name := range []string{"stdout_offset", "stderr_offset"} {
			offset, err := strconv.ParseInt(r.URL.Query().Get(name), 10, 53)
			if err != nil || offset < 0 {
				http.Error(w, "invalid offset", http.StatusBadRequest)
				return
			}
			args[name] = offset
		}
	}
	agent, ok := s.commandMonitorAgent(w, r, tool)
	if !ok {
		return
	}
	result, err := s.monitorCall(r, agent.ID, tool, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, result)
}

func (s *server) handleCommandStop(w http.ResponseWriter, r *http.Request) {
	if !s.verifyCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	agent, ok := s.commandMonitorAgent(w, r, "cancel_command")
	if !ok {
		return
	}
	id := r.FormValue("session_id")
	if id == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}
	result, err := s.monitorCall(r, agent.ID, "cancel_command", map[string]any{"session_id": id})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, result)
}

func (s *server) handleCommandLog(w http.ResponseWriter, r *http.Request) {
	stream, id := r.URL.Query().Get("stream"), r.URL.Query().Get("session_id")
	if (stream != "stdout" && stream != "stderr") || id == "" {
		http.Error(w, "invalid log", http.StatusBadRequest)
		return
	}
	offset := float64(0)
	for r.Context().Err() == nil {
		agent, ok := s.commandMonitorAgent(w, r, "poll_command")
		if !ok {
			return
		}
		args := map[string]any{"session_id": id, "stdout_offset": 0, "stderr_offset": 0, "max_bytes": 65536}
		args[stream+"_offset"] = offset
		result, err := s.monitorCall(r, agent.ID, "poll_command", args)
		if err != nil {
			if offset == 0 {
				http.Error(w, err.Error(), http.StatusBadGateway)
			}
			return
		}
		text, _ := result[stream].(string)
		next, _ := result[stream+"_next_offset"].(float64)
		if offset == 0 {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="`+stream+`.log"`)
		}
		if _, err := fmt.Fprint(w, text); err != nil {
			return
		}
		if next <= offset || text == "" {
			return
		}
		offset = next
		if offset >= 100<<20 {
			return
		}
	}
}
