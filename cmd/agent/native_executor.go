package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// nativeExecutor implements a direct, zero-limit MCP runner in pure Go.
type nativeExecutor struct {
	tools   []mcpToolDefinition
	mu      sync.Mutex
	lastCwd string
}

type mcpToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func newNativeExecutor() *nativeExecutor {
	return &nativeExecutor{
		tools: []mcpToolDefinition{
			{
				Name:        "codex",
				Description: "Run a Codex session. Directly executes commands and file operations locally with zero limits.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"prompt": map[string]any{
							"type":        "string",
							"description": "The command, file operation, or prompt for Codex.",
						},
						"cwd": map[string]any{
							"type":        "string",
							"description": "Working directory for the operation.",
						},
						"sandbox": map[string]any{
							"type":        "string",
							"description": "Sandbox mode (danger-full-access, workspace-write, read-only).",
						},
					},
					"required": []string{"prompt"},
				},
			},
			{
				Name:        "codex-reply",
				Description: "Continue a Codex conversation by providing the thread id and prompt.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"prompt": map[string]any{
							"type":        "string",
							"description": "The next prompt to continue the session.",
						},
						"threadId": map[string]any{
							"type":        "string",
							"description": "Thread ID for this session.",
						},
					},
					"required": []string{"prompt"},
				},
			},
			{
				Name:        "exec_command",
				Description: "Execute a shell command locally on the worker machine (PowerShell on Windows, bash/sh on Unix) with timeout and working directory support.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"description": "Shell command line to execute (e.g. 'python run_backtest.py', 'git status', 'npm test').",
						},
						"cmd": map[string]any{
							"type":        "string",
							"description": "Alternative alias for command.",
						},
						"workdir": map[string]any{
							"type":        "string",
							"description": "Optional working directory path where the command should be run.",
						},
						"timeout_sec": map[string]any{
							"type":        "integer",
							"description": "Optional execution timeout in seconds (default: 600).",
						},
					},
					"required": []string{"command"},
				},
			},
			{
				Name:        "shell_command",
				Description: "Alias for exec_command. Execute a command in the local shell.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"description": "Command line to execute.",
						},
						"workdir": map[string]any{
							"type":        "string",
							"description": "Optional working directory.",
						},
					},
					"required": []string{"command"},
				},
			},
			{
				Name:        "read_file",
				Description: "Read file contents from the local filesystem with optional line offset and limit.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Absolute or relative path to the file to read.",
						},
						"file_path": map[string]any{
							"type":        "string",
							"description": "Alternative alias for path.",
						},
						"offset": map[string]any{
							"type":        "integer",
							"description": "1-based starting line number to read from (optional).",
						},
						"limit": map[string]any{
							"type":        "integer",
							"description": "Maximum number of lines to read (optional).",
						},
					},
					"required": []string{"path"},
				},
			},
			{
				Name:        "write_file",
				Description: "Write text content directly to a file on the local filesystem. Automatically creates parent directories if needed.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path to the file to create or overwrite.",
						},
						"file_path": map[string]any{
							"type":        "string",
							"description": "Alternative alias for path.",
						},
						"content": map[string]any{
							"type":        "string",
							"description": "Full text content to write into the file.",
						},
					},
					"required": []string{"path", "content"},
				},
			},
			{
				Name:        "list_dir",
				Description: "List files and subdirectories in a local directory with file sizes and modification dates.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Directory path to inspect (default: current working directory).",
						},
					},
				},
			},
			{
				Name:        "apply_patch",
				Description: "Apply a unified diff patch to files in the repository using git apply.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"patch": map[string]any{
							"type":        "string",
							"description": "Unified diff patch content to apply.",
						},
						"input": map[string]any{
							"type":        "string",
							"description": "Alternative alias for patch.",
						},
						"workdir": map[string]any{
							"type":        "string",
							"description": "Optional working directory where patch should be applied.",
						},
					},
					"required": []string{"patch"},
				},
			},
			{
				Name:        "grep_search",
				Description: "Search for text or regular expression across files in a directory.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "Text substring or regular expression to search for.",
						},
						"path": map[string]any{
							"type":        "string",
							"description": "Directory or file path to search in (default: current directory).",
						},
						"max_results": map[string]any{
							"type":        "integer",
							"description": "Maximum number of matching lines to return (default: 100).",
						},
					},
					"required": []string{"query"},
				},
			},
		},
	}
}

