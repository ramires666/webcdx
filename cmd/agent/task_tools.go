package main

import (
	"context"
	"log"
	"strings"
	"time"
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
	list := func(description string, limit, length int) map[string]any {
		return map[string]any{"type": "array", "items": str(description, length), "maxItems": limit}
	}
	return []mcpToolDefinition{
		tool("task_capabilities", "Return the configured OpenCode model, workspace modes and task queue limits.", object(map[string]any{}), true),
		tool("start_tasks", "Queue separate OpenCode jobs. Provide disjoint exact write paths for coding tasks. Git tasks read files from a pinned commit and create reviewable patches. Reuse the same request key after a lost response.", object(map[string]any{
			"request_key":  str("Stable request key for this batch.", 128),
			"project_root": str("Absolute configured Git repository root for tasks with files or context_files.", 4096),
			"tasks": map[string]any{"type": "array", "minItems": 1, "maxItems": taskBatchMax, "items": object(map[string]any{
				"key":             str("Unique task key within the batch (letters, digits, underscore, or hyphen).", 48),
				"prompt":          str("Task instructions and input context; up to 16 KiB of UTF-8.", taskPromptMax),
				"depends_on":      list("A task key from this batch to wait for.", taskBatchMax-1, 48),
				"files":           list("Exact project-relative writable file path using forward slashes.", taskFileLimit, 1024),
				"context_files":   list("Exact project-relative read-only file path using forward slashes.", taskFileLimit, 1024),
				"timeout_seconds": map[string]any{"type": "integer", "minimum": 10, "maximum": 1800, "default": 180},
			}, "key", "prompt")},
		}, "request_key", "tasks"), false),
		tool("list_tasks", "List batches owned by this connection and their status counts. Use next_cursor to continue.", object(map[string]any{
			"cursor": str("Opaque next_cursor from the preceding list_tasks page.", 64),
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 25, "default": 10},
		}), true),
		tool("wait_tasks", "Wait briefly for a batch change. Pass the revision from get_tasks or the preceding wait_tasks call.", object(map[string]any{
			"batch_id": batch, "revision": str("Revision of your last batch status.", 64),
			"timeout_ms": map[string]any{"type": "integer", "minimum": 0, "maximum": 5000, "default": 5000},
		}, "batch_id"), true),
		tool("get_tasks", "Get status for all jobs in a batch. Fetch individual output with task_result.", object(map[string]any{"batch_id": batch}, "batch_id"), true),
		tool("task_result", "Get text output or error for one job in a batch.", object(map[string]any{
			"batch_id": batch, "key": str("Task key within this batch.", 48),
		}, "batch_id", "key"), true),
		tool("task_patch", "Read a verified patch chunk after task_result or task_integrate. Use exactly one of key and integration_id.", object(map[string]any{
			"batch_id": batch, "key": str("Task key within this batch.", 48), "integration_id": str("ID returned by task_integrate.", 64),
			"offset":    map[string]any{"type": "integer", "minimum": 0, "maximum": taskPatchBytes},
			"max_bytes": map[string]any{"type": "integer", "minimum": 4, "maximum": 64 << 10},
		}, "batch_id"), true),
		tool("task_integrate", "Combine explicitly selected patches in a private Git worktree. Use digests from task_result. Returns a combined patch for review and leaves the original checkout unchanged.", object(map[string]any{
			"batch_id": batch,
			"patches": map[string]any{"type": "array", "minItems": 1, "maxItems": taskBatchMax, "items": object(map[string]any{
				"key": str("Completed task key.", 48), "sha256": str("Digest of this reviewed task patch.", 64),
			}, "key", "sha256")},
		}, "batch_id", "patches"), false),
		tool("cancel_tasks", "Request cancellation of all queued and running jobs in this batch. Poll get_tasks to confirm termination.", object(map[string]any{"batch_id": batch}, "batch_id"), false),
	}
}

