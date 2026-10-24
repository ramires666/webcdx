package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"
)

type browserConsoleContextKey struct{}

// One trace belongs to the operation goroutine, including work that outlives
// the caller's RPC wait. It never fetches a page solely for logging.
type browserConsoleTrace struct {
	info    browserConsoleInfo
	started time.Time
	tab     *browserTab
}

func browserConsoleRequest(raw []byte) bool {
	var request struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(raw, &request) != nil || request.Method != "tools/call" || contractVersion(request.Params) != "v5" {
		return false
	}
	var call struct {
		Name string `json:"name"`
	}
	return json.Unmarshal(request.Params, &call) == nil && strings.HasPrefix(call.Name, "browser_")
}

func newBrowserConsoleTrace(name string, args map[string]any) *browserConsoleTrace {
	get := func(key string) string { value, _ := args[key].(string); return value }
	id := ""
	if name == "browser_control" {
		id = get("operation_id")
	}
	if id == "" {
		id = browserID("bop_")
	}
	trace := &browserConsoleTrace{started: time.Now(), info: browserConsoleInfo{
		Name: name, Args: args, ID: id, SessionID: get("browser_session_id"), TabID: get("tab_id"),
		URL: get("url"), Profile: get("browser_alias"),
	}}
	log.Print(browserConsoleStart(trace.info))
	return trace
}

func browserConsoleBind(ctx context.Context, session *browserSession, tab *browserTab) {
	trace, _ := ctx.Value(browserConsoleContextKey{}).(*browserConsoleTrace)
	if trace == nil {
		return
	}
	trace.tab = tab
	trace.info.SessionID, trace.info.TabID = session.id, tab.id
	tab.eventMu.Lock()
	if tab.pageTitle != "" {
		trace.info.Title = tab.pageTitle
	}
	if tab.pageURL != "" {
		trace.info.URL = tab.pageURL
	}
	tab.eventMu.Unlock()
	if session.personal != nil {
		session.personal.mu.Lock()
		trace.info.Profile = session.personal.alias
		for _, native := range session.personal.tabs {
			if native.ID == tab.nativeTabID {
				trace.info.Title, trace.info.URL = native.Title, native.URL
				trace.info.WindowID = native.WindowID
				break
			}
		}
		session.personal.mu.Unlock()
	}
	if target := browserConsoleContext(trace.info); target != "" {
		log.Printf("  [%s] %s", browserConsoleShortID(trace.info), target)
	}
}

func (trace *browserConsoleTrace) finish(value any, err error, status, code string) {
	result, _ := value.(map[string]any)
	if image, ok := value.(browserImage); ok {
		result = image.Result
	}
	if window, ok := browserConsoleNumber(result["window_id"]); ok {
		trace.info.WindowID = int(window)
	}
	for key, field := range map[string]*string{
		"browser_session_id": &trace.info.SessionID, "tab_id": &trace.info.TabID,
		"title": &trace.info.Title, "url": &trace.info.URL,
	} {
		if text, ok := result[key].(string); ok && text != "" {
			*field = text
		}
	}
	for _, raw := range browserConsoleSlice(result["tabs"]) {
		if tab, ok := raw.(map[string]any); ok && tab["tab_id"] == trace.info.TabID {
			trace.info.URL, _ = tab["url"].(string)
			trace.info.Title, _ = tab["title"].(string)
			break
		}
	}
	if trace.tab != nil && trace.tab.id == trace.info.TabID && trace.info.Name != "browser_diagnostics" && trace.info.Args["frame_id"] == nil {
		trace.tab.eventMu.Lock()
		trace.tab.pageURL, trace.tab.pageTitle = trace.info.URL, trace.info.Title
		trace.tab.eventMu.Unlock()
	}
	if status == "" || status == "completed" {
		if actual, ok := result["status"].(string); ok {
			status = actual
		}
	}
	if err != nil && code == "" {
		var failure *codedError
		if errors.As(err, &failure) {
			code = failure.code
		} else {
			code = "browser_failed"
		}
	}
	log.Print(browserConsoleFinish(trace.info, result, status, code, time.Since(trace.started)))
}

func (trace *browserConsoleTrace) replay(id string) {
	previous := browserConsoleInfo{ID: id}
	log.Printf("  [%s] Использую сохранённую операцию [%s]; действие повторно не запускается", browserConsoleShortID(trace.info), browserConsoleShortID(previous))
}

func browserConsolePending(info browserConsoleInfo, result map[string]any) {
	log.Print(browserConsoleFinish(info, result, "running", "", time.Second))
}
