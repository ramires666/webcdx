package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type processSession struct {
	id              string
	command         string
	cwd             string
	status          string
	startedAt       time.Time
	finishedAt      time.Time
	exitCode        *int
	deadline        time.Time
	pid             int
	processIdentity string
	sessionDir      string
	metadataPath    string
	stdoutPath      string
	stderrPath      string
	stdoutTruncated bool
	stderrTruncated bool
	cmd             *exec.Cmd
	stdoutFile      *os.File
	stderrFile      *os.File
	budget          *logBudget
	done            chan struct{}
	doneOnce        sync.Once
	metadataMu      sync.Mutex
}

type sessionMetadata struct {
	ID              string     `json:"session_id"`
	CWD             string     `json:"cwd"`
	PID             int        `json:"pid"`
	ProcessIdentity string     `json:"process_identity"`
	Status          string     `json:"status"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	ExitCode        *int       `json:"exit_code"`
	TimeoutDeadline time.Time  `json:"timeout_deadline"`
	StdoutTruncated bool       `json:"stdout_truncated"`
	StderrTruncated bool       `json:"stderr_truncated"`
}

type logBudget struct {
	mu              sync.Mutex
	remaining       int64
	stdoutTruncated bool
	stderrTruncated bool
}

type cappedStreamWriter struct {
	file   *os.File
	budget *logBudget
	stderr bool
}

func (w *cappedStreamWriter) Write(data []byte) (int, error) {
	w.budget.mu.Lock()
	defer w.budget.mu.Unlock()
	part := data
	if int64(len(part)) > w.budget.remaining {
		part = part[:max(0, int(w.budget.remaining))]
		if w.stderr {
			w.budget.stderrTruncated = true
		} else {
			w.budget.stdoutTruncated = true
		}
	}
	if len(part) > 0 {
		n, err := w.file.Write(part)
		w.budget.remaining -= int64(n)
		if err != nil {
			return 0, err
		}
	}
	return len(data), nil
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (e *nativeExecutor) execCommand(ctx context.Context, args map[string]any, v4 bool) (any, error) {
	allowed := []string{"command", "cwd", "timeout_seconds", "yield_time_ms"}
	if v4 {
		allowed = append(allowed, "argv", "env", "stdin")
	}
	if err := onlyArgs(args, allowed...); err != nil {
		return nil, err
	}
	rawCWD, err := requiredString(args, "cwd")
	if err != nil {
		return nil, err
	}
	cwd, err := e.resolvePath(rawCWD)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return nil, pathError("inspect cwd", err)
	}
	if !info.IsDir() {
		return nil, toolErr("not_directory", "cwd is not a directory")
	}
	timeoutSeconds, err := optionalInt(args, "timeout_seconds", 1200, 1, 86400)
	if err != nil {
		return nil, err
	}
	yieldMS, err := optionalInt(args, "yield_time_ms", 10000, 0, 30000)
	if err != nil {
		return nil, err
	}
	command, hasCommand := args["command"].(string)
	rawArgv, hasArgv := args["argv"]
	if !v4 && (!hasCommand || command == "") {
		return nil, toolErr("invalid_arguments", "command is required")
	}
	if v4 && (hasCommand == hasArgv || hasCommand && command == "") {
		return nil, toolErr("invalid_arguments", "exactly one non-empty command or argv is required")
	}
	var cmd *exec.Cmd
	if hasCommand {
		cmd = localShellCommand(command)
	} else {
		argv, err := parseArgv(rawArgv)
		if err != nil {
			return nil, err
		}
		cmd = exec.Command(argv[0], argv[1:]...)
	}
	cmd.Dir = cwd
	if v4 {
		overlay, err := parseEnvironment(args["env"])
		if err != nil {
			return nil, err
		}
		cmd.Env = append(os.Environ(), overlay...)
		stdin, err := optionalString(args, "stdin")
		if err != nil {
			return nil, err
		}
		if len(stdin) > maxStdinBytes {
			return nil, toolErr("too_large", "stdin exceeds size limit")
		}
		if _, exists := args["stdin"]; exists {
			cmd.Stdin = strings.NewReader(stdin)
		}
	}
	id, err := randomSessionID()
	if err != nil {
		return nil, toolErr("io_error", "create session id", err)
	}
	sessionDir := filepath.Join(e.logDir, id)
	if err := os.Mkdir(sessionDir, 0o700); err != nil {
		return nil, toolErr("io_error", "create session directory", err)
	}
	stdoutPath := filepath.Join(sessionDir, "stdout.log")
	stderrPath := filepath.Join(sessionDir, "stderr.log")
	stdoutFile, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, toolErr("io_error", "create stdout log", err)
	}
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		stdoutFile.Close()
		return nil, toolErr("io_error", "create stderr log", err)
	}
	budget := &logBudget{remaining: e.maxLogBytes}
	cmd.Stdout = &cappedStreamWriter{file: stdoutFile, budget: budget}
	cmd.Stderr = &cappedStreamWriter{file: stderrFile, budget: budget, stderr: true}
	configureProcess(cmd)
	started := time.Now().UTC()
	session := &processSession{
		id: id, command: command, cwd: cwd, status: "running", startedAt: started,
		deadline: started.Add(time.Duration(timeoutSeconds) * time.Second), sessionDir: sessionDir,
		metadataPath: filepath.Join(sessionDir, "metadata.json"), stdoutPath: stdoutPath, stderrPath: stderrPath,
		cmd: cmd, stdoutFile: stdoutFile, stderrFile: stderrFile, budget: budget, done: make(chan struct{}),
	}
	e.mu.Lock()
	e.sessions[id] = session
	e.mu.Unlock()
	if err := cmd.Start(); err != nil {
		stdoutFile.Close()
		stderrFile.Close()
		e.mu.Lock()
		session.status = "failed"
		session.finishedAt = time.Now().UTC()
		session.closeDone()
		e.mu.Unlock()
		_ = e.persistSession(session)
		return e.sessionResult(id, nil, nil, 64<<10, v4, false)
	}
	e.mu.Lock()
	session.pid = cmd.Process.Pid
	session.processIdentity, _ = processIdentity(session.pid)
	e.mu.Unlock()
	go e.waitProcess(session)
	if err := e.persistSession(session); err != nil {
		_ = e.stopSession(session.id, "failed")
		return nil, toolErr("io_error", "persist command session", err)
	}
	go e.enforceTimeout(session)
	if yieldMS > 0 {
		timer := time.NewTimer(time.Duration(yieldMS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-session.done:
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	return e.sessionResult(id, nil, nil, 64<<10, v4, false)
}

func parseArgv(raw any) ([]string, error) {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return nil, toolErr("invalid_arguments", "argv must be a non-empty string array")
	}
	argv := make([]string, len(values))
	for index, value := range values {
		argument, ok := value.(string)
		if !ok || index == 0 && argument == "" || strings.IndexByte(argument, 0) >= 0 {
			return nil, toolErr("invalid_arguments", "argv must contain valid strings and a non-empty executable")
		}
		argv[index] = argument
	}
	return argv, nil
}

func parseEnvironment(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, toolErr("invalid_arguments", "env must be an object of string values")
	}
	keys := make([]string, 0, len(values))
	for name, rawValue := range values {
		value, ok := rawValue.(string)
		if !ok || !environmentName.MatchString(name) || strings.IndexByte(value, 0) >= 0 {
			return nil, toolErr("invalid_arguments", "env contains an invalid name or value")
		}
		keys = append(keys, name)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, name := range keys {
		result = append(result, name+"="+values[name].(string))
	}
	return result, nil
}

func localShellCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
	}
	return exec.Command("/bin/sh", "-c", command)
}

func (e *nativeExecutor) waitProcess(session *processSession) {
	err := session.cmd.Wait()
	_ = session.stdoutFile.Close()
	_ = session.stderrFile.Close()
	e.mu.Lock()
	if session.status == "running" {
		session.status = "exited"
	}
	if session.cmd.ProcessState != nil {
		exitCode := session.cmd.ProcessState.ExitCode()
		session.exitCode = &exitCode
	}
	if err != nil && session.cmd.ProcessState == nil && session.status == "running" {
		session.status = "failed"
	}
	if session.budget != nil {
		session.budget.mu.Lock()
		session.stdoutTruncated = session.budget.stdoutTruncated
		session.stderrTruncated = session.budget.stderrTruncated
		session.budget.mu.Unlock()
	}
	session.finishedAt = time.Now().UTC()
	e.mu.Unlock()
	_ = e.persistSession(session)
	session.closeDone()
}

func (e *nativeExecutor) enforceTimeout(session *processSession) {
	timer := time.NewTimer(time.Until(session.deadline))
	defer timer.Stop()
	select {
	case <-timer.C:
		e.stopSession(session.id, "timed_out")
	case <-session.done:
	case <-e.stop:
	}
}

func (e *nativeExecutor) pollCommand(args map[string]any, v4 bool) (any, error) {
	allowed := []string{"session_id", "offset", "max_bytes"}
	if v4 {
		allowed = []string{"session_id", "stdout_offset", "stderr_offset", "max_bytes"}
	}
	if err := onlyArgs(args, allowed...); err != nil {
		return nil, err
	}
	id, err := requiredString(args, "session_id")
	if err != nil {
		return nil, err
	}
	maxBytes, err := optionalInt(args, "max_bytes", 64<<10, 1, 1<<20)
	if err != nil {
		return nil, err
	}
	maxBytes = min(maxBytes, e.maxResponse)
	if v4 {
		stdoutOffset, err := optionalInt(args, "stdout_offset", 0, 0, int(^uint(0)>>1))
		if err != nil {
			return nil, err
		}
		stderrOffset, err := optionalInt(args, "stderr_offset", 0, 0, int(^uint(0)>>1))
		if err != nil {
			return nil, err
		}
		return e.sessionResult(id, &stdoutOffset, &stderrOffset, maxBytes, true, false)
	}
	offset, err := optionalInt(args, "offset", 0, 0, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	return e.sessionResult(id, &offset, nil, maxBytes, false, false)
}

func (e *nativeExecutor) cancelCommand(args map[string]any, v4 bool) (any, error) {
	if err := onlyArgs(args, "session_id"); err != nil {
		return nil, err
	}
	id, err := requiredString(args, "session_id")
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	session := e.sessions[id]
	alreadyFinished := session != nil && session.status != "running"
	e.mu.Unlock()
	if session == nil {
		return nil, toolErr("unknown_session", "unknown session_id")
	}
	if !alreadyFinished {
		if err := e.stopSession(id, "cancelled"); err != nil {
			return nil, err
		}
		select {
		case <-session.done:
		case <-time.After(10 * time.Second):
			return nil, toolErr("cancel_failed", "process tree did not stop within 10 seconds")
		}
	}
	result, err := e.sessionResult(id, nil, nil, 64<<10, v4, alreadyFinished)
	if err == nil && v4 {
		result.(map[string]any)["already_finished"] = alreadyFinished
	}
	return result, err
}

func (e *nativeExecutor) stopSession(id, status string) error {
	e.mu.Lock()
	session := e.sessions[id]
	if session == nil || session.status != "running" {
		e.mu.Unlock()
		return nil
	}
	cmd, pid, identity := session.cmd, session.pid, session.processIdentity
	if cmd == nil && !processAliveWithIdentity(pid, identity) {
		session.status = "orphaned"
		session.finishedAt = time.Now().UTC()
		session.closeDone()
		e.mu.Unlock()
		_ = e.persistSession(session)
		return toolErr("process_identity_mismatch", "process identity no longer matches the persisted session")
	}
	session.status = status
	e.mu.Unlock()
	_ = e.persistSession(session)
	if cmd != nil {
		if err := terminateProcessTree(cmd); err != nil {
			return toolErr("cancel_failed", "terminate process tree", err)
		}
		return nil
	}
	if err := terminatePIDTree(pid); err != nil {
		return toolErr("cancel_failed", "terminate restored process tree", err)
	}
	return nil
}

func (e *nativeExecutor) sessionResult(id string, stdoutOffset, stderrOffset *int, maxBytes int, v4, alreadyFinished bool) (any, error) {
	e.mu.Lock()
	session := e.sessions[id]
	if session == nil {
		e.mu.Unlock()
		return nil, toolErr("unknown_session", "unknown session_id")
	}
	status, startedAt, finishedAt, exitCode := session.status, session.startedAt, session.finishedAt, session.exitCode
	cwd, command := session.cwd, session.command
	stdoutPath, stderrPath := session.stdoutPath, session.stderrPath
	stdoutCapped, stderrCapped := session.stdoutTruncated, session.stderrTruncated
	e.mu.Unlock()
	durationEnd := time.Now().UTC()
	if !finishedAt.IsZero() {
		durationEnd = finishedAt
	}
	finishedValue := any(nil)
	if !finishedAt.IsZero() {
		finishedValue = finishedAt.Format(timeFormat)
	}
	exitValue := any(nil)
	if exitCode != nil {
		exitValue = *exitCode
	}
	if v4 {
		stdout, stderr, stdoutNext, stderrNext, stdoutMore, stderrMore, err := readSeparateLogs(stdoutPath, stderrPath, stdoutOffset, stderrOffset, maxBytes)
		if err != nil {
			return nil, toolErr("io_error", "read command logs", err)
		}
		result := map[string]any{
			"session_id": id, "cwd": cwd, "status": status, "started_at": startedAt.Format(timeFormat), "finished_at": finishedValue,
			"exit_code": exitValue, "duration_ms": max(int64(0), durationEnd.Sub(startedAt).Milliseconds()),
			"stdout_log_path": stdoutPath, "stderr_log_path": stderrPath, "stdout": stdout, "stderr": stderr,
			"stdout_next_offset": stdoutNext, "stderr_next_offset": stderrNext,
			"stdout_truncated": stdoutCapped || stdoutMore, "stderr_truncated": stderrCapped || stderrMore,
		}
		return result, nil
	}
	offset := 0
	if stdoutOffset != nil {
		offset = *stdoutOffset
	}
	output, next, more, err := readCompatibilityLog(stdoutPath, stderrPath, int64(offset), maxBytes)
	if err != nil {
		return nil, toolErr("io_error", "read command logs", err)
	}
	return map[string]any{
		"session_id": id, "command": command, "cwd": cwd, "status": status, "started_at": startedAt.Format(timeFormat),
		"finished_at": finishedValue, "exit_code": exitValue, "duration_ms": max(int64(0), durationEnd.Sub(startedAt).Milliseconds()),
		"log_path": stdoutPath, "output": output, "next_offset": next,
		"truncated": stdoutCapped || stderrCapped || more, "already_finished": alreadyFinished,
	}, nil
}

func readSeparateLogs(stdoutPath, stderrPath string, stdoutOffset, stderrOffset *int, maxBytes int) (string, string, int64, int64, bool, bool, error) {
	stdoutSize, err := fileSize(stdoutPath)
	if err != nil {
		return "", "", 0, 0, false, false, err
	}
	stderrSize, err := fileSize(stderrPath)
	if err != nil {
		return "", "", 0, 0, false, false, err
	}
	stdoutStart, stderrStart := int64(0), int64(0)
	if stdoutOffset != nil {
		stdoutStart = min(int64(*stdoutOffset), stdoutSize)
	} else if stdoutSize > int64(maxBytes/2) {
		stdoutStart = stdoutSize - int64(maxBytes/2)
	}
	if stderrOffset != nil {
		stderrStart = min(int64(*stderrOffset), stderrSize)
	} else if stderrSize > int64(maxBytes/2) {
		stderrStart = stderrSize - int64(maxBytes/2)
	}
	stdoutAvailable, stderrAvailable := stdoutSize-stdoutStart, stderrSize-stderrStart
	stdoutLimit, stderrLimit := maxBytes, 0
	if stderrAvailable > 0 {
		stdoutLimit, stderrLimit = (maxBytes+1)/2, maxBytes/2
		if stdoutAvailable < int64(stdoutLimit) {
			stderrLimit += stdoutLimit - int(stdoutAvailable)
		} else if stderrAvailable < int64(stderrLimit) {
			stdoutLimit += stderrLimit - int(stderrAvailable)
		}
	}
	stdout, stdoutNext, stdoutMore, err := readLog(stdoutPath, stdoutStart, stdoutLimit)
	if err != nil {
		return "", "", 0, 0, false, false, err
	}
	stderr, stderrNext, stderrMore, err := readLog(stderrPath, stderrStart, stderrLimit)
	return stdout, stderr, stdoutNext, stderrNext, stdoutMore || stdoutStart > 0, stderrMore || stderrStart > 0, err
}

func readCompatibilityLog(stdoutPath, stderrPath string, offset int64, maxBytes int) (string, int64, bool, error) {
	stdoutSize, err := fileSize(stdoutPath)
	if err != nil {
		return "", offset, false, err
	}
	stderrSize, err := fileSize(stderrPath)
	if err != nil {
		return "", offset, false, err
	}
	total := stdoutSize + stderrSize
	if offset > total {
		offset = total
	}
	var result strings.Builder
	if offset < stdoutSize {
		text, next, _, err := readLog(stdoutPath, offset, maxBytes)
		if err != nil {
			return "", offset, false, err
		}
		result.WriteString(text)
		offset = next
	}
	virtualOffset := offset
	if virtualOffset >= stdoutSize && result.Len() < maxBytes {
		stderrOffset := max(int64(0), virtualOffset-stdoutSize)
		text, next, _, err := readLog(stderrPath, stderrOffset, maxBytes-result.Len())
		if err != nil {
			return "", virtualOffset, false, err
		}
		result.WriteString(text)
		virtualOffset = stdoutSize + next
	}
	return result.String(), virtualOffset, virtualOffset < total, nil
}

func readLog(path string, offset int64, maxBytes int) (string, int64, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", offset, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", offset, false, err
	}
	if offset > info.Size() {
		offset = info.Size()
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", offset, false, err
	}
	buffer := make([]byte, min(maxBytes, int(info.Size()-offset)))
	n, err := io.ReadFull(file, buffer)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", offset, false, err
	}
	buffer = buffer[:n]
	leading := 0
	for leading < len(buffer) && buffer[leading]&0xc0 == 0x80 {
		leading++
	}
	buffer = buffer[leading:]
	validLength := len(buffer)
	for validLength > 0 && !utf8.Valid(buffer[:validLength]) {
		validLength--
	}
	next := offset + int64(leading+validLength)
	return string(buffer[:validLength]), next, next < info.Size(), nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s *processSession) closeDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

func (e *nativeExecutor) persistSession(session *processSession) error {
	session.metadataMu.Lock()
	defer session.metadataMu.Unlock()
	e.mu.Lock()
	metadata := sessionMetadata{
		ID: session.id, CWD: session.cwd, PID: session.pid, ProcessIdentity: session.processIdentity,
		Status: session.status, StartedAt: session.startedAt, ExitCode: session.exitCode, TimeoutDeadline: session.deadline,
		StdoutTruncated: session.stdoutTruncated, StderrTruncated: session.stderrTruncated,
	}
	if !session.finishedAt.IsZero() {
		finished := session.finishedAt
		metadata.FinishedAt = &finished
	}
	e.mu.Unlock()
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return atomicWrite(session.metadataPath, data, false)
}

func (e *nativeExecutor) restoreSessions() error {
	entries, err := os.ReadDir(e.logDir)
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().Add(-e.processTTL)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "proc_") {
			continue
		}
		sessionDir := filepath.Join(e.logDir, entry.Name())
		metadataPath := filepath.Join(sessionDir, "metadata.json")
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			continue
		}
		var metadata sessionMetadata
		if json.Unmarshal(data, &metadata) != nil || metadata.ID != entry.Name() {
			continue
		}
		ageTime := metadata.StartedAt
		if metadata.FinishedAt != nil {
			ageTime = *metadata.FinishedAt
		}
		if metadata.Status != "running" && ageTime.Before(cutoff) {
			_ = os.RemoveAll(sessionDir)
			continue
		}
		session := &processSession{
			id: metadata.ID, cwd: metadata.CWD, pid: metadata.PID, processIdentity: metadata.ProcessIdentity,
			status: metadata.Status, startedAt: metadata.StartedAt, exitCode: metadata.ExitCode, deadline: metadata.TimeoutDeadline,
			sessionDir: sessionDir, metadataPath: metadataPath, stdoutPath: filepath.Join(sessionDir, "stdout.log"),
			stderrPath: filepath.Join(sessionDir, "stderr.log"), stdoutTruncated: metadata.StdoutTruncated,
			stderrTruncated: metadata.StderrTruncated, done: make(chan struct{}),
		}
		if metadata.FinishedAt != nil {
			session.finishedAt = *metadata.FinishedAt
		}
		if session.status == "running" && processAliveWithIdentity(session.pid, session.processIdentity) {
			e.sessions[session.id] = session
			go e.monitorRestored(session)
			continue
		}
		if session.status == "running" {
			session.status = "orphaned"
			session.finishedAt = time.Now().UTC()
		}
		session.closeDone()
		e.sessions[session.id] = session
		_ = e.persistSession(session)
	}
	return nil
}

func (e *nativeExecutor) monitorRestored(session *processSession) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !session.deadline.IsZero() && time.Now().After(session.deadline) {
				_ = e.stopSession(session.id, "timed_out")
			}
			if !processAliveWithIdentity(session.pid, session.processIdentity) {
				e.mu.Lock()
				if session.status == "running" {
					session.status = "exited"
				}
				session.finishedAt = time.Now().UTC()
				e.mu.Unlock()
				_ = e.persistSession(session)
				session.closeDone()
				return
			}
		case <-session.done:
			return
		case <-e.stop:
			return
		}
	}
}

func (e *nativeExecutor) cleanupLoop() {
	interval := min(e.processTTL, time.Hour)
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cutoff := time.Now().UTC().Add(-e.processTTL)
			var remove []string
			e.mu.Lock()
			for id, session := range e.sessions {
				if session.status != "running" && !session.finishedAt.IsZero() && session.finishedAt.Before(cutoff) {
					delete(e.sessions, id)
					remove = append(remove, session.sessionDir)
				}
			}
			e.mu.Unlock()
			for _, path := range remove {
				_ = os.RemoveAll(path)
			}
		case <-e.stop:
			return
		}
	}
}
