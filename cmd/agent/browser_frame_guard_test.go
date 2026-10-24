package main

import "testing"

func TestBrowserFrameURLGuardRejectsStaleCachedPermission(t *testing.T) {
	f := newBrowserFixture(t, `<main>fixture ready</main>`)
	tab := f.b.sessions[f.session].tabs[f.tab]
	ctx := withBrowserFrameReads(f.ctx)
	if _, err := f.b.pageFrame(ctx, tab, ""); err != nil {
		t.Fatal(err)
	}
	cache := ctx.Value(browserFrameReadContext{}).(*browserFrameRead)
	oldFrames := cache.frames
	f.must("browser_navigate", map[string]any{"action": "url", "url": oldFrames[0]["url"].(string) + "?document=changed", "request_key": "frame-guard-navigation"})
	// Simulate missing invalidation: a cached permission for the prior URL must
	// never allow reading a new document through its newly-created world.
	cache.frames, cache.epoch = oldFrames, browserFrameEpoch(tab)
	var text string
	err := f.b.eval(ctx, tab, "", `document.body.innerText`, &text)
	browserErrorCode(t, err, "frame_changed")
	if text != "" {
		t.Fatalf("stale permission returned page contents: %q", text)
	}
}
