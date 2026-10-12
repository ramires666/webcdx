package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserContractAndURLScope(t *testing.T) {
	e, _ := testExecutor(t)
	if _, err := e.executeTool(context.Background(), "v5", "browser_open", map[string]any{}); err == nil {
		t.Fatal("disabled browser accepted direct call")
	}
	b := e.browser
	for _, url := range []string{"file:///C:/secret", "javascript:alert(1)", "chrome://settings", "https://user:pass@example.com", "http://127.0.0.1", "http://172.16.1.1", "http://[::1]", "http://[fd00::1]"} {
		if b.checkURL(url) == nil {
			t.Fatalf("accepted forbidden URL %s", url)
		}
	}
	if err := b.checkURL("https://example.com"); err != nil {
		t.Fatal(err)
	}
	for _, tool := range browserToolSchemas() {
		if tool.InputSchema["additionalProperties"] != false || tool.OutputSchema["additionalProperties"] != false {
			t.Fatalf("open schema %s", tool.Name)
		}
	}
	if err := validateBrowserArgs("browser_interact", map[string]any{"browser_session_id": "x", "request_key": "x", "action": "fill"}); err == nil {
		t.Fatal("fill without text accepted")
	}
	if err := validateBrowserArgs("browser_open", map[string]any{"mode": "clean", "request_key": "x", "executable": "malicious"}); err == nil {
		t.Fatal("remote executable accepted")
	}
}

