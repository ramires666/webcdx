package main

import (
	"context"
	"sync/atomic"
)

type browserReadCatalog struct {
	tabs      map[string]*browserTab
	selected  string
	boundOnly bool
}

type browserReadCatalogSlot struct {
	value atomic.Pointer[browserReadCatalog]
}

// Called by the session owner. Readers get an immutable metadata catalog;
// diagnostics never traverse a map being changed by a long-running operation.
func (s *browserSession) publishReadCatalog() {
	copy := &browserReadCatalog{tabs: make(map[string]*browserTab, len(s.tabs)), selected: s.selected, boundOnly: s.boundOnly}
	for id, tab := range s.tabs {
		copy.tabs[id] = tab
	}
	s.readCatalog.value.Store(copy)
}

func (b *browserManager) cachedBrowserDiagnostics(ctx context.Context, s *browserSession, args map[string]any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	unavailable := s.disconnected || s.paused || b.closed
	b.mu.Unlock()
	if unavailable {
		return nil, toolErr("access_paused", browserFailureMessage("access_paused"))
	}
	catalog := s.readCatalog.value.Load()
	if catalog == nil {
		return nil, toolErr("browser_busy", "browser tab catalog is not available yet")
	}
	id, _ := optionalString(args, "tab_id")
	revision, _ := optionalString(args, "binding_revision")
	if catalog.boundOnly && revision == "" {
		return nil, toolErr("binding_changed", browserFailureMessage("binding_changed"))
	}
	if id == "" && revision != "" {
		for candidate, tab := range catalog.tabs {
			tab.eventMu.Lock()
			match := tab.targetBinding != nil && tab.targetBinding.Revision == revision
			tab.eventMu.Unlock()
			if match {
				id = candidate
				break
			}
		}
	} else if id == "" {
		id = catalog.selected
	}
	tab := catalog.tabs[id]
	if tab == nil || tab.ctx.Err() != nil {
		return nil, toolErr("unknown_tab", "cached browser tab is no longer available")
	}
	boundCtx, err := browserBindingContext(ctx, s, tab, revision)
	if err != nil {
		return nil, err
	}
	if s.personal != nil {
		s.personal.mu.Lock()
		valid := !s.personal.closed && !s.personal.paused
		if binding, _ := boundCtx.Value(browserTargetContext{}).(*browserTargetBinding); binding != nil {
			valid = valid && binding.Connection == s.personal.id && binding.Scope == s.personal.scopeEpoch
		}
		s.personal.mu.Unlock()
		if !valid {
			return nil, toolErr("scope_changed", browserFailureMessage("scope_changed"))
		}
	}
	browserConsoleBind(ctx, s, tab)
	tab.eventMu.Lock()
	raw := tab.pageURL
	tab.eventMu.Unlock()
	operation, _ := optionalString(args, "operation_id")
	result := browserDiagnostics(tab, raw, operation)
	result["browser_session_id"], result["tab_id"] = s.id, id
	result["status"], result["reason"] = "completed", "cached_diagnostics"
	return result, nil
}
