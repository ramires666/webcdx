package main

import (
	"fmt"
	"path"
	"strings"
	"time"
)

type browserConsoleInfo struct {
	Name      string
	Args      map[string]any
	ID        string
	SessionID string
	TabID     string
	Title     string
	URL       string
	Profile   string
}

func browserConsoleStart(info browserConsoleInfo) string {
	label := browserConsoleOperation(info)
	context := browserConsoleContext(info)
	if context != "" {
		label += " · " + context
	}
	return fmt.Sprintf("▶ [%s] %s", browserConsoleShortID(info), label)
}

func browserConsoleFinish(info browserConsoleInfo, result map[string]any, status, errorCode string, elapsed time.Duration) string {
	info = browserConsoleInfoFromResult(info, result)
	icon, outcome := "✓", "готово"
	if errorCode != "" {
		icon, outcome = "⚠", browserConsoleError(errorCode)
	} else {
		switch status {
		case "failed", "timed_out", "cancelled", "orphaned", "outcome_unknown", "waiting_user", "paused":
			icon = "⚠"
			outcome = browserConsoleStatus(status, result)
		case "running":
			icon, outcome = "⏳", browserConsoleStatus(status, result)
		case "completed", "ready", "":
			outcome = browserConsoleResult(info, result)
		default:
			outcome = browserConsoleStatus(status, result)
		}
	}
	if context := browserConsoleContext(info); context != "" {
		outcome += " · " + context
	}
	if elapsed > 0 {
		outcome += fmt.Sprintf(" · %s", elapsed.Round(time.Millisecond))
	}
	return fmt.Sprintf("%s [%s] %s", icon, browserConsoleShortID(info), outcome)
}

func browserConsoleInfoFromResult(info browserConsoleInfo, result map[string]any) browserConsoleInfo {
	if result == nil {
		return info
	}
	if info.TabID == "" {
		info.TabID, _ = result["tab_id"].(string)
	}
	if info.Title == "" {
		info.Title, _ = result["title"].(string)
	}
	if info.URL == "" {
		info.URL, _ = result["url"].(string)
	}
	if info.Profile == "" {
		info.Profile, _ = result["browser_alias"].(string)
	}
	if info.Profile == "" {
		info.Profile, _ = result["profile_id"].(string)
	}
	return info
}

func browserConsoleShortID(info browserConsoleInfo) string {
	id := info.ID
	if id == "" {
		id = info.SessionID
	}
	var b strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	safe := b.String()
	if len(safe) > 8 {
		safe = safe[len(safe)-8:]
	}
	if safe == "" {
		return "browser"
	}
	return safe
}

func browserConsoleContext(info browserConsoleInfo) string {
	parts := []string{}
	if info.TabID != "" {
		parts = append(parts, "вкладка "+compactString(info.TabID, 32))
	} else if info.SessionID != "" {
		parts = append(parts, "сессия "+compactString(info.SessionID, 32))
	}
	if title := compactString(info.Title, 80); title != "" {
		parts = append(parts, "«"+title+"»")
	}
	if info.URL != "" {
		if u := consoleURL(info.URL); u != "" {
			parts = append(parts, compactString(u, 180))
		}
	}
	if profile := compactString(info.Profile, 64); profile != "" {
		parts = append(parts, "профиль "+profile)
	}
	return strings.Join(parts, " · ")
}

