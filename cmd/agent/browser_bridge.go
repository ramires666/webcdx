package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const browserExtensionID = "pmobmihkpbblajdanleplliihfmcdmpp"
const browserNativeHostName = "org.webcodex.browser"
const browserBridgeMax = 1 << 20
const browserPersonalTabLimit = 128
const browserBridgeConnections = 8

type browserBridgeDescriptor struct {
	Endpoint    string `json:"endpoint"`
	Token       string `json:"token"`
	ExtensionID string `json:"extension_id"`
}
type browserBridgeMessage struct {
	Kind            string               `json:"kind"`
	ID              string               `json:"id,omitempty"`
	Token           string               `json:"token,omitempty"`
	Origin          string               `json:"origin,omitempty"`
	Action          string               `json:"action,omitempty"`
	TabID           int                  `json:"tab_id,omitempty"`
	FrameID         string               `json:"frame_id,omitempty"`
	Method          string               `json:"method,omitempty"`
	Params          any                  `json:"params,omitempty"`
	Result          json.RawMessage      `json:"result,omitempty"`
	Error           string               `json:"error,omitempty"`
	Tabs            []browserPersonalTab `json:"tabs,omitempty"`
	Paused          bool                 `json:"paused,omitempty"`
	LocalPaused     bool                 `json:"local_paused,omitempty"`
	AllowNew        bool                 `json:"allow_new,omitempty"`
	AllowLocal      bool                 `json:"allow_local,omitempty"`
	URL             string               `json:"url,omitempty"`
	Allowed         bool                 `json:"allowed,omitempty"`
	ProfileID       string               `json:"profile_id,omitempty"`
	Alias           string               `json:"alias,omitempty"`
	Product         string               `json:"product,omitempty"`
	CommandProtocol int                  `json:"command_protocol,omitempty"`
	ConnectionID    string               `json:"connection_id,omitempty"`
	DeadlineMS      int64                `json:"deadline_ms,omitempty"`
	ScopeEpoch      uint64               `json:"scope_epoch"`
	ControlEpoch    uint64               `json:"control_epoch,omitempty"`
	Cancelled       bool                 `json:"cancelled,omitempty"`
}
type browserPersonalTab struct {
	ID       int    `json:"tab_id"`
	URL      string `json:"url"`
	Title    string `json:"title,omitempty"`
	WindowID int    `json:"window_id,omitempty"`
	Active   bool   `json:"active,omitempty"`
}

type browserBridge struct {
	mu                            sync.Mutex
	writeMu                       sync.Mutex
	manager                       *browserManager
	conn                          io.ReadWriteCloser
	pending                       map[string]chan browserBridgeMessage
	tabs                          []browserPersonalTab
	allowNew, paused, closed      bool
	localPaused                   bool
	attached                      map[int]*browserTab
	id, profileID, alias, product string
	commandProtocol               int
	scopeEpoch, controlEpoch      uint64
}

func browserBridgePath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "webcodex", "browser-bridge.json")
}

func (b *browserManager) startBridge(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := privateBrowserDirectory(dir); err != nil {
		return err
	}
	listener, endpoint, err := browserListenIPC(dir)
	if err != nil {
		return err
	}
	descriptor := browserBridgeDescriptor{endpoint, browserID("bridge_"), browserExtensionID}
	data, _ := json.Marshal(descriptor)
	if err := atomicWrite(path, data, false); err != nil {
		listener.Close()
		return err
	}
	b.listener = listener
	b.bridgePath = path
	b.bridgePeers = map[io.ReadWriteCloser]bool{}
	b.bridgeWG.Add(1)
	go func() {
		defer b.bridgeWG.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			b.mu.Lock()
			if b.closed {
				b.mu.Unlock()
				conn.Close()
				return
			}
			if len(b.bridgePeers) >= browserBridgeConnections {
				b.mu.Unlock()
				conn.Close()
				continue
			}
			b.bridgePeers[conn] = true
			b.bridgeWG.Add(1)
			b.mu.Unlock()
			go func() {
				defer b.bridgeWG.Done()
				defer func() { b.mu.Lock(); delete(b.bridgePeers, conn); b.mu.Unlock() }()
				b.serveBridge(conn, descriptor)
			}()
		}
	}()
	return nil
}