// call handles MCP JSON-RPC requests directly in Go.
func (e *nativeExecutor) call(ctx context.Context, request json.RawMessage) (json.RawMessage, error) {
	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(request, &msg); err != nil {
		return nil, fmt.Errorf("parse jsonrpc: %w", err)
	}

	rawID := msg.ID
	if len(rawID) == 0 {
		rawID = json.RawMessage("null")
	}

	switch msg.Method {
	case "initialize":
		res := map[string]any{
			"jsonrpc": "2.0",
			"id":      rawID,
			"result": map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities": map[string]any{
					"tools": map[string]any{
						"listChanged": true,
					},
				},
				"serverInfo": map[string]string{
					"name":    "webcodex-direct",
					"title":   "WebCodex Native Direct Agent",
					"version": "1.0.0",
				},
			},
		}
		return json.Marshal(res)

	case "notifications/initialized":
		return nil, nil

	case "ping":
		return json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      rawID,
			"result":  map[string]any{},
		})

	case "tools/list":
		res := map[string]any{
			"jsonrpc": "2.0",
			"id":      rawID,
			"result": map[string]any{
				"tools": e.tools,
			},
		}
		return json.Marshal(res)

	case "tools/call":
		var callParams struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(msg.Params, &callParams); err != nil {
			return makeErrorResult(rawID, fmt.Sprintf("invalid tools/call params: %v", err)), nil
		}

		output, isError := e.executeTool(ctx, callParams.Name, callParams.Arguments)
		resultData := map[string]any{
			"content": []map[string]any{
				{
					"type": "text",
					"text": output,
				},
			},
			"isError": isError,
		}
		if callParams.Name == "codex" || callParams.Name == "codex-reply" {
			resultData["threadId"] = "direct-session-1"
		}

		res := map[string]any{
			"jsonrpc": "2.0",
			"id":      rawID,
			"result":  resultData,
		}
		return json.Marshal(res)

	default:
		return json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      rawID,
			"error": map[string]any{
				"code":    -32601,
				"message": fmt.Sprintf("method not found: %s", msg.Method),
			},
		})
	}
}

func makeErrorResult(id json.RawMessage, message string) json.RawMessage {
	out, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": message},
			},
			"isError": true,
		},
	})
	return out
}

func (e *nativeExecutor) executeTool(ctx context.Context, name string, args map[string]any) (string, bool) {
	if args == nil {
		args = map[string]any{}
	}

	switch name {
	case "exec_command", "shell_command", "bash":
		return e.handleExecCommand(ctx, args)

	case "read_file", "view_file":
		return e.handleReadFile(args)

	case "write_file":
		return e.handleWriteFile(args)

	case "list_dir", "directory_list":
		return e.handleListDir(args)

	case "apply_patch":
		return e.handleApplyPatch(ctx, args)

	case "grep_search", "file_search":
		return e.handleGrepSearch(args)

	case "codex", "codex-reply":
		return e.handleCodexCall(ctx, args)

	default:
		return fmt.Sprintf("unknown tool: %q", name), true
	}
}

func (e *nativeExecutor) handleExecCommand(ctx context.Context, args map[string]any) (string, bool) {
	cmdStr, _ := args["command"].(string)
	if cmdStr == "" {
		cmdStr, _ = args["cmd"].(string)
	}
	if strings.TrimSpace(cmdStr) == "" {
		return "error: missing required argument 'command'", true
	}

	workdir, _ := args["workdir"].(string)
	if workdir == "" {
		workdir, _ = args["cwd"].(string)
	}

	timeoutSec := 600
	if t, ok := args["timeout_sec"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(cmdCtx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", cmdStr)
	} else {
		cmd = exec.CommandContext(cmdCtx, "bash", "-c", cmdStr)
	}

	if workdir != "" {
		cmd.Dir = workdir
	}

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	err := cmd.Run()
	output := combined.String()

	const maxOutput = 512 * 1024 // 512 KB
	if len(output) > maxOutput {
		output = output[:maxOutput] + "\n... [output truncated, exceeded 512KB]"
	}

	if cmdCtx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("Command timed out after %d seconds.\nOutput so far:\n%s", timeoutSec, output), true
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Sprintf("Command finished with exit code %d.\n%s", exitErr.ExitCode(), output), true
		}
		return fmt.Sprintf("Execution error: %v\nOutput:\n%s", err, output), true
	}

	if strings.TrimSpace(output) == "" {
		return "Command executed successfully (no output).", false
	}
	return output, false
}

