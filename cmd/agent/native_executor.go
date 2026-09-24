package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxReadBytes       = 10 << 20
	maxSearchFileBytes = 10 << 20
	defaultResponseMax = 256 << 10
	defaultLogMax      = 50 << 20
)

type mcpToolDefinition struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
	Annotations  map[string]any `json:"annotations"`
}

type executorConfig struct {
	AllowedRoots     []string
	LogDir           string
	ProcessTTL       time.Duration
	MaxLogBytes      int64
	MaxResponseBytes int
}

type nativeExecutor struct {
	tools        []mcpToolDefinition
	allowedRoots []string
	allowAll     bool
	logDir       string
	processTTL   time.Duration
	maxLogBytes  int64
	maxResponse  int

	mu       sync.Mutex
	sessions map[string]*processSession
	stop     chan struct{}
	closed   sync.Once
}

type processSession struct {
	id         string
	command    string
	cwd        string
	status     string
	startedAt  time.Time
	finishedAt time.Time
	exitCode   *int
	logPath    string
	cmd        *exec.Cmd
	log        *cappedLogWriter
	done       chan struct{}
}

type cappedLogWriter struct {
	mu      sync.Mutex
	file    *os.File
	max     int64
	written int64
}

func (w *cappedLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if remaining := w.max - w.written; remaining > 0 {
		part := p
		if int64(len(part)) > remaining {
			part = part[:remaining]
		}
		n, err := w.file.Write(part)
		w.written += int64(n)
		if err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *cappedLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

func newNativeExecutor() (*nativeExecutor, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get startup directory: %w", err)
	}
	logDir := env("WEBCODEX_LOG_DIR", "")
	if logDir == "" {
		if executable, executableErr := os.Executable(); executableErr == nil {
			logDir = filepath.Join(filepath.Dir(executable), "logs")
		} else {
			logDir = filepath.Join(cwd, "logs")
		}
	}
	return newNativeExecutorWithConfig(executorConfig{
		AllowedRoots:     splitPathList(env("WEBCODEX_ALLOWED_ROOTS", cwd)),
		LogDir:           logDir,
		ProcessTTL:       durationEnv("WEBCODEX_PROCESS_TTL", 24*time.Hour),
		MaxLogBytes:      int64Env("WEBCODEX_MAX_LOG_BYTES", defaultLogMax),
		MaxResponseBytes: intEnv("WEBCODEX_MAX_RESPONSE_BYTES", defaultResponseMax),
	})
}

func newNativeExecutorWithConfig(config executorConfig) (*nativeExecutor, error) {
	if len(config.AllowedRoots) == 0 {
		return nil, errors.New("at least one allowed root is required")
	}
	if config.ProcessTTL <= 0 {
		config.ProcessTTL = 24 * time.Hour
	}
	if config.MaxLogBytes <= 0 {
		config.MaxLogBytes = defaultLogMax
	}
	if config.MaxResponseBytes <= 0 {
		config.MaxResponseBytes = defaultResponseMax
	}
	if config.LogDir == "" {
		return nil, errors.New("log directory is required")
	}
	logDir, err := filepath.Abs(config.LogDir)
	if err != nil {
		return nil, fmt.Errorf("resolve log directory: %w", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	e := &nativeExecutor{
		tools: localTools(), logDir: filepath.Clean(logDir), processTTL: config.ProcessTTL,
		maxLogBytes: config.MaxLogBytes, maxResponse: config.MaxResponseBytes,
		sessions: make(map[string]*processSession), stop: make(chan struct{}),
	}
	for _, root := range config.AllowedRoots {
		root = strings.TrimSpace(root)
		if root == "*" {
			e.allowAll = true
			continue
		}
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("allowed root must be absolute: %q", root)
		}
		resolved, err := filepath.EvalSymlinks(filepath.Clean(root))
		if err != nil {
			return nil, fmt.Errorf("resolve allowed root %q: %w", root, err)
		}
		e.allowedRoots = append(e.allowedRoots, resolved)
	}
	go e.cleanupLoop()
	return e, nil
}

func localTools() []mcpToolDefinition {
	objectOutput := map[string]any{"type": "object", "additionalProperties": true}
	tool := func(name, description string, properties map[string]any, required []string, readOnly, destructive, openWorld bool) mcpToolDefinition {
		return mcpToolDefinition{
			Name: name, Description: description,
			InputSchema:  map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required},
			OutputSchema: objectOutput,
			Annotations:  map[string]any{"readOnlyHint": readOnly, "destructiveHint": destructive, "openWorldHint": openWorld},
		}
	}
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	integer := func(description string, minimum int) map[string]any {
		return map[string]any{"type": "integer", "minimum": minimum, "description": description}
	}
	return []mcpToolDefinition{
		tool("read_file", "Read a UTF-8 text file or a line range from an absolute local path.", map[string]any{
			"path": str("Absolute file path."), "offset": integer("First line, starting at 1.", 1), "limit": integer("Maximum number of lines.", 1),
		}, []string{"path"}, true, false, false),
		tool("write_file", "Atomically replace a local file with exact text content.", map[string]any{
			"path": str("Absolute file path."), "content": str("Exact text content; an empty string is valid."),
		}, []string{"path", "content"}, false, true, false),
		tool("list_directory", "List one local directory without recursion.", map[string]any{
			"path": str("Absolute directory path."), "max_entries": integer("Maximum entries to return.", 1),
		}, []string{"path"}, true, false, false),
		tool("search_files", "Search text files below an absolute local path for a substring or regular expression.", map[string]any{
			"path": str("Absolute directory path."), "pattern": str("Text or regular expression to find."), "glob": str("Optional file-name glob, for example *.go."),
			"regex":       map[string]any{"type": "boolean", "description": "Treat pattern as a Go regular expression."},
			"max_results": integer("Maximum matching lines to return.", 1),
		}, []string{"path", "pattern"}, true, false, false),
		tool("exec_command", "Start a non-interactive shell command in an explicit local working directory and stream output to a log.", map[string]any{
			"command": str("Shell command."), "cwd": str("Absolute working directory."),
			"timeout_seconds": integer("Maximum process lifetime in seconds.", 1), "yield_time_ms": integer("Wait up to 30000 ms before returning a running session.", 0),
		}, []string{"command", "cwd"}, false, true, true),
		tool("poll_command", "Read new output and status from a command session.", map[string]any{
			"session_id": str("Process session ID."), "offset": integer("Log byte offset.", 0), "max_bytes": integer("Maximum log bytes to return.", 1),
		}, []string{"session_id"}, true, false, false),
		tool("cancel_command", "Terminate a command session and its process tree.", map[string]any{
			"session_id": str("Process session ID."),
		}, []string{"session_id"}, false, true, false),
	}
}

func (e *nativeExecutor) call(ctx context.Context, request json.RawMessage) (json.RawMessage, error) {
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(request, &message); err != nil {
		return rpcError(nil, -32700, "invalid JSON"), nil
	}
	switch message.Method {
	case "initialize":
		return rpcResult(message.ID, map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "local-workspace", "title": "Local Workspace", "version": "1.0.0"},
			"instructions":    "Use explicit file paths and command working directories. Long commands continue through process sessions and logs.",
		}), nil
	case "ping", "notifications/initialized":
		return rpcResult(message.ID, map[string]any{}), nil
	case "tools/list":
		return rpcResult(message.ID, map[string]any{"tools": e.tools}), nil
	case "tools/call":
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(message.Params, &call); err != nil || call.Name == "" {
			return toolResult(message.ID, nil, errors.New("invalid tool call")), nil
		}
		if call.Arguments == nil {
			call.Arguments = map[string]any{}
		}
		result, err := e.executeTool(ctx, call.Name, call.Arguments)
		return toolResult(message.ID, result, err), nil
	default:
		return rpcError(message.ID, -32601, "method not found"), nil
	}
}