func (b *browserManager) serveBridge(conn io.ReadWriteCloser, descriptor browserBridgeDescriptor) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 65536), browserBridgeMax)
	timer := time.AfterFunc(5*time.Second, func() { conn.Close() })
	if !scanner.Scan() {
		timer.Stop()
		return
	}
	var hello browserBridgeMessage
	if json.Unmarshal(scanner.Bytes(), &hello) != nil || hello.Kind != "hello" || hello.Token != descriptor.Token || hello.Origin != "chrome-extension://"+descriptor.ExtensionID+"/" || !browserIPCPeer(conn) {
		timer.Stop()
		return
	}
	timer.Stop()
	link := &browserBridge{manager: b, conn: conn, pending: map[string]chan browserBridgeMessage{}, attached: map[int]*browserTab{}}
	link.id = browserID("connection_")
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	if b.bridges == nil {
		b.bridges = map[string]*browserBridge{}
	}
	b.bridges[link.id] = link
	if b.bridge == nil {
		b.bridge = link
	}
	b.mu.Unlock()
	defer func() {
		link.mu.Lock()
		link.closed = true
		for _, ch := range link.pending {
			close(ch)
		}
		for _, tab := range link.attached {
			tab.cancel()
		}
		link.mu.Unlock()
		b.mu.Lock()
		for _, session := range b.sessions {
			if session.personal == link {
				session.disconnected = true
				session.workEpoch++
				session.work.cancelPending()
				if session.active != nil {
					session.active()
				}
			}
		}
		if b.bridge == link {
			b.bridge = nil
		}
		delete(b.bridges, link.id)
		b.mu.Unlock()
	}()
	if err := link.send(browserBridgeMessage{Kind: "hello", AllowLocal: b.config.AllowLocal, CommandProtocol: browserCommandProtocol, ConnectionID: link.id}); err != nil {
		return
	}
	for scanner.Scan() {
		var message browserBridgeMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			return
		}
		switch message.Kind {
		case "identity":
			if !b.identifyBrowser(link, message) {
				return
			}
		case "url_check":
			if len(message.URL) > 16384 {
				return
			}
			allowed := b.checkURL(message.URL) == nil
			if err := link.send(browserBridgeMessage{Kind: "url_result", ID: message.ID, Allowed: allowed}); err != nil {
				return
			}
		case "response", "cancel_ack":
			link.mu.Lock()
			ch := link.pending[message.ID]
			if ch != nil {
				select {
				case ch <- message:
				default:
				}
			}
			link.mu.Unlock()
		case "scope":
			if len(message.Tabs) > browserPersonalTabLimit {
				return
			}
			valid := []browserPersonalTab{}
			for _, tab := range message.Tabs {
				if tab.ID > 0 && (tab.URL == "about:blank" || b.checkURL(tab.URL) == nil) {
					tab.Title, _ = truncateUTF8(tab.Title, 256)
					if tab.WindowID < 0 {
						return
					}
					valid = append(valid, tab)
				}
			}
			b.mu.Lock()
			link.mu.Lock()
			if link.commandProtocol == browserCommandProtocol && (message.ScopeEpoch < link.scopeEpoch || (message.ControlEpoch < link.controlEpoch && !message.LocalPaused)) {
				link.mu.Unlock()
				b.mu.Unlock()
				continue
			}
			scopeChanged := link.commandProtocol == browserCommandProtocol && message.ScopeEpoch != link.scopeEpoch
			link.scopeEpoch = message.ScopeEpoch
			link.tabs = valid
			link.paused = message.Paused
			link.localPaused = message.LocalPaused
			link.allowNew = message.AllowNew
			for id, tab := range link.attached {
				exists := false
				for _, approved := range valid {
					if approved.ID == id {
						exists = true
						break
					}
				}
				if !exists {
					tab.cancel()
					delete(link.attached, id)
				}
			}
			link.mu.Unlock()
			for _, session := range b.sessions {
				if session.personal == link {
					session.paused = message.Paused
					if message.Paused || scopeChanged {
						session.workEpoch++
						session.work.cancelPending()
						if session.active != nil {
							session.active()
						}
					}
				}
			}
			b.mu.Unlock()
		case "event":
			link.mu.Lock()
			tab := link.attached[message.TabID]
			link.mu.Unlock()
			if tab != nil {
				var event map[string]any
				if json.Unmarshal(message.Result, &event) == nil {
					b.recordBrowserEvent(tab, message.Method, event)
				}
			}
		}
	}
}

func (l *browserBridge) send(message browserBridgeMessage) error {
	return l.sendContext(context.Background(), message)
}

func (l *browserBridge) sendContext(ctx context.Context, message browserBridgeMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) >= browserBridgeMax {
		return errors.New("browser message exceeds limit")
	}
	done := make(chan error, 1)
	go func() {
		l.writeMu.Lock()
		defer l.writeMu.Unlock()
		if err := ctx.Err(); err != nil {
			done <- err
			return
		}
		stop := time.AfterFunc(3*time.Second, func() { l.conn.Close() })
		defer stop.Stop()
		_, err := l.conn.Write(append(data, '\n'))
		done <- err
	}()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		l.conn.Close()
		return errors.New("native bridge write timed out")
	}
}

