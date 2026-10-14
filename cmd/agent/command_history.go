package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type commandRequestID struct{}
type commandIdentity struct{ key, hash string }

func commandHash(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (e *nativeExecutor) execCommandOnce(ctx context.Context, args map[string]any) (any, error) {
	key, err := requiredString(args, "request_key")
	if err != nil || len(key) > 128 {
		return nil, toolErr("invalid_arguments", "request_key must contain 1 to 128 characters")
	}
	clean := make(map[string]any, len(args)-1)
	fingerprint := make(map[string]any, len(args)-1)
	for name, value := range args {
		if name != "request_key" {
			clean[name] = value
			if name != "yield_time_ms" {
				fingerprint[name] = value
			}
		}
	}
	identity := commandIdentity{commandHash(key), commandHash(fingerprint)}
	e.execMu.Lock()
	defer e.execMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	for _, session := range e.sessions {
		if session.requestKeyHash != identity.key {
			continue
		}
		id, hash := session.id, session.requestHash
		e.mu.Unlock()
		if hash != identity.hash {
			return nil, toolErr("request_key_conflict", "request_key was already used with different command arguments")
		}
		return e.sessionResult(id, nil, nil, 64<<10, true, false)
	}
	e.mu.Unlock()
	return e.execCommand(context.WithValue(ctx, commandIdentity{}, identity), clean, true)
}

type commandEvent struct {
	At        time.Time `json:"at"`
	Event     string    `json:"event"`
	SessionID string    `json:"session_id"`
	RequestID string    `json:"request_id,omitempty"`
	Status    string    `json:"status"`
	PID       int       `json:"pid"`
	ExitCode  *int      `json:"exit_code"`
}

func (e *nativeExecutor) commandEvent(session *processSession, event string) {
	session.metadataMu.Lock()
	defer session.metadataMu.Unlock()
	e.mu.Lock()
	entry := commandEvent{time.Now().UTC(), event, session.id, session.requestID, session.status, session.pid, session.exitCode}
	e.mu.Unlock()
	path := filepath.Join(session.sessionDir, "events.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		defer file.Close()
		if info, statErr := file.Stat(); statErr == nil && info.Size() < 64<<10 {
			err = json.NewEncoder(file).Encode(entry)
		}
	}
	if err != nil {
		if e.verbose {
			log.Printf("[session=%s] EVENT_LOG_FAILED error=%q", session.id, err.Error())
		} else {
			log.Printf("⚠ Не удалось сохранить события команды: %s", compactString(err.Error(), 180))
		}
	}
}

func (e *nativeExecutor) commandEvents(session *processSession) []commandEvent {
	session.metadataMu.Lock()
	defer session.metadataMu.Unlock()
	entries := []commandEvent{}
	file, err := os.Open(filepath.Join(session.sessionDir, "events.jsonl"))
	if err != nil {
		return entries
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	for len(entries) < 128 {
		var event commandEvent
		if decoder.Decode(&event) != nil {
			break
		}
		entries = append(entries, event)
	}
	return entries
}

func (e *nativeExecutor) listCommands(args map[string]any) (any, error) {
	if err := onlyArgs(args, "limit", "status"); err != nil {
		return nil, err
	}
	limit, err := optionalInt(args, "limit", 100, 1, 500)
	if err != nil {
		return nil, err
	}
	status, err := optionalString(args, "status")
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	sessions := []*processSession{}
	for _, session := range e.sessions {
		if status == "" || status == session.status {
			sessions = append(sessions, session)
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].startedAt.After(sessions[j].startedAt) })
	result := []map[string]any{}
	for _, session := range sessions[:min(limit, len(sessions))] {
		end := time.Now()
		if !session.finishedAt.IsZero() {
			end = session.finishedAt
		}
		result = append(result, map[string]any{
			"session_id": session.id, "cwd": session.cwd, "pid": session.pid, "status": session.status,
			"started_at": session.startedAt.Format(timeFormat), "exit_code": exitCodeValue(session.exitCode),
			"duration_ms": max(int64(0), end.Sub(session.startedAt).Milliseconds()),
		})
	}
	return map[string]any{"sessions": result, "truncated": len(sessions) > limit}, nil
}