func (e *nativeExecutor) executeTool(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "read_file":
		return e.readFile(args)
	case "write_file":
		return e.writeFile(args)
	case "list_directory":
		return e.listDirectory(args)
	case "search_files":
		return e.searchFiles(ctx, args)
	case "exec_command":
		return e.execCommand(ctx, args)
	case "poll_command":
		return e.pollCommand(args)
	case "cancel_command":
		return e.cancelCommand(args)
	default:
		return nil, errors.New("unknown tool")
	}
}

func rpcResult(id json.RawMessage, result any) json.RawMessage {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return out
}

func rpcError(id json.RawMessage, code int, message string) json.RawMessage {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
	return out
}

func toolResult(id json.RawMessage, value any, callErr error) json.RawMessage {
	if callErr != nil {
		value = map[string]any{"error": callErr.Error()}
	}
	encoded, _ := json.Marshal(value)
	return rpcResult(id, map[string]any{
		"isError": callErr != nil, "content": []map[string]any{{"type": "text", "text": string(encoded)}}, "structuredContent": value,
	})
}

func (e *nativeExecutor) readFile(args map[string]any) (any, error) {
	if err := onlyArgs(args, "path", "offset", "limit"); err != nil {
		return nil, err
	}
	path, err := e.resolvePath(requiredString(args, "path"))
	if err != nil {
		return nil, err
	}
	data, err := readLimitedFile(path, maxReadBytes)
	if err != nil {
		return nil, err
	}
	if isBinary(data) {
		return nil, errors.New("binary files are not supported")
	}
	offset, err := optionalInt(args, "offset", 1, 1, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	limit, err := optionalInt(args, "limit", int(^uint(0)>>1), 1, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	lines := strings.SplitAfter(string(data), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	} else if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start := min(offset-1, len(lines))
	requestedEnd := min(start+limit, len(lines))
	end := start
	var content strings.Builder
	byteTruncated := false
	for end < requestedEnd {
		line := lines[end]
		if content.Len()+len(line) > e.maxResponse {
			if content.Len() == 0 {
				line, _ = truncateUTF8(line, e.maxResponse)
				content.WriteString(line)
				end++
			}
			byteTruncated = true
			break
		}
		content.WriteString(line)
		end++
	}
	return map[string]any{
		"path": path, "content": content.String(), "offset": offset, "line_count": end - start,
		"next_offset": end + 1, "truncated": end < len(lines) || byteTruncated,
	}, nil
}

func (e *nativeExecutor) writeFile(args map[string]any) (any, error) {
	if err := onlyArgs(args, "path", "content"); err != nil {
		return nil, err
	}
	path, err := e.resolvePath(requiredString(args, "path"))
	if err != nil {
		return nil, err
	}
	content, ok := args["content"].(string)
	if !ok {
		return nil, errors.New("content must be a string")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create parent directory: %w", err)
	}
	if _, err := e.resolvePath(path); err != nil {
		return nil, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".webcodex-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err = io.WriteString(temporary, content); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("write temporary file: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return nil, fmt.Errorf("replace file: %w", err)
	}
	return map[string]any{"path": path, "bytes_written": len([]byte(content))}, nil
}

func (e *nativeExecutor) listDirectory(args map[string]any) (any, error) {
	if err := onlyArgs(args, "path", "max_entries"); err != nil {
		return nil, err
	}
	path, err := e.resolvePath(requiredString(args, "path"))
	if err != nil {
		return nil, err
	}
	maxEntries, err := optionalInt(args, "max_entries", 1000, 1, 10000)
	if err != nil {
		return nil, err
	}
	maxEntries = min(maxEntries, max(1, e.maxResponse/256))
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("list directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	total := len(entries)
	if len(entries) > maxEntries {
		entries = entries[:maxEntries]
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			items = append(items, map[string]any{"name": entry.Name(), "type": "unknown", "error": infoErr.Error()})
			continue
		}
		kind := "file"
		if info.IsDir() {
			kind = "directory"
		} else if info.Mode()&os.ModeSymlink != 0 {
			kind = "symlink"
		}
		items = append(items, map[string]any{"name": entry.Name(), "type": kind, "size": info.Size(), "modified_at": info.ModTime().UTC()})
	}
	return map[string]any{"path": path, "entries": items, "truncated": total > len(entries), "total_entries": total}, nil
}

type searchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func (e *nativeExecutor) searchFiles(ctx context.Context, args map[string]any) (any, error) {
	if err := onlyArgs(args, "path", "pattern", "glob", "regex", "max_results"); err != nil {
		return nil, err
	}
	root, err := e.resolvePath(requiredString(args, "path"))
	if err != nil {
		return nil, err
	}
	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return nil, errors.New("pattern is required")
	}
	glob, err := optionalString(args, "glob")
	if err != nil {
		return nil, err
	}
	if glob != "" {
		if _, err := filepath.Match(glob, "probe"); err != nil {
			return nil, fmt.Errorf("invalid glob: %w", err)
		}
	}
	maxResults, err := optionalInt(args, "max_results", 200, 1, 5000)
	if err != nil {
		return nil, err
	}
	useRegex, err := optionalBool(args, "regex")
	if err != nil {
		return nil, err
	}
	var expression *regexp.Regexp
	if useRegex {
		expression, err = regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regular expression: %w", err)
		}
	}
	matches := make([]searchMatch, 0, min(maxResults, 200))
	accessErrors := []string{}
	truncated := false
	responseBytes := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if len(accessErrors) < 100 {
				accessErrors = append(accessErrors, fmt.Sprintf("%s: %v", path, walkErr))
			}
			return nil
		}
		if entry.IsDir() {
			if path != root && skippedDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(matches) >= maxResults {
			truncated = true
			return fs.SkipAll
		}
		if glob != "" {
			matched, _ := filepath.Match(glob, entry.Name())
			if !matched {
				return nil
			}
		}
		safePath, resolveErr := e.resolvePath(path)
		if resolveErr != nil {
			if len(accessErrors) < 100 {
				accessErrors = append(accessErrors, fmt.Sprintf("%s: %v", path, resolveErr))
			}
			return nil
		}
		path = safePath
		data, readErr := readLimitedFile(path, maxSearchFileBytes)
		if readErr != nil {
			if !errors.Is(readErr, errFileTooLarge) {
				if len(accessErrors) < 100 {
					accessErrors = append(accessErrors, fmt.Sprintf("%s: %v", path, readErr))
				}
			}
			return nil
		}
		if isBinary(data) {
			return nil
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		for lineNumber := 1; scanner.Scan(); lineNumber++ {
			line := scanner.Text()
			found := strings.Contains(line, pattern)
			if expression != nil {
				found = expression.MatchString(line)
			}
			if found {
				remaining := e.maxResponse - responseBytes
				if remaining <= 0 {
					truncated = true
					return fs.SkipAll
				}
				line, _ = truncateUTF8(line, min(4096, remaining))
				matches = append(matches, searchMatch{Path: path, Line: lineNumber, Text: line})
				responseBytes += len(path) + len(line) + 64
				if len(matches) >= maxResults {
					truncated = true
					return fs.SkipAll
				}
			}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			if len(accessErrors) < 100 {
				accessErrors = append(accessErrors, fmt.Sprintf("%s: %v", path, scanErr))
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, fmt.Errorf("search files: %w", err)
	}
	return map[string]any{"path": root, "matches": matches, "errors": accessErrors, "truncated": truncated}, nil
}

func (e *nativeExecutor) execCommand(ctx context.Context, args map[string]any) (any, error) {
	if err := onlyArgs(args, "command", "cwd", "timeout_seconds", "yield_time_ms"); err != nil {
		return nil, err
	}
	command := requiredString(args, "command")
	if command == "" {
		return nil, errors.New("command is required")
	}
	cwd, err := e.resolvePath(requiredString(args, "cwd"))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("cwd is not a directory: %s", cwd)
	}
	timeoutSeconds, err := optionalInt(args, "timeout_seconds", 1200, 1, 86400)
	if err != nil {
		return nil, err
	}
	yieldMS, err := optionalInt(args, "yield_time_ms", 10000, 0, 30000)
	if err != nil {
		return nil, err
	}
	id, err := randomSessionID()
	if err != nil {
		return nil, err
	}
	logPath := filepath.Join(e.logDir, time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+id+".log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create command log: %w", err)
	}
	logWriter := &cappedLogWriter{file: file, max: e.maxLogBytes}
	cmd := localShellCommand(command)
	cmd.Dir = cwd
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	configureProcess(cmd)
	session := &processSession{
		id: id, command: command, cwd: cwd, status: "running", startedAt: time.Now().UTC(), logPath: logPath,
		cmd: cmd, log: logWriter, done: make(chan struct{}),
	}
	e.mu.Lock()
	e.sessions[id] = session
	e.mu.Unlock()
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintf(logWriter, "start command: %v\n", err)
		_ = logWriter.Close()
		e.mu.Lock()
		session.status = "failed"
		session.finishedAt = time.Now().UTC()
		close(session.done)
		e.mu.Unlock()
		return e.sessionResult(id, nil, 64<<10)
	}
	go e.waitProcess(id)
	go func() {
		timer := time.NewTimer(time.Duration(timeoutSeconds) * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			e.stopSession(id, "timed_out")
		case <-session.done:
		case <-e.stop:
		}
	}()
	if yieldMS > 0 {
		timer := time.NewTimer(time.Duration(yieldMS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-session.done:
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	return e.sessionResult(id, nil, 64<<10)
}

func localShellCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
	}
	return exec.Command("/bin/sh", "-c", command)
}

