package main

import (
	"testing"
)

func nativeInputFixture(t *testing.T) *browserFixture {
	t.Helper()
	f := newBrowserFixture(t, `<style>
#covered{position:fixed;left:10px;bottom:10px;width:120px;height:24px;z-index:1}
#shield{position:fixed;left:0;bottom:0;width:180px;height:80px;background:#f00;z-index:2}
#hidden{display:none}
</style>
<input id="native" value="before" data-track="native">
<textarea id="multi" data-track="multi">old text</textarea>
<div id="rich" contenteditable="true" data-track="rich">old rich text</div>
<input id="password" type="password" value="keep-secret" data-track="password">
<input id="disabled" disabled value="keep-disabled" data-track="disabled">
<input id="readonly" readonly value="keep-readonly" data-track="readonly">
<input id="number" type="number" value="4" data-track="number">
<input id="covered" value="keep-covered" data-track="covered"><div id="shield"></div>
<input id="hidden" value="keep-hidden" data-track="hidden">
<div id="autosave"></div><div id="events"></div>
<script>
for(const el of document.querySelectorAll('[data-track]'))el.addEventListener('input',()=>{const id=el.id,counter=document.querySelector('#events');counter.setAttribute(id,String(Number(counter.getAttribute(id)||0)+1));document.querySelector('#autosave').textContent=el.value??el.innerText});
</script>`)
	return f
}

