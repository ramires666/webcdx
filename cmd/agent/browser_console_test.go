package main

import (
	"strings"
	"testing"
	"time"
)

func TestBrowserConsoleStartFamiliesAndSafeContext(t *testing.T) {
	tests := []struct {
		name string
		info browserConsoleInfo
		want string
	}{
		{"open", browserConsoleInfo{Name: "browser_open", ID: "operation_12345678", Args: map[string]any{"url": "https://example.test/a?token=hidden"}, Profile: "рабочий"}, "Открываю браузер"},
		{"navigate", browserConsoleInfo{Name: "browser_navigate", ID: "op-nav", Args: map[string]any{"action": "back"}}, "предыдущую страницу"},
		{"snapshot", browserConsoleInfo{Name: "browser_snapshot", Args: map[string]any{"container_selector": "main"}}, "Читаю страницу"},
		{"screenshot", browserConsoleInfo{Name: "browser_screenshot"}, "снимок страницы"},
		{"tabs", browserConsoleInfo{Name: "browser_tabs", Args: map[string]any{"action": "new"}}, "новую вкладку"},
		{"interaction", browserConsoleInfo{Name: "browser_interact", Args: map[string]any{"action": "click", "selector": "#send", "role": "button", "name": "Отправить", "text": "SECRET_TEXT", "value": "SECRET_VALUE"}}, "#send"},
		{"wait", browserConsoleInfo{Name: "browser_wait", Args: map[string]any{"condition": "element", "selector": "#ready"}}, "Жду элемент"},
		{"diagnostics", browserConsoleInfo{Name: "browser_diagnostics"}, "диагностику"},
		{"files", browserConsoleInfo{Name: "browser_files", Args: map[string]any{"action": "upload", "path": "C:\\Users\\alice\\secret.txt"}}, "Загружаю файл"},
		{"control", browserConsoleInfo{Name: "browser_control", Args: map[string]any{"action": "discover"}}, "Ищу подключённые браузеры"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := browserConsoleStart(test.info)
			if !strings.Contains(got, "▶") || !strings.Contains(got, test.want) {
				t.Fatalf("start line %q does not include %q", got, test.want)
			}
			if strings.Contains(got, "SECRET") || strings.Contains(got, "alice") {
				t.Fatalf("unsafe argument leaked in %q", got)
			}
		})
	}

	info := browserConsoleInfo{Name: "browser_snapshot", ID: "operation_12345678", TabID: "tab_42", Title: "Заголовок\n\x1b[31m", URL: "https://user:pass@example.test/page?token=secret#part", Profile: "work"}
	got := browserConsoleStart(info)
	for _, want := range []string{"[12345678]", "вкладка tab_42", "Заголовок", "https://example.test/page", "профиль work", `\x1b`} {
		if !strings.Contains(got, want) {
			t.Errorf("start line %q missing %q", got, want)
		}
	}
	for _, secret := range []string{"pass", "token", "secret", "\x1b[31m"} {
		if strings.Contains(got, secret) {
			t.Errorf("unsafe context leaked %q in %q", secret, got)
		}
	}
}

func TestBrowserConsoleFinishSnapshotAndTabs(t *testing.T) {
	preview := browserConsoleTextPreview("read https://user:secret@example.test/image.png?ex=SECRET&hm=TOKEN#fragment", 240)
	if strings.Contains(preview, "secret") || strings.Contains(preview, "SECRET") || strings.Contains(preview, "TOKEN") || strings.Contains(preview, "fragment") || !strings.Contains(preview, "https://example.test/image.png") {
		t.Fatalf("snapshot preview exposed URL access parameters: %s", preview)
	}
	text := strings.Repeat("界", 250) + "tail-secret"
	snapshot := browserConsoleFinish(browserConsoleInfo{Name: "browser_snapshot", ID: "op-12345678"}, map[string]any{
		"counts": map[string]any{"elements_found": float64(12), "elements_returned": 8, "dom_visited": int64(99)},
		"text":   text, "truncated": true, "image_payload": "must-not-print",
	}, "completed", "", 1250*time.Millisecond)
	for _, want := range []string{"✓ [12345678]", "элементов найдено 12", "узлов просмотрено 99", "текст 261 знаков", "усечён", "1.25s"} {
		if !strings.Contains(snapshot, want) {
			t.Errorf("snapshot summary %q missing %q", snapshot, want)
		}
	}
	if strings.Contains(snapshot, "tail-secret") || strings.Contains(snapshot, "must-not-print") {
		t.Fatalf("snapshot preview exceeded its safe bound: %q", snapshot)
	}

	tabs := make([]any, 10)
	for i := range tabs {
		tabs[i] = map[string]any{"tab_id": "tab_" + string(rune('0'+i)), "title": "Раздел", "url": "https://example.test/p?token=secret"}
	}
	line := browserConsoleFinish(browserConsoleInfo{Name: "browser_tabs", ID: "list-op"}, map[string]any{"tabs": tabs}, "ready", "", 0)
	if !strings.Contains(line, "вкладки:") || !strings.Contains(line, "список вкладок усечён") || strings.Contains(line, "token") || strings.Contains(line, "secret") {
		t.Fatalf("unexpected tabs summary %q", line)
	}
	if strings.Count(line, "https://example.test/p") != 8 {
		t.Fatalf("expected eight sanitized tabs, got %q", line)
	}
}