func (e *nativeExecutor) waitProcess(id string) {
	e.mu.Lock()
	session := e.sessions[id]
	e.mu.Unlock()
	if session == nil {
		return
	}
	err := session.cmd.Wait()
	_ = session.log.Close()
	e.mu.Lock()
	defer e.mu.Unlock()
	if session.status == "running" {
		session.status = "exited"
	}
	if session.cmd.ProcessState != nil {
		exitCode := session.cmd.ProcessState.ExitCode()
		session.exitCode = &exitCode
	}
	if err != nil && session.cmd.ProcessState == nil && session.status == "running" {
		session.status = "failed"
	}
	session.finishedAt = time.Now().UTC()
	close(session.done)
}

func (e *nativeExecutor) pollCommand(args map[string]any) (any, error) {
	if err := onlyArgs(args, "session_id", "offset", "max_bytes"); err != nil {
		return nil, err
	}
	offset, err := optionalInt(args, "offset", 0, 0, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	maxBytes, err := optionalInt(args, "max_bytes", 64<<10, 1, 1<<20)
	if err != nil {
		return nil, err
	}
	maxBytes = min(maxBytes, e.maxResponse)
	return e.sessionResult(requiredString(args, "session_id"), &offset, maxBytes)
}

func (e *nativeExecutor) cancelCommand(args map[string]any) (any, error) {
	if err := onlyArgs(args, "session_id"); err != nil {
		return nil, err
	}
	id := requiredString(args, "session_id")
	e.mu.Lock()
	session := e.sessions[id]
	e.mu.Unlock()
	if session == nil {
		return nil, errors.New("unknown session_id")
	}
	e.stopSession(id, "cancelled")
	select {
	case <-session.done:
	case <-time.After(10 * time.Second):
		return nil, errors.New("process tree did not stop within 10 seconds")
	}
	return e.sessionResult(id, nil, 64<<10)
}

func (e *nativeExecutor) stopSession(id, status string) {
	e.mu.Lock()
	session := e.sessions[id]
	if session == nil || session.status != "running" || session.cmd.Process == nil {
		e.mu.Unlock()
		return
	}
	session.status = status
	cmd := session.cmd
	e.mu.Unlock()
	_ = terminateProcessTree(cmd)
}

func (e *nativeExecutor) sessionResult(id string, offset *int, maxBytes int) (any, error) {
	e.mu.Lock()
	session := e.sessions[id]
	if session == nil {
		e.mu.Unlock()
		return nil, errors.New("unknown session_id")
	}
	status, startedAt, finishedAt, exitCode := session.status, session.startedAt, session.finishedAt, session.exitCode
	logPath, command, cwd := session.logPath, session.command, session.cwd
	e.mu.Unlock()
	start := int64(0)
	if offset != nil {
		start = int64(*offset)
	} else if info, err := os.Stat(logPath); err == nil && info.Size() > int64(maxBytes) {
		start = info.Size() - int64(maxBytes)
	}
	output, nextOffset, more, err := readLog(logPath, start, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("read command log: %w", err)
	}
	durationEnd := time.Now().UTC()
	if !finishedAt.IsZero() {
		durationEnd = finishedAt
	}
	result := map[string]any{
		"session_id": id, "command": command, "cwd": cwd, "status": status, "started_at": startedAt,
		"duration_ms": durationEnd.Sub(startedAt).Milliseconds(), "log_path": logPath, "output": output,
		"next_offset": nextOffset, "truncated": more || start > 0,
	}
	if !finishedAt.IsZero() {
		result["finished_at"] = finishedAt
	}
	if exitCode != nil {
		result["exit_code"] = *exitCode
	}
	return result, nil
}

func readLog(path string, offset int64, maxBytes int) (string, int64, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", offset, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", offset, false, err
	}
	if offset > info.Size() {
		offset = info.Size()
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", offset, false, err
	}
	buffer := make([]byte, min(maxBytes, int(info.Size()-offset)))
	n, err := io.ReadFull(file, buffer)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", offset, false, err
	}
	buffer = buffer[:n]
	leading := 0
	for leading < len(buffer) && buffer[leading]&0xc0 == 0x80 {
		leading++
	}
	buffer = buffer[leading:]
	validLength := len(buffer)
	for validLength > 0 && !utf8.Valid(buffer[:validLength]) {
		validLength--
	}
	next := offset + int64(leading+validLength)
	return string(buffer[:validLength]), next, next < info.Size(), nil
}