func isTaskTool(name string) bool {
	switch name {
	case "task_capabilities", "start_tasks", "get_tasks", "task_result", "cancel_tasks", "task_patch", "task_integrate", "list_tasks", "wait_tasks":
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
		modes := []string{"text_only"}
		if s.workspaces != nil {
			modes = append(modes, "git_worktree")
		}
		return map[string]any{"runner": "opencode", "model": s.model, "reasoning": "none", "mode": "text_only", "workspace_modes": modes,
			"dependencies": true, "max_parallel": s.parallel, "max_batch_size": taskBatchMax, "max_pending": taskPendingMax}, nil
	case "list_tasks":
		if err := onlyArgs(args, "cursor", "limit"); err != nil {
			return nil, err
		}
		cursor, err := optionalString(args, "cursor")
		if err != nil || cursor != "" && !taskIDPattern.MatchString(cursor) {
			return nil, toolErr("invalid_arguments", "invalid task history cursor")
		}
		limit, err := optionalInt(args, "limit", 10, 1, 25)
		if err != nil {
			return nil, err
		}
		return s.listTaskGroups(owner, cursor, limit)
	case "start_tasks":
		if err := onlyArgs(args, "request_key", "tasks", "project_root"); err != nil {
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
		project, err := optionalString(args, "project_root")
		if err != nil {
			return nil, err
		}
		return e.startTaskGroup(ctx, owner, key, project, specs)
	}
	fields := []string{"batch_id"}
	switch name {
	case "wait_tasks":
		fields = append(fields, "revision", "timeout_ms")
	case "task_result":
		fields = append(fields, "key")
	case "task_patch":
		fields = append(fields, "key", "integration_id", "offset", "max_bytes")
	case "task_integrate":
		fields = append(fields, "patches")
	}
	if err := onlyArgs(args, fields...); err != nil {
		return nil, err
	}
	id, _ := args["batch_id"].(string)
	if !taskIDPattern.MatchString(id) {
		return nil, toolErr("invalid_arguments", "invalid batch_id")
	}
	if name == "wait_tasks" {
		revision, err := optionalString(args, "revision")
		if err != nil || revision != "" && !taskIDPattern.MatchString(revision) {
			return nil, toolErr("invalid_arguments", "invalid task revision")
		}
		ms, err := optionalInt(args, "timeout_ms", 5000, 0, 5000)
		if err != nil {
			return nil, err
		}
		return s.waitTaskGroup(ctx, owner, id, revision, time.Duration(ms)*time.Millisecond)
	}
	if name == "task_integrate" {
		selection, err := parseTaskPatchSelection(args["patches"])
		if err != nil {
			return nil, err
		}
		return e.integrateTaskPatches(ctx, owner, id, selection)
	}
	key, err := optionalString(args, "key")
	if err != nil {
		return nil, err
	}
	if name == "task_patch" {
		integration, err := optionalString(args, "integration_id")
		if err != nil || (key == "") == (integration == "") ||
			key != "" && !taskNamePattern.MatchString(key) || integration != "" && !taskIDPattern.MatchString(integration) {
			return nil, toolErr("invalid_arguments", "provide exactly one valid key or integration_id")
		}
		offset, err := optionalInt(args, "offset", 0, 0, taskPatchBytes)
		if err != nil {
			return nil, err
		}
		limit, err := optionalInt(args, "max_bytes", 32<<10, 4, 64<<10)
		if err != nil {
			return nil, err
		}
		return s.readPatch(owner, id, key, integration, offset, limit)
	}
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
		if err := onlyArgs(args, "key", "prompt", "timeout_seconds", "depends_on", "files", "context_files"); err != nil {
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
		deps, err := optionalTaskStringArray(args, "depends_on", taskBatchMax-1)
		if err != nil {
			return nil, err
		}
		writePaths, err := optionalTaskStringArray(args, "files", taskFileLimit)
		if err != nil {
			return nil, err
		}
		readPaths, err := optionalTaskStringArray(args, "context_files", taskFileLimit)
		if err != nil {
			return nil, err
		}
		spec := taskSpec{Key: key, Prompt: prompt, Timeout: timeout, DependsOn: deps}
		if len(writePaths)+len(readPaths) > 0 {
			spec.Workspace = &taskWorkspaceSpec{Files: writePaths, ContextFiles: readPaths}
		}
		specs = append(specs, spec)
	}
	if err := validateTaskDependencies(specs); err != nil {
		return nil, err
	}
	return specs, nil
}

func (e *nativeExecutor) gateTaskOwner(clientID, agentID string) string {
	if clientID == "" || agentID == "" || e.gateOperationDir == "" {
		return ""
	}
	return commandHash([]string{"gate", e.gateOperationDir, clientID, agentID})
}
