package protocol

import (
	"encoding/json"
	"time"
)

const ExecutionInstructions = "Use explicit file paths and command working directories. Before executing a command, tell the user the command and its purpose, omitting secrets. Prefer yield_time_ms of 1000 to 3000 for prompt feedback. Set timeout_seconds for the expected computation time: default 1200, maximum 86400. A running status means the process is still active, not completed. Continue polling active sessions with poll_command using the returned next offsets until a terminal status, unless the user asks to stop monitoring. Report meaningful progress about every 10 to 20 seconds while monitoring; if there is no new output, report that the process is still running. A client disconnect does not stop the process: after a retry, resume polling the known session_id instead of executing the command again. Report the final status and exit_code, and do not describe a nonzero exit code as success."

// AgentRequest is one raw MCP JSON-RPC message sent from the gate to the local agent.
type AgentRequest struct {
	ID       string          `json:"id"`
	Request  json.RawMessage `json:"request"`
	Deadline time.Time       `json:"deadline"`
}

// AgentResponse is one raw MCP JSON-RPC response sent from the local agent to the gate.
type AgentResponse struct {
	ID       string          `json:"id"`
	Response json.RawMessage `json:"response,omitempty"`
	Error    string          `json:"error,omitempty"`
}
