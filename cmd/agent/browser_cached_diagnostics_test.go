package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBrowserCachedDiagnosticsBypassesBusySessionQueue(t *testing.T) {
	fixture, session := newBrowserFrameBudgetSession(t)
	tab := fixture.tab
	operationID := "operation_diagnostics_correlated"
	tab.eventMu.Lock()
	tab.pageURL = "https://example.com/private/path?token=secret"
	tab.lastOperation = operationID
	tab.eventMu.Unlock()
	fixture.manager.recordBrowserEvent(tab, "Network.requestWillBeSent", map[string]any{
		"requestId": "request-private",
		"request":   map[string]any{"url": "https://provider.example/private?token=secret"},
	})
	fixture.manager.recordBrowserEvent(tab, "Network.loadingFailed", map[string]any{
		"requestId": "request-private",
		"errorText": "net::ERR_TIMED_OUT",
	})
	session.publishReadCatalog()

	release, err := session.work.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	started := time.Now()
	value, err := fixture.manager.call(context.Background(), "browser_diagnostics", map[string]any{
		"browser_session_id": session.id,
	})
	if err != nil {
		t.Fatalf("diagnostics while session permit is held: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("cached diagnostics took %s with an unresponsive renderer", elapsed)
	}
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("diagnostics result = %#v", value)
	}
	events, _ := result["events"].([]map[string]any)
	if len(events) != 1 || events[0]["operation_id"] != operationID || events[0]["failure_type"] != "timeout" {
		t.Fatalf("diagnostic event lost correlation: %#v", result["events"])
	}
	if result["url"] != "https://example.com/" {
		t.Fatalf("diagnostic URL was not redacted: %#v", result["url"])
	}
	if methods := fixture.takeMethods(); len(methods) != 0 {
		t.Fatalf("diagnostics sent CDP while renderer permit was held: %v", methods)
	}
}

func TestBrowserCachedDiagnosticsRejectsInvalidReadCatalogTargets(t *testing.T) {
	fixture, session := newBrowserFrameBudgetSession(t)
	session.publishReadCatalog()
	base := map[string]any{"browser_session_id": session.id}
	assertCodedFailure := func(args map[string]any, want string) {
		t.Helper()
		_, err := fixture.manager.call(context.Background(), "browser_diagnostics", args)
		var coded *codedError
		if !errors.As(err, &coded) || coded.code != want {
			t.Fatalf("diagnostics error = %v, want code %q", err, want)
		}
		if methods := fixture.takeMethods(); len(methods) != 0 {
			t.Fatalf("rejected diagnostics sent CDP: %v", methods)
		}
	}

	wrongTab := map[string]any{"browser_session_id": session.id, "tab_id": "tab_does_not_exist"}
	assertCodedFailure(wrongTab, "unknown_tab")

	fixture.manager.mu.Lock()
	session.paused = true
	fixture.manager.mu.Unlock()
	assertCodedFailure(base, "access_paused")
	fixture.manager.mu.Lock()
	session.paused = false
	session.boundOnly = true
	fixture.manager.mu.Unlock()
	session.publishReadCatalog()
	assertCodedFailure(base, "binding_changed")
}
