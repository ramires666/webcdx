package main

import (
	"context"
	"testing"
)

func TestBrowserClickPreservesScrollAndActivateStillClicks(t *testing.T) {
	f := newBrowserFixture(t, `<style>body{height:2400px}#visible{position:fixed;right:24px;bottom:24px}</style>
<button id="visible" onclick="document.querySelector('#count').textContent=String(Number(document.querySelector('#count').textContent)+1)">Click</button>
<button id="activate" onclick="document.querySelector('#count').textContent=String(Number(document.querySelector('#count').textContent)+1)">Activate click</button><div id="count">0</div>`)
	tab := f.b.sessions[f.session].tabs[f.tab]
	if err := f.b.eval(f.ctx, tab, "", `(()=>{scrollTo(0,700);return true})()`, nil); err != nil {
		t.Fatal(err)
	}
	var before float64
	if err := f.b.eval(f.ctx, tab, "", `scrollY`, &before); err != nil {
		t.Fatal(err)
	}
	if before < 400 {
		t.Fatalf("fixture did not reach its intended scroll position: %v", before)
	}
	result, err := f.call("browser_interact", map[string]any{
		"action": "click", "selector": "#visible", "request_key": "focus-preserve-click",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["confirmation"] != "action_dispatched" {
		t.Fatalf("visible click result = %#v", result)
	}
	var after float64
	if err := f.b.eval(f.ctx, tab, "", `scrollY`, &after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("visible click changed scrollY: before=%v after=%v", before, after)
	}
	var count string
	if err := f.b.eval(f.ctx, tab, "", `document.querySelector('#count').textContent`, &count); err != nil {
		t.Fatal(err)
	}
	if count != "1" {
		t.Fatalf("visible click did not perform its action: count=%q", count)
	}

	_, err = f.call("browser_interact", map[string]any{
		"action": "click", "selector": "#activate", "focus_policy": "activate", "request_key": "focus-activate-click",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.b.eval(f.ctx, tab, "", `document.querySelector('#count').textContent`, &count); err != nil {
		t.Fatal(err)
	}
	if count != "2" {
		t.Fatalf("activate click did not perform its action: count=%q", count)
	}
}

func TestBrowserPersonalTabSelectFocusPolicy(t *testing.T) {
	fixture, session := newBrowserFrameBudgetSession(t)
	childCtx, cancel := context.WithCancel(session.ctx)
	t.Cleanup(cancel)
	other := &browserTab{
		id: browserID("tab_"), ctx: childCtx, cancel: cancel,
		personal: fixture.bridge, nativeTabID: 8, personalReady: true, worlds: map[string]int{"main": 1},
	}
	session.tabs[other.id] = other
	fixture.bridge.mu.Lock()
	fixture.bridge.tabs = append(fixture.bridge.tabs, browserPersonalTab{ID: 8, URL: "https://example.com/other", Title: "Other", Active: false})
	fixture.bridge.attached[8] = other
	fixture.bridge.mu.Unlock()

	if _, err := fixture.manager.execute(context.Background(), "browser_tabs", map[string]any{
		"browser_session_id": session.id, "tab_id": other.id, "action": "select",
	}); err != nil {
		t.Fatal(err)
	}
	defaultMethods := fixture.takeMethods()
	if countBrowserMethods(defaultMethods, "Page.bringToFront") != 0 {
		t.Fatalf("default select activated a target: %v", defaultMethods)
	}
	if session.selected != other.id {
		t.Fatalf("default select did not select target: selected=%s want=%s", session.selected, other.id)
	}

	if _, err := fixture.manager.execute(context.Background(), "browser_tabs", map[string]any{
		"browser_session_id": session.id, "tab_id": other.id, "action": "select", "focus_policy": "activate",
	}); err != nil {
		t.Fatal(err)
	}
	activateMethods := fixture.takeMethods()
	if countBrowserMethods(activateMethods, "Page.bringToFront") != 1 {
		t.Fatalf("activate select bridge methods = %v; want one Page.bringToFront", activateMethods)
	}
}
