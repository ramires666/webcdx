package main

import (
	"context"
	"strings"
	"sync/atomic"
)

const browserCommandProtocol = 2

// Once a mutation may have crossed the transport, these errors cannot establish
// whether the page changed. Recover by inspecting state, never by replaying it.
func browserUncertainTransport(code string) bool {
	switch code {
	case "scope_changed", "command_expired", "cancelled_before_dispatch", "access_paused", "disconnected", "response_too_large":
		return true
	}
	return false
}

type browserExecutionState struct {
	phase      atomic.Uint32
	dispatched atomic.Bool
}
type browserExecutionContext struct{}
type browserAccessContext struct{}
type browserAccessBinding struct {
	connection string
	scope      uint64
}

func browserSetPhase(ctx context.Context, phase uint32) {
	if state, _ := ctx.Value(browserExecutionContext{}).(*browserExecutionState); state != nil {
		state.phase.Store(phase)
	}
}

func browserMayChange(ctx context.Context, method string) bool {
	if strings.HasPrefix(method, "Input.") {
		return true
	}
	switch method {
	case "Page.navigate", "Page.reload", "Page.navigateToHistoryEntry", "Page.handleJavaScriptDialog", "Page.bringToFront", "DOM.setFileInputFiles", "WebCodex.newTab", "WebCodex.closeTab", "WebCodex.enableDownloads", "WebCodex.prepare", "Browser.launch":
		return true
	case "Runtime.evaluate":
		return ctx.Value(browserUserGesture{}) == true
	}
	return false
}

func browserMarkDispatch(ctx context.Context, method string) {
	if state, _ := ctx.Value(browserExecutionContext{}).(*browserExecutionState); state != nil && browserMayChange(ctx, method) {
		state.dispatched.Store(true)
		state.phase.Store(3)
	}
}

func (op *browserOperation) currentPhase() string {
	if op.execution != nil && op.Status == "running" {
		switch op.execution.phase.Load() {
		case 1:
			return "queued"
		case 2:
			return "executing"
		case 3:
			return "dispatch_started"
		}
	}
	if op.Phase != "" {
		return op.Phase
	}
	return "unknown"
}

func (l *browserBridge) bindAccess(ctx context.Context) context.Context {
	l.mu.Lock()
	binding := browserAccessBinding{l.id, l.scopeEpoch}
	l.mu.Unlock()
	return context.WithValue(ctx, browserAccessContext{}, binding)
}
