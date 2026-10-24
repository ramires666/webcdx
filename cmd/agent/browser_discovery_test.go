package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func connectBrowserFixture(t *testing.T, b *browserManager, profile, alias, product string) (io.ReadWriteCloser, *json.Encoder) {
	t.Helper()
	data, err := os.ReadFile(b.bridgePath)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor browserBridgeDescriptor
	if err := json.Unmarshal(data, &descriptor); err != nil {
		t.Fatal(err)
	}
	conn, err := browserDialIPC(descriptor.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(browserBridgeMessage{Kind: "hello", Token: descriptor.Token, Origin: "chrome-extension://" + browserExtensionID + "/"}); err != nil {
		t.Fatal(err)
	}
	if !bufio.NewScanner(conn).Scan() {
		t.Fatal("concurrent bridge was not accepted")
	}
	if err := encoder.Encode(browserBridgeMessage{Kind: "identity", ProfileID: profile, Alias: alias, Product: product}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(browserBridgeMessage{Kind: "scope", Tabs: []browserPersonalTab{{ID: 1, URL: "https://example.com/linkedin", Title: "Profile", WindowID: 5, Active: true}}}); err != nil {
		t.Fatal(err)
	}
	return conn, encoder
}

func waitBrowserDiscovery(t *testing.T, b *browserManager, condition func([]map[string]any) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		catalog := b.discoverPersonal()
		assertBrowserOutput(t, catalog)
		if condition(catalog["browsers"].([]map[string]any)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("browser catalog did not converge")
}

func TestPersonalBrowserDiscoveryIsolationAndReconnect(t *testing.T) {
	e, root := testExecutor(t)
	b, err := newBrowserManager(e, browserConfig{Enabled: true, Personal: true, Dir: filepath.Join(root, "browser"), BridgePath: filepath.Join(root, "bridge", "bridge.json")})
	if err != nil {
		t.Fatal(err)
	}
	e.browser = b
	firstID := "profile_11111111111111111111111111111111"
	secondID := "profile_22222222222222222222222222222222"
	first, firstEncoder := connectBrowserFixture(t, b, firstID, "linkedin", "chrome")
	_, _ = connectBrowserFixture(t, b, secondID, "linkedin", "edge")
	waitBrowserDiscovery(t, b, func(items []map[string]any) bool {
		if len(items) != 2 {
			return false
		}
		for _, item := range items {
			if item["identified"] != true || len(item["tabs"].([]map[string]any)) != 1 {
				return false
			}
		}
		return true
	})
	_, err = b.openPersonal(context.Background(), map[string]any{})
	browserErrorCode(t, err, "ambiguous_browser")
	_, err = b.openPersonal(context.Background(), map[string]any{"browser_alias": "linkedin"})
	browserErrorCode(t, err, "ambiguous_browser")
	opened, err := b.openPersonal(context.Background(), map[string]any{"preferred_browser": "chrome", "browser_alias": "linkedin", "target_origin": "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	assertBrowserOutput(t, opened)
	chromeSession := opened.(map[string]any)["browser_session_id"].(string)
	edge, err := b.openPersonal(context.Background(), map[string]any{"profile_id": secondID})
	if err != nil {
		t.Fatal(err)
	}
	edgeSession := edge.(map[string]any)["browser_session_id"].(string)
	if err := firstEncoder.Encode(browserBridgeMessage{Kind: "scope", Paused: true, LocalPaused: true, Tabs: []browserPersonalTab{{ID: 1, URL: "https://example.com/"}}}); err != nil {
		t.Fatal(err)
	}
	waitBrowserDiscovery(t, b, func(items []map[string]any) bool {
		for _, item := range items {
			if item["profile_id"] == firstID {
				return item["paused"] == true
			}
		}
		return false
	})
	_, err = b.openPersonal(context.Background(), map[string]any{"profile_id": firstID})
	browserErrorCode(t, err, "waiting_user")
	_, err = b.control(map[string]any{"action": "resume", "browser_session_id": chromeSession})
	browserErrorCode(t, err, "access_paused")
	edgeAgain, err := b.openPersonal(context.Background(), map[string]any{"profile_id": secondID})
	if err != nil || edgeAgain.(map[string]any)["browser_session_id"] != edgeSession {
		t.Fatal("pause leaked into other browser")
	}
	first.Close()
	waitBrowserDiscovery(t, b, func(items []map[string]any) bool { return len(items) == 1 })
	_, err = b.openPersonal(context.Background(), map[string]any{"profile_id": firstID})
	browserErrorCode(t, err, "browser_not_found")
	_, reconnected := connectBrowserFixture(t, b, firstID, "chrome-linkedin", "chrome")
	waitBrowserDiscovery(t, b, func(items []map[string]any) bool {
		for _, item := range items {
			if item["profile_id"] == firstID {
				return item["alias"] == "chrome-linkedin" && len(item["tabs"].([]map[string]any)) == 1
			}
		}
		return false
	})
	newSession, err := b.openPersonal(context.Background(), map[string]any{"browser_alias": "chrome-linkedin"})
	if err != nil || newSession.(map[string]any)["browser_session_id"] == chromeSession {
		t.Fatal("reconnect reused stale session")
	}
	if err := reconnected.Encode(browserBridgeMessage{Kind: "scope", Tabs: []browserPersonalTab{{ID: 1, URL: "https://example.com/a"}, {ID: 2, URL: "https://example.com/b"}}}); err != nil {
		t.Fatal(err)
	}
	waitBrowserDiscovery(t, b, func(items []map[string]any) bool {
		for _, item := range items {
			if item["profile_id"] == firstID {
				return len(item["tabs"].([]map[string]any)) == 2
			}
		}
		return false
	})
	_, err = b.openPersonal(context.Background(), map[string]any{"profile_id": firstID, "target_origin": "https://example.com"})
	browserErrorCode(t, err, "ambiguous_tab")
	_, err = b.openPersonal(context.Background(), map[string]any{"profile_id": firstID, "target_origin": "https://example.com/path"})
	browserErrorCode(t, err, "invalid_arguments")
}

func TestBrowserProfileIdentityRejectsCollisionsAndRemoteChanges(t *testing.T) {
	a, b := &browserBridge{}, &browserBridge{}
	manager := &browserManager{bridges: map[string]*browserBridge{"a": a, "b": b}}
	identity := browserBridgeMessage{ProfileID: "profile_11111111111111111111111111111111", Alias: "chrome-personal", Product: "chrome"}
	if !manager.identifyBrowser(a, identity) {
		t.Fatal("valid identity rejected")
	}
	if manager.identifyBrowser(b, identity) {
		t.Fatal("two live connections accepted the same profile identity")
	}
	changed := identity
	changed.ProfileID = "profile_22222222222222222222222222222222"
	if manager.identifyBrowser(a, changed) {
		t.Fatal("connection changed its profile identity")
	}
	changed = identity
	changed.Alias = "bad\nname"
	if manager.identifyBrowser(a, changed) {
		t.Fatal("control characters accepted in alias")
	}
	changed = identity
	changed.Product = "arbitrary"
	if manager.identifyBrowser(a, changed) {
		t.Fatal("unknown product accepted")
	}
	if err := validateBrowserArgs("browser_control", map[string]any{"action": "identity", "alias": "remote-change"}); err == nil {
		t.Fatal("remote tool can change local alias")
	}
}