func (e *nativeExecutor) handleReadFile(args map[string]any) (string, bool) {
	path, _ := args["path"].(string)
	if path == "" {
		path, _ = args["file_path"].(string)
	}
	if path == "" {
		return "error: missing required argument 'path'", true
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("failed to read file %q: %v", path, err), true
	}

	offset := 0
	if off, ok := args["offset"].(float64); ok && off > 0 {
		offset = int(off) - 1
	}

	limit := -1
	if lim, ok := args["limit"].(float64); ok && lim > 0 {
		limit = int(lim)
	}

	if offset > 0 || limit > 0 {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		var lines []string
		currentLine := 0
		for scanner.Scan() {
			if currentLine >= offset {
				if limit > 0 && len(lines) >= limit {
					break
				}
				lines = append(lines, scanner.Text())
			}
			currentLine++
		}
		return strings.Join(lines, "\n"), false
	}

	return string(data), false
}

func (e *nativeExecutor) handleWriteFile(args map[string]any) (string, bool) {
	path, _ := args["path"].(string)
	if path == "" {
		path, _ = args["file_path"].(string)
	}
	if path == "" {
		return "error: missing required argument 'path'", true
	}

	content, ok := args["content"].(string)
	if !ok {
		return "error: missing required argument 'content'", true
	}

	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Sprintf("failed to create directory %q: %v", dir, err), true
		}
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Sprintf("failed to write file %q: %v", path, err), true
	}

	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path), false
}

func (e *nativeExecutor) handleListDir(args map[string]any) (string, bool) {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Sprintf("failed to list directory %q: %v", path, err), true
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Directory listing of %s (%d entries):\n", path, len(entries)))

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if entry.IsDir() {
			sb.WriteString(fmt.Sprintf("  [DIR]  %-30s  %s\n", entry.Name()+"/", info.ModTime().Format("2006-01-02 15:04")))
		} else {
			sb.WriteString(fmt.Sprintf("  [FILE] %-30s  %8d bytes  %s\n", entry.Name(), info.Size(), info.ModTime().Format("2006-01-02 15:04")))
		}
	}

	return sb.String(), false
}

func (e *nativeExecutor) handleApplyPatch(ctx context.Context, args map[string]any) (string, bool) {
	patch, _ := args["patch"].(string)
	if patch == "" {
		patch, _ = args["input"].(string)
	}
	if strings.TrimSpace(patch) == "" {
		return "error: missing required argument 'patch'", true
	}

	workdir, _ := args["workdir"].(string)

	cmd := exec.CommandContext(ctx, "git", "apply", "--whitespace=nowarn", "-")
	if workdir != "" {
		cmd.Dir = workdir
	}
	cmd.Stdin = strings.NewReader(patch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("git apply failed: %v\n%s", err, string(out)), true
	}

	return "Patch applied successfully.", false
}

func (e *nativeExecutor) handleGrepSearch(args map[string]any) (string, bool) {
	query, _ := args["query"].(string)
	if query == "" {
		return "error: missing required argument 'query'", true
	}

	rootPath, _ := args["path"].(string)
	if rootPath == "" {
		rootPath = "."
	}

	maxResults := 100
	if m, ok := args["max_results"].(float64); ok && m > 0 {
		maxResults = int(m)
	}

	regex, err := regexp.Compile("(?i)" + query)
	if err != nil {
		return fmt.Sprintf("invalid search pattern: %v", err), true
	}

	var results []string
	ignoreDirs := map[string]bool{
		".git": true, "node_modules": true, "target": true, "bin": true,
		".venv": true, "venv": true, "__pycache__": true, ".idea": true,
	}

	err = filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if ignoreDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		if len(results) >= maxResults {
			return filepath.SkipAll
		}

		ext := strings.ToLower(filepath.Ext(path))
		if isBinaryExt(ext) {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		lineNum := 1
		for scanner.Scan() {
			line := scanner.Text()
			if regex.MatchString(line) {
				results = append(results, fmt.Sprintf("%s:%d: %s", path, lineNum, strings.TrimSpace(line)))
				if len(results) >= maxResults {
					return filepath.SkipAll
				}
			}
			lineNum++
		}
		return nil
	})

	if err != nil && err != filepath.SkipAll {
		return fmt.Sprintf("search error: %v", err), true
	}

	if len(results) == 0 {
		return fmt.Sprintf("No matches found for %q in %s", query, rootPath), false
	}

	return fmt.Sprintf("Found %d matches:\n%s", len(results), strings.Join(results, "\n")), false
}