func TestBrowserManagedEndToEnd(t *testing.T) {
	if os.Getenv("WEBCODEX_BROWSER_TEST") != "1" {
		t.Skip("set WEBCODEX_BROWSER_TEST=1 to exercise installed Chrome")
	}
	e, root := testExecutor(t)
	var submits atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/submit":
			submits.Add(1)
			fmt.Fprint(w, "submitted")
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "test", Path: "/", MaxAge: 3600})
			fmt.Fprint(w, "logged in")
		case "/private":
			if _, err := r.Cookie("session"); err != nil {
				w.WriteHeader(401)
				fmt.Fprint(w, "not logged in")
			} else {
				fmt.Fprint(w, "private account")
			}
		case "/frame":
			fmt.Fprint(w, `<button id="inside">Frame button</button>`)
		case "/download":
			w.Header().Set("Content-Disposition", `attachment; filename="report.txt"`)
			fmt.Fprint(w, "test report")
		default:
			fmt.Fprint(w, `<!doctype html><title>Browser test</title><form action="/submit" method="post"><label>Name<input id="name" name="name"></label><button id="submit">Submit</button></form><input type="password" value="PASSWORD_SECRET"><input id="upload" type="file"><a id="download" href="/download">Download</a><button id="popup" onclick="window.open('/frame')">Popup</button><a href="/login" id="login">Login</a><iframe src="/frame"></iframe><div id="shadow"></div><script>document.querySelector('#shadow').attachShadow({mode:'open'}).innerHTML='<button id="shadow-button">Shadow button</button>';console.error('DO_NOT_LOG_SECRET')</script>`)
		}
	}))
	defer site.Close()
	b, err := newBrowserManager(e, browserConfig{Enabled: true, Dir: filepath.Join(root, "browser"), Executable: os.Getenv("WEBCODEX_BROWSER_PATH"), AllowLocal: true, Headless: os.Getenv("WEBCODEX_BROWSER_TEST_VISIBLE") != "1"})
	if err != nil {
		t.Fatal(err)
	}
	e.browser = b
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	call := func(name string, args map[string]any) any {
		result, err := b.call(ctx, name, args)
		for err == nil {
			object, ok := result.(map[string]any)
			if !ok || object["status"] != "running" {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
			result, err = b.call(ctx, "browser_control", map[string]any{"action": "status", "operation_id": object["operation_id"]})
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return result
	}
	opened := call("browser_open", map[string]any{"mode": "persistent", "request_key": "open"}).(map[string]any)
	id := opened["browser_session_id"].(string)
	args := func(fields map[string]any) map[string]any { fields["browser_session_id"] = id; return fields }
	call("browser_navigate", args(map[string]any{"action": "url", "url": site.URL, "request_key": "nav"}))
	snapshot := call("browser_snapshot", args(map[string]any{})).(map[string]any)
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "PASSWORD_SECRET") || !strings.Contains(string(encoded), "Shadow button") {
		t.Fatalf("snapshot secrecy/shadow: %s", encoded)
	}
	frames := snapshot["frames"].([]map[string]any)
	if len(frames) < 2 {
		t.Fatal("iframe not discovered")
	}
	frame := call("browser_snapshot", args(map[string]any{"frame_id": frames[1]["frame_id"]})).(map[string]any)
	if !strings.Contains(frame["text"].(string), "Frame button") {
		t.Fatal("iframe not readable")
	}
	call("browser_interact", args(map[string]any{"action": "fill", "selector": "#name", "text": "Alice", "request_key": "fill"}))
	file := filepath.Join(root, "upload.txt")
	if err := os.WriteFile(file, []byte("upload contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	call("browser_files", args(map[string]any{"action": "upload", "selector": "#upload", "path": file, "request_key": "upload"}))
	call("browser_files", args(map[string]any{"action": "enable_downloads", "request_key": "enable-downloads"}))
	call("browser_interact", args(map[string]any{"action": "click", "selector": "#download", "request_key": "download-click"}))
	var downloads []browserDownload
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		list := call("browser_files", args(map[string]any{"action": "downloads"})).(map[string]any)
		downloads = list["downloads"].([]browserDownload)
		if len(downloads) > 0 && downloads[0].Status == "completed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(downloads) == 0 || downloads[0].Status != "completed" {
		t.Fatal("download did not complete")
	}
	destination := filepath.Join(root, "report.txt")
	call("browser_files", args(map[string]any{"action": "save_download", "download_id": downloads[0].ID, "path": destination, "request_key": "save"}))
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "test report" {
		t.Fatalf("download: %s %v", data, err)
	}
	if _, err := b.call(ctx, "browser_files", args(map[string]any{"action": "save_download", "download_id": downloads[0].ID, "path": destination, "request_key": "save-overwrite"})); err == nil {
		t.Fatal("download overwrote existing file")
	}
	call("browser_interact", args(map[string]any{"action": "click", "selector": "#popup", "request_key": "popup"}))
	tabs := call("browser_tabs", args(map[string]any{"action": "list"})).(map[string]any)
	if len(tabs["tabs"].([]map[string]any)) < 2 {
		t.Fatal("popup was not discovered")
	}
	call("browser_control", args(map[string]any{"action": "pause"}))
	if _, err := b.call(ctx, "browser_snapshot", args(map[string]any{})); err == nil {
		t.Fatal("paused browser still accepted operations")
	}
	call("browser_control", args(map[string]any{"action": "resume"}))
	submit := args(map[string]any{"action": "click", "selector": "#submit", "request_key": "submit-once"})
	call("browser_interact", submit)
	call("browser_interact", submit)
	call("browser_wait", args(map[string]any{"condition": "text", "text": "submitted"}))
	if submits.Load() != 1 {
		t.Fatalf("Retry repeated submit %d", submits.Load())
	}
	if _, err := b.call(ctx, "browser_interact", args(map[string]any{"action": "click", "element_id": "0", "snapshot_id": snapshot["snapshot_id"], "request_key": "stale"})); err == nil {
		t.Fatal("stale element accepted")
	}
	image := call("browser_screenshot", args(map[string]any{})).(browserImage)
	rpc := toolResult(json.RawMessage(`1`), image, nil)
	if !strings.Contains(string(rpc), `"type":"image"`) || len(rpc) > 2<<20 {
		t.Fatal("invalid MCP image envelope")
	}
	call("browser_navigate", args(map[string]any{"action": "url", "url": site.URL + "/login", "request_key": "login"}))
	call("browser_control", args(map[string]any{"action": "disconnect"}))
	second := call("browser_open", map[string]any{"mode": "persistent", "request_key": "open-again"}).(map[string]any)
	id = second["browser_session_id"].(string)
	call("browser_navigate", args(map[string]any{"action": "url", "url": site.URL + "/private", "request_key": "private"}))
	private := call("browser_snapshot", args(map[string]any{})).(map[string]any)
	if !strings.Contains(private["text"].(string), "private account") {
		t.Fatal("persistent login lost after reopening")
	}
}
