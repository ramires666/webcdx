package main

import "context"

type browserFrameReadContext struct{}
type browserFrameRead struct {
	tab    *browserTab
	epoch  uint64
	frames []map[string]any
}

func withBrowserFrameReads(ctx context.Context) context.Context {
	if ctx.Value(browserFrameReadContext{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, browserFrameReadContext{}, &browserFrameRead{})
}

func browserFrameEpoch(t *browserTab) uint64 {
	t.eventMu.Lock()
	defer t.eventMu.Unlock()
	return t.frameEpoch
}
