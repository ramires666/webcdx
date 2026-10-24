package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserPreviewRecoveryAndVerifiedSubmit(t *testing.T) {
	var submits atomic.Int32
	var saved atomic.Value
	saved.Store("")
	f := newBrowserFixtureHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/preview":
			w.WriteHeader(500)
			fmt.Fprint(w, "preview failed")
			return
		case "/slow-preview":
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
			return
		case "/save":
			data, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
			saved.Store(string(data))
			submits.Add(1)
			fmt.Fprint(w, "saved")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><div>fixture ready</div><textarea id="draft"></textarea><button id="save" onclick="fetch('/save',{method:'POST',body:document.querySelector('#draft').value}).then(()=>document.querySelector('#saved').textContent='Saved '+document.querySelector('#draft').value)">Save</button><div id="saved"></div><button id="media" onclick="document.querySelector('#modal').hidden=false">Media</button><div id="modal" hidden><button id="preview" onclick="fetch('/preview').then(r=>{if(!r.ok)document.querySelector('#error').textContent='Preview failed'})">Preview</button><button id="slow" onclick="fetch('/slow-preview',{signal:AbortSignal.timeout(100)}).catch(()=>document.querySelector('#error').textContent='Preview timed out')">Slow</button><div id="error"></div><button id="back" onclick="document.querySelector('#modal').hidden=true">Back</button></div>`)
	}))
	draft := "Prepared project text"
	f.must("browser_interact", map[string]any{"action": "fill", "selector": "#draft", "text": draft, "request_key": "draft"})
	for _, tc := range []struct{ button, text string }{{"#preview", "Preview failed"}, {"#slow", "Preview timed out"}} {
		f.must("browser_interact", map[string]any{"action": "click", "selector": "#media", "request_key": "open" + tc.button})
		result := f.must("browser_interact", map[string]any{"action": "click", "selector": tc.button, "verify_selector": "#error", "verify_text": tc.text, "verify_timeout_seconds": 3, "request_key": tc.button}).(map[string]any)
		if result["confirmation"] != "state_verified" {
			t.Fatal("preview error state not verified")
		}
		f.must("browser_snapshot", map[string]any{"container_selector": "#modal"})
		f.must("browser_interact", map[string]any{"action": "click", "selector": "#back", "request_key": "back" + tc.button})
		f.must("browser_wait", map[string]any{"condition": "value", "selector": "#draft", "value": draft})
	}
	// Discard the first successful response, then retry exactly the same request.
	args := map[string]any{"action": "click", "selector": "#save", "verify_selector": "#saved", "verify_text": "Saved " + draft, "request_key": "save-once"}
	f.must("browser_interact", args)
	result := f.must("browser_interact", args).(map[string]any)
	f.must("browser_control", map[string]any{"action": "status", "operation_id": result["operation_id"]})
	if submits.Load() != 1 || saved.Load() != draft {
		t.Fatal("lost-response recovery duplicated submission or lost draft")
	}
	diagnostics := f.must("browser_diagnostics", map[string]any{}).(map[string]any)
	encoded := fmt.Sprint(diagnostics["events"])
	if !strings.Contains(encoded, "HTTP 500") || !strings.Contains(encoded, "operation_id") {
		t.Fatalf("preview failure was not diagnosed: %s", encoded)
	}
}

func TestBrowserVerificationFailureNeverReplaysMutation(t *testing.T) {
	f := newBrowserFixture(t, `<button id="submit" onclick="document.querySelector('#count').textContent=String(Number(document.querySelector('#count').textContent)+1)">Save</button><span id="count">0</span>`)
	args := map[string]any{"action": "click", "selector": "#submit", "verify_selector": "#never", "verify_timeout_seconds": 1, "request_key": "unknown-after-dispatch"}
	for i := 0; i < 2; i++ {
		_, err := f.call("browser_interact", args)
		failure := browserErrorCode(t, err, "state_verification_failed")
		if failure.details["verification_error_code"] != "wait_timeout" || failure.details["recovery"] != "verify_outcome" {
			t.Fatalf("ambiguous recovery: %v", failure.details)
		}
	}
	f.must("browser_wait", map[string]any{"condition": "text", "container_selector": "#count", "text": "1"})
	_, err := f.call("browser_interact", map[string]any{"action": "click", "selector": "#submit", "verify_selector": "[", "request_key": "invalid-verification"})
	browserErrorCode(t, err, "invalid_selector")
	f.must("browser_wait", map[string]any{"condition": "text", "container_selector": "#count", "text": "1"})
}
