package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func browserErrorCode(t *testing.T, err error, want string) *codedError {
	t.Helper()
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != want {
		t.Fatalf("error = %v; want code %s", err, want)
	}
	return coded
}

func TestBrowserSafeFailuresSurvivePollingAndRestart(t *testing.T) {
	e, root := testExecutor(t)
	config := browserConfig{Enabled: true, Dir: filepath.Join(root, "browser")}
	b, err := newBrowserManager(e, config)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	op := &browserOperation{ID: browserID("bop_"), KeyHash: commandHash("key"), Status: "failed", At: time.Now()}
	op.fail(&codedError{code: "ambiguous_locator", message: "PAGE_SECRET", details: map[string]any{"matches": 2, "text": "FORM_SECRET"}}, map[string]any{"tab_id": browserID("tab_"), "snapshot_id": browserID("snapshot_")})
	if err := b.saveOperation(op); err != nil {
		t.Fatal(err)
	}
	for _, manager := range []*browserManager{b} {
		loaded, err := manager.loadOperation(op.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = manager.operationResult(loaded)
		failure := browserErrorCode(t, err, "ambiguous_locator")
		if failure.details["operation_id"] != op.ID || failure.details["snapshot_id"] != op.ErrorDetails["snapshot_id"] {
			t.Fatalf("failure lost context: %v", failure.details)
		}
	}
	b.Close()
	restarted, err := newBrowserManager(e, config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	_, err = restarted.control(map[string]any{"action": "status", "operation_id": op.ID})
	failure := browserErrorCode(t, err, "ambiguous_locator")
	encoded := toolResult(json.RawMessage(`1`), nil, failure)
	if !strings.Contains(string(encoded), `"matches":2`) || !strings.Contains(string(encoded), op.ID) || strings.Contains(string(encoded), "SECRET") {
		t.Fatalf("unsafe or incomplete MCP failure: %s", encoded)
	}
	data, err := os.ReadFile(filepath.Join(config.Dir, "operations", op.ID+".json"))
	if err != nil || strings.Contains(string(data), "SECRET") {
		t.Fatalf("unsafe journal: %v", err)
	}
	// Old journal entries contain neither a message nor details.
	_, err = restarted.operationResult(&browserOperation{ID: op.ID, ErrorCode: "outcome_unknown"})
	if !strings.Contains(browserErrorCode(t, err, "outcome_unknown").message, "verify") {
		t.Fatal("old journal not recoverable")
	}
}

func TestBrowserTransportAndDOMFailuresAreBounded(t *testing.T) {
	for message, code := range map[string]string{
		"Local control paused": "access_paused", "Tab left permitted scope": "scope_denied",
		"Cannot find context with specified id": "frame_changed", "UNEXPECTED_PAGE_SECRET": "browser_failed",
	} {
		failure := browserErrorCode(t, browserTransportFailure(message), code)
		if strings.Contains(failure.Error(), "SECRET") {
			t.Fatal("transport leaked raw failure")
		}
	}
	for _, raw := range []string{`{"error":{"code":"unknown_PAGE_SECRET"}}`, `{"error":{"code":"page_evaluation_failed"}}`} {
		failure := browserErrorCode(t, browserEvalResult(json.RawMessage(raw), nil), "page_evaluation_failed")
		if strings.Contains(failure.Error(), "SECRET") {
			t.Fatal("DOM failure leaked raw page text")
		}
	}
}

type browserFixture struct {
	b            *browserManager
	ctx          context.Context
	session, tab string
	t            *testing.T
}

func newBrowserFixture(t *testing.T, html string) *browserFixture {
	t.Helper()
	if os.Getenv("WEBCODEX_BROWSER_TEST") != "1" {
		t.Skip("set WEBCODEX_BROWSER_TEST=1 for local browser fixtures")
	}
	return newBrowserFixtureHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>Fixture</title><div>fixture ready</div>" + html))
	}))
}

