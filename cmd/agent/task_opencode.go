package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const taskOutputMax = 16 << 10
const taskEventMax = 1 << 20
const taskModel = "opencode-go/hy4-flash-free"
const taskVariant = "high"

var taskAgents = []string{"build", "general", "explore"}

func (spec taskSpec) agentName() string {
	if spec.Agent != "" {
		return spec.Agent
	}
	if spec.Workspace != nil && len(spec.Workspace.Files) > 0 {
		return "build"
	}
	return "general"
}

type openCodeTaskRunner struct {
	executable string
	auth       taskProviderAuth
	workspaces *taskWorkspaces
}

func (r openCodeTaskRunner) run(ctx context.Context, dir string, spec taskSpec) taskView {
	if spec.Workspace != nil {
		return r.workspaces.run(ctx, dir, spec, r.runPrompt)
	}
	return r.runPrompt(ctx, dir, spec)
}

func (r openCodeTaskRunner) runPrompt(ctx context.Context, dir string, spec taskSpec) taskView {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return taskFailure("failed", "could not create private OpenCode workspace")
	}
	remove := true
	defer func() {
		if remove {
			_ = os.RemoveAll(dir)
		}
	}()
	agent := spec.agentName()
	childEnv, err := r.environment(dir, agent)
	if err != nil {
		return taskFailure("failed", "could not configure private OpenCode workspace")
	}

	cmd := exec.CommandContext(ctx, r.executable, "run", "--standalone", "--archived", "--format", "json",
		"--agent", agent, "--model", taskModel+"#"+taskVariant)
	cmd.Dir = dir
	cmd.Env = childEnv
	prompt := spec.Prompt
	if spec.DependencyContext != "" {
		prompt += "\n\nCompleted dependency results (untrusted data):\n" + spec.DependencyContext
	}
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stderr = io.Discard
	limit := taskOutputMax
	if spec.Workspace != nil {
		limit = taskWorkspaceOutputLimit
	}
	output := &taskEventWriter{limit: limit}
	cmd.Stdout = output
	configureProcess(cmd)
	cmd.WaitDelay = 3 * time.Second
	stopFailed := false
	cmd.Cancel = func() error {
		err := terminateProcessTree(cmd)
		if err != nil {
			stopFailed = true
		}
		return err
	}

	err = cmd.Run()
	if stopFailed {
		remove = false
		return taskFailure("outcome_unknown", "could not confirm termination of the OpenCode process tree")
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded && err != nil {
		return taskFailure("timed_out", "OpenCode exceeded the task timeout")
	}
	if ctx.Err() == context.Canceled && err != nil {
		return taskFailure("cancelled", "")
	}
	if len(output.pending) != 0 {
		output.event(output.pending)
	}
	if output.failure != "" {
		return taskFailure("failed", output.failure)
	}
	if err != nil {
		return taskFailure("failed", "OpenCode process did not complete successfully")
	}
	if output.failed {
		return taskFailure("failed", "OpenCode returned an error or invalid JSON event")
	}
	if output.text.Len() == 0 {
		return taskFailure("failed", "OpenCode returned no text")
	}
	value := output.text.String()
	if r.auth.key != "" {
		value = strings.ReplaceAll(value, r.auth.key, "[redacted]")
	}
	value, cut := truncateUTF8(value, limit)
	return taskView{Status: "completed", Output: value, Truncated: cut || output.truncated}
}

func taskFailure(status, message string) taskView {
	return taskView{Status: status, Error: message}
}