func browserConsoleOperation(info browserConsoleInfo) string {
	a := info.Args
	get := func(key string) string {
		value, _ := a[key].(string)
		return compactString(value, 80)
	}
	join := func(parts ...string) string {
		kept := parts[:0]
		for _, part := range parts {
			if part != "" {
				kept = append(kept, part)
			}
		}
		return strings.Join(kept, " · ")
	}
	action := get("action")
	switch info.Name {
	case "browser_open":
		url := consoleURL(get("url"))
		return join("Открываю браузер", url)
	case "browser_navigate":
		url := consoleURL(get("url"))
		if url != "" {
			return "Перехожу на " + url
		}
		return map[string]string{"back": "Возвращаюсь на предыдущую страницу", "forward": "Перехожу на следующую страницу", "reload": "Обновляю страницу"}[action]
	case "browser_snapshot":
		return join("Читаю страницу", get("container_selector"))
	case "browser_screenshot":
		return "Делаю снимок страницы"
	case "browser_tabs":
		labels := map[string]string{"list": "Показываю вкладки", "select": "Выбираю вкладку", "new": "Открываю новую вкладку", "close": "Закрываю вкладку"}
		return labels[action]
	case "browser_interact":
		verbs := map[string]string{"click": "Нажимаю", "fill": "Заполняю поле", "select": "Выбираю", "hover": "Навожу указатель на", "press": "Нажимаю клавишу для", "scroll": "Прокручиваю", "drag": "Перетаскиваю", "coordinates": "Нажимаю на странице", "dialog": "Отвечаю на диалог"}
		verb := verbs[action]
		if verb == "" {
			verb = "Взаимодействую с"
		}
		detail := ""
		if action == "press" {
			detail = get("key")
		} else if action == "coordinates" || action == "scroll" || action == "drag" {
			x, _ := browserConsoleNumber(a["x"])
			y, _ := browserConsoleNumber(a["y"])
			detail = fmt.Sprintf("x=%d, y=%d", x, y)
			if action == "drag" {
				endX, _ := browserConsoleNumber(a["end_x"])
				endY, _ := browserConsoleNumber(a["end_y"])
				detail += fmt.Sprintf(" → x=%d, y=%d", endX, endY)
			}
		} else if action == "dialog" {
			detail = "отклоняю"
			if a["accept"] == true {
				detail = "принимаю"
			}
		}
		return join(verb, detail, get("selector"), get("role"), get("name"), get("element_id"))
	case "browser_wait":
		condition := map[string]string{"load": "Загружаю страницу", "url": "Жду URL", "text": "Жду текст", "element": "Жду элемент", "value": "Жду значение"}[get("condition")]
		if condition == "" {
			condition = "Жду условие страницы"
		}
		return join(condition, get("selector"), get("role"), get("name"), get("state"))
	case "browser_diagnostics":
		return "Проверяю диагностику браузера"
	case "browser_files":
		labels := map[string]string{"upload": "Загружаю файл", "enable_downloads": "Разрешаю загрузки", "downloads": "Проверяю загрузки", "save_download": "Сохраняю загрузку"}
		if label := labels[action]; label != "" {
			if file, _ := a["path"].(string); file != "" {
				return join(label, compactString(path.Base(strings.ReplaceAll(file, `\`, "/")), 100))
			}
			return label
		}
		return "Работаю с файлами браузера"
	case "browser_control":
		labels := map[string]string{"discover": "Ищу подключённые браузеры", "status": "Проверяю состояние браузера", "pause": "Приостанавливаю браузер", "resume": "Возобновляю работу браузера", "cancel": "Отменяю действие браузера", "waiting_user": "Жду действия пользователя", "disconnect": "Отключаю управление браузером"}
		if label := labels[action]; label != "" {
			return label
		}
		return "Управляю браузером"
	default:
		return "Выполняю " + compactString(info.Name, 64)
	}
}

func browserConsoleResult(info browserConsoleInfo, result map[string]any) string {
	if result == nil {
		return "действие выполнено"
	}
	if status, _ := result["status"].(string); status != "" && status != "completed" && status != "ready" {
		return browserConsoleStatus(status, result)
	}
	switch info.Name {
	case "browser_snapshot":
		counts, _ := result["counts"].(map[string]any)
		countText := browserConsoleCounts(counts)
		text, _ := result["text"].(string)
		textLength := len([]rune(text))
		preview := browserConsoleTextPreview(text, 240)
		parts := []string{}
		if countText != "" {
			parts = append(parts, countText)
		}
		parts = append(parts, fmt.Sprintf("текст %d знаков", textLength))
		if result["truncated"] == true {
			parts = append(parts, "усечён")
		}
		if preview != "" {
			parts = append(parts, "прочитано: «"+preview+"»")
		}
		return strings.Join(parts, " · ")
	case "browser_tabs":
		if tabs := browserConsoleSlice(result["tabs"]); tabs != nil {
			return browserConsoleTabs(tabs, result["truncated"] == true)
		}
		if tab, _ := result["tab_id"].(string); tab != "" {
			return "текущая вкладка " + compactString(tab, 32)
		}
	case "browser_diagnostics":
		events := browserConsoleSlice(result["events"])
		message := fmt.Sprintf("событий диагностики: %d", len(events))
		if result["truncated"] == true {
			message += " · часть событий отброшена"
		}
		return message
	case "browser_files":
		if n, ok := browserConsoleNumber(result["bytes"]); ok {
			return fmt.Sprintf("файл сохранён · %d байт", n)
		}
		if downloads := browserConsoleSlice(result["downloads"]); downloads != nil {
			return fmt.Sprintf("загрузок: %d", len(downloads))
		}
	case "browser_screenshot":
		width, _ := browserConsoleNumber(result["image_width"])
		height, _ := browserConsoleNumber(result["image_height"])
		bytes, _ := browserConsoleNumber(result["image_bytes"])
		return fmt.Sprintf("снимок %d×%d · %d байт", width, height, bytes)
	case "browser_control":
		if status, _ := result["status"].(string); status != "" {
			return "состояние: " + compactString(status, 32)
		}
		if browsers := browserConsoleSlice(result["browsers"]); browsers != nil {
			return fmt.Sprintf("подключённых браузеров: %d", len(browsers))
		}
	case "browser_open":
		if profile, _ := result["browser_alias"].(string); profile != "" {
			return "браузер подключён · профиль " + compactString(profile, 64)
		}
	}
	if confirmation, _ := result["confirmation"].(string); confirmation != "" {
		return browserConsoleConfirmation(confirmation)
	}
	return "действие выполнено"
}

func browserConsoleTextPreview(text string, maxRunes int) string {
	preview := compactString(consoleText(text), maxRunes)
	runes := []rune(preview)
	if len(runes) > maxRunes {
		if maxRunes <= 0 {
			return ""
		}
		return string(runes[:maxRunes-1]) + "…"
	}
	return preview
}

func browserConsoleStatus(status string, result map[string]any) string {
	switch status {
	case "failed":
		return "действие завершилось с ошибкой"
	case "timed_out":
		return "истекло время ожидания"
	case "cancelled":
		return "действие отменено"
	case "orphaned", "outcome_unknown":
		return "исход неизвестен; проверьте состояние перед повтором"
	case "waiting_user":
		reason, _ := result["reason"].(string)
		if reason == "password_entry" {
			return "браузер ждёт ввода пароля пользователем"
		}
		return "браузер ждёт действия пользователя"
	case "paused":
		return "управление браузером приостановлено"
	case "running":
		phase, _ := result["phase"].(string)
		phases := map[string]string{"queued": "в очереди", "executing": "выполняется", "dispatch_started": "действие отправлено браузеру"}
		if label := phases[phase]; label != "" {
			return label
		}
		return "операция выполняется"
	default:
		return "состояние: " + compactString(status, 48)
	}
}

func browserConsoleError(code string) string {
	switch code {
	case "unknown_session":
		return "браузерная сессия не найдена"
	case "unknown_tab":
		return "выбранная вкладка закрыта или не принадлежит этой сессии"
	case "unknown_operation":
		return "операция не найдена или её срок хранения истёк"
	case "cancelled_before_dispatch":
		return "операция отменена до отправки изменения браузеру"
	case "scope_changed":
		return "доступ к вкладкам изменился; получите свежий снимок"
	case "protocol_upgrade_required":
		return "обновите расширение браузера для выполнения изменений"
	case "outcome_unknown":
		return "исход неизвестен; проверьте состояние перед повтором"
	case "page_evaluation_failed":
		return "не удалось прочитать или изменить страницу; проверьте диагностику"
	case "wait_timeout", "command_expired":
		return "истекло время ожидания; проверьте состояние страницы"
	case "access_paused":
		return "доступ к браузеру приостановлен локально"
	case "scope_denied":
		return "действие вне разрешённой вкладки или адреса"
	case "disconnected", "browser_disconnected":
		return "браузер или вкладка отключены"
	case "browser_busy":
		return "очередь браузера занята; повторите позже"
	case "invalid_selector":
		return "неверный CSS-селектор"
	case "element_not_found":
		return "элемент не найден; получите свежий снимок страницы"
	case "stale_snapshot":
		return "снимок устарел; получите новый снимок страницы"
	case "response_too_large":
		return "ответ браузера превышает допустимый размер"
	default:
		if known := readableError(map[string]any{"code": code}); known != "" && known != "не удалось выполнить действие" && !strings.Contains(known, "browser operation failed") {
			return known
		}
		if code == "" {
			return "операция браузера не выполнена"
		}
		return "ошибка браузера «" + compactString(code, 64) + "»; проверьте диагностику"
	}
}

func browserConsoleConfirmation(confirmation string) string {
	switch confirmation {
	case "action_dispatched":
		return "действие отправлено браузеру"
	case "condition_matched":
		return "условие выполнено"
	default:
		return compactString(confirmation, 64)
	}
}

func browserConsoleCounts(counts map[string]any) string {
	keys := []string{"elements_found", "elements_returned", "dom_visited", "ax_found", "ax_returned"}
	labels := map[string]string{"elements_found": "элементов найдено", "elements_returned": "показано", "dom_visited": "узлов просмотрено", "ax_found": "элементов доступности", "ax_returned": "доступных показано"}
	parts := []string{}
	for _, key := range keys {
		if n, ok := browserConsoleNumber(counts[key]); ok {
			parts = append(parts, fmt.Sprintf("%s %d", labels[key], n))
		}
	}
	return strings.Join(parts, ", ")
}

func browserConsoleNumber(value any) (int64, bool) {
	switch n := value.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if n >= 0 && n <= 1e12 {
			return int64(n), true
		}
	}
	return 0, false
}

func browserConsoleSlice(value any) []any {
	switch list := value.(type) {
	case []any:
		return list
	case []map[string]any:
		out := make([]any, len(list))
		for i := range list {
			out[i] = list[i]
		}
		return out
	default:
		return nil
	}
}

func browserConsoleTabs(tabs []any, truncated bool) string {
	const maxTabs = 8
	lines := make([]string, 0, maxTabs+1)
	for i, raw := range tabs {
		if i >= maxTabs {
			truncated = true
			break
		}
		tab, _ := raw.(map[string]any)
		id, _ := tab["tab_id"].(string)
		title, _ := tab["title"].(string)
		url, _ := tab["url"].(string)
		parts := []string{}
		if id != "" {
			parts = append(parts, compactString(id, 32))
		}
		if title != "" {
			parts = append(parts, "«"+compactString(title, 80)+"»")
		}
		if safeURL := consoleURL(url); safeURL != "" {
			parts = append(parts, compactString(safeURL, 180))
		}
		if len(parts) > 0 {
			lines = append(lines, "  "+strings.Join(parts, " · "))
		}
	}
	if truncated {
		lines = append(lines, "  … список вкладок усечён")
	}
	if len(lines) == 0 {
		return "вкладок нет"
	}
	return "вкладки:\n" + strings.Join(lines, "\n")
}
