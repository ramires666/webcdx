package main

import (
	"strings"
	"testing"
	"time"
)

func snapshotHasReason(snapshot map[string]any, reason string) bool {
	switch reasons := snapshot["truncation_reasons"].(type) {
	case []any:
		for _, item := range reasons {
			if item == reason {
				return true
			}
		}
	case []string:
		for _, item := range reasons {
			if item == reason {
				return true
			}
		}
	}
	return false
}

func TestBrowserSnapshotLimitsAndScopedRead(t *testing.T) {
	f := newBrowserFixture(t, `<div id="large">`+strings.Repeat("long text ", 1800)+`</div>`+strings.Repeat(`<button>Item</button>`, 320)+`<section id="about"><h2>About</h2><button id="target">Target</button></section>`)
	snapshot := f.must("browser_snapshot", map[string]any{}).(map[string]any)
	if snapshot["truncated"] != true || !snapshotHasReason(snapshot, "text_limit") || !snapshotHasReason(snapshot, "element_limit") || !snapshotHasReason(snapshot, "accessibility_limit") {
		t.Fatalf("missing limits: %v", snapshot["truncation_reasons"])
	}
	if snapshot["frame_id"] == "" || snapshot["observed_at"] == nil || snapshot["document_generation"] == nil || snapshot["counts"] == nil {
		t.Fatal("snapshot metadata missing")
	}
	generation := snapshot["document_generation"]
	scoped := f.must("browser_snapshot", map[string]any{"container_selector": "#about"}).(map[string]any)
	if scoped["text"] != "About\nTarget" || scoped["truncated"] != false || scoped["accessibility_status"] != "omitted_for_scope" || scoped["document_generation"] != generation {
		t.Fatalf("scoped read leaked other sections: %v", scoped)
	}
	elements := scoped["elements"].([]any)
	f.must("browser_interact", map[string]any{"action": "click", "frame_id": scoped["frame_id"], "snapshot_id": scoped["snapshot_id"], "element_id": elements[0].(map[string]any)["element_id"], "request_key": "canonical-frame"})
	f.b.executor.maxResponse = 8192
	small := f.must("browser_snapshot", map[string]any{}).(map[string]any)
	if !snapshotHasReason(small, "response_budget") {
		t.Fatal("response budget reduction was hidden")
	}
}

func TestBrowserWaitForHydrationAndPermanentFailures(t *testing.T) {
	f := newBrowserFixture(t, `<div id="about" style="display:none">About section</div><input id="query" oninput="setTimeout(()=>document.querySelector('#suggestions').innerHTML='<div role=option>Docker</div>',300)"><div id="suggestions"></div><button class="ambiguous">Same</button><button class="ambiguous">Same</button><script>setTimeout(()=>document.querySelector('#about').style.display='block',1600)</script>`)
	f.must("browser_wait", map[string]any{"condition": "load"})
	first := f.must("browser_snapshot", map[string]any{}).(map[string]any)
	if strings.Contains(first["text"].(string), "About section") {
		t.Fatal("fixture hydrated before initial observation")
	}
	f.must("browser_wait", map[string]any{"condition": "element", "selector": "#about", "timeout_seconds": 5})
	f.must("browser_interact", map[string]any{"action": "fill", "selector": "#query", "text": "Docker", "request_key": "query"})
	f.must("browser_wait", map[string]any{"condition": "element", "role": "option", "name": "Docker", "timeout_seconds": 3})
	f.must("browser_wait", map[string]any{"condition": "value", "selector": "#query", "value": "Docker"})
	f.must("browser_wait", map[string]any{"condition": "element", "selector": "#missing", "state": "absent"})
	start := time.Now()
	_, err := f.call("browser_wait", map[string]any{"condition": "element", "selector": ".ambiguous", "timeout_seconds": 10})
	browserErrorCode(t, err, "ambiguous_locator")
	if time.Since(start) > 2*time.Second {
		t.Fatal("permanent locator error waited for timeout")
	}
	_, err = f.call("browser_wait", map[string]any{"condition": "element", "selector": "#never", "timeout_seconds": 1})
	browserErrorCode(t, err, "wait_timeout")
	f.must("browser_interact", map[string]any{"action": "fill", "selector": "#query", "text": "", "request_key": "clear"})
	f.must("browser_wait", map[string]any{"condition": "value", "selector": "#query", "value": ""})
}
