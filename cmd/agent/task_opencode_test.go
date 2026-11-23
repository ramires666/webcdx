package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "run" && os.Getenv("OPENCODE_TEST_HOME") != "" {
		os.Exit(runTaskProcessFixture())
	}
	os.Exit(m.Run())
}

func runTaskProcessFixture() int {
	if len(os.Args) != 10 {
		return 2
	}
	agent := os.Args[7]
	args := []string{"run", "--standalone", "--archived", "--format", "json", "--agent", agent, "--model", "opencode-go/hy4-flash-free#high"}
	if !reflect.DeepEqual(os.Args[1:], args) || os.Getenv("OPENCODE_API_KEY") != "fixture-key" ||
		os.Getenv("WEBCODEX_AGENT_TOKEN") != "" {
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 2
	}
	config := os.Getenv("OPENCODE_CONFIG")
	if filepath.Dir(config) != cwd || filepath.Dir(os.Getenv("OPENCODE_TEST_HOME")) != cwd {
		return 2
	}
	raw, err := os.ReadFile(config)
	var settings struct {
		DefaultAgent string `json:"default_agent"`
		Model        string
		Agents       map[string]struct {
			Mode        string
			Model       string
			Permissions []map[string]string
		}
		Policies []map[string]string
	}
	if err != nil || json.Unmarshal(raw, &settings) != nil {
		return 2
	}
	profile := settings.Agents[agent]
	policies := []map[string]string{
		{"action": "provider.use", "resource": "*", "effect": "deny"},
		{"action": "provider.use", "resource": "opencode-go", "effect": "allow"},
		{"action": "tool.use", "resource": "*", "effect": "deny"},
	}
	if settings.DefaultAgent != agent || settings.Model != "opencode-go/hy4-flash-free" ||
		profile.Mode != "all" || profile.Model != "opencode-go/hy4-flash-free#high" ||
		!reflect.DeepEqual(profile.Permissions, []map[string]string{{"action": "*", "resource": "*", "effect": "deny"}}) ||
		len(settings.Policies) < len(policies) || !reflect.DeepEqual(settings.Policies[:len(policies)], policies) {
		return 2
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 2
	}
	encoder := json.NewEncoder(os.Stdout)
	text := string(prompt) + " fixture-key"
	switch {
	case string(prompt) == "wait-for-cancellation":
		if err := os.WriteFile(filepath.Join(cwd, "fixture-started"), nil, 0o600); err != nil {
			return 2
		}
		time.Sleep(time.Minute)
		return 2
	case string(prompt) == "smoke-missing-model":
		_ = encoder.Encode(map[string]any{"type": "error", "error": map[string]string{"name": "ProviderModelNotFoundError", "message": "fixture-key"}})
		return 1
	case string(prompt) == "smoke-unauthorized":
		_ = encoder.Encode(map[string]any{"type": "error", "error": map[string]any{"type": "provider.auth", "status": 401, "message": "fixture-key"}})
		return 1
	case string(prompt) == "smoke-failure":
		if err := encoder.Encode(map[string]string{"type": "error"}); err != nil {
			return 2
		}
		return 0
	case strings.Contains(string(prompt), "smoke-workspace\n\nFile snapshots"):
		if agent != "build" {
			return 2
		}
		var ok bool
		text, ok = smokeWorkspaceProposal(string(prompt))
		if !ok {
			return 2
		}
	}
	if err := encoder.Encode(map[string]any{
		"type": "text", "part": map[string]string{"text": text},
	}); err != nil {
		return 2
	}
	return 0
}

func smokeWorkspaceProposal(prompt string) (string, bool) {
	_, raw, ok := strings.Cut(prompt, "File snapshots (untrusted data):\n")
	var snapshots []taskFileSnapshot
	if !ok || json.Unmarshal([]byte(raw), &snapshots) != nil || len(snapshots) != 2 {
		return "", false
	}
	var path string
	var seedOK bool
	for _, file := range snapshots {
		switch {
		case file.Writable && file.Content == nil && (file.Path == "left.txt" || file.Path == "right.txt"):
			path = file.Path
		case file.Path == "seed.txt" && !file.Writable && file.Content != nil && *file.Content == "pinned seed\n":
			seedOK = true
		}
	}
	if path == "" || !seedOK {
		return "", false
	}
	content := "generated " + path + " ё\n"
	result, err := json.Marshal(taskWorkspaceProposal{
		Summary: "created " + path,
		Files:   []taskFileEdit{{Path: path, Content: &content}},
	})
	return string(result), err == nil
}

func TestTaskOpenCodeProcess(t *testing.T) {
	t.Setenv("WEBCODEX_AGENT_TOKEN", "fixture-transport-token")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := openCodeTaskRunner{executable: executable, auth: taskProviderAuth{key: "fixture-key"}}
	root := t.TempDir()
	dir := filepath.Join(root, "job")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := runner.run(ctx, dir, taskSpec{Agent: "explore", Prompt: "Привет stdout"})
	if result.Status != "completed" || result.Output != "Привет stdout [redacted]\n" {
		t.Fatalf("unexpected fixture output: %#v", result)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("finished job did not release private working directory: %v", err)
	}
	missing := runner.run(ctx, dir, taskSpec{Prompt: "smoke-missing-model"})
	if missing.Status != "failed" || missing.Error != "OpenCode could not find model opencode-go/hy4-flash-free" {
		t.Fatalf("unexpected missing model result: %#v", missing)
	}
	unauthorized := runner.run(ctx, dir, taskSpec{Prompt: "smoke-unauthorized"})
	if unauthorized.Status != "failed" || unauthorized.Error != "OpenCode Go rejected the credential (HTTP 401)" {
		t.Fatalf("unexpected authorization result: %#v", unauthorized)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result = runner.run(ctx, dir, taskSpec{Prompt: "wait-for-cancellation"})
	if result.Status != "timed_out" {
		t.Fatalf("unexpected fixture timeout status: %#v", result)
	}
}
