package main

import (
	"errors"
	"strings"
	"testing"
)

func TestBrowserScopedSnapshotWalk(t *testing.T) {
	unrelated := strings.Repeat(`<div class="unrelated"><span>outside</span></div>`, 2000)
	f := newBrowserFixture(t, unrelated+`<div class="duplicate">first</div><div class="duplicate">second</div><section id="scope"><button aria-label="Inside action">inside</button><div id="shadow-host"></div></section>`)
	tab := f.b.sessions[f.session].tabs[f.tab]

	instrument := `(()=>{
const selected=document.querySelector('#scope'),host=document.querySelector('#shadow-host');
const shadow=host.attachShadow({mode:'open'});shadow.innerHTML='<button aria-label="Shadow action">shadow</button>';
globalThis.__webcodexWalkCalls=[];
const docQuery=Document.prototype.querySelectorAll;
Document.prototype.querySelectorAll=function(selector){if(selector==='*')globalThis.__webcodexWalkCalls.push('document');return docQuery.call(this,selector)};
const elementQuery=Element.prototype.querySelectorAll;
Element.prototype.querySelectorAll=function(selector){if(selector==='*')globalThis.__webcodexWalkCalls.push(this===selected?'selected':'outside:'+this.tagName);return elementQuery.call(this,selector)};
const fragmentQuery=DocumentFragment.prototype.querySelectorAll;
DocumentFragment.prototype.querySelectorAll=function(selector){if(selector==='*')globalThis.__webcodexWalkCalls.push(this===shadow?'shadow':'other_fragment');return fragmentQuery.call(this,selector)};
return true;
})()`
	if err := f.b.eval(f.ctx, tab, "", instrument, nil); err != nil {
		t.Fatal(err)
	}

	var result map[string]any
	if err := f.b.eval(f.ctx, tab, "", browserSnapshotExpression("snapshot_scoped_walk", "#scope"), &result); err != nil {
		t.Fatal(err)
	}
	var walkCalls []string
	if err := f.b.eval(f.ctx, tab, "", `(()=>globalThis.__webcodexWalkCalls)()`, &walkCalls); err != nil {
		t.Fatal(err)
	}
	if strings.Join(walkCalls, ",") != "selected,shadow" {
		t.Fatalf("querySelectorAll('*') walk receivers = %v; want only selected container and its shadow root", walkCalls)
	}
	counts := result["counts"].(map[string]any)
	if counts["dom_visited"] != float64(4) || counts["elements_found"] != float64(2) || counts["elements_returned"] != float64(2) {
		t.Fatalf("scoped counts = %#v", counts)
	}
	elements := result["elements"].([]any)
	if len(elements) != 2 {
		t.Fatalf("scoped elements = %#v", elements)
	}
	want := []string{"Inside action", "Shadow action"}
	for i, element := range elements {
		item := element.(map[string]any)
		if item["name"] != want[i] || item["element_id"] != string(rune('0'+i)) {
			t.Fatalf("scoped element %d = %#v", i, item)
		}
	}
	if strings.Contains(result["text"].(string), "outside") {
		t.Fatal("scoped snapshot included text outside its container")
	}

	for _, tc := range []struct {
		selector string
		code     string
		matches  int
	}{
		{selector: "[", code: "invalid_selector"},
		{selector: ".duplicate", code: "ambiguous_locator", matches: 2},
	} {
		var ignored map[string]any
		err := f.b.eval(f.ctx, tab, "", browserSnapshotExpression("snapshot_scoped_error", tc.selector), &ignored)
		var coded *codedError
		if !errors.As(err, &coded) || coded.code != tc.code {
			t.Fatalf("selector %q error = %v; want %s", tc.selector, err, tc.code)
		}
		if tc.matches != 0 && coded.details["matches"] != tc.matches {
			t.Fatalf("selector %q matches = %v; want %v", tc.selector, coded.details["matches"], tc.matches)
		}
	}
}
