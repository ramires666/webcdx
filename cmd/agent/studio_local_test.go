package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestStudioLocalAuthorizationAndIdentity(t *testing.T) {
	e, _ := testExecutor(t)
	defer e.Close()
	h := studioLocalHandler(e, &studioOperations{dir: t.TempDir()}, map[string]bool{studioExtensionID: true}, "127.0.0.1:47831", "agent-one")
	request := func(origin, extension, host, identity, method string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(studioRequest{ID: "1", Method: method})
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47831/studio/rpc", bytes.NewReader(body))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Host = host
		r.Header.Set("Origin", origin)
		r.Header.Set("X-WebCodex-Extension", extension)
		r.Header.Set("X-WebCodex-Agent", identity)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, origin := range []string{"https://aistudio.google.com", "null", "chrome-extension://other"} {
		if w := request(origin, studioExtensionID, "127.0.0.1:47831", "", "initialize"); w.Code != http.StatusForbidden {
			t.Fatalf("untrusted origin %s: %d", origin, w.Code)
		}
	}
	if w := request("", "other", "127.0.0.1:47831", "", "initialize"); w.Code != http.StatusForbidden {
		t.Fatal("untrusted extension allowed")
	}
	if w := request("", studioExtensionID, "attacker.example", "", "initialize"); w.Code != http.StatusForbidden {
		t.Fatal("DNS rebinding allowed")
	}
	w := request("chrome-extension://"+studioExtensionID, studioExtensionID, "127.0.0.1:47831", "", "initialize")
	var response struct {
		Result struct {
			LocalAgent struct {
				ID string `json:"id"`
			} `json:"localAgent"`
		} `json:"result"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Result.LocalAgent.ID != "agent-one" {
		t.Fatalf("initialize: %s", w.Body)
	}
	for _, identity := range []string{"", "another-agent"} {
		if w := request("", studioExtensionID, "127.0.0.1:47831", identity, "tools/list"); w.Code != http.StatusConflict {
			t.Fatal("unpinned request allowed")
		}
	}
	if w := request("", studioExtensionID, "127.0.0.1:47831", "agent-one", "tools/list"); w.Code != http.StatusOK {
		t.Fatalf("pinned request: %s", w.Body)
	}
}

func TestStudioLocalDiscoversOnlyExtensionFromThisInstallation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	profile := filepath.Join(dir, "Google", "Chrome", "User Data", "Profile 10")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	trustedID, otherID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	settings := map[string]any{"extensions": map[string]any{"settings": map[string]any{
		trustedID: map[string]any{"path": filepath.Join(dir, "install", "ai-studio-extension")},
		otherID:   map[string]any{"path": filepath.Join(dir, "another", "ai-studio-extension")},
	}}}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(profile, "Secure Preferences"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ids := studioLocalExtensionIDs(filepath.Join(dir, "install", "bin", "ai-studio-bridge", "agent.exe"))
	if !ids[trustedID] || ids[otherID] {
		t.Fatalf("incorrect trusted extensions: %v", ids)
	}
}

func TestStudioLocalSkipsOccupiedPort(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	_, portText, _ := net.SplitHostPort(occupied.Addr().String())
	port, _ := strconv.Atoi(portText)
	listener, err := listenStudioLocal(port, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if listener.Addr().String() == occupied.Addr().String() {
		t.Fatal("occupied listener replaced")
	}
}
