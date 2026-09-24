package main

func localTools(version string) []mcpToolDefinition {
	tool := func(name, description string, input, output map[string]any, readOnly, destructive, openWorld bool) mcpToolDefinition {
		return mcpToolDefinition{
			Name: name, Description: description, InputSchema: input, OutputSchema: output,
			Annotations: map[string]any{"readOnlyHint": readOnly, "destructiveHint": destructive, "openWorldHint": openWorld},
		}
	}
	stringField := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	integerField := func(description string, minimum int) map[string]any {
		return map[string]any{"type": "integer", "minimum": minimum, "description": description}
	}
	booleanField := func(description string) map[string]any {
		return map[string]any{"type": "boolean", "description": description}
	}
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}
	array := func(items map[string]any) map[string]any { return map[string]any{"type": "array", "items": items} }
	nullableString := map[string]any{"type": []string{"string", "null"}}
	nullableInteger := map[string]any{"type": []string{"integer", "null"}}

	fileReadOutput := object(map[string]any{
		"path": stringField("Normalized absolute path."), "content": stringField("UTF-8 text."),
		"sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "description": "Lowercase SHA-256 of the complete file."}, "size": integerField("Complete file size in bytes.", 0),
		"offset": integerField("First returned line.", 1), "line_count": integerField("Returned line count.", 0),
		"next_offset": integerField("Next line offset.", 1), "truncated": booleanField("Whether more content is available."),
	}, "path", "content", "sha256", "size", "offset", "line_count", "next_offset", "truncated")
	writeOutput := object(map[string]any{
		"path": stringField("Normalized absolute path."), "bytes_written": integerField("Bytes written.", 0),
		"sha256": stringField("Lowercase SHA-256 of the resulting file."),
	}, "path", "bytes_written", "sha256")
	entry := object(map[string]any{
		"name": stringField("Base name."), "type": map[string]any{"type": "string", "enum": []string{"file", "directory", "symlink", "other", "unknown"}},
		"size": integerField("Size in bytes.", 0), "modified_at": stringField("UTC modification time."), "error": nullableString,
	}, "name", "type", "size", "modified_at", "error")
	listOutput := object(map[string]any{
		"path": stringField("Normalized absolute path."), "entries": array(entry),
		"truncated": booleanField("Whether entries were omitted."), "total_entries": integerField("Total entry count.", 0),
	}, "path", "entries", "truncated", "total_entries")
	match := object(map[string]any{
		"path": stringField("Normalized absolute file path."), "line": integerField("One-based line number.", 1), "text": stringField("Matching line."),
	}, "path", "line", "text")
	searchOutput := object(map[string]any{
		"path": stringField("Normalized absolute search root."), "matches": array(match), "errors": array(stringField("Per-path access error.")),
		"truncated": booleanField("Whether results were omitted."),
	}, "path", "matches", "errors", "truncated")
	status := map[string]any{"type": "string", "enum": []string{"running", "exited", "failed", "timed_out", "cancelled", "orphaned"}}
	v3SessionOutput := object(map[string]any{
		"session_id": stringField("Process session ID."), "command": stringField("Shell command."), "cwd": stringField("Working directory."),
		"status": status, "started_at": stringField("UTC start time."), "finished_at": nullableString, "exit_code": nullableInteger,
		"duration_ms": integerField("Elapsed milliseconds.", 0), "log_path": stringField("Compatibility log path."),
		"output": stringField("Bounded command output."), "next_offset": integerField("Next byte offset.", 0),
		"truncated": booleanField("Whether output was omitted."), "already_finished": booleanField("Whether cancellation found a finished process."),
	}, "session_id", "command", "cwd", "status", "started_at", "finished_at", "exit_code", "duration_ms", "log_path", "output", "next_offset", "truncated", "already_finished")
	v4SessionProperties := map[string]any{
		"session_id": stringField("Process session ID."), "cwd": stringField("Working directory."), "status": status,
		"started_at": stringField("UTC start time."), "finished_at": nullableString, "exit_code": nullableInteger,
		"duration_ms": integerField("Elapsed milliseconds.", 0), "stdout_log_path": stringField("Standard output log path."),
		"stderr_log_path": stringField("Standard error log path."), "stdout": stringField("Standard output bytes."),
		"stderr": stringField("Standard error bytes."), "stdout_next_offset": integerField("Next standard output offset.", 0),
		"stderr_next_offset": integerField("Next standard error offset.", 0), "stdout_truncated": booleanField("Whether standard output was omitted."),
		"stderr_truncated": booleanField("Whether standard error was omitted."),
	}
	v4SessionRequired := []string{"session_id", "cwd", "status", "started_at", "finished_at", "exit_code", "duration_ms", "stdout_log_path", "stderr_log_path", "stdout", "stderr", "stdout_next_offset", "stderr_next_offset", "stdout_truncated", "stderr_truncated"}
	v4SessionOutput := object(v4SessionProperties, v4SessionRequired...)
	v4CancelProperties := make(map[string]any, len(v4SessionProperties)+1)
	for key, value := range v4SessionProperties {
		v4CancelProperties[key] = value
	}
	v4CancelProperties["already_finished"] = booleanField("Whether the session was already finished.")
	v4CancelOutput := object(v4CancelProperties, append(v4SessionRequired, "already_finished")...)

	readInput := object(map[string]any{
		"path": stringField("Absolute file path."), "offset": integerField("First line, starting at 1.", 1), "limit": integerField("Maximum number of lines.", 1),
	}, "path")
	v3WriteInput := object(map[string]any{
		"path": stringField("Absolute file path."), "content": stringField("Exact text content; an empty string is valid."),
	}, "path", "content")
	v4WriteInput := object(map[string]any{
		"path": stringField("Absolute file path."), "content": stringField("Exact text content; an empty string is valid."),
		"if_absent":       map[string]any{"type": "boolean", "const": true, "description": "Require the destination not to exist."},
		"expected_sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "description": "Required complete-file hash when replacing a file."},
	}, "path", "content")
	v4WriteInput["oneOf"] = []any{
		map[string]any{"required": []string{"if_absent"}, "not": map[string]any{"required": []string{"expected_sha256"}}},
		map[string]any{"required": []string{"expected_sha256"}, "not": map[string]any{"required": []string{"if_absent"}}},
	}
	listInput := object(map[string]any{
		"path": stringField("Absolute directory path."), "max_entries": integerField("Maximum entries to return.", 1),
	}, "path")
	searchInput := object(map[string]any{
		"path": stringField("Absolute directory path."), "pattern": stringField("Text or regular expression to find."),
		"glob": stringField("Optional basename glob."), "regex": booleanField("Treat pattern as a Go regular expression."),
		"max_results": integerField("Maximum matching lines to return.", 1),
	}, "path", "pattern")
	v3ExecInput := object(map[string]any{
		"command": stringField("Shell command."), "cwd": stringField("Absolute working directory."),
		"timeout_seconds": integerField("Maximum process lifetime in seconds.", 1), "yield_time_ms": integerField("Wait up to 30000 ms before returning.", 0),
	}, "command", "cwd")
	argvField := array(stringField("Executable or argument."))
	argvField["minItems"] = 1
	v4ExecInput := object(map[string]any{
		"command": stringField("Shell command."), "argv": argvField, "cwd": stringField("Absolute working directory."),
		"env": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}, "stdin": stringField("Standard input written once and closed."),
		"timeout_seconds": integerField("Maximum process lifetime in seconds.", 1), "yield_time_ms": integerField("Wait up to 30000 ms before returning.", 0),
	}, "cwd")
	v4ExecInput["oneOf"] = []any{
		map[string]any{"required": []string{"command"}, "not": map[string]any{"required": []string{"argv"}}},
		map[string]any{"required": []string{"argv"}, "not": map[string]any{"required": []string{"command"}}},
	}
	v3PollInput := object(map[string]any{
		"session_id": stringField("Process session ID."), "offset": integerField("Compatibility log byte offset.", 0), "max_bytes": integerField("Maximum bytes to return.", 1),
	}, "session_id")
	v4PollInput := object(map[string]any{
		"session_id": stringField("Process session ID."), "stdout_offset": integerField("Standard output byte offset.", 0),
		"stderr_offset": integerField("Standard error byte offset.", 0), "max_bytes": integerField("Maximum combined bytes to return.", 1),
	}, "session_id")
	cancelInput := object(map[string]any{"session_id": stringField("Process session ID.")}, "session_id")

	tools := []mcpToolDefinition{
		tool("read_file", "Read a UTF-8 text file or line range from an absolute local path.", readInput, fileReadOutput, true, false, false),
		tool("write_file", "Atomically write exact UTF-8 text to an absolute local path.", v3WriteInput, writeOutput, false, true, false),
		tool("list_directory", "List one local directory without recursion.", listInput, listOutput, true, false, false),
		tool("search_files", "Search text files below an absolute local path.", searchInput, searchOutput, true, false, false),
		tool("exec_command", "Start a non-interactive shell command in an explicit working directory.", v3ExecInput, v3SessionOutput, false, true, true),
		tool("poll_command", "Read new output and status from a command session.", v3PollInput, v3SessionOutput, true, false, false),
		tool("cancel_command", "Terminate a command session and its process tree.", cancelInput, v3SessionOutput, false, true, false),
	}
	if version != "v4" {
		for index := range tools {
			tools[index].OutputSchema = map[string]any{"type": "object", "additionalProperties": true}
		}
		return tools
	}
	tools[1].InputSchema = v4WriteInput
	tools[4].InputSchema, tools[4].OutputSchema = v4ExecInput, v4SessionOutput
	tools[5].InputSchema, tools[5].OutputSchema = v4PollInput, v4SessionOutput
	tools[6].OutputSchema = v4CancelOutput

	editItem := object(map[string]any{
		"old_text": stringField("Exact non-empty text to replace."), "new_text": stringField("Replacement text; empty is valid."),
		"replace_all": booleanField("Replace every occurrence."),
	}, "old_text", "new_text")
	editInput := object(map[string]any{
		"path": stringField("Absolute file path."), "expected_sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "description": "Current complete-file hash."},
		"edits": map[string]any{"type": "array", "minItems": 1, "items": editItem},
	}, "path", "expected_sha256", "edits")
	editOutput := object(map[string]any{
		"path": stringField("Normalized absolute path."), "old_sha256": stringField("Previous complete-file hash."),
		"new_sha256": stringField("New complete-file hash."), "replacements": integerField("Replacement count.", 1),
		"bytes_written": integerField("Bytes written.", 0),
	}, "path", "old_sha256", "new_sha256", "replacements", "bytes_written")
	findEntry := object(map[string]any{
		"path": stringField("Normalized absolute path."), "relative_path": stringField("Slash-separated path relative to the root."),
		"type": map[string]any{"type": "string", "enum": []string{"file", "directory", "symlink", "other"}},
		"size": integerField("Size in bytes.", 0), "modified_at": stringField("UTC modification time."),
	}, "path", "relative_path", "type", "size", "modified_at")
	findInput := object(map[string]any{
		"path": stringField("Absolute directory path."), "name_glob": stringField("Basename glob."),
		"kind":        map[string]any{"type": "string", "enum": []string{"file", "directory", "any"}},
		"max_results": integerField("Maximum entries to return.", 1),
	}, "path")
	findOutput := object(map[string]any{
		"path": stringField("Normalized absolute root."), "entries": array(findEntry), "errors": array(stringField("Per-path access error.")),
		"truncated": booleanField("Whether results were omitted."),
	}, "path", "entries", "errors", "truncated")
	moveInput := object(map[string]any{
		"source": stringField("Absolute source path."), "destination": stringField("Absolute destination path."),
		"expected_sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "description": "Required complete-file hash for a regular file."},
	}, "source", "destination")
	moveOutput := object(map[string]any{
		"source": stringField("Normalized absolute source path."), "destination": stringField("Normalized absolute destination path."),
		"type": map[string]any{"type": "string", "enum": []string{"file", "directory"}},
	}, "source", "destination", "type")
	deleteInput := object(map[string]any{
		"path": stringField("Absolute path."), "expected_sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "description": "Required complete-file hash for a regular file."},
	}, "path")
	deleteOutput := object(map[string]any{
		"path": stringField("Normalized absolute path."), "deleted": booleanField("Whether deletion succeeded."),
		"type": map[string]any{"type": "string", "enum": []string{"file", "directory"}},
	}, "path", "deleted", "type")
	return append(tools,
		tool("edit_file", "Apply ordered exact text replacements with a complete-file hash precondition.", editInput, editOutput, false, true, false),
		tool("find_files", "Find files and directories recursively by basename.", findInput, findOutput, true, false, false),
		tool("move_path", "Rename a regular file or empty directory without overwriting.", moveInput, moveOutput, false, true, false),
		tool("delete_path", "Delete a regular file or empty directory.", deleteInput, deleteOutput, false, true, false),
	)
}
