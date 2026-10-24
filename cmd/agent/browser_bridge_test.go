package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeBrowserFraming(t *testing.T) {
	var buffer bytes.Buffer
	data := []byte(`{"kind":"hello"}`)
	if err := writeNativeMessage(&buffer, data); err != nil {
		t.Fatal(err)
	}
	result, err := readNativeMessage(&buffer)
	if err != nil || !bytes.Equal(result, data) {
		t.Fatalf("framing: %s %v", result, err)
	}
	if err := binary.Write(&buffer, binary.LittleEndian, uint32(browserBridgeMax+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := readNativeMessage(&buffer); err == nil {
		t.Fatal("oversized native message accepted")
	}
	manifest := browserHostManifest("/agent")
	if !bytes.Contains(manifest, []byte("chrome-extension://"+browserExtensionID+"/")) || bytes.Contains(manifest, []byte("*")) {
		t.Fatalf("unsafe native host origins: %s", manifest)
	}
}

func TestPersonalCatalogIsLazyAndReusableAcrossChats(t *testing.T) {
	link := &browserBridge{attached: map[int]*browserTab{}}
	for id := 1; id <= 40; id++ {
		link.tabs = append(link.tabs, browserPersonalTab{ID: id, URL: "https://example.com/?token=hidden#secret"})
	}
	b := &browserManager{bridge: link, sessions: map[string]*browserSession{}}
	ctx := context.Background()
	opened, err := b.openPersonal(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	id := opened.(map[string]any)["browser_session_id"].(string)
	session := b.sessions[id]
	oldID := session.selected
	oldTab := session.tabs[oldID]
	if len(session.tabs) != 40 {
		t.Fatalf("personal catalog has %d tabs, want 40", len(session.tabs))
	}
	for _, tab := range session.tabs {
		defer tab.cancel()
		if tab.personalReady {
			t.Fatal("opening personal eagerly initialized a tab")
		}
	}
	reopened, err := b.openPersonal(ctx, map[string]any{})
	if err != nil || reopened.(map[string]any)["browser_session_id"] != id {
		t.Fatalf("new chat could not reuse personal session: %v %v", reopened, err)
	}
	listed, err := b.execute(ctx, "browser_tabs", map[string]any{"browser_session_id": id, "action": "list"})
	if err != nil {
		t.Fatal(err)
	}
	tabs := listed.(map[string]any)["tabs"].([]map[string]any)
	if len(tabs) != 40 {
		t.Fatalf("list has %d tabs, want 40", len(tabs))
	}
	for _, tab := range tabs {
		if tab["url"] != "https://example.com/" {
			t.Fatalf("tab catalog leaked query or fragment: %v", tab)
		}
	}
	// Remove the selected tab and make another website available without a local click.
	link.tabs = append(link.tabs[1:], browserPersonalTab{ID: 41, URL: "https://other.example/"})
	listed, err = b.execute(ctx, "browser_tabs", map[string]any{"browser_session_id": id, "action": "list"})
	if err != nil || len(listed.(map[string]any)["tabs"].([]map[string]any)) != 40 {
		t.Fatalf("catalog did not refresh: %v %v", listed, err)
	}
	if oldTab.ctx.Err() == nil || session.tabs[oldID] != nil || session.selected == oldID {
		t.Fatal("closed tab kept its context or selection")
	}
	if _, err := b.execute(ctx, "browser_snapshot", map[string]any{"browser_session_id": id, "tab_id": oldID}); err == nil {
		t.Fatal("removed personal tab could still be used")
	}
	link.paused = true
	if _, err := b.openPersonal(ctx, map[string]any{}); err == nil {
		t.Fatal("reopening personal bypassed local pause")
	}
}

func TestPersonalBrowserIPCAndRevocation(t *testing.T) {
	e, root := testExecutor(t)
	var actual *browserTab
	approvedURL := "http://localhost/test"
	if os.Getenv("WEBCODEX_BROWSER_TEST") == "1" {
		site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/login" {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "existing", Path: "/", MaxAge: 3600})
				fmt.Fprint(w, "logged in")
				return
			}
			if cookie, err := r.Cookie("session"); err != nil || cookie.Value != "existing" {
				w.WriteHeader(401)
				fmt.Fprint(w, "no account")
				return
			}
			fmt.Fprint(w, "existing private account")
		}))
		defer site.Close()
		managed, err := newBrowserManager(e, browserConfig{Enabled: true, Dir: filepath.Join(root, "source-browser"), Executable: os.Getenv("WEBCODEX_BROWSER_PATH"), AllowLocal: true, Headless: true})
		if err != nil {
			t.Fatal(err)
		}
		defer managed.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		opened, err := managed.open(ctx, map[string]any{"mode": "clean"})
		if err != nil {
			t.Fatal(err)
		}
		id := opened.(map[string]any)["browser_session_id"].(string)
		session := managed.sessions[id]
		actual = session.tabs[session.selected]
		for _, path := range []string{"/login", "/private"} {
			if _, err := managed.tabAction(ctx, "browser_navigate", session, actual, map[string]any{"action": "url", "url": site.URL + path}); err != nil {
				t.Fatal(err)
			}
		}
		approvedURL = site.URL + "/private"
	}
	b, err := newBrowserManager(e, browserConfig{Enabled: true, Dir: filepath.Join(root, "browser"), Personal: true, BridgePath: filepath.Join(root, "bridge", "bridge.json"), AllowLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	e.browser = b
	data, err := os.ReadFile(b.bridgePath)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor browserBridgeDescriptor
	if err := json.Unmarshal(data, &descriptor); err != nil {
		t.Fatal(err)
	}
	bad, err := browserDialIPC(descriptor.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(bad).Encode(browserBridgeMessage{Kind: "hello", Token: "wrong", Origin: "chrome-extension://" + browserExtensionID + "/"}); err != nil {
		t.Fatal(err)
	}
	bad.Close()
	time.Sleep(100 * time.Millisecond)
	b.mu.Lock()
	accepted := b.bridge != nil
	b.mu.Unlock()
	if accepted {
		t.Fatal("unauthorized bridge accepted")
	}
	conn, err := browserDialIPC(descriptor.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(browserBridgeMessage{Kind: "hello", Token: descriptor.Token, Origin: "chrome-extension://" + browserExtensionID + "/"}); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatal("no bridge hello")
	}
	var hello browserBridgeMessage
	if json.Unmarshal(scanner.Bytes(), &hello) != nil || hello.CommandProtocol != browserCommandProtocol || hello.ConnectionID == "" {
		t.Fatal("bridge did not negotiate command protocol 2")
	}
	if err := encoder.Encode(browserBridgeMessage{Kind: "identity", ProfileID: "profile_33333333333333333333333333333333", Product: "chrome", CommandProtocol: browserCommandProtocol}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(browserBridgeMessage{Kind: "scope", Tabs: []browserPersonalTab{}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		for scanner.Scan() {
			var message browserBridgeMessage
			if json.Unmarshal(scanner.Bytes(), &message) != nil {
				return
			}
			if message.Kind == "command_cancel" {
				_ = encoder.Encode(browserBridgeMessage{Kind: "cancel_ack", ID: message.ID, Cancelled: true})
				continue
			}
			if message.Kind != "command" {
				continue
			}
			if message.CommandProtocol != browserCommandProtocol || message.ConnectionID != hello.ConnectionID || message.DeadlineMS <= time.Now().UnixMilli() {
				t.Error("command envelope lost its protocol, connection or deadline")
				return
			}
			data := json.RawMessage(`{}`)
			errorText := ""
			if message.Method == "Page.getFrameTree" {
				data = json.RawMessage(`{"frameTree":{"frame":{"id":"frame","url":"http://localhost/test"}}}`)
			}
			if message.Method == "WebCodex.prepare" {
				if err := encoder.Encode(browserBridgeMessage{Kind: "scope", Tabs: []browserPersonalTab{{ID: 7, URL: approvedURL}}}); err != nil {
					return
				}
			} else if message.Method == "WebCodex.frames" {
				data = json.RawMessage(`[]`)
				if actual != nil {
					frames, err := actual.frames(ctx)
					if err != nil {
						errorText = "frame catalog failed"
					} else {
						data, _ = json.Marshal(frames)
					}
				}
			} else if actual != nil {
				var result map[string]any
				err := actual.commandFrame(ctx, message.FrameID, message.Method, message.Params, &result)
				if err != nil {
					errorText = "CDP failed"
				} else {
					data, _ = json.Marshal(result)
				}
			}
			if err := encoder.Encode(browserBridgeMessage{Kind: "response", ID: message.ID, Result: data, Error: errorText}); err != nil {
				return
			}
		}
	}()
	for {
		b.mu.Lock()
		link := b.bridge
		b.mu.Unlock()
		ready := false
		if link != nil {
			link.mu.Lock()
			ready = link.commandProtocol == browserCommandProtocol
			link.mu.Unlock()
		}
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	opened, err := b.openPersonal(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	id := opened.(map[string]any)["browser_session_id"].(string)
	listed, err := b.execute(ctx, "browser_tabs", map[string]any{"browser_session_id": id, "action": "list"})
	if err != nil || len(listed.(map[string]any)["tabs"].([]map[string]any)) != 1 {
		t.Fatalf("personal tabs: %v %v", listed, err)
	}
	if _, err := b.execute(ctx, "browser_snapshot", map[string]any{"browser_session_id": id, "tab_id": "foreign"}); err == nil {
		t.Fatal("foreign personal tab accepted")
	}
	if actual != nil {
		snapshot, err := b.execute(ctx, "browser_snapshot", map[string]any{"browser_session_id": id})
		if err != nil || snapshot.(map[string]any)["text"] != "existing private account" {
			t.Fatalf("existing browser authorization lost: %v %v", snapshot, err)
		}
	}
	if err := encoder.Encode(browserBridgeMessage{Kind: "scope", Tabs: []browserPersonalTab{}, Paused: true, LocalPaused: true}); err != nil {
		t.Fatal(err)
	}
	for {
		b.mu.Lock()
		paused := b.sessions[id].paused
		b.mu.Unlock()
		if paused {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := b.control(map[string]any{"browser_session_id": id, "action": "resume"}); err == nil {
		t.Fatal("remote resume bypassed local pause")
	}
	if _, err := b.execute(ctx, "browser_tabs", map[string]any{"browser_session_id": id, "action": "list"}); err == nil {
		t.Fatal("revoked personal access accepted")
	}
	if actual != nil && actual.ctx.Err() != nil {
		t.Fatal("revoking personal access closed the original browser")
	}
}
