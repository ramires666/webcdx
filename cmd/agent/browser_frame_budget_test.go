package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
)

type browserFrameBudgetBridge struct {
	manager        *browserManager
	bridge         *browserBridge
	tab            *browserTab
	peer           net.Conn
	methods        chan string
	mu             sync.Mutex
	navigated      bool
	navigateOnEval bool
}

func newBrowserFrameBudgetSession(t *testing.T) (*browserFrameBudgetBridge, *browserSession) {
	t.Helper()
	e, root := testExecutor(t)
	b, err := newBrowserManager(e, browserConfig{Enabled: true, Dir: filepath.Join(root, "frame-budget-browser")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	link := &browserBridge{
		id:              "connection_frame_budget",
		commandProtocol: browserCommandProtocol,
		scopeEpoch:      1,
		conn:            local,
		pending:         map[string]chan browserBridgeMessage{},
		attached:        map[int]*browserTab{},
		tabs:            []browserPersonalTab{{ID: 7, URL: "https://example.com/", Active: true}},
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	session := &browserSession{id: browserID("browser_"), mode: "personal", ctx: rootCtx, cancel: cancel, personal: link, tabs: map[string]*browserTab{}}
	tab := &browserTab{id: browserID("tab_"), ctx: rootCtx, cancel: func() {}, personal: link, nativeTabID: 7, personalReady: true, worlds: map[string]int{"main": 1}, frameContexts: map[string]context.Context{}}
	session.tabs[tab.id] = tab
	session.selected = tab.id
	link.attached[7] = tab
	b.mu.Lock()
	b.bridge = link
	b.sessions[session.id] = session
	b.mu.Unlock()

	fixture := &browserFrameBudgetBridge{manager: b, bridge: link, tab: tab, peer: peer, methods: make(chan string, 64)}
	go fixture.serve()
	return fixture, session
}

func (fixture *browserFrameBudgetBridge) serve() {
	scanner := bufio.NewScanner(fixture.peer)
	for scanner.Scan() {
		var command browserBridgeMessage
		if json.Unmarshal(scanner.Bytes(), &command) != nil || command.Kind != "command" {
			continue
		}
		fixture.methods <- command.Method
		fixture.mu.Lock()
		if command.Method == "Runtime.evaluate" && fixture.navigateOnEval {
			fixture.navigateOnEval = false
			fixture.navigated = true
			fixture.manager.recordBrowserEvent(fixture.tab, "Page.frameNavigated", map[string]any{"frame": map[string]any{"id": "main", "url": "https://example.com/after"}})
		}
		navigated := fixture.navigated
		fixture.mu.Unlock()

		fixture.bridge.mu.Lock()
		response := fixture.bridge.pending[command.ID]
		fixture.bridge.mu.Unlock()
		if response == nil {
			continue
		}
		response <- browserBridgeMessage{Kind: "response", ID: command.ID, Result: fixture.result(command.Method, navigated)}
	}
}

func (fixture *browserFrameBudgetBridge) result(method string, navigated bool) json.RawMessage {
	switch method {
	case "Page.getFrameTree":
		url := "https://example.com/"
		if navigated {
			url = "https://example.com/after"
		}
		data, _ := json.Marshal(map[string]any{"frameTree": map[string]any{"frame": map[string]any{
			"id": "main", "url": url, "loaderId": "loader", "securityOrigin": "https://example.com", "mimeType": "text/html",
		}}})
		return data
	case "WebCodex.frames":
		return json.RawMessage(`[]`)
	case "Page.createIsolatedWorld":
		return json.RawMessage(`{"executionContextId":1}`)
	case "Runtime.evaluate":
		url := "https://example.com/"
		if navigated {
			url = "https://example.com/after"
		}
		page := map[string]any{
			"snapshot_id": "snapshot_mock", "document_generation": "snapshot_mock", "ready_state": "complete",
			"url": url, "title": "Mock page", "viewport_width": 1024, "viewport_height": 768,
			"text": "mock page text", "elements": []any{},
			"counts":             map[string]any{"dom_visited": 1, "elements_found": 0, "elements_returned": 0},
			"truncation_reasons": []any{}, "truncated": false,
		}
		data, _ := json.Marshal(map[string]any{"result": map[string]any{"value": map[string]any{"value": page}}})
		return data
	case "Accessibility.getFullAXTree":
		return json.RawMessage(`{"nodes":[{"nodeId":"1","role":{"value":"RootWebArea"},"name":{"value":"Mock page"},"childIds":[]}]}`)
	default:
		return json.RawMessage(`{}`)
	}
}

func (fixture *browserFrameBudgetBridge) takeMethods() []string {
	methods := []string{}
	for {
		select {
		case method := <-fixture.methods:
			methods = append(methods, method)
		default:
			return methods
		}
	}
}

func countBrowserMethods(methods []string, target string) int {
	count := 0
	for _, method := range methods {
		if method == target {
			count++
		}
	}
	return count
}

func TestBrowserSnapshotFrameCommandBudget(t *testing.T) {
	fixture, session := newBrowserFrameBudgetSession(t)
	args := map[string]any{"browser_session_id": session.id}
	full, err := fixture.manager.call(context.Background(), "browser_snapshot", args)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := full.(map[string]any); !ok {
		t.Fatalf("full snapshot result = %#v", full)
	}
	fullMethods := fixture.takeMethods()
	if len(fullMethods) > 4 || countBrowserMethods(fullMethods, "Accessibility.getFullAXTree") != 1 {
		t.Fatalf("full snapshot used %d bridge calls: %v", len(fullMethods), fullMethods)
	}
	if countBrowserMethods(fullMethods, "Page.getFrameTree") != 1 || countBrowserMethods(fullMethods, "WebCodex.frames") != 1 || countBrowserMethods(fullMethods, "Runtime.evaluate") != 1 {
		t.Fatalf("full snapshot did not reuse frame enumeration: %v", fullMethods)
	}

	light, err := fixture.manager.call(context.Background(), "browser_snapshot", map[string]any{"browser_session_id": session.id, "detail": "light"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := light.(map[string]any); !ok {
		t.Fatalf("light snapshot result = %#v", light)
	}
	lightMethods := fixture.takeMethods()
	if len(lightMethods) > 3 || countBrowserMethods(lightMethods, "Accessibility.getFullAXTree") != 0 {
		t.Fatalf("light snapshot used %d bridge calls or requested AX: %v", len(lightMethods), lightMethods)
	}
	if countBrowserMethods(lightMethods, "Page.getFrameTree") != 1 || countBrowserMethods(lightMethods, "WebCodex.frames") != 1 || countBrowserMethods(lightMethods, "Runtime.evaluate") != 1 {
		t.Fatalf("light snapshot did not reuse frame enumeration: %v", lightMethods)
	}
}

func TestBrowserSnapshotNavigationInvalidatesOperationFrameCache(t *testing.T) {
	fixture, session := newBrowserFrameBudgetSession(t)
	fixture.mu.Lock()
	fixture.navigateOnEval = true
	fixture.mu.Unlock()
	args := map[string]any{"browser_session_id": session.id}
	_, err := fixture.manager.call(context.Background(), "browser_snapshot", args)
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != "frame_changed" {
		t.Fatalf("snapshot spanning navigation error = %v; want frame_changed", err)
	}
	methods := fixture.takeMethods()
	result, err := fixture.manager.call(context.Background(), "browser_snapshot", args)
	if err != nil {
		t.Fatal(err)
	}
	methods = append(methods, fixture.takeMethods()...)
	if countBrowserMethods(methods, "Page.getFrameTree") < 2 || countBrowserMethods(methods, "WebCodex.frames") < 2 {
		t.Fatalf("navigation reused stale frame enumeration: %v", methods)
	}
	frames, _ := result.(map[string]any)["frames"].([]map[string]any)
	if len(frames) != 1 || frames[0]["url"] != "https://example.com/after" {
		t.Fatalf("snapshot frames after navigation = %#v", result.(map[string]any)["frames"])
	}
}