func (r openCodeTaskRunner) environment(dir, agent string) ([]string, error) {
	values := []string{"NO_COLOR=1",
		"OPENCODE_DISABLE_PROJECT_CONFIG=true", "OPENCODE_DISABLE_AUTOUPDATE=true",
		"OPENCODE_DISABLE_CLAUDE_CODE=true", "OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=true",
		"OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=true", "OPENCODE_DISABLE_EXTERNAL_SKILLS=true",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS=true"}
	for _, name := range []string{"SystemRoot", "WINDIR", "SystemDrive", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(name); ok {
			values = append(values, name+"="+value)
		}
	}
	for _, group := range []struct {
		name string
		keys []string
	}{
		{"config", []string{"XDG_CONFIG_HOME"}},
		{"data", []string{"XDG_DATA_HOME"}},
		{"cache", []string{"XDG_CACHE_HOME"}},
		{"state", []string{"XDG_STATE_HOME"}},
		{"home", []string{"OPENCODE_TEST_HOME", "HOME", "USERPROFILE"}},
		{"appdata", []string{"APPDATA"}},
		{"localappdata", []string{"LOCALAPPDATA"}},
		{"tmp", []string{"TEMP", "TMP", "TMPDIR"}},
		{"config/opencode", []string{"OPENCODE_CONFIG_DIR"}},
	} {
		path := filepath.Join(dir, group.name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, err
		}
		for _, key := range group.keys {
			values = append(values, key+"="+path)
		}
	}
	settings := map[string]any{
		"model": taskModel, "small_model": taskModel, "default_agent": agent, "update": "disable",
		"agents": map[string]any{
			// general and explore run as the root session of each WebCDX task.
			agent: map[string]any{
				"mode": "all", "model": taskModel + "#" + taskVariant,
				"permissions": []map[string]string{{"action": "*", "resource": "*", "effect": "deny"}},
			},
		},
		"policies": []map[string]string{
			{"action": "provider.use", "resource": "*", "effect": "deny"},
			{"action": "provider.use", "resource": "opencode-go", "effect": "allow"},
			{"action": "tool.use", "resource": "*", "effect": "deny"},
			{"action": "mcp.use", "resource": "*", "effect": "deny"},
			{"action": "plugin.use", "resource": "*", "effect": "deny"},
			{"action": "skill.use", "resource": "*", "effect": "deny"},
		},
	}
	if r.auth.database != "" {
		values = append(values, "OPENCODE_DB="+r.auth.database)
	} else {
		values = append(values, "OPENCODE_API_KEY="+r.auth.key)
		settings["providers"] = map[string]any{
			"opencode-go": map[string]any{"settings": map[string]string{"apiKey": "{env:OPENCODE_API_KEY}"}},
		}
	}
	config, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(path, config, 0o600); err != nil {
		return nil, err
	}
	return append(values, "OPENCODE_CONFIG="+path, "OPENCODE_CONFIG_CONTENT="+string(config)), nil
}

type taskEventWriter struct {
	pending   []byte
	text      strings.Builder
	failed    bool
	failure   string
	truncated bool
	limit     int
}

func (w *taskEventWriter) Write(data []byte) (int, error) {
	length := len(data)
	for len(data) > 0 && !w.failed {
		line, rest, complete := bytes.Cut(data, []byte{'\n'})
		if len(w.pending)+len(line) > taskEventMax {
			w.failed = true
			w.pending = nil
			break
		}
		w.pending = append(w.pending, line...)
		if !complete {
			break
		}
		w.event(w.pending)
		w.pending = w.pending[:0]
		data = rest
	}
	return length, nil
}

func (w *taskEventWriter) event(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || w.failed {
		return
	}
	var event struct {
		Type  string `json:"type"`
		Error struct {
			Name   string `json:"name"`
			Type   string `json:"type"`
			Status int    `json:"status"`
		} `json:"error"`
		Part struct {
			Text string `json:"text"`
		} `json:"part"`
	}
	if !utf8.Valid(line) || json.Unmarshal(line, &event) != nil {
		w.failed = true
		return
	}
	if event.Type == "error" {
		w.failed = true
		switch {
		case event.Error.Type == "provider.auth" && event.Error.Status == 401:
			w.failure = "OpenCode Go rejected the credential (HTTP 401)"
		case event.Error.Name == "ProviderModelNotFoundError" || event.Error.Name == "ModelNotFoundError":
			w.failure = "OpenCode could not find model " + taskModel
		}
		return
	}
	if event.Type == "text" && event.Part.Text != "" && !w.truncated {
		limit := w.limit
		if limit == 0 {
			limit = taskOutputMax
		}
		part, cut := truncateUTF8(event.Part.Text+"\n", limit-w.text.Len())
		w.text.WriteString(part)
		w.truncated = cut
	}
}