func (e *nativeExecutor) cleanupLoop() {
	interval := min(e.processTTL, time.Hour)
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cutoff := time.Now().UTC().Add(-e.processTTL)
			e.mu.Lock()
			for id, session := range e.sessions {
				if !session.finishedAt.IsZero() && session.finishedAt.Before(cutoff) {
					delete(e.sessions, id)
					_ = os.Remove(session.logPath)
				}
			}
			e.mu.Unlock()
		case <-e.stop:
			return
		}
	}
}

func (e *nativeExecutor) Close() {
	e.closed.Do(func() {
		close(e.stop)
		e.mu.Lock()
		sessions := make([]*processSession, 0, len(e.sessions))
		for _, session := range e.sessions {
			if session.status == "running" {
				sessions = append(sessions, session)
			}
		}
		e.mu.Unlock()
		for _, session := range sessions {
			e.stopSession(session.id, "cancelled")
		}
		deadline := time.After(10 * time.Second)
		for _, session := range sessions {
			select {
			case <-session.done:
			case <-deadline:
				return
			}
		}
	})
}

func (e *nativeExecutor) resolvePath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("absolute path is required")
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("path must be absolute: %q", raw)
	}
	path := filepath.Clean(raw)
	resolved, err := resolveExistingPrefix(path)
	if err != nil {
		return "", err
	}
	if e.allowAll {
		return resolved, nil
	}
	for _, root := range e.allowedRoots {
		relative, relErr := filepath.Rel(root, resolved)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path is outside WEBCODEX_ALLOWED_ROOTS: %s", path)
}

