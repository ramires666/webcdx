package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

type browserConfig struct {
	Enabled              bool
	Executable, Dir      string
	AllowLocal, Headless bool
	Personal             bool
	BridgePath           string
	DownloadDir          string
}

type browserManager struct {
	mu          sync.Mutex
	config      browserConfig
	executor    *nativeExecutor
	sessions    map[string]*browserSession
	operations  map[string]*browserOperation
	journal     map[string]browserJournalEntry
	cleanupStop context.CancelFunc
	cleanupDone chan struct{}
	closed      bool
	listener    *browserIPCListener
	bridge      *browserBridge
	bridges     map[string]*browserBridge
	bridgePeers map[io.ReadWriteCloser]bool
	bridgeWG    sync.WaitGroup
	bridgePath  string
	artifactMu  sync.Mutex
}

type browserSession struct {
	mu                   sync.Mutex
	id, mode, profile    string
	ctx                  context.Context
	cancel               context.CancelFunc
	tabs                 map[string]*browserTab
	selected             string
	paused, disconnected bool
	active               context.CancelFunc
	personal             *browserBridge
	downloadMu           sync.Mutex
	downloads            map[string]*browserDownload
}

type browserTab struct {
	id                      string
	ctx                     context.Context
	cancel                  context.CancelFunc
	eventMu                 sync.Mutex
	worlds                  map[string]int
	snapshot, snapshotFrame string
	snapshotMain            bool
	diagnostics             []map[string]any
	diagnosticsDropped      int
	requests                map[string]browserRequestTrace
	lastOperation           string
	dialog                  bool
	blocked                 bool
	personal                *browserBridge
	nativeTabID             int
	personalReady           bool
	frameContexts           map[string]context.Context
}

