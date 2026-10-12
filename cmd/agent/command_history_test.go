package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCommandRetryAndHistory(t *testing.T) {
	executor, root := testExecutor(t)
	args := map[string]any{"command": outputThenSleepCommand("RETRY", 1), "cwd": root,
		"request_key": "one-launch", "yield_time_ms": float64(0), "env": map[string]any{"PRIVATE": "secret"}}
	var wg sync.WaitGroup
	ids := make(chan string, 4)
	for range 4 {
		wg.Go(func() {
			result, err := executor.executeTool(context.Background(), "v5", "exec_command", args)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- result.(map[string]any)["session_id"].(string)
		})
	}
	wg.Wait()
	close(ids)
	id := ""
	for got := range ids {
		if id != "" && got != id {
			t.Fatal("Retry launched a duplicate process")
		}
		id = got
	}
	if id == "" {
		t.Fatal("command was not launched")
	}
	<-executor.sessions[id].done
	listed, err := executor.listCommands(map[string]any{})
	if err != nil || len(listed.(map[string]any)["sessions"].([]map[string]any)) != 1 {
		t.Fatalf("history: %v %v", listed, err)
	}
	journal, err := os.ReadFile(filepath.Join(executor.logDir, id, "events.jsonl"))
	if err != nil || !strings.Contains(string(journal), "finished") || strings.Contains(string(journal), "secret") || strings.Contains(string(journal), "RETRY") {
		t.Fatalf("journal must contain lifecycle only: %s %v", journal, err)
	}
	executor.Close()
	restored, err := newNativeExecutorWithConfig(executorConfig{AllowedRoots: []string{root}, LogDir: executor.logDir})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	retry, err := restored.executeTool(context.Background(), "v5", "exec_command", args)
	if err != nil || retry.(map[string]any)["session_id"] != id {
		t.Fatalf("restart retry: %v %v", retry, err)
	}
	args["command"] = "different"
	if _, err := restored.executeTool(context.Background(), "v5", "exec_command", args); err == nil {
		t.Fatal("reused key with different arguments accepted")
	}
}

func TestCommandReservationHasUnknownOutcomeAfterCrash(t *testing.T) {
	executor, root := testExecutor(t)
	args := map[string]any{"command": "must-not-run", "cwd": root}
	id := "proc_crashreservation"
	dir := filepath.Join(executor.logDir, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stdout.log", "stderr.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(sessionMetadata{ID: id, CWD: root, Status: "starting", StartedAt: time.Now(), RequestKeyHash: commandHash("crash"), RequestHash: commandHash(args)})
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	executor.Close()
	restored, err := newNativeExecutorWithConfig(executorConfig{AllowedRoots: []string{root}, LogDir: executor.logDir})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	args["request_key"] = "crash"
	result, err := restored.executeTool(context.Background(), "v5", "exec_command", args)
	if err != nil || result.(map[string]any)["status"] != "outcome_unknown" || result.(map[string]any)["session_id"] != id {
		t.Fatalf("ambiguous launch must never repeat: %v %v", result, err)
	}
	legacy, err := restored.pollCommand(map[string]any{"session_id": id}, true)
	if err != nil || legacy.(map[string]any)["status"] != "orphaned" {
		t.Fatalf("legacy polling broke its status contract: %v %v", legacy, err)
	}
}
