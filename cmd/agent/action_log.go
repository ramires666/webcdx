package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

func formatActionRequest(raw []byte) string {
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return "Получен некорректный запрос"
	}
	if msg.Method != "tools/call" {
		return ""
	}
	return describeToolCall(msg.Params.Name, msg.Params.Arguments)
}

func describeToolCall(name string, args map[string]any) string {
	text := func(key string) string { value, _ := args[key].(string); return compactString(value, 180) }
	switch name {
	case "exec_command":
		return "Запускаю команду в " + text("cwd")
	case "read_file":
		return "Читаю файл " + text("path")
	case "write_file":
		if args["if_absent"] == true {
			return "Создаю файл " + text("path")
		}
		return "Записываю файл " + text("path")
	case "edit_file":
		return "Правлю файл " + text("path")
	case "delete_path":
		return "Удаляю " + text("path")
	case "list_directory":
		return "Читаю содержимое папки " + text("path")
	case "search_files":
		return "Ищу «" + text("pattern") + "» в " + text("path")
	case "find_files":
		return "Ищу файлы в " + text("path")
	case "move_path":
		return "Перемещаю " + text("source") + " → " + text("destination")
	case "list_allowed_roots":
		return "Проверяю доступные каталоги"
	case "poll_command", "list_commands", "cancel_command":
		return ""
	case "browser_open":
		return "Открываю браузер " + consoleURL(text("url"))
	case "browser_navigate":
		if target := text("url"); target != "" {
			return "Открываю страницу " + consoleURL(target)
		}
		if label := map[string]string{"back": "Возвращаюсь на предыдущую страницу", "forward": "Перехожу на следующую страницу", "reload": "Обновляю страницу"}[text("action")]; label != "" {
			return label
		}
		return "Перехожу на страницу браузера"
	case "browser_snapshot":
		return "Читаю страницу браузера"
	case "browser_screenshot":
		return "Делаю снимок страницы"
	case "browser_tabs":
		return "Работаю с вкладками браузера"
	case "browser_interact":
		labels := map[string]string{"click": "Нажимаю элемент", "fill": "Заполняю поле", "select": "Выбираю значение", "hover": "Навожу указатель", "press": "Нажимаю клавишу", "scroll": "Прокручиваю страницу", "drag": "Перетаскиваю элемент", "coordinates": "Нажимаю на странице", "dialog": "Отвечаю на диалог"}
		label := labels[text("action")]
		if label == "" {
			label = "Работаю со страницей"
		}
		return label + " " + text("selector") + " " + text("name")
	case "browser_wait":
		return "Жду загрузку или элемент страницы"
	case "browser_files":
		return "Работаю с файлами браузера " + text("path")
	case "browser_diagnostics":
		return "Проверяю ошибки страницы"
	case "browser_control":
		return map[string]string{"pause": "Приостанавливаю браузер", "resume": "Продолжаю работу в браузере", "cancel": "Отменяю действие браузера", "disconnect": "Отключаю управление браузером", "waiting_user": "Браузер ждёт действия пользователя"}[text("action")]
	default:
		return "Выполняю действие " + compactString(name, 80)
	}
}

func formatActionResponse(raw []byte, callErr error) string {
	if callErr != nil {
		return "Ошибка: " + compactString(callErr.Error(), 180)
	}
	var msg struct {
		Result struct {
			IsError    bool           `json:"isError"`
			Structured map[string]any `json:"structuredContent"`
			Content    []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return "Ошибка: не удалось прочитать ответ"
	}
	if msg.Error != nil {
		return "Ошибка: " + compactString(msg.Error.Message, 180)
	}
	data := msg.Result.Structured
	if msg.Result.IsError {
		if detail, ok := data["error"].(map[string]any); ok {
			return "Ошибка: " + readableError(detail)
		}
		if len(msg.Result.Content) > 0 {
			return "Ошибка: " + compactString(msg.Result.Content[0].Text, 180)
		}
		return "Ошибка выполнения"
	}
	if problems, ok := data["errors"].([]any); ok && len(problems) > 0 {
		return fmt.Sprintf("Не удалось обработать часть файлов (%d): %s", len(problems), compactString(fmt.Sprint(problems[0]), 150))
	}
	if code, _ := data["error_code"].(string); code != "" {
		return "Браузер: " + readableError(map[string]any{"code": code})
	}
	status, _ := data["status"].(string)
	// Process lifecycle is printed once by the executor, independently of client polling.
	if _, process := data["session_id"]; process && status != "outcome_unknown" && status != "orphaned" {
		return ""
	}
	switch status {
	case "failed":
		return "Действие завершилось с ошибкой"
	case "timed_out":
		return "Превышено время ожидания"
	case "cancelled":
		return "Действие отменено"
	case "orphaned", "outcome_unknown":
		return "Результат неизвестен; проверьте состояние перед повтором"
	case "waiting_user", "paused":
		return "Браузер ждёт действия пользователя"
	case "exited":
		if code, ok := data["exit_code"].(float64); ok && code != 0 {
			return fmt.Sprintf("Команда завершилась с ошибкой: код %d", int(code))
		}
	}
	return ""
}

func consoleURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
	return parsed.String()
}