type browserOperation struct {
	ID             string         `json:"operation_id"`
	KeyHash        string         `json:"key_hash,omitempty"`
	Hash           string         `json:"hash,omitempty"`
	Status         string         `json:"status"`
	At             time.Time      `json:"at"`
	Result         map[string]any `json:"result,omitempty"`
	ErrorCode      string         `json:"error_code,omitempty"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	ErrorDetails   map[string]any `json:"error_details,omitempty"`
	done           chan struct{}
	cancel         context.CancelFunc
	image          string
	errorTransient map[string]any
}

type browserOperationContext struct{}

type browserImage struct {
	Result map[string]any
	Data   string
}

func browserID(prefix string) string {
	id, err := randomSessionID()
	if err != nil {
		panic(err)
	}
	return prefix + strings.TrimPrefix(id, "proc_")
}

func newBrowserManager(e *nativeExecutor, config browserConfig) (*browserManager, error) {
	b := &browserManager{config: config, executor: e, sessions: map[string]*browserSession{}, operations: map[string]*browserOperation{}, journal: map[string]browserJournalEntry{}}
	if !config.Enabled {
		return b, nil
	}
	if config.Dir == "" {
		return nil, errors.New("browser data directory required")
	}
	for _, sub := range []string{"profiles", "operations", "artifacts"} {
		if err := os.MkdirAll(filepath.Join(config.Dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	if err := privateBrowserDirectory(config.Dir); err != nil {
		return nil, err
	}
	if _, err := b.cleanArtifacts(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(config.Dir, "operations"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "bop_") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(config.Dir, "operations", entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var op browserOperation
		if json.Unmarshal(data, &op) != nil {
			return nil, errors.New("invalid browser operation journal")
		}
		if !browserOperationID.MatchString(op.ID) || entry.Name() != op.ID+".json" || op.KeyHash == "" {
			return nil, errors.New("invalid browser operation journal")
		}
		if time.Since(op.At) > browserOperationTTL {
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			continue
		}
		if op.Status == "running" {
			op.Status = "failed"
			op.ErrorCode = "outcome_unknown"
		}
		op.done = make(chan struct{})
		close(op.done)
		b.operations[op.ID] = &op
		b.journal[op.KeyHash] = browserJournalEntry{ID: op.ID, At: op.At}
		b.trimOperations(browserOperationCacheMax)
	}
	if config.Personal {
		path := config.BridgePath
		if path == "" {
			path = browserBridgePath()
		}
		if err := b.startBridge(path); err != nil {
			return nil, err
		}
	}
	b.startOperationCleanup()
	return b, nil
}

func (b *browserManager) saveOperation(op *browserOperation) error {
	if op.KeyHash == "" {
		return nil
	}
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(b.config.Dir, "operations", op.ID+".json"), data, false); err != nil {
		return err
	}
	b.journal[op.KeyHash] = browserJournalEntry{ID: op.ID, At: op.At}
	return nil
}

func browserMutation(name string, args map[string]any) bool {
	return name == "browser_open" || name == "browser_navigate" || name == "browser_interact" || name == "browser_files" && args["action"] != "downloads" || name == "browser_tabs" && args["action"] != "list"
}

func (b *browserManager) call(ctx context.Context, name string, args map[string]any) (any, error) {
	if ctx.Err() != nil {
		return nil, toolErr("cancelled", "browser request already expired")
	}
	if !b.config.Enabled {
		return nil, toolErr("browser_disabled", "enable WEBCODEX_BROWSER_ENABLED locally")
	}
	if err := validateBrowserArgs(name, args); err != nil {
		return nil, err
	}
	if name == "browser_control" {
		return b.control(args)
	}
	keyHash := ""
	if browserMutation(name, args) {
		key, err := requiredString(args, "request_key")
		if err != nil || len(key) > 128 {
			return nil, toolErr("invalid_arguments", "request_key must contain 1 to 128 characters")
		}
		keyHash = commandHash(key)
	}
	hash := commandHash(map[string]any{"name": name, "arguments": args})
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, toolErr("disconnected", "browser manager closed")
	}
	if keyHash != "" {
		entry, ok := b.journal[keyHash]
		if ok && time.Since(entry.At) > browserOperationTTL {
			if err := b.expireOperations(time.Now()); err != nil {
				b.mu.Unlock()
				return nil, toolErr("io_error", "expire browser retry journal", err)
			}
			entry, ok = b.journal[keyHash]
		}
		if ok {
			op, err := b.loadOperation(entry.ID)
			if err != nil {
				b.mu.Unlock()
				return nil, toolErr("io_error", "read browser retry journal", err)
			}
			if op.Hash != hash {
				b.mu.Unlock()
				return nil, toolErr("request_key_conflict", "key was used with different arguments")
			}
			b.mu.Unlock()
			return b.await(ctx, op)
		}
	}
	running := 0
	for _, op := range b.operations {
		if op.Status == "running" {
			running++
		}
	}
	if running >= browserOperationRunningMax {
		b.mu.Unlock()
		return nil, toolErr("limit_reached", "128 browser operations are running; wait for an operation to finish")
	}
	b.trimOperations(browserOperationCacheMax - 1)
	opCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	op := &browserOperation{ID: browserID("bop_"), KeyHash: keyHash, Hash: hash, Status: "running", At: time.Now().UTC(), done: make(chan struct{}), cancel: cancel}
	if err := b.saveOperation(op); err != nil {
		b.mu.Unlock()
		cancel()
		return nil, toolErr("io_error", "reserve browser operation", err)
	}
	b.operations[op.ID] = op
	b.mu.Unlock()
	go func() {
		defer cancel()
		result, err := b.execute(context.WithValue(opCtx, browserOperationContext{}, op.ID), name, args)
		b.mu.Lock()
		op.Status = "completed"
		if err != nil {
			op.Status = "failed"
			op.fail(err, args)
			if opCtx.Err() != nil {
				op.Status = "cancelled"
				op.ErrorCode = "cancelled"
				if keyHash != "" {
					op.ErrorCode = "outcome_unknown"
				}
				op.ErrorMessage = browserFailureMessage(op.ErrorCode)
			}
		} else if img, ok := result.(browserImage); ok {
			op.Result, op.image = img.Result, img.Data
		} else {
			op.Result, _ = result.(map[string]any)
		}
		if op.Result != nil {
			op.Result["operation_id"] = op.ID
		}
		if err := b.saveOperation(op); err != nil {
			op.Status = "failed"
			op.ErrorCode = "outcome_unknown"
			op.ErrorMessage = browserFailureMessage(op.ErrorCode)
		}
		if b.executor.verbose {
			log.Printf("[browser operation=%s] %s status=%s error_code=%s", op.ID, name, op.Status, op.ErrorCode)
		} else if op.Status == "failed" || op.ErrorCode != "" {
			log.Printf("⚠ %s: %s", strings.TrimSpace(describeToolCall(name, args)), readableError(map[string]any{"code": op.ErrorCode}))
		}
		close(op.done)
		b.mu.Unlock()
	}()
	return b.await(ctx, op)
}

func (b *browserManager) await(ctx context.Context, op *browserOperation) (any, error) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-op.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.operationResult(op)
}

func (b *browserManager) operationResult(op *browserOperation) (any, error) {
	if op.Status == "running" {
		return map[string]any{"operation_id": op.ID, "status": "running", "truncated": false}, nil
	}
	if op.ErrorCode != "" {
		message := op.ErrorMessage
		if message == "" {
			message = browserFailureMessage(op.ErrorCode)
		}
		details := map[string]any{"operation_id": op.ID}
		for key, value := range op.ErrorDetails {
			details[key] = value
		}
		for key, value := range op.errorTransient {
			details[key] = value
		}
		return nil, &codedError{code: op.ErrorCode, message: message, details: details}
	}
	if op.image != "" {
		return browserImage{op.Result, op.image}, nil
	}
	return op.Result, nil
}

func (b *browserManager) control(args map[string]any) (any, error) {
	action, _ := requiredString(args, "action")
	if action == "discover" {
		return b.discoverPersonal(), nil
	}
	if id, _ := optionalString(args, "operation_id"); id != "" {
		b.mu.Lock()
		defer b.mu.Unlock()
		op, err := b.loadOperation(id)
		if errors.Is(err, os.ErrNotExist) {
			return nil, toolErr("unknown_operation", "unknown operation_id")
		}
		if err != nil {
			return nil, toolErr("io_error", "read browser operation", err)
		}
		if action == "cancel" {
			if op.cancel != nil {
				op.cancel()
			}
			return map[string]any{"operation_id": id, "status": op.Status, "truncated": false}, nil
		}
		if action != "status" {
			return nil, toolErr("invalid_arguments", "operation supports status or cancel")
		}
		return b.operationResult(op)
	}
	id, err := requiredString(args, "browser_session_id")
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	s := b.sessions[id]
	if s == nil {
		b.mu.Unlock()
		return nil, toolErr("unknown_session", "unknown browser_session_id")
	}
	var closeSession context.CancelFunc
	switch action {
	case "pause":
		s.paused = true
		if s.active != nil {
			s.active()
		}
	case "resume":
		if s.personal != nil {
			s.personal.mu.Lock()
			local := s.personal.localPaused
			s.personal.mu.Unlock()
			if local {
				b.mu.Unlock()
				return nil, toolErr("access_paused", "resume locally in the extension first")
			}
		}
		s.paused = false
	case "waiting_user":
		s.paused = true
		if s.active != nil {
			s.active()
		}
	case "cancel":
		if s.active != nil {
			s.active()
		}
	case "disconnect":
		s.disconnected = true
		if s.active != nil {
			s.active()
		}
		closeSession = s.cancel
	case "status":
	default:
		b.mu.Unlock()
		return nil, toolErr("invalid_arguments", "unknown control action")
	}
	status := "ready"
	if s.paused {
		status = "paused"
	}
	if action == "waiting_user" {
		status = "waiting_user"
	}
	if s.disconnected {
		status = "disconnected"
	}
	link := s.personal
	b.mu.Unlock()
	if closeSession != nil {
		closeSession()
	}
	if link != nil && (action == "pause" || action == "waiting_user" || action == "resume") {
		control := action
		if control == "waiting_user" {
			control = "pause"
		}
		link.mu.Lock()
		link.paused = control == "pause"
		link.mu.Unlock()
		if err := link.send(browserBridgeMessage{Kind: "control", Action: control}); err != nil {
			return nil, toolErr("disconnected", "personal control write failed")
		}
	}
	return map[string]any{"browser_session_id": id, "status": status, "truncated": false}, nil
}

func safeBrowserURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String()
}

func (b *browserManager) checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return toolErr("invalid_url", "only HTTP/HTTPS URLs without credentials are allowed")
	}
	if !b.config.AllowLocal {
		host := strings.ToLower(u.Hostname())
		ip := net.ParseIP(host)
		if host == "localhost" || !strings.Contains(host, ".") && ip == nil || strings.HasSuffix(host, ".local") || ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()) {
			return toolErr("url_not_allowed", "local/intranet access is disabled locally")
		}
		if ip == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return toolErr("url_not_allowed", "hostname could not be resolved safely")
			}
			for _, address := range addresses {
				ip := address.IP
				if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
					return toolErr("url_not_allowed", "hostname resolves to a local address")
				}
			}
		}
	}
	return nil
}

func (b *browserManager) open(ctx context.Context, args map[string]any) (any, error) {
	mode, err := requiredString(args, "mode")
	if err != nil {
		return nil, err
	}
	if mode == "personal" {
		return b.openPersonal(ctx, args)
	}
	if mode != "clean" && mode != "persistent" {
		return nil, toolErr("unsupported_mode", "managed browser supports clean and persistent")
	}
	for _, field := range []string{"connection_id", "profile_id", "browser_alias", "preferred_browser", "target_origin"} {
		if _, ok := args[field]; ok {
			return nil, toolErr("invalid_arguments", "personal browser selection requires mode=personal")
		}
	}
	profile := ""
	clean := mode == "clean"
	if clean {
		profile, err = os.MkdirTemp(filepath.Join(b.config.Dir, "profiles"), "clean-")
	} else {
		profile = filepath.Join(b.config.Dir, "profiles", "persistent")
		err = os.MkdirAll(profile, 0o700)
	}
	if err != nil {
		return nil, toolErr("io_error", "create browser profile", err)
	}
	b.mu.Lock()
	active := 0
	for _, session := range b.sessions {
		if !session.disconnected {
			active++
		}
	}
	if active >= 4 {
		b.mu.Unlock()
		if clean {
			_ = os.RemoveAll(profile)
		}
		return nil, toolErr("limit_reached", "at most four browser sessions")
	}
	for _, existing := range b.sessions {
		if existing.profile == profile && !existing.disconnected {
			b.mu.Unlock()
			return nil, toolErr("profile_in_use", "persistent profile already open")
		}
	}
	options := []chromedp.ExecAllocatorOption{chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck, chromedp.UserDataDir(profile), chromedp.WindowSize(1280, 900), chromedp.WSURLReadTimeout(10 * time.Second), chromedp.Flag("disable-background-timer-throttling", true), chromedp.Flag("disable-renderer-backgrounding", true), chromedp.Flag("disable-backgrounding-occluded-windows", true)}
	if b.config.Executable != "" {
		options = append(options, chromedp.ExecPath(b.config.Executable))
	}
	if b.config.Headless {
		options = append(options, chromedp.Headless)
	} else {
		options = append(options, chromedp.VisibleWindow, chromedp.Flag("start-maximized", false))
	}
	alloc, stopAlloc := chromedp.NewExecAllocator(context.Background(), options...)
	root, stopRoot := chromedp.NewContext(alloc, chromedp.WithLogf(func(string, ...any) {}), chromedp.WithErrorf(func(string, ...any) {}))
	s := &browserSession{id: browserID("browser_"), mode: mode, profile: profile, ctx: root, tabs: map[string]*browserTab{}, downloads: map[string]*browserDownload{}}
	var closeOnce sync.Once
	s.cancel = func() {
		closeOnce.Do(func() {
			shutdown, done := context.WithTimeout(root, 10*time.Second)
			_ = chromedp.Cancel(shutdown)
			done()
			stopRoot()
			stopAlloc()
			if clean {
				_ = os.RemoveAll(profile)
			}
		})
	}
	b.sessions[s.id] = s
	b.mu.Unlock()
	ready := false
	defer func() {
		if !ready {
			b.mu.Lock()
			s.disconnected = true
			b.mu.Unlock()
			s.cancel()
		}
	}()
	stop := context.AfterFunc(ctx, stopRoot)
	err = chromedp.Do(root)
	stop()
	if err != nil {
		s.cancel()
		b.mu.Lock()
		s.disconnected = true
		b.mu.Unlock()
		return nil, toolErr("browser_start_failed", "cannot launch visible browser in this desktop session")
	}
	tab := &browserTab{id: browserID("tab_"), ctx: root, cancel: func() {}, worlds: map[string]int{}}
	s.tabs[tab.id] = tab
	s.selected = tab.id
	b.trackDownloads(s)
	if err := chromedp.FromContext(root).Browser.Call(ctx, "Browser.setDownloadBehavior", map[string]any{"behavior": "deny"}, nil); err != nil {
		s.cancel()
		return nil, toolErr("browser_failed", "set safe download defaults")
	}
	if err := b.setupTab(ctx, tab); err != nil {
		s.cancel()
		return nil, err
	}
	if raw, _ := optionalString(args, "url"); raw != "" {
		if err := b.checkURL(raw); err != nil {
			s.cancel()
			return nil, err
		}
		if err := tab.command(ctx, "Page.navigate", map[string]any{"url": raw}, nil); err != nil {
			return nil, err
		}
	}
	ready = true
	return map[string]any{"browser_session_id": s.id, "tab_id": tab.id, "mode": mode, "status": "ready", "truncated": false}, nil
}

func (t *browserTab) command(ctx context.Context, method string, params, result any) error {
	if t.ctx.Err() != nil {
		return toolErr("disconnected", "browser tab closed")
	}
	if t.personal != nil {
		return t.personal.call(ctx, t.nativeTabID, method, params, result)
	}
	if err := chromedp.FromContext(t.ctx).Target.Call(ctx, method, params, result); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return browserTransportFailure(err.Error())
	}
	return nil
}

func (b *browserManager) setupTab(ctx context.Context, t *browserTab) error {
	if t.personal != nil {
		if t.personalReady {
			return nil
		}
		t.personal.mu.Lock()
		t.personal.attached[t.nativeTabID] = t
		t.personal.mu.Unlock()
		for _, method := range []string{"Page.enable", "Runtime.enable", "Network.enable"} {
			if err := t.command(ctx, method, map[string]any{}, nil); err != nil {
				return err
			}
		}
		t.personalReady = true
		return nil
	}
	events, stop := chromedp.FromContext(t.ctx).Target.Subscribe("Fetch.requestPaused")
	go func() {
		defer stop()
		for {
			select {
			case <-t.ctx.Done():
				return
			case raw, ok := <-events:
				if !ok {
					return
				}
				var event struct {
					ID      string `json:"requestId"`
					Request struct {
						URL string `json:"url"`
					} `json:"request"`
				}
				if json.Unmarshal(raw, &event) != nil {
					continue
				}
				requestCtx, cancel := context.WithTimeout(t.ctx, 5*time.Second)
				if err := b.checkURL(event.Request.URL); err != nil {
					_ = t.command(requestCtx, "Fetch.failRequest", map[string]any{"requestId": event.ID, "errorReason": "BlockedByClient"}, nil)
				} else {
					_ = t.command(requestCtx, "Fetch.continueRequest", map[string]any{"requestId": event.ID}, nil)
				}
				cancel()
			}
		}
	}()
	if err := t.command(ctx, "Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": "*", "requestStage": "Request"}}}, nil); err != nil {
		return err
	}
	for _, method := range []string{"Page.enable", "Runtime.enable", "Network.enable"} {
		if err := t.command(ctx, method, map[string]any{}, nil); err != nil {
			return err
		}
	}
	for _, method := range []string{"Page.frameNavigated", "Page.navigatedWithinDocument", "Page.javascriptDialogOpening", "Page.javascriptDialogClosed", "Runtime.exceptionThrown", "Runtime.consoleAPICalled", "Network.loadingFailed", "Network.responseReceived", "Network.requestWillBeSent", "Network.loadingFinished"} {
		events, stop := chromedp.FromContext(t.ctx).Target.Subscribe(method)
		go func(method string) {
			defer stop()
			for {
				select {
				case <-t.ctx.Done():
					return
				case raw, ok := <-events:
					if !ok {
						return
					}
					var event map[string]any
					if json.Unmarshal(raw, &event) != nil {
						continue
					}
					b.recordBrowserEvent(t, method, event)
				}
			}
		}(method)
	}
	return nil
}

func (b *browserManager) execute(ctx context.Context, name string, args map[string]any) (any, error) {
	if name == "browser_open" {
		return b.open(ctx, args)
	}
	id, err := requiredString(args, "browser_session_id")
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	s := b.sessions[id]
	b.mu.Unlock()
	if s == nil {
		return nil, toolErr("unknown_session", "unknown browser_session_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b.mu.Lock()
	if s.paused || s.disconnected {
		b.mu.Unlock()
		return nil, toolErr("access_paused", "browser session paused or disconnected")
	}
	callCtx, cancel := context.WithCancel(ctx)
	s.active = cancel
	b.mu.Unlock()
	defer func() { cancel(); b.mu.Lock(); s.active = nil; b.mu.Unlock() }()
	if s.personal != nil {
		b.syncPersonalTabs(s)
	}
	tabID, _ := optionalString(args, "tab_id")
	if tabID == "" {
		tabID = s.selected
	}
	t := s.tabs[tabID]
	if t == nil {
		return nil, toolErr("unknown_tab", "tab is not in this browser session")
	}
	if name == "browser_tabs" {
		return b.tabs(callCtx, s, t, args)
	}
	if t.personal != nil {
		if err := b.setupTab(callCtx, t); err != nil {
			return nil, err
		}
	}
	t.eventMu.Lock()
	dialog := t.dialog
	t.eventMu.Unlock()
	if dialog && !(name == "browser_interact" && args["action"] == "dialog") {
		return map[string]any{"browser_session_id": id, "tab_id": tabID, "status": "waiting_user", "reason": "browser_dialog", "truncated": false}, nil
	}
	if browserMutation(name, args) {
		t.eventMu.Lock()
		t.lastOperation, _ = ctx.Value(browserOperationContext{}).(string)
		t.eventMu.Unlock()
	}
	result, err := b.tabAction(callCtx, name, s, t, args)
	if err != nil {
		contextArgs := map[string]any{"browser_session_id": id, "tab_id": tabID, "snapshot_id": args["snapshot_id"]}
		return nil, browserErrorContext(err, contextArgs)
	}
	if object, ok := result.(map[string]any); ok {
		if name == "browser_interact" && object["status"] != "waiting_user" && object["confirmation"] == nil {
			object["confirmation"] = "action_dispatched"
		}
		object["browser_session_id"] = id
		object["tab_id"] = tabID
		if _, ok := object["status"]; !ok {
			object["status"] = "completed"
		}
		if _, ok := object["truncated"]; !ok {
			object["truncated"] = false
		}
	}
	return result, nil
}

func (b *browserManager) tabs(ctx context.Context, s *browserSession, t *browserTab, args map[string]any) (any, error) {
	if s.personal != nil {
		return b.personalTabs(ctx, s, t, args)
	}
	action, _ := requiredString(args, "action")
	if action == "list" {
		available, err := chromedp.Targets(s.ctx)
		if err != nil {
			return nil, toolErr("disconnected", "cannot enumerate tabs")
		}
		for _, info := range available {
			if info.Type != "page" || len(s.tabs) >= 16 {
				continue
			}
			known := false
			for _, tab := range s.tabs {
				if chromedp.FromContext(tab.ctx).Target.TargetID == info.TargetID {
					known = true
					break
				}
			}
			if known {
				continue
			}
			if info.URL != "about:blank" {
				if err := b.checkURL(info.URL); err != nil {
					continue
				}
			}
			child, cancel := chromedp.NewContext(s.ctx, chromedp.WithTargetID(target.ID(info.TargetID)))
			if err := chromedp.Do(child); err != nil {
				cancel()
				continue
			}
			newTab := &browserTab{id: browserID("tab_"), ctx: child, cancel: cancel, worlds: map[string]int{}}
			if err := b.setupTab(ctx, newTab); err != nil {
				cancel()
				continue
			}
			s.tabs[newTab.id] = newTab
		}
	}
	switch action {
	case "new":
		if len(s.tabs) >= 16 {
			return nil, toolErr("limit_reached", "at most sixteen tabs")
		}
		child, cancel := chromedp.NewContext(s.ctx)
		if err := chromedp.Do(child); err != nil {
			cancel()
			return nil, toolErr("browser_failed", "create tab")
		}
		t = &browserTab{id: browserID("tab_"), ctx: child, cancel: cancel, worlds: map[string]int{}}
		if err := b.setupTab(ctx, t); err != nil {
			cancel()
			return nil, err
		}
		s.tabs[t.id] = t
		s.selected = t.id
	case "select":
		s.selected = t.id
		if err := t.command(ctx, "Page.bringToFront", map[string]any{}, nil); err != nil {
			return nil, err
		}
	case "close":
		if len(s.tabs) == 1 {
			return nil, toolErr("last_tab", "disconnect the session to close its last tab")
		}
		if err := chromedp.FromContext(s.ctx).Browser.Call(ctx, "Target.closeTarget", map[string]any{"targetId": chromedp.FromContext(t.ctx).Target.TargetID}, nil); err != nil {
			return nil, toolErr("browser_failed", "close tab")
		}
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
	list := []map[string]any{}
	for _, tab := range s.tabs {
		var tree map[string]any
		if err := tab.command(ctx, "Page.getFrameTree", map[string]any{}, &tree); err != nil {
			continue
		}
		frame, _ := tree["frameTree"].(map[string]any)
		f, _ := frame["frame"].(map[string]any)
		u, _ := f["url"].(string)
		list = append(list, map[string]any{"tab_id": tab.id, "url": safeBrowserURL(u)})
	}
	return map[string]any{"browser_session_id": s.id, "tab_id": s.selected, "tabs": list, "status": "ready", "truncated": false}, nil
}

func (b *browserManager) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	if b.cleanupStop != nil {
		b.cleanupStop()
	}
	var cancels []context.CancelFunc
	for _, op := range b.operations {
		if op.cancel != nil {
			op.cancel()
		}
	}
	for _, s := range b.sessions {
		s.disconnected = true
		cancels = append(cancels, s.cancel)
	}
	link := b.bridge
	peers := make([]io.ReadWriteCloser, 0, len(b.bridgePeers))
	for peer := range b.bridgePeers {
		peers = append(peers, peer)
	}
	b.mu.Unlock()
	if b.cleanupDone != nil {
		<-b.cleanupDone
	}
	if b.listener != nil {
		b.listener.Close()
	}
	if link != nil {
		link.conn.Close()
	}
	for _, peer := range peers {
		peer.Close()
	}
	b.bridgeWG.Wait()
	for _, cancel := range cancels {
		cancel()
	}
}

func (b *browserManager) screenshot(ctx context.Context, s *browserSession, t *browserTab, args map[string]any) (any, error) {
	full, _, err := optionalBool(args, "full_page")
	if err != nil {
		return nil, err
	}
	var result struct {
		Data string `json:"data"`
	}
	params := map[string]any{"format": "jpeg", "quality": 60, "captureBeyondViewport": full}
	if full {
		var metrics struct {
			Size struct{ Width, Height float64 } `json:"cssContentSize"`
		}
		if err := t.command(ctx, "Page.getLayoutMetrics", map[string]any{}, &metrics); err != nil {
			return nil, err
		}
		if metrics.Size.Width*metrics.Size.Height > 50_000_000 {
			return nil, toolErr("image_too_large", "page exceeds 50 megapixels; use viewport")
		}
		params["clip"] = map[string]any{"x": 0, "y": 0, "width": metrics.Size.Width, "height": metrics.Size.Height, "scale": 1}
	}
	if err := t.command(ctx, "Page.captureScreenshot", params, &result); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		return nil, toolErr("browser_failed", "invalid screenshot")
	}
	truncated := false
	budget := min(b.executor.maxResponse/2, 1<<20)
	for len(base64.StdEncoding.EncodeToString(data)) > budget {
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, toolErr("browser_failed", "decode screenshot")
		}
		bounds := img.Bounds()
		width, height := bounds.Dx()*3/4, bounds.Dy()*3/4
		if width < 64 || height < 64 {
			return nil, toolErr("image_too_large", "response budget cannot fit screenshot")
		}
		resized := image.NewRGBA(image.Rect(0, 0, width, height))
		for y := range height {
			for x := range width {
				resized.Set(x, y, img.At(bounds.Min.X+x*bounds.Dx()/width, bounds.Min.Y+y*bounds.Dy()/height))
			}
		}
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, resized, &jpeg.Options{Quality: 55}); err != nil {
			return nil, err
		}
		data = encoded.Bytes()
		truncated = true
	}
	metadata := map[string]any{"browser_session_id": s.id, "tab_id": t.id, "status": "completed", "mime_type": "image/jpeg", "truncated": truncated}
	dimensions, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, toolErr("browser_failed", "decode screenshot dimensions")
	}
	metadata["image_width"], metadata["image_height"] = dimensions.Width, dimensions.Height
	if save, _, err := optionalBool(args, "save"); err != nil {
		return nil, err
	} else if save {
		b.artifactMu.Lock()
		defer b.artifactMu.Unlock()
		size, err := b.cleanArtifacts()
		if err != nil {
			return nil, toolErr("io_error", "inspect artifacts", err)
		}
		if size+int64(len(data)) > browserArtifactMax {
			return nil, toolErr("limit_reached", "browser artifact budget reached")
		}
		path := filepath.Join(b.config.Dir, "artifacts", browserID("image_")+".jpg")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, toolErr("io_error", "save screenshot", err)
		}
		metadata["artifact_path"] = path
	}
	return browserImage{metadata, base64.StdEncoding.EncodeToString(data)}, nil
}
