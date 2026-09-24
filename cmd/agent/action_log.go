package main

import (
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
		return "✨ [mcp] Сессия готова"
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
	case "exec_command":
		cwd, _ := args["cwd"].(string)
		if cwd != "" {
			return fmt.Sprintf("⚡ [exec_command] cwd: %s", cwd)
		}
		return "⚡ [exec_command]"
	case "read_file":
		path := getPathArg(args)
		return fmt.Sprintf("📖 [read_file] %s", path)
	case "write_file":
		path := getPathArg(args)
		return fmt.Sprintf("💾 [write_file] %s", path)
	case "list_directory":
		path := getPathArg(args)
		return fmt.Sprintf("📁 [list_directory] %s", path)
	case "search_files":
		path := getPathArg(args)
		return fmt.Sprintf("🔍 [search_files] %s", path)
	case "poll_command", "cancel_command":
		sessionID, _ := args["session_id"].(string)
		return fmt.Sprintf("⚙️ [%s] %s", name, sessionID)
	default:
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

	return fmt.Sprintf("✅ Успешно за %s", elapsed.Round(time.Millisecond))
}

func getPathArg(args map[string]any) string {
	value, _ := args["path"].(string)
	return value
}

func compactString(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return s
}
