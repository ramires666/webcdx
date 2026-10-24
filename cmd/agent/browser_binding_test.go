package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type browserBindingFixture struct {
	b       *browserManager
	link    *browserBridge
	peer    net.Conn
	mu      sync.Mutex
	actual  map[int]browserPersonalTab
	methods chan browserBridgeMessage
}

func newBrowserBindingFixture(t *testing.T, tabs []browserPersonalTab, capabilities []string) *browserBindingFixture {
	t.Helper()
	e, root := testExecutor(t)
	b, err := newBrowserManager(e, browserConfig{Enabled: true, Dir: filepath.Join(root, "binding-browser"), AllowLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	local, peer := net.Pipe()
	f := &browserBindingFixture{b: b, peer: peer, actual: map[int]browserPersonalTab{}, methods: make(chan browserBridgeMessage, 256)}
	for _, tab := range tabs {
		f.actual[tab.ID] = tab
	}
	f.link = &browserBridge{
		manager: b, id: "connection_binding_test", profileID: "profile_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", alias: "binding-test", product: "chrome",
		commandProtocol: browserCommandProtocol, scopeEpoch: 7, capabilities: append([]string(nil), capabilities...),
		conn: local, pending: map[string]chan browserBridgeMessage{}, attached: map[int]*browserTab{}, tabs: append([]browserPersonalTab(nil), tabs...),
	}
	b.bridge = f.link
	b.bridges = map[string]*browserBridge{f.link.id: f.link}
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	go f.serve()
	return f
}

func (f *browserBindingFixture) serve() {
	scanner := bufio.NewScanner(f.peer)
	for scanner.Scan() {
		var command browserBridgeMessage
		if json.Unmarshal(scanner.Bytes(), &command) != nil || command.Kind != "command" {
			continue
		}
		f.methods <- command
		f.mu.Lock()
		actual, exists := f.actual[command.TabID]
		f.mu.Unlock()
		response := browserBridgeMessage{Kind: "response", ID: command.ID}
		if !exists || command.ExpectedURL != "" && (actual.URL != command.ExpectedURL || actual.WindowID != command.ExpectedWindowID) {
			response.Error = "Command target changed"
		} else {
			response.Result = f.result(command.Method, actual)
		}
		f.link.mu.Lock()
		pending := f.link.pending[command.ID]
		f.link.mu.Unlock()
		if pending != nil {
			pending <- response
		}
	}
}

func (f *browserBindingFixture) result(method string, actual browserPersonalTab) json.RawMessage {
	switch method {
	case "WebCodex.target":
		data, _ := json.Marshal(actual)
		return data
	case "Page.getFrameTree":
		data, _ := json.Marshal(map[string]any{"frameTree": map[string]any{"frame": map[string]any{
			"id": "main", "url": actual.URL, "loaderId": "loader", "securityOrigin": "https://example.test", "mimeType": "text/html",
		}}})
		return data
	case "WebCodex.frames":
		return json.RawMessage(`[]`)
	case "Page.createIsolatedWorld":
		return json.RawMessage(`{"executionContextId":1}`)
	case "Accessibility.getFullAXTree":
		return json.RawMessage(`{"nodes":[]}`)
	case "Runtime.evaluate":
		page := map[string]any{
			"url": actual.URL, "title": actual.Title, "text": "verified target text", "ready_state": "complete",
			"snapshot_id": "mock_snapshot", "document_generation": "mock_snapshot", "viewport_width": 1000, "viewport_height": 700,
			"elements": []any{}, "counts": map[string]any{"dom_visited": 1, "elements_found": 0, "elements_returned": 0},
			"truncation_reasons": []any{}, "truncated": false,
		}
		data, _ := json.Marshal(map[string]any{"result": map[string]any{"value": map[string]any{"value": page}}})
		return data
	default:
		return json.RawMessage(`{}`)
	}
}

func (f *browserBindingFixture) open(t *testing.T, exactURL string, windowID int) map[string]any {
	t.Helper()
	args := map[string]any{"mode": "personal", "reuse_only": true, "exact_url": exactURL, "request_key": "bind-" + exactURL}
	if windowID != 0 {
		args["window_id"] = float64(windowID)
	}
	value, err := f.b.call(context.Background(), "browser_open", args)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("browser_open result = %T: %#v", value, value)
	}
	return result
}

func browserBindingErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != want {
		t.Fatalf("error = %v; want code %s", err, want)
	}
}

func (f *browserBindingFixture) drainMethods() []browserBridgeMessage {
	var commands []browserBridgeMessage
	for {
		select {
		case command := <-f.methods:
			commands = append(commands, command)
		default:
			return commands
		}
	}
}

