package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

func (b *browserManager) openPersonal(ctx context.Context, args map[string]any) (any, error) {
	b.mu.Lock()
	link := b.bridge
	if link == nil {
		b.mu.Unlock()
		return nil, toolErr("waiting_user", "connect a tab using the WebCodex extension")
	}
	link.mu.Lock()
	available := len(link.tabs)
	paused := link.paused
	link.mu.Unlock()
	if available == 0 && !paused {
		b.mu.Unlock()
		if err := link.call(ctx, 0, "WebCodex.prepare", map[string]any{}, nil); err != nil {
			return nil, toolErr("waiting_user", "enable permanent access or connect a website tab in the extension")
		}
		link.mu.Lock()
		available = len(link.tabs)
		link.mu.Unlock()
		if available == 0 {
			return nil, toolErr("waiting_user", "open a permitted website in the connected browser")
		}
		return b.openPersonal(ctx, args)
	}
	if available == 0 || paused {
		b.mu.Unlock()
		return nil, toolErr("waiting_user", "select and unpause a tab locally in the extension")
	}
	var s *browserSession
	for _, existing := range b.sessions {
		if existing.personal == link && !existing.disconnected {
			s = existing
			break
		}
	}
	if s == nil {
		root, cancel := context.WithCancel(context.Background())
		s = &browserSession{id: browserID("browser_"), mode: "personal", ctx: root, personal: link, tabs: map[string]*browserTab{}}
		s.cancel = func() { cancel(); _ = link.send(browserBridgeMessage{Kind: "control", Action: "detach"}) }
		b.sessions[s.id] = s
	}
	b.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	b.syncPersonalTabs(s)
	if s.selected == "" {
		return nil, toolErr("waiting_user", "open a permitted website in the connected browser")
	}
	if raw, _ := optionalString(args, "url"); raw != "" {
		if err := b.checkURL(raw); err != nil {
			return nil, err
		}
		t := s.tabs[s.selected]
		if err := b.setupTab(ctx, t); err != nil {
			return nil, err
		}
		if err := t.command(ctx, "Page.navigate", map[string]any{"url": raw}, nil); err != nil {
			return nil, err
		}
	}
	return map[string]any{"browser_session_id": s.id, "tab_id": s.selected, "mode": "personal", "status": "ready", "truncated": false}, nil
}

// Catalog permitted tabs without attaching debuggers or reading page contents.
// The extension attaches a debugger only when a tool actually uses that tab.
func (b *browserManager) syncPersonalTabs(s *browserSession) []browserPersonalTab {
	s.personal.mu.Lock()
	defer s.personal.mu.Unlock()
	approved := append([]browserPersonalTab{}, s.personal.tabs...)
	byNativeID := make(map[int]*browserTab, len(s.tabs))
	for _, tab := range s.tabs {
		byNativeID[tab.nativeTabID] = tab
	}
	valid := make(map[int]bool, len(approved))
	for _, native := range approved {
		valid[native.ID] = true
		if tab := byNativeID[native.ID]; tab != nil && tab.ctx.Err() == nil {
			continue
		}
		child, cancel := context.WithCancel(s.ctx)
		tab := &browserTab{id: browserID("tab_"), ctx: child, cancel: cancel, personal: s.personal, nativeTabID: native.ID, worlds: map[string]int{}}
		s.tabs[tab.id] = tab
		s.personal.attached[native.ID] = tab
	}
	for id, tab := range s.tabs {
		if !valid[tab.nativeTabID] || tab.ctx.Err() != nil {
			tab.cancel()
			delete(s.tabs, id)
			if s.personal.attached[tab.nativeTabID] == tab {
				delete(s.personal.attached, tab.nativeTabID)
			}
		}
	}
	if s.tabs[s.selected] == nil {
		s.selected = ""
		for _, native := range approved {
			for id, tab := range s.tabs {
				if tab.nativeTabID == native.ID {
					s.selected = id
					return approved
				}
			}
		}
	}
	return approved
}

