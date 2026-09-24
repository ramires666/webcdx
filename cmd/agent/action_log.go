package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// formatActionRequest parses an incoming JSON-RPC request and returns a human-readable action description.
func formatActionRequest(raw []byte) string {
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "📦 [mcp] Входящий запрос (не удалось распарсить JSON)"
	}

	switch msg.Method {
	case "initialize":
		return "🔌 [mcp] Инициализация сессии ChatGPT ➔ Agent"
	case "notifications/initialized":
		return "✨ [mcp] Сессия успешно запущена и готова к работе"
	case "tools/list":
		return "📋 [mcp] Запрос доступных инструментов агента"
	case "ping":
		return "🏓 [mcp] Ping"
	case "tools/call":
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(msg.Params, &call); err != nil {
			return "🔧 [tool] Вызов инструмента"
		}
		return describeToolCall(call.Name, call.Arguments)
	default:
		if msg.Method != "" {
			return fmt.Sprintf("📨 [mcp] Метод: %s", msg.Method)
		}
		return "📦 [mcp] Входящий запрос"
	}
}

// describeToolCall produces a concise, readable summary of a specific tool invocation.
func describeToolCall(name string, args map[string]any) string {
	if args == nil {
		args = map[string]any{}
	}

	switch name {
	case "shell_command", "exec_command", "bash":
		cmdStr, _ := args["command"].(string)
		if cmdStr == "" {
			cmdStr, _ = args["cmd"].(string)
		}
		cmdStr = compactString(cmdStr, 80)
		workdir, _ := args["workdir"].(string)
		if workdir != "" {
			return fmt.Sprintf("⚡ [shell] %s  (папка: %s)", cmdStr, workdir)
		}
		return fmt.Sprintf("⚡ [shell] %s", cmdStr)

	case "apply_patch":
		patchStr := ""
		if val, ok := args["input"].(string); ok {
			patchStr = val
		} else if val, ok := args["patch"].(string); ok {
			patchStr = val
		}
		files := extractFilesFromPatch(patchStr)
		if len(files) > 0 {
			return fmt.Sprintf("📝 [patch] %s", strings.Join(files, ", "))
		}
		return "📝 [patch] Применение патча к файлам"

	case "read_file", "view_file":
		path := getPathArg(args)
		if path != "" {
			return fmt.Sprintf("📖 [read] %s", path)
		}
		return "📖 [read] Чтение файла"

	case "write_file":
		path := getPathArg(args)
		if path != "" {
			return fmt.Sprintf("💾 [write] %s", path)
		}
		return "💾 [write] Запись файла"

	case "edit_file", "modify_file":
		path := getPathArg(args)
		if path != "" {
			return fmt.Sprintf("✏️ [edit] %s", path)
		}
		return "✏️ [edit] Редактирование файла"

	case "list_dir", "directory_list":
		path := getPathArg(args)
		if path != "" {
			return fmt.Sprintf("📁 [list] %s", path)
		}
		return "📁 [list] Список файлов в директории"

	case "grep_search", "file_search":
		query, _ := args["query"].(string)
		path := getPathArg(args)
		if path != "" {
			return fmt.Sprintf("🔍 [%s] %q в %s", name, compactString(query, 40), path)
		}
		return fmt.Sprintf("🔍 [%s] %q", name, compactString(query, 50))

	case "update_plan":
		if plan, ok := args["plan"].([]any); ok && len(plan) > 0 {
			var currentStep string
			for _, item := range plan {
				if itemMap, ok := item.(map[string]any); ok {
					if status, ok := itemMap["status"].(string); ok && status == "in_progress" {
						if step, ok := itemMap["step"].(string); ok {
							currentStep = step
							break
						}
					}
				}
			}
			if currentStep != "" {
				return fmt.Sprintf("📋 [plan] Текущий шаг: %s (всего шагов: %d)", compactString(currentStep, 60), len(plan))
			}
			return fmt.Sprintf("📋 [plan] Обновление плана (%d шагов)", len(plan))
		}
		if expl, ok := args["explanation"].(string); ok && expl != "" {
			return fmt.Sprintf("📋 [plan] %s", compactString(expl, 60))
		}
		return "📋 [plan] Обновление плана задач"

	case "view_image":
		path := getPathArg(args)
		if path != "" {
			return fmt.Sprintf("🖼️ [image] %s", path)
		}
		return "🖼️ [image] Просмотр изображения"

	default:
		compactArgs := formatCompactArgs(args)
		if compactArgs != "" {
			return fmt.Sprintf("🔧 [%s] %s", name, compactArgs)
		}
		return fmt.Sprintf("🔧 [%s]", name)
	}
}

// formatActionResponse parses the agent response and returns a short readable summary of the result.
func formatActionResponse(raw []byte, callErr error, elapsed time.Duration) string {
	if callErr != nil {
		return fmt.Sprintf("❌ Ошибка выполнения за %s: %v", elapsed.Round(time.Millisecond), callErr)
	}

	var msg struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(raw, &msg); err != nil {
		return fmt.Sprintf("✅ Завершено за %s (%d байт)", elapsed.Round(time.Millisecond), len(raw))
	}

	if msg.Error != nil && msg.Error.Message != "" {
		return fmt.Sprintf("❌ Ошибка за %s: %s", elapsed.Round(time.Millisecond), compactString(msg.Error.Message, 80))
	}

	if msg.Result.IsError {
		errorText := "сбой выполнения"
		if len(msg.Result.Content) > 0 && msg.Result.Content[0].Text != "" {
			errorText = msg.Result.Content[0].Text
		}
		return fmt.Sprintf("⚠️ Завершено с предупреждением/ошибкой за %s: %s", elapsed.Round(time.Millisecond), compactString(errorText, 90))
	}

	if len(msg.Result.Content) > 0 && msg.Result.Content[0].Text != "" {
		firstLine := strings.Split(strings.TrimSpace(msg.Result.Content[0].Text), "\n")[0]
		return fmt.Sprintf("✅ Успешно за %s: %s", elapsed.Round(time.Millisecond), compactString(firstLine, 80))
	}

	return fmt.Sprintf("✅ Успешно за %s", elapsed.Round(time.Millisecond))
}

func getPathArg(args map[string]any) string {
	for _, key := range []string{
		"path", "file_path", "filePath", "file", "filename",
		"target_file", "targetFile", "target", "dir", "directory",
	} {
		if val, ok := args[key].(string); ok && val != "" {
			return val
		}
	}
	return ""
}

func extractFilesFromPatch(patch string) []string {
	var files []string
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(patch))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "*** ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "Index: ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				f := strings.Trim(fields[1], `"'`)
				if f != "/dev/null" && f != "a/" && f != "b/" && !seen[f] && len(f) > 1 {
					seen[f] = true
					files = append(files, f)
				}
			}
		}
	}
	return files
}

func compactString(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

func formatCompactArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	var pairs []string
	for k, v := range args {
		valStr := fmt.Sprintf("%v", v)
		pairs = append(pairs, fmt.Sprintf("%s: %s", k, compactString(valStr, 30)))
		if len(pairs) >= 3 {
			break
		}
	}
	return strings.Join(pairs, ", ")
}
