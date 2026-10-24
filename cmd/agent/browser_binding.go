package main

import (
	"context"
	"strings"
)

const browserTargetCapability = "target_binding_v1"

type browserTargetContext struct{}
type browserTargetBinding struct {
	Revision, URL, Connection string
	Window, NativeTab         int
	Scope                     uint64
}

func (l *browserBridge) supports(capability string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, value := range l.capabilities {
		if value == capability {
			return true
		}
	}
	return false
}

func browserBindingContext(ctx context.Context, s *browserSession, t *browserTab, revision string) (context.Context, error) {
	if revision == "" {
		return ctx, nil
	}
	t.eventMu.Lock()
	binding := t.targetBinding
	t.eventMu.Unlock()
	if binding == nil || binding.Revision != revision || s.personal == nil || !s.personal.supports(browserTargetCapability) {
		return nil, toolErr("binding_changed", browserFailureMessage("binding_changed"))
	}
	return context.WithValue(ctx, browserTargetContext{}, binding), nil
}

func (b *browserManager) verifyExistingBinding(ctx context.Context, s *browserSession, t *browserTab, choice browserExistingChoice) (map[string]any, error) {
	if !s.personal.supports(browserTargetCapability) {
		return nil, toolErr("protocol_upgrade_required", "update the browser extension for verified existing-tab binding")
	}
	s.personal.mu.Lock()
	binding := &browserTargetBinding{URL: choice.Tab.URL, Window: choice.Tab.WindowID, NativeTab: t.nativeTabID, Connection: s.personal.id, Scope: s.personal.scopeEpoch}
	s.personal.mu.Unlock()
	ctx = context.WithValue(withBrowserFrameReads(ctx), browserTargetContext{}, binding)
	var actual browserPersonalTab
	if err := s.personal.call(ctx, t.nativeTabID, "WebCodex.target", map[string]any{}, &actual); err != nil {
		return nil, err
	}
	expected, _ := canonicalBrowserExistingURL(choice.Tab.URL)
	actualURL, _ := canonicalBrowserExistingURL(actual.URL)
	if actual.ID != t.nativeTabID || actualURL != expected || actual.WindowID != choice.Tab.WindowID {
		return nil, toolErr("binding_changed", browserFailureMessage("binding_changed"))
	}
	if err := b.setupTab(ctx, t); err != nil {
		return nil, err
	}
	var proof struct {
		URL, Title, Text string
	}
	if err := b.eval(ctx, t, "", `({url:location.href,title:document.title,text:(document.body?.innerText||'').slice(0,240)})`, &proof); err != nil {
		return nil, err
	}
	observed, _ := canonicalBrowserExistingURL(proof.URL)
	if observed != expected {
		return nil, toolErr("binding_changed", browserFailureMessage("binding_changed"))
	}
	s.personal.mu.Lock()
	unchanged := !s.personal.closed && !s.personal.paused && s.personal.id == binding.Connection && s.personal.scopeEpoch == binding.Scope
	s.personal.mu.Unlock()
	if !unchanged {
		return nil, toolErr("scope_changed", browserFailureMessage("scope_changed"))
	}
	t.eventMu.Lock()
	previous := t.targetBinding
	if previous != nil && previous.URL == binding.URL && previous.Window == binding.Window && previous.Scope == binding.Scope && previous.Connection == binding.Connection {
		binding.Revision = previous.Revision
	} else {
		binding.Revision = browserID("binding_")
	}
	t.targetBinding = binding
	t.pageTitle, t.pageURL = proof.Title, proof.URL
	t.eventMu.Unlock()
	return map[string]any{"binding_revision": binding.Revision, "binding_verified": true, "readable": true, "auth_state": "unverified", "window_id": actual.WindowID,
		"selection_reason": choice.Reason, "matching_tabs": choice.Matches, "url": safeBrowserURL(proof.URL), "title": proof.Title, "text": strings.TrimSpace(proof.Text)}, nil
}