func (b *browserManager) personalTabs(ctx context.Context, s *browserSession, t *browserTab, args map[string]any) (any, error) {
	action, _ := requiredString(args, "action")
	switch action {
	case "new":
		if len(s.tabs) >= browserPersonalTabLimit {
			return nil, toolErr("limit_reached", "personal browser tab limit reached")
		}
		s.personal.mu.Lock()
		allowed := s.personal.allowNew
		s.personal.mu.Unlock()
		if !allowed {
			return map[string]any{"browser_session_id": s.id, "status": "waiting_user", "reason": "allow_new_tabs_in_extension", "truncated": false}, nil
		}
		var result browserPersonalTab
		if err := s.personal.call(ctx, t.nativeTabID, "WebCodex.newTab", map[string]any{}, &result); err != nil {
			return nil, err
		}
		child, cancel := context.WithCancel(s.ctx)
		tab := &browserTab{id: browserID("tab_"), ctx: child, cancel: cancel, personal: s.personal, nativeTabID: result.ID, worlds: map[string]int{}}
		if err := b.setupTab(ctx, tab); err != nil {
			cancel()
			return nil, err
		}
		s.tabs[tab.id] = tab
		s.selected = tab.id
	case "select":
		if err := b.setupTab(ctx, t); err != nil {
			return nil, err
		}
		if err := t.command(ctx, "Page.bringToFront", map[string]any{}, nil); err != nil {
			return nil, err
		}
		s.selected = t.id
	case "close":
		if len(s.tabs) == 1 {
			return nil, toolErr("last_tab", "disconnect personal to preserve the last tab")
		}
		if err := s.personal.call(ctx, t.nativeTabID, "WebCodex.closeTab", map[string]any{}, nil); err != nil {
			return nil, err
		}
		t.cancel()
		delete(s.tabs, t.id)
		if s.selected == t.id {
			for id := range s.tabs {
				s.selected = id
				break
			}
		}
	case "list":
	default:
		return nil, toolErr("invalid_arguments", "unknown tabs action")
	}
	s.personal.mu.Lock()
	urls := make(map[int]string, len(s.personal.tabs))
	for _, native := range s.personal.tabs {
		urls[native.ID] = native.URL
	}
	s.personal.mu.Unlock()
	list := []map[string]any{}
	for id, tab := range s.tabs {
		if tab.ctx.Err() != nil {
			delete(s.tabs, id)
			continue
		}
		url, ok := urls[tab.nativeTabID]
		if !ok {
			continue
		}
		list = append(list, map[string]any{"tab_id": id, "url": safeBrowserURL(url)})
	}
	return map[string]any{"browser_session_id": s.id, "tab_id": s.selected, "status": "ready", "tabs": list, "truncated": false}, nil
}

func (b *browserManager) recordBrowserEvent(t *browserTab, method string, event map[string]any) {
	t.eventMu.Lock()
	defer t.eventMu.Unlock()
	switch method {
	case "Page.frameNavigated", "Page.navigatedWithinDocument", "WebCodex.detached":
		t.snapshot = ""
		t.worlds = map[string]int{}
	case "Page.javascriptDialogOpening":
		t.dialog = true
	case "Page.javascriptDialogClosed":
		t.dialog = false
	case "Runtime.exceptionThrown", "Runtime.consoleAPICalled", "Network.loadingFailed", "Network.responseReceived":
		response, _ := event["response"].(map[string]any)
		status, _ := response["status"].(float64)
		if method == "Runtime.consoleAPICalled" && event["type"] != "error" && event["type"] != "warning" || method == "Network.responseReceived" && status < 400 {
			return
		}
		if len(t.diagnostics) >= 100 {
			t.diagnostics = t.diagnostics[1:]
		}
		entry := map[string]any{"event": method, "at": time.Now().UTC().Format(timeFormat)}
		if raw, ok := response["url"].(string); ok {
			entry["url"] = safeBrowserURL(raw)
		}
		if status >= 400 {
			entry["detail"] = fmt.Sprintf("HTTP %.0f", status)
		}
		t.diagnostics = append(t.diagnostics, entry)
	}
}

var personalDownloadID = regexp.MustCompile(`^[0-9]{1,20}$`)

func (b *browserManager) personalFiles(ctx context.Context, s *browserSession, t *browserTab, args map[string]any) (any, error) {
	action, _ := requiredString(args, "action")
	if action == "upload" {
		return b.upload(ctx, t, args)
	}
	if action == "enable_downloads" {
		return map[string]any{}, s.personal.call(ctx, t.nativeTabID, "WebCodex.enableDownloads", map[string]any{"session_id": s.id}, nil)
	}
	var downloads []struct {
		ID       string `json:"download_id"`
		Status   string `json:"status"`
		Bytes    int64  `json:"bytes"`
		Filename string `json:"filename"`
	}
	if err := s.personal.call(ctx, t.nativeTabID, "WebCodex.downloads", map[string]any{}, &downloads); err != nil {
		return nil, err
	}
	if action == "downloads" {
		list := []browserDownload{}
		for _, d := range downloads {
			list = append(list, browserDownload{d.ID, d.Status, d.Bytes})
		}
		return map[string]any{"downloads": list}, nil
	}
	if action != "save_download" {
		return nil, toolErr("invalid_arguments", "unknown files action")
	}
	id, err := requiredString(args, "download_id")
	if err != nil {
		return nil, err
	}
	if !personalDownloadID.MatchString(id) {
		return nil, toolErr("invalid_arguments", "invalid download_id")
	}
	for _, download := range downloads {
		if download.ID != id || download.Status != "complete" {
			continue
		}
		root := b.config.DownloadDir
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			root = filepath.Join(home, "Downloads")
		}
		expected := filepath.Join(root, "WebCodex", s.id, id)
		resolved, err := resolveExistingPrefix(download.Filename)
		if err != nil {
			return nil, err
		}
		relative, relErr := filepath.Rel(expected, resolved)
		if relErr != nil || relative != "." {
			return nil, toolErr("invalid_path", "download is outside the configured personal download directory")
		}
		raw, err := requiredString(args, "path")
		if err != nil {
			return nil, err
		}
		destination, err := b.executor.resolvePath(raw)
		if err != nil {
			return nil, err
		}
		data, info, err := readRegularFile(resolved, 64<<20)
		if err != nil {
			return nil, err
		}
		if err := atomicCreate(destination, data); err != nil {
			return nil, err
		}
		return map[string]any{"path": destination, "download_id": id, "bytes": info.Size()}, nil
	}
	return nil, toolErr("download_not_ready", "personal download has not completed")
}