func TestBrowserConsoleFinishOperationFamilies(t *testing.T) {
	tests := []struct {
		name   string
		result map[string]any
		status string
		want   string
	}{
		{"browser_tabs", map[string]any{"tab_id": "tab_1"}, "ready", "текущая вкладка tab_1"},
		{"browser_wait", map[string]any{"status": "completed", "confirmation": "condition_matched"}, "completed", "условие выполнено"},
		{"browser_diagnostics", map[string]any{"events": []any{map[string]any{}, map[string]any{}}, "truncated": true}, "completed", "событий диагностики: 2"},
		{"browser_files", map[string]any{"bytes": int64(64)}, "completed", "64 байт"},
		{"browser_files", map[string]any{"downloads": []any{map[string]any{}}}, "completed", "загрузок: 1"},
		{"browser_control", map[string]any{"status": "paused"}, "completed", "управление браузером приостановлено"},
		{"browser_control", map[string]any{"browsers": []any{map[string]any{}, map[string]any{}}}, "completed", "подключённых браузеров: 2"},
		{"browser_open", map[string]any{"browser_alias": "рабочий", "status": "ready"}, "ready", "профиль рабочий"},
	}
	for _, test := range tests {
		t.Run(test.name+"/"+test.want, func(t *testing.T) {
			got := browserConsoleFinish(browserConsoleInfo{Name: test.name, ID: "op-result"}, test.result, test.status, "", 0)
			if !strings.Contains(got, test.want) {
				t.Fatalf("finish line %q does not include %q", got, test.want)
			}
		})
	}
}

func TestBrowserConsoleFinishErrorAndUncertainOutcome(t *testing.T) {
	tests := []struct {
		status, code, want string
	}{
		{"failed", "unknown_session", "браузерная сессия не найдена"},
		{"outcome_unknown", "outcome_unknown", "проверьте состояние перед повтором"},
		{"waiting_user", "", "действия пользователя"},
		{"timed_out", "", "истекло время ожидания"},
	}
	for _, test := range tests {
		got := browserConsoleFinish(browserConsoleInfo{ID: "op-error"}, nil, test.status, test.code, 0)
		if !strings.Contains(got, "⚠") || !strings.Contains(got, test.want) {
			t.Errorf("finish(%q, %q) = %q; want warning and %q", test.status, test.code, got, test.want)
		}
	}
	if got := browserConsoleFinish(browserConsoleInfo{ID: "op-code"}, nil, "failed", "page_evaluation_failed", 0); !strings.Contains(got, "диагностик") {
		t.Fatalf("known browser error was not made readable: %q", got)
	}
}

func TestBrowserConsoleFinishAddsResolvedContextAndMarksRunning(t *testing.T) {
	info := browserConsoleInfo{Name: "browser_snapshot", ID: "context-op"}
	result := map[string]any{"tab_id": "tab_9", "title": "Page title", "url": "https://example.test/read?token=hidden", "text": "read text"}
	line := browserConsoleFinish(info, result, "completed", "", 0)
	for _, want := range []string{"tab_9", "Page title", "https://example.test/read"} {
		if !strings.Contains(line, want) {
			t.Errorf("finish line %q missing context %q", line, want)
		}
	}
	if strings.Contains(line, "token") || strings.Contains(line, "hidden") {
		t.Fatalf("query data leaked in finish line %q", line)
	}
	running := browserConsoleFinish(browserConsoleInfo{ID: "running-op"}, nil, "running", "", 0)
	if !strings.Contains(running, "\u23f3") || strings.Contains(running, "\u2713") {
		t.Fatalf("running operation should use the pending marker: %q", running)
	}
	queued := browserConsoleFinish(browserConsoleInfo{ID: "queue-op", SessionID: "session_12345"}, map[string]any{"phase": "queued"}, "running", "", 0)
	if !strings.Contains(queued, "в очереди") || !strings.Contains(queued, "сессия session_12345") {
		t.Fatalf("queued session context missing: %q", queued)
	}
	dispatch := browserConsoleFinish(browserConsoleInfo{ID: "dispatch-op"}, map[string]any{"phase": "dispatch_started"}, "running", "", 0)
	if !strings.Contains(dispatch, "действие отправлено браузеру") {
		t.Fatalf("dispatch phase not described: %q", dispatch)
	}
}
