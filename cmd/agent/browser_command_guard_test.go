package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"
)

func newBrowserCommandGuardBridge(t *testing.T, protocol int) (*browserBridge, net.Conn) {
	t.Helper()
	local, peer := net.Pipe()
	t.Cleanup(func() {
		_ = local.Close()
		_ = peer.Close()
	})
	return &browserBridge{
		id:              "connection_test",
		commandProtocol: protocol,
		scopeEpoch:      7,
		conn:            local,
		pending:         map[string]chan browserBridgeMessage{},
	}, peer
}

func readBrowserCommand(t *testing.T, peer net.Conn) browserBridgeMessage {
	t.Helper()
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var message browserBridgeMessage
	if err := json.NewDecoder(peer).Decode(&message); err != nil {
		t.Fatalf("read bridge command: %v", err)
	}
	_ = peer.SetReadDeadline(time.Time{})
	return message
}

func assertNoBrowserCommand(t *testing.T, peer net.Conn) {
	t.Helper()
	if err := peer.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	_, err := peer.Read(one[:])
	_ = peer.SetReadDeadline(time.Time{})
	if err == nil {
		t.Fatal("unexpected bridge command bytes")
	}
	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("read without command = %v; want timeout", err)
	}
}

func deliverBrowserCommandResponse(t *testing.T, bridge *browserBridge, message browserBridgeMessage) {
	t.Helper()
	bridge.mu.Lock()
	response := bridge.pending[message.ID]
	bridge.mu.Unlock()
	if response == nil {
		t.Fatalf("no pending request for %q", message.ID)
	}
	response <- browserBridgeMessage{Kind: "response", ID: message.ID, Result: json.RawMessage(`{}`)}
}

func TestBrowserCommandGuardCanceledContextSendsNoBytes(t *testing.T) {
	bridge, peer := newBrowserCommandGuardBridge(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := bridge.callFrame(ctx, 4, "", "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("callFrame error = %v; want context.Canceled", err)
	}
	assertNoBrowserCommand(t, peer)
}

func TestBrowserCommandGuardProtocolTwoEnvelope(t *testing.T) {
	bridge, peer := newBrowserCommandGuardBridge(t, 2)
	deadline := time.Now().Add(2 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- bridge.callFrame(ctx, 4, "", "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed"}, nil)
	}()
	message := readBrowserCommand(t, peer)
	if message.CommandProtocol != 2 || message.ConnectionID != bridge.id || message.ScopeEpoch != bridge.scopeEpoch {
		t.Fatalf("command envelope = %#v", message)
	}
	if message.DeadlineMS <= 0 || message.DeadlineMS > deadline.UnixMilli() {
		t.Fatalf("deadline_ms = %d; context deadline = %d", message.DeadlineMS, deadline.UnixMilli())
	}
	if message.Kind != "command" || message.Method != "Input.dispatchMouseEvent" {
		t.Fatalf("unexpected command: %#v", message)
	}
	deliverBrowserCommandResponse(t, bridge, message)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("callFrame: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("callFrame did not finish after response")
	}
}

func TestBrowserCommandGuardLegacyProtocol(t *testing.T) {
	bridge, peer := newBrowserCommandGuardBridge(t, 0)
	err := bridge.callFrame(context.Background(), 4, "", "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed"}, nil)
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != "protocol_upgrade_required" {
		t.Fatalf("side-effecting call error = %v; want protocol_upgrade_required", err)
	}
	assertNoBrowserCommand(t, peer)

	done := make(chan error, 1)
	go func() { done <- bridge.callFrame(context.Background(), 4, "", "Page.getFrameTree", nil, nil) }()
	message := readBrowserCommand(t, peer)
	if message.Method != "Page.getFrameTree" {
		t.Fatalf("read command method = %q", message.Method)
	}
	deliverBrowserCommandResponse(t, bridge, message)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("read-only call: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("read-only call did not finish after response")
	}
}

func TestBrowserCommandGuardCancelAcknowledgementKeepsConnection(t *testing.T) {
	bridge, peer := newBrowserCommandGuardBridge(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- bridge.call(ctx, 4, "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed"}, nil)
	}()
	command := readBrowserCommand(t, peer)
	cancel()
	abort := readBrowserCommand(t, peer)
	if abort.Kind != "command_cancel" || abort.ID != command.ID || abort.ConnectionID != bridge.id {
		t.Fatalf("cancel envelope = %#v", abort)
	}
	bridge.mu.Lock()
	response := bridge.pending[command.ID]
	bridge.mu.Unlock()
	response <- browserBridgeMessage{Kind: "cancel_ack", ID: command.ID, Cancelled: true}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled command = %v", err)
	}
	// Cancelling one request must not destroy the entire profile connection.
	go func() { done <- bridge.call(context.Background(), 4, "Page.getFrameTree", nil, nil) }()
	fresh := readBrowserCommand(t, peer)
	deliverBrowserCommandResponse(t, bridge, fresh)
	if err := <-done; err != nil {
		t.Fatalf("fresh read after cancellation: %v", err)
	}
}

func TestBrowserCommandGuardOldAccessBindingSendsNoBytes(t *testing.T) {
	bridge, peer := newBrowserCommandGuardBridge(t, 2)
	ctx := bridge.bindAccess(context.Background())
	bridge.scopeEpoch++
	err := bridge.call(ctx, 4, "Input.dispatchMouseEvent", nil, nil)
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != "scope_changed" {
		t.Fatalf("old access binding = %v", err)
	}
	assertNoBrowserCommand(t, peer)
}
