package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestBrowserBridgeURLCheckDoesNotBlockCommandReader(t *testing.T) {
	e, root := testExecutor(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	checks := newBrowserURLChecks(func(ctx context.Context, rawURL string) bool {
		if rawURL != "https://files.example.test/slow.bin?token=private" {
			return false
		}
		entered <- struct{}{}
		select {
		case <-release:
			return true
		case <-ctx.Done():
			return false
		}
	})
	manager := &browserManager{
		config:      browserConfig{Enabled: true, Dir: filepath.Join(root, "url-reader-browser")},
		executor:    e,
		sessions:    map[string]*browserSession{},
		operations:  map[string]*browserOperation{},
		journal:     map[string]browserJournalEntry{},
		bridges:     map[string]*browserBridge{},
		bridgePeers: map[io.ReadWriteCloser]bool{},
		urlChecks:   checks,
	}
	t.Cleanup(manager.Close)
	listener, endpoint, err := browserListenIPC(filepath.Join(root, "url-reader-ipc"))
	if err != nil {
		t.Fatal(err)
	}
	manager.listener = listener
	descriptor := browserBridgeDescriptor{Endpoint: endpoint, Token: "test-bridge-token", ExtensionID: browserExtensionID}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		manager.mu.Lock()
		manager.bridgePeers[conn] = true
		manager.bridgeWG.Add(1)
		manager.mu.Unlock()
		go func() {
			defer manager.bridgeWG.Done()
			defer func() { manager.mu.Lock(); delete(manager.bridgePeers, conn); manager.mu.Unlock() }()
			manager.serveBridge(conn, descriptor)
		}()
	}()
	conn, err := browserDialIPC(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	encoder := json.NewEncoder(conn)
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), browserBridgeMax)
	if err := encoder.Encode(browserBridgeMessage{Kind: "hello", Token: descriptor.Token, Origin: "chrome-extension://" + browserExtensionID + "/"}); err != nil {
		t.Fatal(err)
	}
	readMessage := func() browserBridgeMessage {
		t.Helper()
		type scanResult struct {
			message browserBridgeMessage
			err     error
		}
		result := make(chan scanResult, 1)
		go func() {
			if !scanner.Scan() {
				result <- scanResult{err: scanner.Err()}
				return
			}
			var message browserBridgeMessage
			err := json.Unmarshal(scanner.Bytes(), &message)
			result <- scanResult{message: message, err: err}
		}()
		select {
		case got := <-result:
			if got.err != nil {
				t.Fatalf("read native bridge message: %v", got.err)
			}
			return got.message
		case <-time.After(2 * time.Second):
			t.Fatal("timed out reading native bridge message")
			return browserBridgeMessage{}
		}
	}
	hello := readMessage()
	if hello.Kind != "hello" || hello.CommandProtocol != browserCommandProtocol {
		t.Fatalf("bridge hello = %#v", hello)
	}
	if err := encoder.Encode(browserBridgeMessage{Kind: "identity", ProfileID: "profile_11111111111111111111111111111111", Product: "chrome", CommandProtocol: browserCommandProtocol}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(browserBridgeMessage{Kind: "scope", ScopeEpoch: 1, ControlEpoch: 1, Tabs: []browserPersonalTab{}}); err != nil {
		t.Fatal(err)
	}

	const slowID = "slow-url-check"
	if err := encoder.Encode(browserBridgeMessage{Kind: "url_check", ID: slowID, URL: "https://files.example.test/slow.bin?token=private"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("URL checker did not start")
	}

	manager.mu.Lock()
	link := manager.bridges[hello.ConnectionID]
	manager.mu.Unlock()
	if link == nil {
		t.Fatal("bridge was not registered")
	}
	commandResult := make(chan error, 1)
	go func() {
		var result map[string]any
		commandResult <- link.call(context.Background(), 7, "Page.getFrameTree", map[string]any{}, &result)
	}()
	command := readMessage()
	if command.Kind != "command" || command.Method != "Page.getFrameTree" {
		t.Fatalf("command reader stalled behind URL check; message = %#v", command)
	}
	if err := encoder.Encode(browserBridgeMessage{Kind: "response", ID: command.ID, Result: json.RawMessage(`{"frameTree":{"frame":{"id":"main","url":"https://example.test/"}}}`)}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-commandResult:
		if err != nil {
			t.Fatalf("normal read command failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("normal command did not finish before DNS check release")
	}

	if err := encoder.Encode(browserBridgeMessage{Kind: "scope", ScopeEpoch: 2, ControlEpoch: 2, Paused: true, LocalPaused: true, Tabs: []browserPersonalTab{}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		link.mu.Lock()
		paused := link.paused && link.scopeEpoch == 2
		link.mu.Unlock()
		if paused {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bridge did not apply local pause")
		}
		time.Sleep(time.Millisecond)
	}
	releaseOnce.Do(func() { close(release) })
	urlResult := readMessage()
	if urlResult.Kind != "url_result" || urlResult.ID != slowID || urlResult.Allowed {
		t.Fatalf("late URL decision did not fail closed after pause: %#v", urlResult)
	}
}
