package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxReadBytes       = 10 << 20
	maxSearchFileBytes = 10 << 20
	maxStdinBytes      = 10 << 20
	defaultResponseMax = 256 << 10
	defaultLogMax      = 50 << 20
	contractMarker     = "_webcodex_contract"
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

type codedError struct {
	code    string
	message string
	cause   error
}

func (e *codedError) Error() string {
	if e.cause == nil {
		return e.message
	}
	return e.message + ": " + e.cause.Error()
}

func toolErr(code, message string, cause ...error) error {
	var err error
	if len(cause) > 0 {
		err = cause[0]
	}
	return &codedError{code: code, message: message, cause: err}
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
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	e := &nativeExecutor{
		logDir: filepath.Clean(logDir), processTTL: config.ProcessTTL,
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
		e.allowedRoots = append(e.allowedRoots, filepath.Clean(resolved))
	}
	if err := e.restoreSessions(); err != nil {
		return nil, fmt.Errorf("restore command sessions: %w", err)
	}
	go e.cleanupLoop()
	return e, nil
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
	version := contractVersion(message.Params)
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
		return rpcResult(message.ID, map[string]any{"tools": localTools(version)}), nil
	case "tools/call":
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(message.Params, &call); err != nil || call.Name == "" {
			return toolResult(message.ID, nil, toolErr("invalid_arguments", "invalid tool call")), nil
		}
		if call.Arguments == nil {
			call.Arguments = map[string]any{}
		}
		result, err := e.executeTool(ctx, version, call.Name, call.Arguments)
		return toolResult(message.ID, result, err), nil
	default:
		return rpcError(message.ID, -32601, "method not found"), nil
	}
}

func contractVersion(params json.RawMessage) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(params, &object) == nil {
		var version string
		if json.Unmarshal(object[contractMarker], &version) == nil && version == "v4" {
			return version
		}
	}
	return "v3"
}

func (e *nativeExecutor) executeTool(ctx context.Context, version, name string, args map[string]any) (any, error) {
	switch name {
	case "read_file":
		return e.readFile(args)
	case "write_file":
		return e.writeFile(args, version == "v4")
	case "list_directory":
		return e.listDirectory(args)
	case "search_files":
		return e.searchFiles(ctx, args)
	case "exec_command":
		return e.execCommand(ctx, args, version == "v4")
	case "poll_command":
		return e.pollCommand(args, version == "v4")
	case "cancel_command":
		return e.cancelCommand(args, version == "v4")
	case "edit_file":
		if version == "v4" {
			return e.editFile(args)
		}
	case "find_files":
		if version == "v4" {
			return e.findFiles(ctx, args)
		}
	case "move_path":
		if version == "v4" {
			return e.movePath(args)
		}
	case "delete_path":
		if version == "v4" {
			return e.deletePath(args)
		}
	}
	return nil, toolErr("unknown_tool", "unknown tool")
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
		code := "operation_failed"
		var coded *codedError
		if errors.As(callErr, &coded) {
			code = coded.code
		}
		value = map[string]any{"error": map[string]any{"code": code, "message": callErr.Error()}}
	}
	encoded, _ := json.Marshal(value)
	return rpcResult(id, map[string]any{
		"isError": callErr != nil, "content": []map[string]any{{"type": "text", "text": string(encoded)}}, "structuredContent": value,
	})
}

func (e *nativeExecutor) resolvePath(raw string) (string, error) {
	if raw == "" {
		return "", toolErr("invalid_path", "absolute path is required")
	}
	if !filepath.IsAbs(raw) {
		return "", toolErr("invalid_path", fmt.Sprintf("path must be absolute: %q", raw))
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
	return "", toolErr("outside_allowed_roots", fmt.Sprintf("path is outside WEBCODEX_ALLOWED_ROOTS: %s", path))
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
			return "", toolErr("invalid_path", "resolve path", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", toolErr("invalid_path", "resolve path", err)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func onlyArgs(args map[string]any, allowed ...string) error {
	set := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		set[name] = true
	}
	for name := range args {
		if !set[name] {
			return toolErr("invalid_arguments", fmt.Sprintf("unknown argument: %s", name))
		}
	}
	return nil
}

func requiredString(args map[string]any, name string) (string, error) {
	value, ok := args[name].(string)
	if !ok || value == "" {
		return "", toolErr("invalid_arguments", fmt.Sprintf("%s is required", name))
	}
	return value, nil
}

func optionalInt(args map[string]any, name string, fallback, minimum, maximum int) (int, error) {
	value, exists := args[name]
	if !exists {
		return fallback, nil
	}
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) || int(number) < minimum || int(number) > maximum {
		return 0, toolErr("invalid_arguments", fmt.Sprintf("%s must be an integer from %d to %d", name, minimum, maximum))
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
		return "", toolErr("invalid_arguments", fmt.Sprintf("%s must be a string", name))
	}
	return text, nil
}

func optionalBool(args map[string]any, name string) (bool, bool, error) {
	value, exists := args[name]
	if !exists {
		return false, false, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return false, true, toolErr("invalid_arguments", fmt.Sprintf("%s must be a boolean", name))
	}
	return flag, true, nil
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

func randomSessionID() (string, error) {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("create session id: %w", err)
	}
	return "proc_" + hex.EncodeToString(data), nil
}

func (e *nativeExecutor) Close() {
	e.closed.Do(func() { close(e.stop) })
}
