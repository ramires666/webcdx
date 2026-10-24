package main

import (
	"errors"
	"testing"
)

func containerScrollFixture(t *testing.T, body string) *browserFixture {
	t.Helper()
	return newBrowserFixture(t, body)
}

func TestBrowserContainerScrollVirtualizedRowsStayInsideContainer(t *testing.T) {
	f := containerScrollFixture(t, `<aside id="sidebar" style="position:fixed;left:0;top:0;width:100px;height:100px">Sidebar</aside><div id="list" style="position:absolute;left:120px;top:20px;width:320px;height:180px;overflow:auto;border:1px solid"><div id="rows" style="height:3000px;position:relative"></div></div><script>
const list=document.querySelector('#list'),rows=document.querySelector('#rows');
function render(start){rows.innerHTML=Array.from({length:Math.min(20,100-start)},(_,i)=>'<div class="row" data-key="row-'+(start+i)+'" style="position:absolute;top:'+((start+i)*30)+'px;height:30px">Row '+(start+i)+'</div>').join('')}
render(0);list.addEventListener('scroll',()=>setTimeout(()=>render(Math.floor(list.scrollTop/30)),300));
</script>`)
	var before struct {
		WindowY  float64 `json:"window_y"`
		SideLeft float64 `json:"side_left"`
		SideTop  float64 `json:"side_top"`
	}
	if err := f.b.eval(f.ctx, f.b.sessions[f.session].tabs[f.tab], "", `({window_y:window.scrollY,side_left:document.querySelector('#sidebar').getBoundingClientRect().left,side_top:document.querySelector('#sidebar').getBoundingClientRect().top})`, &before); err != nil {
		t.Fatal(err)
	}
	result, err := f.b.scrollBrowserContainer(f.ctx, f.b.sessions[f.session].tabs[f.tab], map[string]any{
		"container_selector": "#list", "record_selector": ".row", "id_attribute": "data-key", "y": float64(240),
	})
	if err != nil {
		t.Fatal(err)
	}
	data := result.(map[string]any)
	if data["scroll_moved"] != true || data["dom_changed"] != true || data["first_id"] != "row-8" || len(data["new_ids"].([]any)) == 0 {
		t.Fatalf("virtualized container did not report changed visible rows: %#v", data)
	}
	if data["history_end_verified"] != false {
		t.Fatalf("geometric scroll incorrectly claims history completeness: %#v", data)
	}
	var after struct {
		WindowY   float64 `json:"window_y"`
		SideLeft  float64 `json:"side_left"`
		SideTop   float64 `json:"side_top"`
		ScrollTop float64 `json:"scroll_top"`
	}
	if err := f.b.eval(f.ctx, f.b.sessions[f.session].tabs[f.tab], "", `({window_y:window.scrollY,side_left:document.querySelector('#sidebar').getBoundingClientRect().left,side_top:document.querySelector('#sidebar').getBoundingClientRect().top,scroll_top:document.querySelector('#list').scrollTop})`, &after); err != nil {
		t.Fatal(err)
	}
	if after.WindowY != before.WindowY || after.SideLeft != before.SideLeft || after.SideTop != before.SideTop || after.ScrollTop == 0 {
		t.Fatalf("scroll affected the page/sidebar or missed container: before=%+v after=%+v", before, after)
	}
	end, err := f.b.scrollBrowserContainer(f.ctx, f.b.sessions[f.session].tabs[f.tab], map[string]any{
		"container_selector": "#list", "record_selector": ".row", "id_attribute": "data-key", "y": float64(100000),
	})
	if err != nil {
		t.Fatal(err)
	}
	endData := end.(map[string]any)
	if endData["at_scroll_end"] != true || endData["history_end_verified"] != false {
		t.Fatalf("end of scroll geometry should not claim history completeness: %#v", endData)
	}
}

func TestBrowserContainerScrollRejectsBadSelectorsAndUnstableIDsBeforeMutation(t *testing.T) {
	f := containerScrollFixture(t, `<div id="first-pane" class="pane" style="height:80px;overflow:auto"><div class="row" data-key="same">One</div><div class="row" data-key="same">Two</div></div><div class="pane" style="height:80px;overflow:auto"></div>`)
	tab := f.b.sessions[f.session].tabs[f.tab]
	cases := []struct {
		name, container, row, attribute, want string
	}{
		{"invalid-selector", "[", ".row", "data-key", "invalid_selector"},
		{"ambiguous-container", ".pane", ".row", "data-key", "ambiguous_locator"},
		{"duplicate-record-ids", "#first-pane", ".row", "data-key", "ambiguous_record"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var before float64
			if err := f.b.eval(f.ctx, tab, "", `document.querySelector('.pane').scrollTop`, &before); err != nil {
				t.Fatal(err)
			}
			_, err := f.b.scrollBrowserContainer(f.ctx, tab, map[string]any{
				"container_selector": test.container, "record_selector": test.row, "id_attribute": test.attribute, "y": float64(50),
			})
			var coded *codedError
			if !errors.As(err, &coded) || coded.code != test.want {
				t.Fatalf("error = %v; want %s", err, test.want)
			}
			var after float64
			if err := f.b.eval(f.ctx, tab, "", `document.querySelector('.pane').scrollTop`, &after); err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatalf("invalid request mutated container scroll position: %v -> %v", before, after)
			}
		})
	}
}

func TestBrowserContainerScrollRejectsMissingStableRecordID(t *testing.T) {
	f := containerScrollFixture(t, `<div id="list" style="height:80px;overflow:auto"><div class="row">No stable key</div></div>`)
	tab := f.b.sessions[f.session].tabs[f.tab]
	_, err := f.b.scrollBrowserContainer(f.ctx, tab, map[string]any{"container_selector": "#list", "record_selector": ".row", "y": float64(30)})
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != "unstable_record" {
		t.Fatalf("error = %v; want unstable_record", err)
	}
	var scrollTop float64
	if err := f.b.eval(f.ctx, tab, "", `document.querySelector('#list').scrollTop`, &scrollTop); err != nil {
		t.Fatal(err)
	}
	if scrollTop != 0 {
		t.Fatalf("unstable record request scrolled before rejection: %v", scrollTop)
	}
}
