package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

const openCodeProgressTail = 16 << 10

var openCodeShellWord = regexp.MustCompile("(?:\"[^\"\\r\\n]*\"|'[^'\\r\\n]*'|[^\\s'\";|&])+")
var openCodeANSI = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")
var openCodeBackgroundWait = regexp.MustCompile("^waiting for [0-9]+ background jobs?$")

type openCodeCommand struct {
	Number int    `json:"number"`
	Agent  string `json:"agent"`
	Title  string `json:"title"`
}

func detectOpenCodeCommand(command string, argv []string) *openCodeCommand {
	if command != "" {
		argv = nil
		first, _, _ := strings.Cut(strings.TrimSpace(command), "\n")
		first = strings.TrimSpace(strings.TrimPrefix(first, "&"))
		if first == "" {
			return nil
		}
		words := openCodeShellWord.FindAllStringIndex(first, -1)
		end := 0
		for _, word := range words {
			if strings.TrimSpace(first[end:word[0]]) != "" {
				break
			}
			argv = append(argv, first[word[0]:word[1]])
			end = word[1]
		}
	}
	if len(argv) < 2 || !strings.EqualFold(argv[1], "run") {
		return nil
	}
	exe := strings.Trim(argv[0], "'\"")
	switch strings.ToLower(path.Base(strings.ReplaceAll(exe, "\\", "/"))) {
	case "opencode", "opencode.exe", "opencode.cmd":
	default:
		return nil
	}
	agent := &openCodeCommand{Agent: "OpenCode", Title: "Задача без заголовка"}
	for i := 2; i < len(argv); i++ {
		if argv[i] == "--" {
			break
		}
		flag, value, inline := strings.Cut(argv[i], "=")
		if flag != "--agent" && flag != "--title" {
			continue
		}
		if !inline {
			if i+1 == len(argv) || strings.HasPrefix(argv[i+1], "--") {
				continue
			}
			i++
			value = argv[i]
		}
		value = compactString(strings.Trim(value, "'\""), 140)
		if value == "" {
			continue
		}
		if flag == "--agent" {
			agent.Agent = value
		} else {
			agent.Title = value
		}
	}
	return agent
}

func (c openCodeCommand) label() string {
	return fmt.Sprintf("OpenCode #%d [%s] — %s", c.Number, c.Agent, c.Title)
}

type openCodeLogSnapshot struct {
	bytes    int64
	written  time.Time
	action   string
	position int64
}

type openCodeProgress struct {
	openCodeCommand
	LastAction   string `json:"last_action,omitempty"`
	LastOutputAt string `json:"last_output_at,omitempty"`
	QuietMS      int64  `json:"quiet_ms"`
	Deadline     string `json:"deadline"`
	StdoutBytes  int64  `json:"stdout_bytes"`
	StderrBytes  int64  `json:"stderr_bytes"`
}

// Both formats keep the original logs intact. The progress preview reads only
// a bounded tail and reports tool names rather than prompts or shell arguments.
func readOpenCodeLog(filename string, scan bool) openCodeLogSnapshot {
	file, err := os.Open(filename)
	if err != nil {
		return openCodeLogSnapshot{}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return openCodeLogSnapshot{}
	}
	log := openCodeLogSnapshot{bytes: info.Size(), written: info.ModTime()}
	if !scan {
		return log
	}
	offset := max(0, log.bytes-openCodeProgressTail)
	buffer := make([]byte, log.bytes-offset)
	n, err := file.ReadAt(buffer, offset)
	if err != nil && err != io.EOF {
		return log
	}
	value := string(buffer[:n])
	if offset > 0 {
		firstBreak := strings.IndexByte(value, '\n')
		if firstBreak < 0 {
			return log
		}
		offset += int64(firstBreak + 1)
		value = value[firstBreak+1:]
	}
	for _, line := range strings.Split(value, "\n") {
		if action := openCodeLineAction(line); action != "" {
			log.action, log.position = action, offset
		}
		offset += int64(len(line) + 1)
	}
	return log
}

func openCodeLineAction(raw string) string {
	line := strings.TrimSpace(openCodeANSI.ReplaceAllString(raw, ""))
	if line == "" {
		return ""
	}
	if strings.HasPrefix(line, "{") {
		var event struct {
			Type string
			Part struct {
				Tool string
			}
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			return ""
		}
		switch event.Type {
		case "tool_use":
			return openCodeToolAction(event.Part.Tool)
		case "step_start":
			return "Модель начала шаг"
		case "step_finish":
			return "Модель завершила шаг"
		case "text":
			return "Модель получила текстовый ответ"
		case "error":
			return "OpenCode сообщил об ошибке"
		}
		return ""
	}
	if openCodeBackgroundWait.MatchString(line) {
		return "Ожидает фоновые операции"
	}
	if strings.HasPrefix(line, "$ ") {
		return "Запуск команды"
	}
	prefix, rest, found := strings.Cut(line, " ")
	if !found || prefix != "→" && prefix != "←" && prefix != "~" {
		return ""
	}
	tool, target, _ := strings.Cut(rest, " ")
	action := openCodeToolAction(tool)
	switch strings.ToLower(tool) {
	case "read", "write", "edit", "patch":
		target, _, _ = strings.Cut(target, " (")
		if name := path.Base(strings.ReplaceAll(strings.TrimSpace(target), "\\", "/")); name != "." && name != "/" {
			action += " " + compactString(name, 80)
		}
	}
	return action
}

func openCodeToolAction(tool string) string {
	switch strings.ToLower(tool) {
	case "read":
		return "Чтение файла"
	case "write":
		return "Запись файла"
	case "edit", "patch", "apply_patch":
		return "Изменение файла"
	case "bash", "shell":
		return "Запуск команды"
	case "glob", "grep", "list":
		return "Поиск в проекте"
	case "task", "subagent":
		return "Работа дочерней задачи"
	default:
		return ""
	}
}

func (s *processSession) openCodeSnapshot(end time.Time, scan bool) (openCodeProgress, int64, bool) {
	stdout := readOpenCodeLog(s.stdoutPath, scan)
	stderr := readOpenCodeLog(s.stderrPath, scan)
	latest := stdout.written
	if stderr.written.After(latest) {
		latest = stderr.written
	}
	event, onStderr := stdout, false
	if stderr.action != "" && (stdout.action == "" || stdout.written.Before(stderr.written)) {
		event, onStderr = stderr, true
	}
	result := openCodeProgress{
		openCodeCommand: *s.openCode,
		LastAction:      event.action,
		Deadline:        s.deadline.Format(timeFormat),
		StdoutBytes:     stdout.bytes,
		StderrBytes:     stderr.bytes,
	}
	if s.budget != nil {
		s.budget.mu.Lock()
		if s.budget.lastOutput.After(latest) {
			latest = s.budget.lastOutput
		}
		s.budget.mu.Unlock()
	}
	if latest.IsZero() {
		latest = s.startedAt
	} else {
		result.LastOutputAt = latest.UTC().Format(timeFormat)
	}
	result.QuietMS = max(0, end.Sub(latest).Milliseconds())
	return result, event.position, onStderr
}

func (e *nativeExecutor) logOpenCodeOutcome(session *processSession, lastAction string) {
	last := ""
	if lastAction != "" {
		last = " · последнее действие: " + lastAction
	}
	e.logf("%s — %s · %s%s", session.openCode.label(), commandOutcome(session.status, session.exitCode), session.finishedAt.Sub(session.startedAt).Round(time.Second), last)
}