func (l *browserBridge) call(ctx context.Context, tabID int, method string, params, result any) error {
	return l.callFrame(ctx, tabID, "", method, params, result)
}

func (l *browserBridge) callFrame(ctx context.Context, tabID int, frameID, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := browserID("bridge_request_")
	ch := make(chan browserBridgeMessage, 1)
	l.mu.Lock()
	if l.closed || l.paused {
		l.mu.Unlock()
		return toolErr("access_paused", "personal browser disconnected or paused")
	}
	if binding, ok := ctx.Value(browserAccessContext{}).(browserAccessBinding); ok && (binding.connection != l.id || binding.scope != l.scopeEpoch) {
		l.mu.Unlock()
		return toolErr("scope_changed", "local browser access changed; obtain fresh state")
	}
	protocol, epoch, connection := l.commandProtocol, l.scopeEpoch, l.id
	if browserMayChange(ctx, method) && protocol != browserCommandProtocol {
		l.mu.Unlock()
		return toolErr("protocol_upgrade_required", "update the browser extension before changing the page")
	}
	l.pending[id] = ch
	l.mu.Unlock()
	defer func() { l.mu.Lock(); delete(l.pending, id); l.mu.Unlock() }()
	deadline := time.Now().Add(30 * time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	browserMarkDispatch(ctx, method)
	if err := l.sendContext(ctx, browserBridgeMessage{Kind: "command", ID: id, TabID: tabID, FrameID: frameID, Method: method, Params: params, CommandProtocol: protocol, ConnectionID: connection, ScopeEpoch: epoch, DeadlineMS: deadline.UnixMilli()}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return toolErr("disconnected", "native bridge write failed")
	}
	select {
	case response, ok := <-ch:
		if !ok {
			return toolErr("disconnected", "personal browser disconnected")
		}
		if response.Error != "" {
			return browserTransportFailure(response.Error)
		}
		if result != nil {
			if err := json.Unmarshal(response.Result, result); err != nil {
				return toolErr("browser_failed", "invalid extension response")
			}
		}
		return nil
	case <-ctx.Done():
		if protocol == browserCommandProtocol {
			cancelCtx, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
			if l.sendContext(cancelCtx, browserBridgeMessage{Kind: "command_cancel", ID: id, ConnectionID: connection, CommandProtocol: protocol}) == nil {
				select {
				case <-ch:
				case <-cancelCtx.Done():
				}
			}
			stop()
		}
		return ctx.Err()
	}
}

func readNativeMessage(reader io.Reader) ([]byte, error) {
	var length uint32
	if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
		return nil, err
	}
	if length == 0 || length > browserBridgeMax {
		return nil, errors.New("native message exceeds limit")
	}
	data := make([]byte, length)
	_, err := io.ReadFull(reader, data)
	if err == nil && !json.Valid(data) {
		err = errors.New("invalid native JSON")
	}
	return data, err
}
func writeNativeMessage(writer io.Writer, data []byte) error {
	if len(data) == 0 || len(data) > browserBridgeMax || !json.Valid(data) {
		return errors.New("invalid native message")
	}
	if err := binary.Write(writer, binary.LittleEndian, uint32(len(data))); err != nil {
		return err
	}
	_, err := writer.Write(data)
	return err
}

func runBrowserNativeHost(origin string) error {
	data, err := os.ReadFile(browserBridgePath())
	if err != nil {
		return err
	}
	var descriptor browserBridgeDescriptor
	if json.Unmarshal(data, &descriptor) != nil || origin != "chrome-extension://"+descriptor.ExtensionID+"/" || descriptor.Token == "" {
		return errors.New("extension origin not allowed")
	}
	conn, err := browserDialIPC(descriptor.Endpoint)
	if err != nil {
		return err
	}
	defer conn.Close()
	hello, _ := json.Marshal(browserBridgeMessage{Kind: "hello", Token: descriptor.Token, Origin: origin})
	if _, err := conn.Write(append(hello, '\n')); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 65536), browserBridgeMax)
		for scanner.Scan() {
			if err := writeNativeMessage(os.Stdout, scanner.Bytes()); err != nil {
				done <- err
				return
			}
		}
		done <- scanner.Err()
	}()
	go func() {
		for {
			data, err := readNativeMessage(os.Stdin)
			if err != nil {
				done <- err
				return
			}
			if _, err := conn.Write(append(data, '\n')); err != nil {
				done <- err
				return
			}
		}
	}()
	return <-done
}

func nativeHostInvocation(args []string) bool {
	return len(args) > 1 && strings.HasPrefix(args[1], "chrome-extension://")
}
