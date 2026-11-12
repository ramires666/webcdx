package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequestSourceCommandHelper(t *testing.T) {
	if os.Getenv("GO_WANT_SOURCE_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, os.Getenv("SOURCE_OUTPUT"))
	os.Exit(0)
}

func TestLocalAndGateKeepRootsCommandsAndRetryKeysSeparate(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "shared-logs")
	var console bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&console)
	t.Cleanup(func() { log.SetOutput(previous) })
	configs := map[string]executorConfig{}
	executors := map[string]*nativeExecutor{}
	args := map[string]map[string]any{}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"local", "gate"} {
		dir := filepath.Join(root, source)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fixture.txt"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		configs[source] = executorConfig{Source: source, AllowedRoots: []string{dir}, LogDir: logDir, BrowserBackend: "disabled"}
		executors[source], err = newNativeExecutorWithConfig(configs[source])
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(executors[source].Close)
		args[source] = map[string]any{"argv": []any{executable, "-test.run=^TestRequestSourceCommandHelper$"}, "cwd": dir,
			"env": map[string]any{"GO_WANT_SOURCE_HELPER": "1", "SOURCE_OUTPUT": source}, "request_key": "same-client-key", "yield_time_ms": float64(1000)}
	}
	for source, e := range executors {
		ownFile := filepath.Join(root, source, "fixture.txt")
		callTool(t, e, "read_file", map[string]any{"path": ownFile})
		other := "gate"
		if source == "gate" {
			other = "local"
		}
		request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "same-id", "method": "tools/call", "params": map[string]any{"name": "read_file", "arguments": map[string]any{"path": filepath.Join(root, other, "fixture.txt")}}})
		response, err := e.call(context.Background(), request)
		if err != nil || !bytes.Contains(response, []byte(`"isError":true`)) {
			t.Fatalf("%s accessed the other channel's root: %s %v", source, response, err)
		}
	}
	type commandResult struct {
		source string
		value  any
		err    error
	}
	results := make(chan commandResult, 2)
	var wg sync.WaitGroup
	for source, e := range executors {
		wg.Go(func() {
			value, err := e.executeTool(context.Background(), "v5", "exec_command", args[source])
			results <- commandResult{source, value, err}
		})
	}
	wg.Wait()
	close(results)
	ids := map[string]string{}
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		id := result.value.(map[string]any)["session_id"].(string)
		ids[result.source] = id
		select {
		case <-executors[result.source].sessions[id].done:
		case <-time.After(5 * time.Second):
			t.Fatal("command did not finish")
		}
	}
	if ids["local"] == ids["gate"] {
		t.Fatal("channels shared a command reservation")
	}
	for source, e := range executors {
		other := "gate"
		if source == "gate" {
			other = "local"
		}
		if _, err := e.pollCommand(map[string]any{"session_id": ids[other]}, true); err == nil {
			t.Fatalf("%s polled the other channel's command", source)
		}
		if _, err := e.cancelCommand(map[string]any{"session_id": ids[other]}, true); err == nil {
			t.Fatalf("%s cancelled the other channel's command", source)
		}
		listed, err := e.listCommands(map[string]any{})
		if err != nil || len(listed.(map[string]any)["sessions"].([]map[string]any)) != 1 {
			t.Fatalf("%s command history mixed: %v %v", source, listed, err)
		}
		e.Close()
		restored, err := newNativeExecutorWithConfig(configs[source])
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(restored.Close)
		if len(restored.sessions) != 1 || restored.sessions[ids[source]] == nil {
			t.Fatalf("%s restored another channel's sessions", source)
		}
		retry, err := restored.executeTool(context.Background(), "v5", "exec_command", args[source])
		if err != nil || retry.(map[string]any)["session_id"] != ids[source] {
			t.Fatalf("%s retry crossed channels or relaunched: %v %v", source, retry, err)
		}
		if !strings.Contains(retry.(map[string]any)["stdout"].(string), source) {
			t.Fatalf("%s received another channel's output", source)
		}
		journal, err := os.ReadFile(filepath.Join(logDir, ids[source], "events.jsonl"))
		if err != nil || !bytes.Contains(journal, []byte(`"source":"`+source+`"`)) {
			t.Fatalf("%s journal lacks source: %s %v", source, journal, err)
		}
		restored.Close()
	}
	for _, source := range []string{"local", "gate"} {
		if !strings.Contains(console.String(), "[source="+source+"] request") || !strings.Contains(console.String(), "[source="+source+"] ▶ Команда") {
			t.Fatalf("%s console source missing: %s", source, console.String())
		}
	}
}
