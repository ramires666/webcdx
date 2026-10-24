package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
)

type browserCallOutcome struct {
	result any
	err    error
}

func newQueuedPersonalSession(t *testing.T) (*browserManager, *browserSession, <-chan string) {
	t.Helper()
	e, root := testExecutor(t)
	b, err := newBrowserManager(e, browserConfig{Enabled: true, Dir: filepath.Join(root, "queued-browser")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)

	local, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	link := &browserBridge{
		id:              "connection_queued_test",
		commandProtocol: browserCommandProtocol,
		scopeEpoch:      1,
		conn:            local,
		pending:         map[string]chan browserBridgeMessage{},
		attached:        map[int]*browserTab{},
		tabs:            []browserPersonalTab{{ID: 1, URL: "https://example.com/", Active: true}},
	}
	b.bridge = link
	methods := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(peer)
		for scanner.Scan() {
			var command browserBridgeMessage
			if json.Unmarshal(scanner.Bytes(), &command) != nil || command.Kind != "command" {
				continue
			}
			methods <- command.Method
			link.mu.Lock()
			response := link.pending[command.ID]
			link.mu.Unlock()
			if response == nil {
				continue
			}
			result := json.RawMessage(`{}`)
			if command.Method == "WebCodex.newTab" {
				result = json.RawMessage(`{"tab_id":2,"url":"https://example.com/"}`)
			}
			response <- browserBridgeMessage{Kind: "response", ID: command.ID, Result: result}
		}
	}()
	opened, err := b.openPersonal(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := opened.(map[string]any)["browser_session_id"].(string)
	session := b.sessions[sessionID]
	return b, session, methods
}

func waitForSessionWork(t *testing.T, session *browserSession, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		session.work.mu.Lock()
		ready := len(session.work.waiting) == count
		session.work.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session queue did not reach %d waiters", count)
}

func waitForOperation(t *testing.T, b *browserManager, key string) *browserOperation {
	t.Helper()
	keyHash := commandHash(key)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		for _, op := range b.operations {
			if op.KeyHash == keyHash {
				b.mu.Unlock()
				return op
			}
		}
		b.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("operation for key %q was not reserved", key)
	return nil
}

func TestBrowserOperationCancelBeforeDispatch(t *testing.T) {
	b, session, methods := newQueuedPersonalSession(t)
	release, err := session.work.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	const requestKey = "queued-tab-create-cancel"
	result := make(chan browserCallOutcome, 1)
	go func() {
		value, err := b.call(context.Background(), "browser_tabs", map[string]any{
			"browser_session_id": session.id,
			"action":             "new",
			"request_key":        requestKey,
		})
		result <- browserCallOutcome{value, err}
	}()
	op := waitForOperation(t, b, requestKey)
	waitForSessionWork(t, session, 1)
	if _, err := b.control(map[string]any{"action": "cancel", "operation_id": op.ID}); err != nil {
		t.Fatal(err)
	}
	release()

	select {
	case outcome := <-result:
		var coded *codedError
		if !errors.As(outcome.err, &coded) || coded.code != "cancelled_before_dispatch" {
			t.Fatalf("call error = %v (result %v); want cancelled_before_dispatch", outcome.err, outcome.result)
		}
		if coded.details["phase"] != "not_dispatched" || coded.details["recovery"] != "stop" {
			t.Fatalf("cancellation details = %#v", coded.details)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled operation did not finish")
	}
	if op.execution.dispatched.Load() {
		t.Fatal("cancelled operation marked a browser side effect dispatched")
	}
	if len(session.tabs) != 1 {
		t.Fatalf("cancelled tab creation changed the session: %d tabs", len(session.tabs))
	}
	select {
	case method := <-methods:
		t.Fatalf("cancelled queued operation sent bridge command %q", method)
	default:
	}
}

func TestBrowserOperationPauseResumeDropsOldWaiter(t *testing.T) {
	b, session, methods := newQueuedPersonalSession(t)
	release, err := session.work.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	oldResult := make(chan browserCallOutcome, 1)
	go func() {
		value, err := b.call(context.Background(), "browser_tabs", map[string]any{
			"browser_session_id": session.id,
			"action":             "new",
			"request_key":        "paused-old-waiter",
		})
		oldResult <- browserCallOutcome{value, err}
	}()
	waitForSessionWork(t, session, 1)
	if _, err := b.control(map[string]any{"action": "pause", "browser_session_id": session.id}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.control(map[string]any{"action": "resume", "browser_session_id": session.id}); err != nil {
		t.Fatal(err)
	}
	release()

	select {
	case outcome := <-oldResult:
		var coded *codedError
		if !errors.As(outcome.err, &coded) || coded.code != "cancelled_before_dispatch" {
			t.Fatalf("old waiter error = %v (result %v); want cancelled_before_dispatch", outcome.err, outcome.result)
		}
		if coded.details["phase"] != "not_dispatched" || coded.details["recovery"] != "stop" {
			t.Fatalf("old waiter cancellation details = %#v", coded.details)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old waiter did not finish")
	}
	if len(session.tabs) != 1 {
		t.Fatalf("paused old waiter changed the session: %d tabs", len(session.tabs))
	}

	value, err := b.call(context.Background(), "browser_tabs", map[string]any{
		"browser_session_id": session.id,
		"action":             "list",
	})
	if err != nil {
		t.Fatalf("new operation after resume failed: %v", err)
	}
	if value.(map[string]any)["status"] != "ready" {
		t.Fatalf("new operation result = %v", value)
	}
	if len(value.(map[string]any)["tabs"].([]map[string]any)) != 1 {
		t.Fatalf("new operation returned unexpected tabs: %v", value)
	}
	select {
	case method := <-methods:
		t.Fatalf("paused waiter sent bridge command %q", method)
	default:
	}
}