func readableError(detail map[string]any) string {
	code, _ := detail["code"].(string)
	labels := map[string]string{
		"invalid_option_value": "значение не соответствует доступным вариантам списка", "element_disabled": "элемент отключён", "element_hidden": "элемент скрыт", "element_obscured": "элемент перекрыт другим окном", "element_detached": "элемент удалён; нужно перечитать страницу", "element_not_editable": "поле недоступно для ввода", "invalid_control_type": "элемент не поддерживает это действие", "invalid_selector": "неверный CSS-селектор", "frame_changed": "контекст вкладки изменился; нужно перечитать страницу", "page_evaluation_failed": "ошибка чтения или изменения страницы; доступна диагностика", "state_verification_failed": "действие отправлено; результат нужно проверить перед повтором", "scope_denied": "действие вне разрешённой области браузера", "access_paused": "доступ к браузеру приостановлен", "disconnected": "соединение с браузером потеряно",
		"access_denied": "доступ запрещён", "permission_denied": "доступ запрещён", "not_found": "файл или папка не найдены", "outside_allowed_roots": "путь вне разрешённых каталогов",
		"stale_hash": "файл изменился; нужно перечитать его", "text_not_found": "заменяемый текст не найден", "ambiguous_match": "найдено несколько совпадений текста",
		"already_exists": "файл уже существует", "binary_file": "двоичный файл нельзя прочитать как текст", "browser_disabled": "браузинг отключён", "cancelled": "действие отменено",
		"outcome_unknown": "результат неизвестен; проверьте состояние перед повтором", "waiting_user": "требуется действие пользователя",
		"timeout": "превышено время ожидания", "element_not_found": "элемент страницы не найден", "ambiguous_locator": "на странице несколько подходящих элементов", "stale_snapshot": "страница изменилась; нужно перечитать её", "browser_disconnected": "браузер отключён", "paused": "работа браузера приостановлена", "not_directory": "путь не является папкой", "not_empty": "папка не пуста", "too_large": "превышен допустимый размер", "invalid_arguments": "неверные параметры действия",
	}
	if label := labels[code]; label != "" {
		return label
	}
	message, _ := detail["message"].(string)
	if message == "" || strings.HasPrefix(message, "browser operation ended:") {
		message = "не удалось выполнить действие"
	}
	return compactString(message, 180)
}

func commandLabel(session *processSession) string {
	if session.command != "" {
		return compactString(session.command, 100)
	}
	if session.cmd != nil {
		return compactString(strings.Join(session.cmd.Args, " "), 100)
	}
	return "в " + compactString(session.cwd, 100)
}

func commandOutcome(status string, code *int) string {
	switch status {
	case "failed":
		return "⚠ Команда завершилась с ошибкой"
	case "timed_out":
		return "⚠ Команда остановлена: превышено время ожидания"
	case "cancelled":
		return "Команда отменена"
	case "orphaned", "outcome_unknown":
		return "⚠ Результат команды неизвестен"
	}
	if code == nil {
		return "Команда завершилась; код завершения неизвестен"
	}
	if *code != 0 {
		return fmt.Sprintf("⚠ Команда завершилась с ошибкой: код %d", *code)
	}
	return "✓ Команда завершена"
}

func compactString(s string, maxLen int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(consoleText(s))
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return string(runes)
}

func consoleText(s string) string {
	var safe strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			safe.WriteString(strings.Trim(strconv.QuoteRune(r), "'"))
		} else {
			safe.WriteRune(r)
		}
	}
	return safe.String()
}
