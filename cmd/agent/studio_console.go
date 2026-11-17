package main

import (
	"encoding/json"
	"fmt"
)

func studioCallDescription(name string, args map[string]any) string {
	switch name {
	case "list_agents":
		return "Проверяю доступных агентов"
	case "poll_command":
		return "Проверяю результат команды"
	case "list_commands":
		return "Показываю список команд"
	case "cancel_command":
		return "Останавливаю команду"
	default:
		return compactString(describeToolCall(name, args), 80)
	}
}

func studioCallSummary(value any, callErr *studioRPCError) string {
	if callErr != nil {
		return "Ошибка: " + compactString(callErr.Message, 80)
	}
	result, _ := value.(map[string]any)
	data, _ := result["structuredContent"].(map[string]any)
	// Read only metadata: file bodies, stdin and command output stay out of the console.
	details := map[string]any{"error": data["error"], "error_code": data["error_code"]}
	if problems, ok := data["errors"].([]any); ok && len(problems) > 0 {
		details["errors"] = problems[:1]
	}
	metadata := map[string]any{"isError": result["isError"], "structuredContent": details}
	raw, _ := json.Marshal(map[string]any{"result": metadata})
	if summary := formatActionResponse(raw, nil); summary != "" {
		return compactString(summary, 90)
	}
	if result["isError"] == true {
		return "Ошибка выполнения"
	}
	if status, _ := data["status"].(string); status != "" {
		switch status {
		case "running", "starting":
			return "Команда выполняется"
		case "exited", "failed", "timed_out", "cancelled", "orphaned", "outcome_unknown":
			var code *int
			if number, ok := data["exit_code"].(float64); ok {
				n := int(number)
				code = &n
			}
			return commandOutcome(status, code)
		}
	}
	if count, ok := data["bytes_written"]; ok {
		return fmt.Sprintf("Записано %v байт", count)
	}
	if count, ok := data["line_count"]; ok {
		return fmt.Sprintf("Прочитано строк: %v", count)
	}
	for _, field := range []struct{ key, label string }{{"entries", "Найдено элементов"}, {"matches", "Найдено совпадений"}, {"roots", "Доступно папок"}, {"agents", "Доступно агентов"}, {"sessions", "Команд в списке"}} {
		if items, ok := data[field.key].([]any); ok {
			return fmt.Sprintf("%s: %d", field.label, len(items))
		}
	}
	return "Готово"
}
