package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureElement(t *testing.T, snapshot map[string]any, context string) map[string]any {
	t.Helper()
	for _, raw := range snapshot["elements"].([]any) {
		el := raw.(map[string]any)
		if el["context"] == context && el["role"] == "combobox" {
			return el
		}
	}
	t.Fatalf("combobox in %s not found", context)
	return nil
}

func TestBrowserSelectOptionsAndFieldContext(t *testing.T) {
	f := newBrowserFixture(t, `<fieldset id="start"><legend>Start date</legend><label>Month<select><option value="">Choose</option><option value="10">October</option><option value="11">November</option><option value="12" disabled>December</option></select></label></fieldset><fieldset id="end"><legend>End date</legend><label>Month<select><option value="">Choose</option><option value="10">October</option></select></label></fieldset><fieldset style="display:none"><label>Month<select><option>Hidden</option></select></label></fieldset><select id="collision"><option value="11">10</option><option value="10">Other</option></select><select id="duplicates"><option value="a">Same</option><option value="b">Same</option></select><select id="reset" onchange="this.value=''"><option value="">Choose</option><option value="10">October</option></select>`)
	snapshot := f.must("browser_snapshot", map[string]any{}).(map[string]any)
	start := fixtureElement(t, snapshot, "Start date")
	options := start["options"].([]any)
	if options[1].(map[string]any)["value"] != "10" || options[1].(map[string]any)["label"] != "October" || options[3].(map[string]any)["disabled"] != true {
		t.Fatalf("missing option map: %v", options)
	}
	selected := f.must("browser_interact", map[string]any{"action": "select", "role": "combobox", "name": "Month", "context": "Start date", "value": "October", "request_key": "october-label"}).(map[string]any)
	if selected["selected_value"] != "10" || selected["confirmation"] != "control_value_verified" {
		t.Fatalf("label selection failed: %v", selected)
	}
	snapshot = f.must("browser_snapshot", map[string]any{}).(map[string]any)
	if fixtureElement(t, snapshot, "End date")["value"] != "" {
		t.Fatal("start-date action changed end date")
	}
	f.must("browser_interact", map[string]any{"action": "select", "role": "combobox", "name": "Month", "container_selector": "#start", "value": "11", "request_key": "november-value"})
	selected = f.must("browser_interact", map[string]any{"action": "select", "selector": "#collision", "value": "10", "request_key": "value-precedence"}).(map[string]any)
	if selected["selected_label"] != "Other" {
		t.Fatal("label took precedence over exact value")
	}
	for _, tc := range []struct{ selector, value, by, code string }{
		{"#start select", "December", "label", "invalid_option_value"},
		{"#start select", "10", "label", "invalid_option_value"},
		{"#duplicates", "Same", "label", "ambiguous_locator"},
		{"#reset", "10", "value", "state_verification_failed"},
	} {
		_, err := f.call("browser_interact", map[string]any{"action": "select", "selector": tc.selector, "value": tc.value, "select_by": tc.by, "request_key": tc.selector + tc.value})
		failure := browserErrorCode(t, err, tc.code)
		if tc.code == "invalid_option_value" {
			if failure.details["options"] == nil {
				t.Fatal("invalid selection omitted allowed options")
			}
			data, err := os.ReadFile(filepath.Join(f.b.config.Dir, "operations", failure.details["operation_id"].(string)+".json"))
			if err != nil || strings.Contains(string(data), "October") {
				t.Fatal("option labels leaked into persistent failure journal")
			}
		}
		if tc.code == "state_verification_failed" && failure.details["recovery"] != "verify_outcome" {
			t.Fatal("state failure would encourage blind retry")
		}
	}
}

func TestBrowserContentEditableAndNativeFill(t *testing.T) {
	f := newBrowserFixture(t, `<div id="editor" contenteditable="true" aria-label="About">old</div><input id="native" aria-label="Name"><textarea id="readonly" readonly>old</textarea><input id="password" type="password" value="PRIVATE_SECRET"><span id="count">0</span><script>document.querySelector('#editor').addEventListener('input',()=>document.querySelector('#count').textContent=String(Number(document.querySelector('#count').textContent)+1))</script>`)
	for i, text := range []string{"literal <b>text</b>", "", "новый текст"} {
		f.must("browser_interact", map[string]any{"action": "fill", "selector": "#editor", "text": text, "request_key": string(rune('a' + i))})
		var actual string
		tab := f.b.sessions[f.session].tabs[f.tab]
		if err := f.b.eval(f.ctx, tab, "", `document.querySelector('#editor').innerText`, &actual); err != nil || actual != text {
			t.Fatalf("editor text = %q: %v", actual, err)
		}
	}
	f.must("browser_wait", map[string]any{"condition": "text", "text": "3"})
	f.must("browser_interact", map[string]any{"action": "fill", "selector": "#native", "text": "имя", "request_key": "native"})
	_, err := f.call("browser_interact", map[string]any{"action": "fill", "selector": "#readonly", "text": "bad", "request_key": "readonly"})
	browserErrorCode(t, err, "element_not_editable")
	waiting := f.must("browser_interact", map[string]any{"action": "fill", "selector": "#password", "text": "bad", "request_key": "password"}).(map[string]any)
	if waiting["status"] != "waiting_user" || waiting["confirmation"] != nil {
		t.Fatal("password entry was not reserved for user")
	}
}
