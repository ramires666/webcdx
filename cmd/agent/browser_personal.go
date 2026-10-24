package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func (b *browserManager) openPersonal(ctx context.Context, args map[string]any) (any, error) {
	reuseOnly, _, err := optionalBool(args, "reuse_only")
	if err != nil {
		return nil, err
	}
	exactURL, _ := optionalString(args, "exact_url")
	requestedURL, _ := optionalString(args, "url")
	if reuseOnly && exactURL == "" {
		exactURL = requestedURL
	}
	windowID, err := optionalInt(args, "window_id", 0, 0, 2147483647)
	if err != nil {
		return nil, err
	}
	selectionArgs := args
	if exactURL != "" {
		reuseOnly = true
		canonical, err := canonicalBrowserExistingURL(exactURL)
		if err != nil {
			return nil, toolErr("invalid_arguments", "exact_url must be an HTTP/HTTPS URL without credentials")
		}
		if requestedURL != "" {
			other, err := canonicalBrowserExistingURL(requestedURL)
			if err != nil || other != canonical {
				return nil, toolErr("invalid_arguments", "url and exact_url must identify the same existing tab")
			}
		}
		if err := b.checkURL(canonical); err != nil {
			return nil, err
		}
		selectionArgs = map[string]any{}
		for key, value := range args {
			selectionArgs[key] = value
		}
		selectionArgs["exact_url"] = canonical
	} else if reuseOnly || windowID != 0 {
		return nil, toolErr("invalid_arguments", "reuse_only and window_id require exact_url or url")
	}
	origin, err := personalTargetOrigin(args)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	link, err := b.choosePersonal(selectionArgs, origin)
	if err != nil {
		b.mu.Unlock()
		return nil, err
	}
	link.mu.Lock()
	available := len(link.tabs)
	paused := link.paused
	link.mu.Unlock()
	if available == 0 && !paused {
		b.mu.Unlock()
		if reuseOnly {
			return nil, toolErr("tab_not_found", "the requested existing tab is not locally approved")
		}
		if err := link.call(ctx, 0, "WebCodex.prepare", map[string]any{}, nil); err != nil {
			return nil, err
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
		active := 0
		for id, session := range b.sessions {
			if session.disconnected {
				delete(b.sessions, id)
			} else {
				active++
			}
		}
		if active >= 4 {
			b.mu.Unlock()
			return nil, toolErr("limit_reached", "at most four browser sessions")
		}
		root, cancel := context.WithCancel(context.Background())
		s = &browserSession{id: browserID("browser_"), mode: "personal", ctx: root, personal: link, tabs: map[string]*browserTab{}}
		s.cancel = func() { cancel(); _ = link.send(browserBridgeMessage{Kind: "control", Action: "detach"}) }
		b.sessions[s.id] = s
	}
	epoch := s.workEpoch
	b.mu.Unlock()
	browserSetPhase(ctx, 1)
	release, err := s.work.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	defer s.publishReadCatalog()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	browserSetPhase(ctx, 2)
	b.mu.Lock()
	if epoch != s.workEpoch {
		b.mu.Unlock()
		return nil, context.Canceled
	}
	if s.paused || s.disconnected {
		b.mu.Unlock()
		return nil, toolErr("access_paused", "browser session paused or disconnected")
	}
	if s.boundOnly && !reuseOnly {
		b.mu.Unlock()
		return nil, toolErr("binding_changed", "reuse the exact existing target explicitly before changing this session")
	}
	callCtx, cancel := context.WithCancel(ctx)
	s.active = cancel
	b.mu.Unlock()
	defer func() { cancel(); b.mu.Lock(); s.active = nil; b.mu.Unlock() }()
	ctx = callCtx
	ctx = link.bindAccess(ctx)
	approved := b.syncPersonalTabs(s)
	var choice browserExistingChoice
	if reuseOnly {
		choice, err = chooseBrowserExistingTab(approved, exactURL, windowID)
		if err != nil {
			return nil, err
		}
		for id, tab := range s.tabs {
			if tab.nativeTabID == choice.Tab.ID {
				s.selected = id
				break
			}
		}
	} else if origin != "" {
		matches := []int{}
		for _, tab := range approved {
			if strings.ToLower(browserDiagnosticURL(tab.URL)) == origin {
				matches = append(matches, tab.ID)
			}
		}
		if len(matches) == 0 {
			return nil, toolErr("browser_not_found", "target origin is not in the approved tab catalog")
		}
		if len(matches) > 1 {
			return nil, toolErr("ambiguous_tab", "multiple tabs match target_origin; open the profile without target_origin and explicitly select from browser_tabs")
		}
		for id, tab := range s.tabs {
			if tab.nativeTabID == matches[0] {
				s.selected = id
				break
			}
		}
	}
	if s.selected == "" {
		return nil, toolErr("waiting_user", "open a permitted website in the connected browser")
	}
	browserConsoleBind(ctx, s, s.tabs[s.selected])
	var proof map[string]any
	if reuseOnly {
		proof, err = b.verifyExistingBinding(ctx, s, s.tabs[s.selected], choice)
		if err != nil {
			return nil, err
		}
		s.boundOnly = true
	} else if raw, _ := optionalString(args, "url"); raw != "" {
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
	link.mu.Lock()
	profile, alias, product, connection := link.profileID, link.alias, link.product, link.id
	link.mu.Unlock()
	if product == "" {
		product = "unknown"
	}
	result := map[string]any{"browser_session_id": s.id, "tab_id": s.selected, "mode": "personal", "status": "ready", "truncated": false, "profile_id": profile, "browser_alias": alias, "product": product, "connection_id": connection, "binding_verified": false, "readable": false, "auth_state": "unverified"}
	for key, value := range proof {
		result[key] = value
	}
	return result, nil
}

// Catalog permitted tabs without attaching debuggers or reading page contents.
// The extension attaches a debugger only when a tool actually uses that tab.
func (b *browserManager) syncPersonalTabs(s *browserSession) []browserPersonalTab {
	defer s.publishReadCatalog()
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
		if args["focus_policy"] == "activate" {
			if err := t.command(ctx, "Page.bringToFront", map[string]any{}, nil); err != nil {
				return nil, err
			}
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
	nativeTabs := make(map[int]browserPersonalTab, len(s.personal.tabs))
	for _, native := range s.personal.tabs {
		nativeTabs[native.ID] = native
	}
	s.personal.mu.Unlock()
	list := []map[string]any{}
	for id, tab := range s.tabs {
		if tab.ctx.Err() != nil {
			delete(s.tabs, id)
			continue
		}
		native, ok := nativeTabs[tab.nativeTabID]
		if !ok {
			continue
		}
		list = append(list, map[string]any{"tab_id": id, "url": safeBrowserURL(native.URL), "title": native.Title, "window_id": native.WindowID, "active": native.Active})
	}
	return map[string]any{"browser_session_id": s.id, "tab_id": s.selected, "status": "ready", "tabs": list, "truncated": false}, nil
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
		return saveBrowserDownloadedFile(resolved, destination, id)
	}
	return nil, toolErr("download_not_ready", "personal download has not completed")
}
