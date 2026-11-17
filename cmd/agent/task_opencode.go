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

type openCodeTaskRunner struct {
	executable string
	model      string
	key        string
}

func (r openCodeTaskRunner) run(ctx context.Context, dir string, spec taskSpec) taskView {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return taskFailure("failed", "could not create private OpenCode workspace")
	}
	remove := true
	defer func() {
		if remove {
			_ = os.RemoveAll(dir)
		}
	}()
	childEnv, err := r.environment(dir)
	if err != nil {
		return taskFailure("failed", "could not configure private OpenCode workspace")
	}

	cmd := exec.CommandContext(ctx, r.executable, "run", "--pure", "--format", "json",
		"--model", r.model, "--variant", "none")
	cmd.Dir = dir
	cmd.Env = childEnv
	cmd.Stdin = strings.NewReader(spec.Prompt)
	cmd.Stderr = io.Discard
	output := &taskEventWriter{}
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
	if err != nil {
		return taskFailure("failed", "OpenCode process did not complete successfully")
	}
	if len(output.pending) != 0 {
		output.event(output.pending)
	}
	if output.failed {
		return taskFailure("failed", "OpenCode returned an error or invalid JSON event")
	}
	if output.text.Len() == 0 {
		return taskFailure("failed", "OpenCode returned no text")
	}
	value := strings.ReplaceAll(output.text.String(), r.key, "[redacted]")
	value, cut := truncateUTF8(value, taskOutputMax)
	return taskView{Status: "completed", Output: value, Truncated: cut || output.truncated}
}

func taskFailure(status, message string) taskView {
	return taskView{Status: status, Error: message}
}

func (r openCodeTaskRunner) environment(dir string) ([]string, error) {
	values := []string{"OPENCODE_API_KEY=" + r.key, "NO_COLOR=1",
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
		{"home", []string{"OPENCODE_TEST_HOME"}},
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
	config, err := json.Marshal(map[string]any{
		"model": r.model, "small_model": r.model, "enabled_providers": []string{"opencode-go"},
		"permission": map[string]string{"*": "deny"}, "mcp": map[string]any{}, "plugin": []string{},
		"autoupdate": false, "share": "disabled", "snapshot": false,
	})
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
	truncated bool
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
		Type string `json:"type"`
		Part struct {
			Text string `json:"text"`
		} `json:"part"`
	}
	if !utf8.Valid(line) || json.Unmarshal(line, &event) != nil || event.Type == "error" {
		w.failed = true
		return
	}
	if event.Type == "text" && event.Part.Text != "" && !w.truncated {
		part, cut := truncateUTF8(event.Part.Text+"\n", taskOutputMax-w.text.Len())
		w.text.WriteString(part)
		w.truncated = cut
	}
}
