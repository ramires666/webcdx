package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
	args := []string{"run", "--pure", "--format", "json", "--model", "opencode-go/fixture", "--variant", "none"}
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
		Permission map[string]string
	}
	if err != nil || json.Unmarshal(raw, &settings) != nil || settings.Permission["*"] != "deny" {
		return 2
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 2
	}
	if string(prompt) == "wait-for-cancellation" {
		time.Sleep(time.Minute)
		return 2
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"type": "text", "part": map[string]string{"text": string(prompt) + " fixture-key"},
	}); err != nil {
		return 2
	}
	return 0
}

func TestTaskOpenCodeProcess(t *testing.T) {
	t.Setenv("WEBCODEX_AGENT_TOKEN", "fixture-transport-token")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := openCodeTaskRunner{executable: executable, model: "opencode-go/fixture", key: "fixture-key"}
	root := t.TempDir()
	dir := filepath.Join(root, "job")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := runner.run(ctx, dir, taskSpec{Prompt: "Привет stdout"})
	if result.Status != "completed" || result.Output != "Привет stdout [redacted]\n" {
		t.Fatalf("unexpected fixture output: %#v", result)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("finished job did not release private working directory: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result = runner.run(ctx, dir, taskSpec{Prompt: "wait-for-cancellation"})
	if result.Status != "timed_out" {
		t.Fatalf("unexpected fixture timeout status: %#v", result)
	}
}