func newBrowserFixtureHandler(t *testing.T, handler http.Handler) *browserFixture {
	t.Helper()
	if os.Getenv("WEBCODEX_BROWSER_TEST") != "1" {
		t.Skip("set WEBCODEX_BROWSER_TEST=1 for local browser fixtures")
	}
	e, root := testExecutor(t)
	site := httptest.NewServer(handler)
	t.Cleanup(site.Close)
	b, err := newBrowserManager(e, browserConfig{Enabled: true, Dir: filepath.Join(root, "fixture-browser"), Executable: os.Getenv("WEBCODEX_BROWSER_PATH"), AllowLocal: true, Headless: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	opened, err := b.open(ctx, map[string]any{"mode": "clean"})
	if err != nil {
		t.Fatal(err)
	}
	object := opened.(map[string]any)
	f := &browserFixture{b, ctx, object["browser_session_id"].(string), object["tab_id"].(string), t}
	f.must("browser_navigate", map[string]any{"action": "url", "url": site.URL, "request_key": "fixture-nav"})
	f.must("browser_wait", map[string]any{"condition": "text", "text": "fixture ready"})
	return f
}

func (f *browserFixture) call(name string, args map[string]any) (any, error) {
	f.t.Helper()
	args["browser_session_id"], args["tab_id"] = f.session, f.tab
	// Match the JSON number types used by real tools/call requests.
	encoded, _ := json.Marshal(args)
	if err := json.Unmarshal(encoded, &args); err != nil {
		f.t.Fatal(err)
	}
	result, err := f.b.call(f.ctx, name, args)
	for err == nil {
		object, ok := result.(map[string]any)
		if !ok || object["status"] != "running" {
			break
		}
		select {
		case <-f.ctx.Done():
			f.t.Fatal(f.ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
		result, err = f.b.control(map[string]any{"action": "status", "operation_id": object["operation_id"]})
	}
	if err == nil {
		if image, ok := result.(browserImage); ok {
			assertBrowserOutput(f.t, image.Result)
		} else {
			assertBrowserOutput(f.t, result)
		}
	} else {
		var coded *codedError
		if errors.As(err, &coded) {
			assertBrowserOutput(f.t, map[string]any{"error": map[string]any{"code": coded.code, "message": coded.message, "details": coded.details}})
		}
	}
	return result, err
}

func (f *browserFixture) must(name string, args map[string]any) any {
	f.t.Helper()
	result, err := f.call(name, args)
	if err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	return result
}

func TestBrowserTypedDOMFailures(t *testing.T) {
	f := newBrowserFixture(t, `<select id="month"><option value="10">October</option></select><button class="dup">Save</button><button class="dup">Save</button><button id="disabled" disabled>Disabled</button><button id="hidden" style="display:none">Hidden</button><button id="covered" style="position:fixed;left:0;top:200px">Covered</button><div style="position:fixed;left:0;top:190px;width:300px;height:100px;background:red"></div><button id="remove" onclick="document.querySelector('#month').remove()">Remove</button>`)
	for index, tc := range []struct{ action, selector, value, code string }{
		{"select", "#month", "October", "invalid_option_value"},
		{"click", "#missing", "", "element_not_found"}, {"click", ".dup", "", "ambiguous_locator"},
		{"click", "#disabled", "", "element_disabled"}, {"click", "#hidden", "", "element_hidden"},
		{"click", "#covered", "", "element_obscured"}, {"click", "[", "", "invalid_selector"},
	} {
		_, err := f.call("browser_interact", map[string]any{"action": tc.action, "selector": tc.selector, "value": tc.value, "select_by": "value", "request_key": string(rune('a' + index))})
		failure := browserErrorCode(t, err, tc.code)
		if failure.details["tab_id"] != f.tab || failure.details["operation_id"] == "" {
			t.Fatalf("missing canonical context: %v", failure.details)
		}
	}
	snapshot := f.must("browser_snapshot", map[string]any{}).(map[string]any)
	f.must("browser_snapshot", map[string]any{})
	_, err := f.call("browser_interact", map[string]any{"action": "click", "element_id": "0", "snapshot_id": snapshot["snapshot_id"], "request_key": "old-snapshot"})
	browserErrorCode(t, err, "stale_snapshot")
	clicked := f.must("browser_interact", map[string]any{"action": "click", "selector": "#remove", "request_key": "remove"}).(map[string]any)
	if clicked["confirmation"] != "action_dispatched" {
		t.Fatal("click misrepresented as verified site state")
	}
	tab := f.b.sessions[f.session].tabs[f.tab]
	err = f.b.eval(f.ctx, tab, "", `(()=>{throw Error('PAGE_SECRET')})()`, nil)
	failure := browserErrorCode(t, err, "page_evaluation_failed")
	if strings.Contains(failure.Error(), "SECRET") {
		t.Fatal("page exception leaked")
	}
	tab.eventMu.Lock()
	for id := range tab.worlds {
		tab.worlds[id] = 2147483646
	}
	tab.eventMu.Unlock()
	err = f.b.eval(f.ctx, tab, "", `true`, nil)
	browserErrorCode(t, err, "frame_changed")
	if err := f.b.eval(f.ctx, tab, "", `true`, nil); err != nil {
		t.Fatalf("execution context did not recover: %v", err)
	}
}
