package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDetectOpenCodeCommand(t *testing.T) {
	fromShell := detectOpenCodeCommand("& \"C:\\Program Files\\OpenCode\\opencode.cmd\" run --agent build --auto --model opencode-go/hy4-flash-free#high --title \"Lumen Circuit 04-actors\"", []string{"powershell.exe", "-Command"})
	if fromShell == nil || fromShell.Agent != "build" || fromShell.Title != "Lumen Circuit 04-actors" {
		t.Fatalf("could not identify the reported Windows command: %+v", fromShell)
	}
	fromArgv := detectOpenCodeCommand("", []string{"/opt/bin/opencode", "run", "--agent=explore", "--title=Проверка сцены"})
	if fromArgv == nil || fromArgv.Agent != "explore" || fromArgv.Title != "Проверка сцены" {
		t.Fatalf("could not identify a direct OpenCode launch: %+v", fromArgv)
	}
	for _, command := range []string{"Write-Output 'opencode.cmd run --agent build'", "opencode models", "opencode-helper run", "node test.cjs"} {
		if result := detectOpenCodeCommand(command, nil); result != nil {
			t.Errorf("unexpected OpenCode classification for %q", command)
		}
	}
}

func TestOpenCodeProgressRecognizesOutput(t *testing.T) {
	samples := map[string]string{
		"\x1b[32m← Write C:/projects/lumen-circuit-3d/src/fx.js\x1b[0m": "Запись файла fx.js",
		"→ Read C:\\projects\\lumen\\PLAN.md (lines 1-22)":              "Чтение файла PLAN.md",
		"$ node --check src/fx.js":                                      "Запуск команды",
		"waiting for 1 background job":                                  "Ожидает фоновые операции",
		"{\"type\":\"tool_use\",\"part\":{\"tool\":\"grep\"}}":          "Поиск в проекте",
		"{\"type\":\"step_finish\",\"part\":{}}":                        "Модель завершила шаг",
		"> build · hy4-flash-free":                                      "",
		"Text from the model":                                           "",
	}
	for line, expected := range samples {
		if actual := openCodeLineAction(line); actual != expected {
			t.Errorf("action for %q = %q, want %q", line, actual, expected)
		}
	}
}

func TestOpenCodeCommandCanBeMonitoredAndCancelled(t *testing.T) {
	e, root := testExecutor(t)
	executable := filepath.Join(root, "opencode")
	fixture := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"tool_use\",\"part\":{\"tool\":\"write\"}}'\nsleep 25\n"
	command := "'" + executable + "'"
	if runtime.GOOS == "windows" {
		executable += ".cmd"
		fixture = "@echo off\r\necho {\"type\":\"tool_use\",\"part\":{\"tool\":\"write\"}}\r\npowershell.exe -NoProfile -Command \"Start-Sleep -Seconds 25\"\r\n"
		command = "& '" + executable + "'"
	}
	if err := os.WriteFile(executable, []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	command += " run --agent build --title '04 Actors'"
	start := callToolV4(t, e, "exec_command", map[string]any{
		"cwd": root, "command": command, "yield_time_ms": 50, "timeout_seconds": 30,
	})
	sessionID, _ := start["session_id"].(string)
	if sessionID == "" || start["status"] != "running" {
		t.Fatalf("expected a running OpenCode command: %v", start)
	}
	t.Cleanup(func() { _ = e.stopSession(sessionID, "cancelled") })
	progress, ok := start["opencode"].(map[string]any)
	if !ok || progress["number"] != float64(1) || progress["agent"] != "build" || progress["title"] != "04 Actors" || progress["deadline"] == "" {
		t.Fatalf("unexpected OpenCode launch metadata: %v", start)
	}

	pollArgs := map[string]any{"session_id": sessionID, "stdout_offset": 0, "stderr_offset": 0}
	var poll map[string]any
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		poll = callToolV4(t, e, "poll_command", pollArgs)
		progress, _ = poll["opencode"].(map[string]any)
		if progress["last_action"] == "Запись файла" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if progress["last_action"] != "Запись файла" || progress["last_output_at"] == "" || !strings.Contains(poll["stdout"].(string), "tool_use") {
		t.Fatalf("expected progress and original stdout: %v", poll)
	}
	empty := callToolV4(t, e, "poll_command", map[string]any{
		"session_id": sessionID, "stdout_offset": int(poll["stdout_next_offset"].(float64)), "stderr_offset": 0,
	})
	if empty["stdout"] != "" || empty["opencode"].(map[string]any)["last_action"] != "Запись файла" {
		t.Fatalf("progress must survive a poll with no new log bytes: %v", empty)
	}
	result := callToolV4(t, e, "cancel_command", map[string]any{"session_id": sessionID})
	if result["status"] != "cancelled" || result["opencode"].(map[string]any)["number"] != float64(1) {
		t.Fatalf("unexpected cancellation result: %v", result)
	}
}