func resolveExistingPrefix(path string) (string, error) {
	current := path
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("resolve path: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve path: %w", err)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

var errFileTooLarge = errors.New("file exceeds size limit")

func readLimitedFile(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, errFileTooLarge
	}
	return data, nil
}

func isBinary(data []byte) bool {
	sample := data
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	return bytes.IndexByte(sample, 0) >= 0 || !utf8.Valid(sample)
}

func skippedDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "dist", "build", ".idea", ".vscode":
		return true
	default:
		return false
	}
}

func onlyArgs(args map[string]any, allowed ...string) error {
	set := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		set[name] = true
	}
	for name := range args {
		if !set[name] {
			return errors.New("unknown argument")
		}
	}
	return nil
}

func requiredString(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return value
}

func optionalInt(args map[string]any, name string, fallback, minimum, maximum int) (int, error) {
	value, exists := args[name]
	if !exists {
		return fallback, nil
	}
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) || int(number) < minimum || int(number) > maximum {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", name, minimum, maximum)
	}
	return int(number), nil
}

func optionalString(args map[string]any, name string) (string, error) {
	value, exists := args[name]
	if !exists {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return text, nil
}

func optionalBool(args map[string]any, name string) (bool, error) {
	value, exists := args[name]
	if !exists {
		return false, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return flag, nil
}

func splitPathList(value string) []string {
	parts := strings.Split(value, string(os.PathListSeparator))
	result := parts[:0]
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			result = append(result, part)
		}
	}
	return result
}

func truncateUTF8(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	cut := maxBytes
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut], true
}

func randomSessionID() (string, error) {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("create session id: %w", err)
	}
	return "proc_" + hex.EncodeToString(data), nil
}
