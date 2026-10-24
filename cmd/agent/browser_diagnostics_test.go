package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBrowserDiagnosticsCorrelationAndRedaction(t *testing.T) {
	b := &browserManager{}
	first, second := browserID("bop_"), browserID("bop_")
	tab := &browserTab{lastOperation: first}
	b.recordBrowserEvent(tab, "Network.requestWillBeSent", map[string]any{"requestId": "req-1", "request": map[string]any{"url": "https://provider.example/PRIVATE_SECRET?token=QUERY_SECRET", "headers": map[string]any{"Authorization": "HEADER_SECRET"}, "postData": "BODY_SECRET"}})
	tab.lastOperation = second
	b.recordBrowserEvent(tab, "Network.loadingFailed", map[string]any{"requestId": "req-1", "errorText": "net::ERR_TIMED_OUT", "canceled": false})
	b.recordBrowserEvent(tab, "Runtime.consoleAPICalled", map[string]any{"type": "error", "args": []any{map[string]any{"value": "CONSOLE_SECRET"}}})
	b.recordBrowserEvent(tab, "Network.requestWillBeSent", map[string]any{"requestId": "req-2", "request": map[string]any{"url": "https://provider.example/OTHER_SECRET"}})
	b.recordBrowserEvent(tab, "Network.responseReceived", map[string]any{"requestId": "req-2", "response": map[string]any{"status": float64(500), "url": "https://provider.example/PATH_SECRET"}})
	diagnostics := browserDiagnostics(tab, "https://page.example/ACCOUNT_SECRET", first)
	events := diagnostics["events"].([]map[string]any)
	if len(events) != 1 || events[0]["operation_id"] != first || events[0]["failure_type"] != "timeout" || events[0]["duration_ms"] == nil || events[0]["url"] != "https://provider.example/" {
		t.Fatalf("lost correlation: %v", events)
	}
	encoded, _ := json.Marshal(browserDiagnostics(tab, "https://page.example/ACCOUNT_SECRET", ""))
	if strings.Contains(string(encoded), "SECRET") {
		t.Fatalf("diagnostics leaked data: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"http_status":500`) {
		t.Fatal("HTTP failure lost status")
	}
}

func TestBrowserDiagnosticsRetentionAndSuccessfulRequestCleanup(t *testing.T) {
	b := &browserManager{}
	tab := &browserTab{}
	for i := 0; i < 300; i++ {
		b.recordBrowserEvent(tab, "Network.requestWillBeSent", map[string]any{"requestId": fmt.Sprint(i)})
	}
	if len(tab.requests) != 256 {
		t.Fatal("request trace is not bounded")
	}
	for id := range tab.requests {
		b.recordBrowserEvent(tab, "Network.loadingFinished", map[string]any{"requestId": id})
	}
	if len(tab.requests) != 0 {
		t.Fatal("successful requests retained")
	}
	tab.requests["expired"] = browserRequestTrace{At: time.Now().Add(-3 * time.Minute)}
	for i := 0; i < 101; i++ {
		b.recordBrowserEvent(tab, "Runtime.consoleAPICalled", map[string]any{"type": "warning"})
	}
	if len(tab.requests) != 0 || len(tab.diagnostics) != 100 || tab.diagnosticsDropped != 1 {
		t.Fatal("retention limits failed")
	}
	if browserDiagnostics(tab, "", "")["truncated"] != true {
		t.Fatal("dropped events not reported")
	}
}