func TestBrowserPersonalStrictExistingBindingSelectsWindowWithoutNavigation(t *testing.T) {
	url := "https://example.test/account?q=exact#details"
	f := newBrowserBindingFixture(t, []browserPersonalTab{
		{ID: 7, URL: url, Title: "Other window", WindowID: 1, Active: true},
		{ID: 8, URL: url, Title: "Selected window", WindowID: 2},
	}, []string{browserTargetCapability})
	result := f.open(t, url, 2)
	if result["binding_verified"] != true || result["readable"] != true || result["auth_state"] != "unverified" || result["window_id"] != 2 || result["title"] != "Selected window" || result["text"] != "verified target text" {
		t.Fatalf("strict binding proof = %#v", result)
	}
	revision, _ := result["binding_revision"].(string)
	if revision == "" {
		t.Fatalf("binding revision missing: %#v", result)
	}
	commands := f.drainMethods()
	if len(commands) == 0 {
		t.Fatal("expected existing-target verification commands")
	}
	for _, command := range commands {
		if command.Method == "Page.navigate" || command.Method == "WebCodex.prepare" || command.Method == "WebCodex.newTab" {
			t.Fatalf("strict reuse unexpectedly created or navigated a tab: %#v", command)
		}
	}
	var selectedTarget bool
	for _, command := range commands {
		if command.Method == "WebCodex.target" {
			selectedTarget = command.TabID == 8 && command.ExpectedURL == url && command.ExpectedWindowID == 2
		}
	}
	if !selectedTarget {
		t.Fatalf("did not verify the exact URL/window target: %#v", commands)
	}
	if len(f.b.sessions) != 1 {
		t.Fatalf("expected only personal session, got %d", len(f.b.sessions))
	}
}

func TestBrowserPersonalStrictExistingBindingFailsClosedForMissingOrLegacyTarget(t *testing.T) {
	existingURL := "https://example.test/account"
	for _, test := range []struct {
		name         string
		requestedURL string
		capabilities []string
		want         string
	}{
		{"missing", "https://example.test/missing", []string{browserTargetCapability}, "tab_not_found"},
		{"legacy", existingURL, nil, "protocol_upgrade_required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newBrowserBindingFixture(t, []browserPersonalTab{{ID: 7, URL: existingURL, Title: "Existing", WindowID: 1, Active: true}}, test.capabilities)
			_, err := f.b.call(context.Background(), "browser_open", map[string]any{"mode": "personal", "reuse_only": true, "exact_url": test.requestedURL, "request_key": "fail-closed-" + test.name})
			browserBindingErrorCode(t, err, test.want)
			for _, command := range f.drainMethods() {
				if command.Method == "WebCodex.prepare" || command.Method == "WebCodex.newTab" || command.Method == "Page.navigate" || command.Method == "WebCodex.target" {
					t.Fatalf("failed strict binding performed a browser action: %#v", command)
				}
			}
		})
	}
}

func TestBrowserBindingRevisionPinsTargetAndRejectsChangedURLOrWindow(t *testing.T) {
	firstURL, secondURL := "https://one.example.test/start", "https://two.example.test/start"
	f := newBrowserBindingFixture(t, []browserPersonalTab{
		{ID: 7, URL: firstURL, Title: "Pinned", WindowID: 4, Active: true},
		{ID: 8, URL: secondURL, Title: "Selected later", WindowID: 5},
	}, []string{browserTargetCapability})
	opened := f.open(t, firstURL, 4)
	revision, _ := opened["binding_revision"].(string)
	sessionID, _ := opened["browser_session_id"].(string)
	if revision == "" || sessionID == "" {
		t.Fatalf("binding setup returned incomplete identity: %#v", opened)
	}
	f.b.mu.Lock()
	var session *browserSession
	for _, candidate := range f.b.sessions {
		if candidate.id == sessionID {
			session = candidate
			break
		}
	}
	if session == nil {
		f.b.mu.Unlock()
		t.Fatal("personal session not found")
	}
	for id, tab := range session.tabs {
		if tab.nativeTabID == 8 {
			session.selected = id
		}
	}
	f.b.mu.Unlock()

	value, err := f.b.call(context.Background(), "browser_snapshot", map[string]any{"browser_session_id": sessionID, "binding_revision": revision, "detail": "light"})
	if err != nil {
		t.Fatalf("pinned snapshot failed after selected tab changed: %v", err)
	}
	if value.(map[string]any)["text"] != "verified target text" {
		t.Fatalf("pinned snapshot returned unexpected page: %#v", value)
	}
	commands := f.drainMethods()
	if len(commands) == 0 {
		t.Fatal("pinned snapshot sent no commands")
	}
	for _, command := range commands {
		if command.TabID != 7 || command.ExpectedURL != firstURL || command.ExpectedWindowID != 4 {
			t.Fatalf("pinned operation escaped its original target: %#v", command)
		}
	}

	interaction := map[string]any{"browser_session_id": sessionID, "binding_revision": revision, "action": "click", "selector": "#submit", "request_key": "pinned-click-navigation"}
	f.mu.Lock()
	changed := f.actual[7]
	changed.URL = "https://one.example.test/after"
	f.actual[7] = changed
	f.mu.Unlock()
	_, err = f.b.call(context.Background(), "browser_interact", interaction)
	browserBindingErrorCode(t, err, "binding_changed")
	for _, command := range f.drainMethods() {
		if strings.HasPrefix(command.Method, "Input.") {
			t.Fatalf("navigation mismatch dispatched input: %#v", command)
		}
	}

	f.mu.Lock()
	changed = f.actual[7]
	changed.URL, changed.WindowID = firstURL, 99
	f.actual[7] = changed
	f.mu.Unlock()
	interaction["request_key"] = "pinned-click-window"
	_, err = f.b.call(context.Background(), "browser_interact", interaction)
	browserBindingErrorCode(t, err, "binding_changed")
	for _, command := range f.drainMethods() {
		if strings.HasPrefix(command.Method, "Input.") {
			t.Fatalf("window mismatch dispatched input: %#v", command)
		}
	}
}
