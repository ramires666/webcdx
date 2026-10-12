package main

func commandToolsV5(tools []mcpToolDefinition) []mcpToolDefinition {
	properties := tools[4].InputSchema["properties"].(map[string]any)
	properties["request_key"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "Unique key for this launch. Reuse the same key and arguments after Retry; never generate a new key for a lost response."}
	tools[4].InputSchema["required"] = []string{"cwd", "request_key"}
	for _, index := range []int{4, 5, 6} {
		fields := tools[index].OutputSchema["properties"].(map[string]any)
		fields["pid"] = map[string]any{"type": "integer"}
		fields["events"] = map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"at": map[string]any{"type": "string"}, "event": map[string]any{"type": "string"},
				"session_id": map[string]any{"type": "string"}, "request_id": map[string]any{"type": "string"},
				"status": map[string]any{"type": "string"}, "pid": map[string]any{"type": "integer"},
				"exit_code": map[string]any{"type": []string{"integer", "null"}},
			}, "required": []string{"at", "event", "session_id", "status", "pid", "exit_code"},
		}}
		fields["status"] = map[string]any{"type": "string", "enum": []string{"starting", "running", "exited", "failed", "timed_out", "cancelled", "orphaned", "outcome_unknown"}}
	}
	summary := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"session_id": map[string]any{"type": "string"}, "cwd": map[string]any{"type": "string"},
		"pid": map[string]any{"type": "integer"}, "status": map[string]any{"type": "string"},
		"started_at": map[string]any{"type": "string"}, "exit_code": map[string]any{"type": []string{"integer", "null"}},
		"duration_ms": map[string]any{"type": "integer"},
	}, "required": []string{"session_id", "cwd", "pid", "status", "started_at", "exit_code", "duration_ms"}}
	return append(tools, mcpToolDefinition{
		Name: "list_commands", Description: "Find active and recent command sessions after Retry. Never relaunch a command solely because its original response was lost.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "status": map[string]any{"type": "string"},
		}, "required": []string{}},
		OutputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"sessions": map[string]any{"type": "array", "items": summary}, "truncated": map[string]any{"type": "boolean"},
		}, "required": []string{"sessions", "truncated"}},
		Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
	})
}