// Regex patterns for parsing ChatGPT instructions to the legacy codex tool
var (
	// Matches file creation instructions:
	// "Create or overwrite the file `path` with the following content:"
	// "Create file C:\projects\foo.txt:"
	fileHeaderRegex = regexp.MustCompile(`(?i)(?:create or overwrite(?: the)? file|create(?: the)? file|overwrite(?: the)? file|write(?: to)?(?: the)? file|save to(?: the)? file|создай(?:те)?(?: файл)?|запиши(?:те)?(?: в)?(?: файл)?)\s*[:]?\s*(?:` + "`" + `([^` + "`" + `\r\n]+)` + "`" + `|"([^"\r\n]+)"|'([^'\r\n]+)'|([A-Za-z]:[^\s\r\n:]+|[^\s\r\n:]+))`)

	// Matches file existence checks:
	// "Check whether C:\projects\gptpacman\pacman.html exists"
	// "Check if pacman.html exists"
	checkExistRegex = regexp.MustCompile(`(?i)(?:check whether|check if|verify that|verify if|does|проверь(?:(?: файл)? существует ли)?)\s+(?:the\s+file\s+)?(?:` + "`" + `([^` + "`" + `\r\n]+)` + "`" + `|"([^"\r\n]+)"|'([^'\r\n]+)'|([A-Za-z]:[^\s\r\n]+|[^\s\r\n]+))\s+(?:exists?|exist|существует)`)

	// Matches file read requests:
	// "Read the file pacman.html"
	readFileRegex = regexp.MustCompile(`(?i)(?:read(?: the)? file|show(?: the)? contents? of(?: the)? file|display(?: the)? file|прочитай(?: файл)?|покажи содержимое(?: файла)?)\s*[:]?\s*(?:` + "`" + `([^` + "`" + `\r\n]+)` + "`" + `|"([^"\r\n]+)"|'([^'\r\n]+)'|([A-Za-z]:[^\s\r\n:]+|[^\s\r\n:]+))`)

	// Matches directory listing requests:
	// "List files in C:\projects"
	listDirRegex = regexp.MustCompile(`(?i)(?:list(?: the)? files?(?: in)?|list(?: the)? directory|show files?(?: in)?|список файлов(?: в)?)\s*[:]?\s*(?:` + "`" + `([^` + "`" + `\r\n]+)` + "`" + `|"([^"\r\n]+)"|'([^'\r\n]+)'|([A-Za-z]:[^\s\r\n:]+|[^\s\r\n:]+))?`)

	// Matches command execution requests:
	// "Run the following command:\n```bash\n...\n```"
	runCmdBlockRegex = regexp.MustCompile(`(?si)(?:run(?: the following)? command|execute(?: the following)? command|run:|execute:)\s*[:]?\s*` + "```(?:[a-zA-Z0-9_-]+)?\\r?\\n(.*?)(?:\\r?\\n```|$)")
	runCmdLineRegex  = regexp.MustCompile(`(?i)(?:run(?: the following)? command|execute(?: the following)? command|run:|execute:)\s*[:]?\s*[` + "`" + `"]?([^` + "`" + `"\r\n]+)[` + "`" + `"]?`)

	// Code block extractor
	codeBlockFenceRegex = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\\r?\\n(.*?)\\r?\\n```")
	openFenceRegex      = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\\r?\\n(.*)$")
)

// handleCodexCall handles calls to legacy "codex" and "codex-reply" tools natively in Go.
func (e *nativeExecutor) handleCodexCall(ctx context.Context, args map[string]any) (string, bool) {
	prompt, _ := args["prompt"].(string)
	cwd, _ := args["cwd"].(string)

	e.mu.Lock()
	if cwd != "" {
		e.lastCwd = cwd
	} else if e.lastCwd != "" {
		cwd = e.lastCwd
	}
	e.mu.Unlock()

	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	// 1. Try extracting and writing files
	if written, err := extractAndWriteFiles(prompt, cwd); err == nil && len(written) > 0 {
		return fmt.Sprintf("Successfully created and wrote %d file(s):\n%s", len(written), strings.Join(written, "\n")), false
	}

	// 2. Check if prompt asks to verify file existence
	if match := checkExistRegex.FindStringSubmatch(prompt); len(match) > 0 {
		filePath := firstNonEmpty(match[1], match[2], match[3], match[4])
		filePath = cleanPath(filePath, cwd)
		if fi, err := os.Stat(filePath); err == nil {
			return fmt.Sprintf("File %s exists (size: %d bytes, last modified: %s).", filePath, fi.Size(), fi.ModTime().Format("2006-01-02 15:04:05")), false
		}
		return fmt.Sprintf("File %s does not exist.", filePath), false
	}

	// 3. Check if prompt asks to read file
	if match := readFileRegex.FindStringSubmatch(prompt); len(match) > 0 {
		filePath := firstNonEmpty(match[1], match[2], match[3], match[4])
		filePath = cleanPath(filePath, cwd)
		return e.handleReadFile(map[string]any{"path": filePath})
	}

	// 4. Check if prompt asks to list directory
	if match := listDirRegex.FindStringSubmatch(prompt); len(match) > 0 {
		targetDir := firstNonEmpty(match[1], match[2], match[3], match[4])
		if targetDir == "" {
			targetDir = cwd
		} else {
			targetDir = cleanPath(targetDir, cwd)
		}
		return e.handleListDir(map[string]any{"path": targetDir})
	}

	// 5. Check if prompt asks to run a command in code block or line
	if match := runCmdBlockRegex.FindStringSubmatch(prompt); len(match) > 1 {
		cmdStr := strings.TrimSpace(match[1])
		return e.handleExecCommand(ctx, map[string]any{"command": cmdStr, "workdir": cwd})
	}
	if match := runCmdLineRegex.FindStringSubmatch(prompt); len(match) > 1 {
		cmdStr := strings.TrimSpace(match[1])
		return e.handleExecCommand(ctx, map[string]any{"command": cmdStr, "workdir": cwd})
	}

	// 6. If prompt is a single line, check if it's a direct command line
	trimmedPrompt := strings.TrimSpace(prompt)
	if !strings.Contains(trimmedPrompt, "\n") && len(trimmedPrompt) > 0 {
		lower := strings.ToLower(trimmedPrompt)
		if strings.HasPrefix(lower, "npm ") || strings.HasPrefix(lower, "pip ") ||
			strings.HasPrefix(lower, "python ") || strings.HasPrefix(lower, "git ") ||
			strings.HasPrefix(lower, "go ") || strings.HasPrefix(lower, "cargo ") ||
			strings.HasPrefix(lower, "dir") || strings.HasPrefix(lower, "ls") ||
			strings.HasPrefix(lower, "echo ") {
			return e.handleExecCommand(ctx, map[string]any{"command": trimmedPrompt, "workdir": cwd})
		}
	}

	// 7. General fallback
	return fmt.Sprintf("Codex action processed locally in %s. Ready for operations.", cwd), false
}

func extractAndWriteFiles(prompt string, cwd string) ([]string, error) {
	locs := fileHeaderRegex.FindAllStringSubmatchIndex(prompt, -1)
	if len(locs) == 0 {
		return nil, errors.New("no file creation instruction found")
	}

	var written []string
	for i, loc := range locs {
		fullMatch := prompt[loc[0]:loc[1]]
		submatch := fileHeaderRegex.FindStringSubmatch(fullMatch)
		rawPath := firstNonEmpty(submatch[1], submatch[2], submatch[3], submatch[4])
		filePath := cleanPath(rawPath, cwd)
		if filePath == "" {
			continue
		}

		headerEnd := loc[1]
		var contentSlice string
		if i+1 < len(locs) {
			contentSlice = prompt[headerEnd:locs[i+1][0]]
		} else {
			contentSlice = prompt[headerEnd:]
		}

		var fileContent string
		if cbMatch := codeBlockFenceRegex.FindStringSubmatch(contentSlice); len(cbMatch) > 1 {
			fileContent = cbMatch[1]
		} else if opMatch := openFenceRegex.FindStringSubmatch(contentSlice); len(opMatch) > 1 {
			fileContent = opMatch[1]
		} else if idx := strings.Index(contentSlice, "<!DOCTYPE"); idx >= 0 {
			fileContent = strings.TrimSpace(contentSlice[idx:])
		} else if idx := strings.Index(contentSlice, "<html"); idx >= 0 {
			fileContent = strings.TrimSpace(contentSlice[idx:])
		}

		if fileContent == "" {
			continue
		}

		dir := filepath.Dir(filePath)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("mkdir %s: %w", dir, err)
			}
		}

		if err := os.WriteFile(filePath, []byte(fileContent), 0644); err != nil {
			return nil, fmt.Errorf("write %s: %w", filePath, err)
		}

		written = append(written, fmt.Sprintf("Wrote %s (%d bytes)", filePath, len(fileContent)))
	}

	if len(written) == 0 {
		return nil, errors.New("no content found to write")
	}
	return written, nil
}

func firstNonEmpty(items ...string) string {
	for _, it := range items {
		if strings.TrimSpace(it) != "" {
			return strings.TrimSpace(it)
		}
	}
	return ""
}

func cleanPath(raw string, cwd string) string {
	p := strings.TrimSpace(raw)
	p = strings.Trim(p, "`\"'")
	p = strings.TrimRight(p, ":.,;")
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) && cwd != "" {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

func isBinaryExt(ext string) bool {
	switch ext {
	case ".exe", ".dll", ".so", ".dylib", ".bin", ".iso", ".zip", ".tar", ".gz", ".7z", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".db", ".sqlite":
		return true
	default:
		return false
	}
}
