package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommandLogging(t *testing.T) {
	console, err := os.CreateTemp(t.TempDir(), "console")
	if err != nil {
		t.Fatal(err)
	}
	previous := log.Writer()
	log.SetOutput(console)
	t.Cleanup(func() { log.SetOutput(previous); console.Close() })
	readConsole := func() string {
		data, err := os.ReadFile(console.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	executor, root := testExecutor(t)
	executor.verbose = true
	executor.maxLogBytes = 4
	executable, _ := os.Executable()
	started := time.Now()
	running := callToolV4(t, executor, "exec_command", map[string]any{
		"argv": []any{executable, "-test.run=TestV4CommandHelper", "--", "observe"},
		"cwd":  root, "env": map[string]any{"GO_WANT_V4_HELPER": "1", "PRIVATE_ENV": "PRIVATE_VALUE"},
		"stdin": "PRIVATE_STDIN", "timeout_seconds": 20, "yield_time_ms": 30000,
	})
	if time.Since(started) > 4*time.Second {
		t.Fatal("even a long requested wait must return promptly")
	}
	if running["status"] != "running" {
		t.Fatalf("expected running process: %v", running)
	}
	id := running["session_id"].(string)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(readConsole(), "ERROR_STREAM") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if text := readConsole(); !strings.Contains(text, "LIVE") || !strings.Contains(text, "ERROR_STREAM") || strings.Contains(text, "EXIT status=exited") {
		t.Fatalf("output must arrive before exit: %s", text)
	}
	executor.mu.Lock()
	done := executor.sessions[id].done
	executor.mu.Unlock()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = executor.stopSession(id, "cancelled")
		t.Fatal("process did not finish")
	}
	text := readConsole()
	for _, want := range []string{"ARGV", "START pid=", "stdout:", "stderr:", "LOG_LIMIT", "RUNNING pid=", "EXIT status=exited exit_code=7", "session=" + id, `\x1b[2J`} {
		if !strings.Contains(text, want) {
			t.Errorf("console missing %q: %s", want, text)
		}
	}
	for _, secret := range []string{"PRIVATE_ENV", "PRIVATE_VALUE", "PRIVATE_STDIN", "\x1b"} {
		if strings.Contains(text, secret) {
			t.Errorf("console contains private input or raw terminal control %q", secret)
		}
	}
	stdoutInfo, _ := os.Stat(running["stdout_log_path"].(string))
	stderrInfo, _ := os.Stat(running["stderr_log_path"].(string))
	if stdoutInfo.Size()+stderrInfo.Size() != 4 {
		t.Fatal("disk log budget changed")
	}
	executor.verbose = false
	quiet := callToolV4(t, executor, "exec_command", map[string]any{
		"argv": []any{executable, "-test.run=TestV4CommandHelper", "--", "observe-quick"},
		"cwd":  root, "env": map[string]any{"GO_WANT_V4_HELPER": "1"}, "yield_time_ms": 5000,
	})
	text = readConsole()[len(text):]
	if strings.Contains(text, "ARGV") || strings.Contains(text, "stdout:") || strings.Contains(text, "stderr:") || !strings.Contains(text, quiet["session_id"].(string)) || !strings.Contains(text, "EXIT") {
		t.Fatalf("quiet mode must keep lifecycle only: %s", text)
	}
	failed := callToolV4(t, executor, "exec_command", map[string]any{"argv": []any{filepath.Join(root, "missing-executable")}, "cwd": root})
	if failed["status"] != "failed" || !strings.Contains(readConsole(), "START_FAILED") {
		t.Fatalf("missing start failure: %v", failed)
	}
}

func TestCommandSurvivesRequestDisconnect(t *testing.T) {
	executor, root := testExecutor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, err := executor.execCommand(ctx, map[string]any{
		"command": outputThenSleepCommand("SURVIVED", 2), "cwd": root, "timeout_seconds": float64(10),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil || result.(map[string]any)["status"] != "running" {
		t.Fatalf("expected disconnected request with running process: %v", result)
	}
	id := result.(map[string]any)["session_id"].(string)
	executor.mu.Lock()
	done := executor.sessions[id].done
	executor.mu.Unlock()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		_ = executor.stopSession(id, "cancelled")
		t.Fatal("process did not finish after request disconnect")
	}
	poll := callToolV4(t, executor, "poll_command", map[string]any{"session_id": id, "stdout_offset": 0, "stderr_offset": 0})
	if poll["status"] != "exited" || poll["exit_code"] != float64(0) || !strings.Contains(poll["stdout"].(string), "SURVIVED") {
		t.Fatalf("could not resume monitoring: %v", poll)
	}
}