func nativeInputEval(t *testing.T, f *browserFixture, expression string, result any) {
	t.Helper()
	tab := f.b.sessions[f.session].tabs[f.tab]
	if err := f.b.eval(f.ctx, tab, "", expression, result); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserNativeInputUnicodeEventsUndoAndMultiline(t *testing.T) {
	f := nativeInputFixture(t)
	tab := f.b.sessions[f.session].tabs[f.tab]
	text := "Привет, мир 🌍"
	result, err := f.b.fillNative(f.ctx, tab, map[string]any{"selector": "#native", "text": text})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["confirmation"] != "control_value_verified" {
		t.Fatalf("native fill result = %#v", result)
	}
	var state map[string]any
	nativeInputEval(t, f, `(()=>({value:document.querySelector('#native').value,events:Number(document.querySelector('#events').getAttribute('native')||0),autosave:document.querySelector('#autosave').textContent}))()`, &state)
	if state["value"] != text || state["autosave"] != text || state["events"].(float64) < 1 {
		t.Fatalf("native input/autosave state = %#v", state)
	}

	for _, event := range []map[string]any{
		{"type": "keyDown", "key": "Control", "code": "ControlLeft", "windowsVirtualKeyCode": 17, "nativeVirtualKeyCode": 17, "modifiers": 2},
		{"type": "keyDown", "key": "z", "code": "KeyZ", "windowsVirtualKeyCode": 90, "nativeVirtualKeyCode": 90, "modifiers": 2},
		{"type": "keyUp", "key": "z", "code": "KeyZ", "windowsVirtualKeyCode": 90, "nativeVirtualKeyCode": 90, "modifiers": 2},
		{"type": "keyUp", "key": "Control", "code": "ControlLeft", "windowsVirtualKeyCode": 17, "nativeVirtualKeyCode": 17},
	} {
		if err := tab.command(f.ctx, "Input.dispatchKeyEvent", event, nil); err != nil {
			t.Fatal(err)
		}
	}
	nativeInputEval(t, f, `(()=>({value:document.querySelector('#native').value,autosave:document.querySelector('#autosave').textContent}))()`, &state)
	if state["value"] != "before" || state["autosave"] != "before" {
		t.Fatalf("Ctrl+Z did not restore input state: %#v", state)
	}

	textarea := "first line\nвторая строка"
	if _, err := f.b.fillNative(f.ctx, tab, map[string]any{"selector": "#multi", "text": textarea}); err != nil {
		t.Fatal(err)
	}
	rich := "строка один\nстрока два 🌱"
	if _, err := f.b.fillNative(f.ctx, tab, map[string]any{"selector": "#rich", "text": rich}); err != nil {
		t.Fatal(err)
	}
	nativeInputEval(t, f, `(()=>({textarea:document.querySelector('#multi').value,rich:document.querySelector('#rich').innerText,events:{multi:document.querySelector('#events').getAttribute('multi'),rich:document.querySelector('#events').getAttribute('rich')}}))()`, &state)
	if state["textarea"] != textarea || state["rich"] != rich {
		t.Fatalf("multiline native controls = %#v", state)
	}
	events := state["events"].(map[string]any)
	for _, id := range []string{"multi", "rich"} {
		if events[id] == nil || events[id] == "0" {
			t.Fatalf("%s input event was not fired: %#v", id, events)
		}
	}
}

func TestBrowserNativeInputThroughBrowserCall(t *testing.T) {
	f := nativeInputFixture(t)
	text := "Привет, мир 🌍 — проверка native input"
	result, err := f.call("browser_interact", map[string]any{
		"action": "fill", "selector": "#native", "text": text,
		"input_mode": "native", "request_key": "native-fill-through-call",
	})
	if err != nil {
		t.Fatal(err)
	}
	object, ok := result.(map[string]any)
	if !ok || object["confirmation"] != "control_value_verified" {
		t.Fatalf("integrated native fill result = %#v", result)
	}
	var state map[string]any
	nativeInputEval(t, f, `(()=>({value:document.querySelector('#native').value,events:Number(document.querySelector('#events').getAttribute('native')||0),autosave:document.querySelector('#autosave').textContent}))()`, &state)
	if state["value"] != text || state["autosave"] != text || state["events"].(float64) < 1 {
		t.Fatalf("integrated native fill state = %#v", state)
	}
	tab := f.b.sessions[f.session].tabs[f.tab]
	for _, event := range []map[string]any{
		{"type": "keyDown", "key": "Control", "code": "ControlLeft", "windowsVirtualKeyCode": 17, "nativeVirtualKeyCode": 17, "modifiers": 2},
		{"type": "keyDown", "key": "z", "code": "KeyZ", "windowsVirtualKeyCode": 90, "nativeVirtualKeyCode": 90, "modifiers": 2},
		{"type": "keyUp", "key": "z", "code": "KeyZ", "windowsVirtualKeyCode": 90, "nativeVirtualKeyCode": 90, "modifiers": 2},
		{"type": "keyUp", "key": "Control", "code": "ControlLeft", "windowsVirtualKeyCode": 17, "nativeVirtualKeyCode": 17},
	} {
		if err := tab.command(f.ctx, "Input.dispatchKeyEvent", event, nil); err != nil {
			t.Fatal(err)
		}
	}
	nativeInputEval(t, f, `(()=>({value:document.querySelector('#native').value,autosave:document.querySelector('#autosave').textContent}))()`, &state)
	if state["value"] != "before" || state["autosave"] != "before" {
		t.Fatalf("integrated Ctrl+Z did not restore prior value: %#v", state)
	}
}

func TestBrowserNativeInputEmptyTextDeletesSelectedContent(t *testing.T) {
	f := nativeInputFixture(t)
	tab := f.b.sessions[f.session].tabs[f.tab]
	for _, selector := range []string{"#native", "#multi", "#rich"} {
		result, err := f.b.fillNative(f.ctx, tab, map[string]any{"selector": selector, "text": ""})
		if err != nil {
			t.Fatalf("empty native fill for %s: %v", selector, err)
		}
		if result.(map[string]any)["confirmation"] != "control_value_verified" {
			t.Fatalf("empty native fill confirmation for %s = %#v", selector, result)
		}
	}
	var state map[string]any
	nativeInputEval(t, f, `(()=>({input:document.querySelector('#native').value,textarea:document.querySelector('#multi').value,rich:document.querySelector('#rich').innerText}))()`, &state)
	if state["input"] != "" || state["textarea"] != "" || state["rich"] != "" {
		t.Fatalf("empty insert did not delete selected content: %#v", state)
	}
}

func TestBrowserNativeInputGuardsHaveNoEffects(t *testing.T) {
	f := nativeInputFixture(t)
	tab := f.b.sessions[f.session].tabs[f.tab]
	password, err := f.b.fillNative(f.ctx, tab, map[string]any{"selector": "#password", "text": "must-not-enter"})
	if err != nil {
		t.Fatal(err)
	}
	if password.(map[string]any)["status"] != "waiting_user" || password.(map[string]any)["reason"] != "password_entry" {
		t.Fatalf("password result = %#v", password)
	}
	for _, tc := range []struct{ selector, code string }{
		{"#disabled", "element_disabled"},
		{"#readonly", "element_not_editable"},
		{"#number", "invalid_control_type"},
		{"#covered", "element_obscured"},
		{"#hidden", "element_hidden"},
	} {
		_, err := f.b.fillNative(f.ctx, tab, map[string]any{"selector": tc.selector, "text": "must-not-enter"})
		browserErrorCode(t, err, tc.code)
	}
	var state map[string]any
	nativeInputEval(t, f, `(()=>({password:document.querySelector('#password').value,disabled:document.querySelector('#disabled').value,readonly:document.querySelector('#readonly').value,number:document.querySelector('#number').value,covered:document.querySelector('#covered').value,hidden:document.querySelector('#hidden').value,events:{password:document.querySelector('#events').getAttribute('password'),disabled:document.querySelector('#events').getAttribute('disabled'),readonly:document.querySelector('#events').getAttribute('readonly'),number:document.querySelector('#events').getAttribute('number'),covered:document.querySelector('#events').getAttribute('covered'),hidden:document.querySelector('#events').getAttribute('hidden')}}))()`, &state)
	for key, want := range map[string]string{
		"password": "keep-secret", "disabled": "keep-disabled", "readonly": "keep-readonly",
		"number": "4", "covered": "keep-covered", "hidden": "keep-hidden",
	} {
		if state[key] != want {
			t.Errorf("guard changed %s: got %v, want %q", key, state[key], want)
		}
	}
	for id, count := range state["events"].(map[string]any) {
		if count != nil && count != "0" {
			t.Errorf("guard caused %s input event: %v", id, count)
		}
	}
}
