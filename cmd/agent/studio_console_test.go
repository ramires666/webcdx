package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStudioConsoleShowsEveryToolAndBriefResult(t *testing.T) {
	e, root := testExecutor(t)
	e.source = "local"
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	path := filepath.Join(root, "report.txt")
	if err := os.WriteFile(path, []byte("PRIVATE_CONTENT\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, call := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"read_file", map[string]any{"path": path}, "Прочитано строк: 1"},
		{"write_file", map[string]any{"path": filepath.Join(root, "new.txt"), "content": "PRIVATE_CONTENT", "if_absent": true}, "Записано 15 байт"},
		{"read_file", map[string]any{"path": filepath.Join(root, "missing.txt")}, "файл или папка не найдены"},
		{"list_agents", nil, "Доступно агентов: 1"},
		{"list_commands", nil, "Команд в списке: 0"},
		{"poll_command", map[string]any{"session_id": "missing"}, "Ошибка"},
		{"cancel_command", map[string]any{"session_id": "missing"}, "Ошибка"},
	} {
		output.Reset()
		studioToolCall(context.Background(), e, call.name, call.args, "local")
		text := output.String()
		if strings.Count(text, "\n") != 2 || !strings.Contains(text, call.name+" — ") || !strings.Contains(text, call.want) {
			t.Fatalf("%s must show its name, action and result in two lines: %s", call.name, text)
		}
		for _, unwanted := range []string{"[source=local]", "PRIVATE_CONTENT", "request id=", "response id="} {
			if strings.Contains(text, unwanted) {
				t.Fatalf("console contains %q: %s", unwanted, text)
			}
		}
	}
}

func TestStudioSessionMarkerLoggingAndValidation(t *testing.T) {
	e, _ := testExecutor(t)
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	marker := strings.Repeat("a", 32)
	for _, value := range []any{marker, marker, ""} {
		_, err := handleStudioRequest(context.Background(), e, nil, studioRequest{Method: "session/set", Params: map[string]any{"marker": value}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(output.String(), "ID активного сеанса: "+marker) != 2 || !strings.Contains(output.String(), "Сеанс завершён") {
		t.Fatalf("session start, reconnect and stop missing: %s", output.String())
	}
	output.Reset()
	for _, value := range []any{nil, 123, "short", strings.Repeat("A", 32), marker + "\nforged"} {
		_, err := handleStudioRequest(context.Background(), e, nil, studioRequest{Method: "session/set", Params: map[string]any{"marker": value}})
		if err == nil || err.Code != -32602 {
			t.Fatalf("invalid marker accepted: %v", value)
		}
	}
	if output.Len() != 0 {
		t.Fatal("invalid marker reached the console")
	}
}

func TestStudioCommandSummary(t *testing.T) {
	for _, item := range []struct {
		status string
		code   any
		want   string
	}{
		{"running", nil, "выполняется"}, {"exited", float64(0), "завершена"}, {"exited", float64(7), "код 7"}, {"timed_out", nil, "время ожидания"}, {"outcome_unknown", nil, "неизвестен"},
	} {
		result := map[string]any{"structuredContent": map[string]any{"status": item.status, "exit_code": item.code, "stdout": "PRIVATE_OUTPUT"}}
		if summary := studioCallSummary(result, nil); !strings.Contains(summary, item.want) || strings.Contains(summary, "PRIVATE_OUTPUT") {
			t.Fatalf("%s: %s", item.status, summary)
		}
	}
}
