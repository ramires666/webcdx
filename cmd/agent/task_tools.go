package main

import (
	"context"
	"log"
	"strings"
)

type taskOwnerContext struct{}

func withTaskOwner(ctx context.Context, owner string) context.Context {
	if owner == "" {
		return ctx
	}
	return context.WithValue(ctx, taskOwnerContext{}, owner)
}

func (e *nativeExecutor) taskToolsAvailable(ctx context.Context) bool {
	owner, _ := ctx.Value(taskOwnerContext{}).(string)
	return e.tasks != nil && owner != "" && (e.source != "gate" || e.tasks.gate)
}

func startConfiguredTasks() *taskService {
	tasks, err := taskServiceFromEnvironment()
	if err != nil {
		log.Printf("OpenCode tasks are unavailable: %v", err)
	}
	return tasks
}

func taskToolSchemas() []mcpToolDefinition {
	str := func(description string, max int) map[string]any {
		return map[string]any{"type": "string", "description": description, "minLength": 1, "maxLength": max}
	}
	object := func(properties map[string]any, required ...string) map[string]any {
		if required == nil {
			required = []string{}
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	tool := func(name, description string, input map[string]any, readOnly bool) mcpToolDefinition {
		return mcpToolDefinition{
			Name: name, Description: description, InputSchema: input,
			Annotations: map[string]any{"readOnlyHint": readOnly, "destructiveHint": false, "openWorldHint": name == "start_tasks"},
		}
	}
	batch := str("Batch ID returned by start_tasks.", 64)
	return []mcpToolDefinition{
		tool("task_capabilities", "Return the configured text-only OpenCode model and task queue limits.", object(map[string]any{}), true),
		tool("start_tasks", "Queue independent text tasks in separate OpenCode processes. Include the necessary context in each prompt. Workers return text with OpenCode tools disabled. Reuse the same request key after a lost response.", object(map[string]any{
			"request_key": str("Stable request key for this batch.", 128),
			"tasks": map[string]any{"type": "array", "minItems": 1, "maxItems": taskBatchMax, "items": object(map[string]any{
				"key":             str("Unique task key within the batch (letters, digits, underscore, or hyphen).", 48),
				"prompt":          str("Task instructions and input context; up to 16 KiB of UTF-8.", taskPromptMax),
				"timeout_seconds": map[string]any{"type": "integer", "minimum": 10, "maximum": 1800, "default": 180},
			}, "key", "prompt")},
		}, "request_key", "tasks"), false),
		tool("get_tasks", "Get status for all jobs in a batch. Fetch individual output with task_result.", object(map[string]any{"batch_id": batch}, "batch_id"), true),
		tool("task_result", "Get text output or error for one job in a batch.", object(map[string]any{
			"batch_id": batch, "key": str("Task key within this batch.", 48),
		}, "batch_id", "key"), true),
		tool("cancel_tasks", "Request cancellation of all queued and running jobs in this batch. Poll get_tasks to confirm termination.", object(map[string]any{"batch_id": batch}, "batch_id"), false),
	}
}

func isTaskTool(name string) bool {
	switch name {
	case "task_capabilities", "start_tasks", "get_tasks", "task_result", "cancel_tasks":
		return true
	}
	return false
}

func (e *nativeExecutor) executeTaskTool(ctx context.Context, name string, args map[string]any) (any, error) {
	if !e.taskToolsAvailable(ctx) {
		return nil, toolErr("task_tools_unavailable", "task tools are disabled or this connection has no task owner")
	}
	owner := ctx.Value(taskOwnerContext{}).(string)
	s := e.tasks
	switch name {
	case "task_capabilities":
		if err := onlyArgs(args); err != nil {
			return nil, err
		}
		return map[string]any{"runner": "opencode", "model": s.model, "reasoning": "none", "mode": "text_only",
			"max_parallel": s.parallel, "max_batch_size": taskBatchMax, "max_pending": taskPendingMax}, nil
	case "start_tasks":
		if err := onlyArgs(args, "request_key", "tasks"); err != nil {
			return nil, err
		}
		key, err := requiredString(args, "request_key")
		if err != nil || len(key) > 128 {
			return nil, toolErr("invalid_arguments", "request_key must contain 1 to 128 bytes")
		}
		specs, err := parseTaskSpecs(args["tasks"])
		if err != nil {
			return nil, err
		}
		return s.start(owner, key, specs)
	}
	fields := []string{"batch_id"}
	if name == "task_result" {
		fields = append(fields, "key")
	}
	if err := onlyArgs(args, fields...); err != nil {
		return nil, err
	}
	id, _ := args["batch_id"].(string)
	if !taskIDPattern.MatchString(id) {
		return nil, toolErr("invalid_arguments", "invalid batch_id")
	}
	key, _ := args["key"].(string)
	if name == "task_result" && !taskNamePattern.MatchString(key) {
		return nil, toolErr("invalid_arguments", "invalid task key")
	}
	return s.lookup(owner, id, name, key)
}

func parseTaskSpecs(input any) ([]taskSpec, error) {
	rows, ok := input.([]any)
	if !ok || len(rows) == 0 || len(rows) > taskBatchMax {
		return nil, toolErr("invalid_arguments", "tasks must contain 1 to 100 entries")
	}
	seen := make(map[string]bool, len(rows))
	specs := make([]taskSpec, 0, len(rows))
	total := 0
	for _, row := range rows {
		args, ok := row.(map[string]any)
		if !ok {
			return nil, toolErr("invalid_arguments", "each task must be an object")
		}
		if err := onlyArgs(args, "key", "prompt", "timeout_seconds"); err != nil {
			return nil, err
		}
		key, _ := args["key"].(string)
		if !taskNamePattern.MatchString(key) || seen[strings.ToLower(key)] {
			return nil, toolErr("invalid_arguments", "tasks must have valid keys unique within the batch (case-insensitive)")
		}
		seen[strings.ToLower(key)] = true
		prompt, _ := args["prompt"].(string)
		total += len(prompt)
		if strings.TrimSpace(prompt) == "" || len(prompt) > taskPromptMax || total > taskBatchPromptMax {
			return nil, toolErr("invalid_arguments", "task prompts exceed the size limits or contain empty text")
		}
		timeout, err := optionalInt(args, "timeout_seconds", 180, 10, 1800)
		if err != nil {
			return nil, err
		}
		specs = append(specs, taskSpec{Key: key, Prompt: prompt, Timeout: timeout})
	}
	return specs, nil
}

func (e *nativeExecutor) gateTaskOwner(clientID, agentID string) string {
	if clientID == "" || agentID == "" || e.gateOperationDir == "" {
		return ""
	}
	return commandHash([]string{"gate", e.gateOperationDir, clientID, agentID})
}
