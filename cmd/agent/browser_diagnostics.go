package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

type browserRequestTrace struct {
	At             time.Time
	Operation, URL string
}

// Origins suffice for correlating provider failures; paths may contain secrets.
func browserDiagnosticURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/"
}

func browserNetworkFailure(raw string, canceled bool) string {
	if canceled {
		return "cancelled"
	}
	switch raw {
	case "net::ERR_TIMED_OUT", "net::ERR_CONNECTION_TIMED_OUT", "timeout":
		return "timeout"
	case "net::ERR_NAME_NOT_RESOLVED", "dns":
		return "dns"
	case "net::ERR_CONNECTION_RESET", "net::ERR_CONNECTION_REFUSED", "net::ERR_CONNECTION_CLOSED", "net::ERR_INTERNET_DISCONNECTED", "connection":
		return "connection"
	case "net::ERR_BLOCKED_BY_CLIENT", "net::ERR_BLOCKED_BY_RESPONSE", "blocked":
		return "blocked"
	case "cancelled":
		return "cancelled"
	default:
		return "other"
	}
}

func (b *browserManager) recordBrowserEvent(t *browserTab, method string, event map[string]any) {
	t.eventMu.Lock()
	defer t.eventMu.Unlock()
	now := time.Now().UTC()
	if t.requests == nil {
		t.requests = map[string]browserRequestTrace{}
	}
	for key, request := range t.requests {
		if now.Sub(request.At) > 2*time.Minute {
			delete(t.requests, key)
		}
	}
	id, _ := event["requestId"].(string)
	if len(id) > 160 || strings.ContainsAny(id, "\r\n\x00") {
		id = ""
	}
	switch method {
	case "Page.frameNavigated", "Page.navigatedWithinDocument", "Runtime.executionContextsCleared", "Runtime.executionContextDestroyed", "WebCodex.detached":
		t.frameEpoch++
		t.snapshot = ""
		t.worlds = map[string]int{}
		if method == "WebCodex.detached" {
			t.requests = map[string]browserRequestTrace{}
		}
		return
	case "Page.javascriptDialogOpening":
		t.dialog = true
		return
	case "Page.javascriptDialogClosed":
		t.dialog = false
		return
	case "Network.requestWillBeSent":
		if id == "" {
			return
		}
		if len(t.requests) >= 256 {
			oldKey := ""
			var oldest time.Time
			for key, request := range t.requests {
				if oldKey == "" || request.At.Before(oldest) {
					oldKey = key
					oldest = request.At
				}
			}
			delete(t.requests, oldKey)
		}
		request, _ := event["request"].(map[string]any)
		raw, _ := request["url"].(string)
		t.requests[id] = browserRequestTrace{now, t.lastOperation, browserDiagnosticURL(raw)}
		return
	case "Network.loadingFinished":
		delete(t.requests, id)
		return
	case "Runtime.exceptionThrown", "Runtime.consoleAPICalled", "Network.loadingFailed", "Network.responseReceived":
	default:
		return
	}
	response, _ := event["response"].(map[string]any)
	status, _ := response["status"].(float64)
	if method == "Runtime.consoleAPICalled" && event["type"] != "error" && event["type"] != "warning" || method == "Network.responseReceived" && status < 400 {
		return
	}
	entry := map[string]any{"event": method, "at": now.Format(timeFormat)}
	if method == "Network.loadingFailed" || method == "Network.responseReceived" {
		if id != "" {
			entry["request_id"] = id
		}
		if request, ok := t.requests[id]; ok {
			entry["duration_ms"] = now.Sub(request.At).Milliseconds()
			entry["url"] = request.URL
			if request.Operation != "" {
				entry["operation_id"] = request.Operation
			}
		}
	} else if t.lastOperation != "" {
		entry["operation_id"] = t.lastOperation
	}
	if raw, ok := response["url"].(string); ok {
		entry["url"] = browserDiagnosticURL(raw)
	}
	if status >= 400 {
		entry["http_status"] = int(status)
		entry["detail"] = fmt.Sprintf("HTTP %.0f", status)
	}
	if method == "Network.loadingFailed" {
		canceled, _ := event["canceled"].(bool)
		raw, _ := event["errorText"].(string)
		if kind, ok := event["failure_type"].(string); ok {
			raw = kind
		}
		entry["failure_type"] = browserNetworkFailure(raw, canceled)
		entry["canceled"] = canceled
		delete(t.requests, id)
	}
	if len(t.diagnostics) >= 100 {
		t.diagnostics = t.diagnostics[1:]
		t.diagnosticsDropped++
	}
	t.diagnostics = append(t.diagnostics, entry)
}

func browserDiagnostics(t *browserTab, rawURL, operation string) map[string]any {
	t.eventMu.Lock()
	defer t.eventMu.Unlock()
	events := []map[string]any{}
	for _, event := range t.diagnostics {
		if operation == "" || event["operation_id"] == operation {
			events = append(events, event)
		}
	}
	return map[string]any{"url": browserDiagnosticURL(rawURL), "events": events, "truncated": t.diagnosticsDropped > 0, "dropped_events": t.diagnosticsDropped, "tracked_requests": len(t.requests)}
}
