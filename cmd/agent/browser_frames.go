package main

import (
	"context"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

func (t *browserTab) frames(ctx context.Context) ([]map[string]any, error) {
	var tree struct {
		FrameTree map[string]any `json:"frameTree"`
	}
	if err := t.command(ctx, "Page.getFrameTree", map[string]any{}, &tree); err != nil {
		return nil, err
	}
	frames := []map[string]any{}
	known := map[string]bool{}
	var collect func(map[string]any)
	collect = func(node map[string]any) {
		frame, _ := node["frame"].(map[string]any)
		id, _ := frame["id"].(string)
		if id != "" && !known[id] {
			known[id] = true
			frames = append(frames, frame)
		}
		children, _ := node["childFrames"].([]any)
		for _, child := range children {
			object, _ := child.(map[string]any)
			collect(object)
		}
	}
	collect(tree.FrameTree)
	if t.personal != nil {
		var childFrames []map[string]any
		if err := t.personal.call(ctx, t.nativeTabID, "WebCodex.frames", map[string]any{}, &childFrames); err != nil {
			return nil, err
		}
		for _, frame := range childFrames {
			id, _ := frame["id"].(string)
			if id != "" && !known[id] && len(frames) < 64 {
				known[id] = true
				frames = append(frames, frame)
			}
		}
		return frames, nil
	}
	targets, err := chromedp.Targets(t.ctx)
	if err != nil {
		return nil, err
	}
	rootID := chromedp.FromContext(t.ctx).Target.TargetID
	for pass := 0; pass < 16; pass++ {
		added := false
		for _, info := range targets {
			if info.Type != "iframe" || known[string(info.TargetID)] || len(frames) >= 64 {
				continue
			}
			if info.ParentID != rootID && !known[string(info.ParentFrameID)] && !known[string(info.ParentID)] {
				continue
			}
			var child struct {
				FrameTree map[string]any `json:"frameTree"`
			}
			if err := t.commandFrame(ctx, string(info.TargetID), "Page.getFrameTree", map[string]any{}, &child); err != nil {
				continue
			}
			collect(child.FrameTree)
			added = true
		}
		if !added {
			break
		}
	}
	return frames, nil
}

func (t *browserTab) commandFrame(ctx context.Context, frameID, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if frameID == "" {
		return t.command(ctx, method, params, result)
	}
	if t.personal != nil {
		return t.personal.callFrame(ctx, t.nativeTabID, frameID, method, params, result)
	}
	t.eventMu.Lock()
	child := t.frameContexts[frameID]
	t.eventMu.Unlock()
	if child != nil && child.Err() == nil {
		browserMarkDispatch(ctx, method)
		if err := chromedp.FromContext(child).Target.Call(ctx, method, params, result); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return browserTransportFailure(err.Error())
		}
		return nil
	}
	targets, err := chromedp.Targets(t.ctx)
	if err != nil {
		return toolErr("disconnected", "cannot enumerate frame targets")
	}
	for _, info := range targets {
		if info.Type != "iframe" || string(info.TargetID) != frameID {
			continue
		}
		child, cancel := chromedp.NewContext(t.ctx, chromedp.WithTargetID(target.ID(info.TargetID)))
		stop := context.AfterFunc(ctx, cancel)
		err := chromedp.Do(child)
		stop()
		if err != nil {
			cancel()
			return toolErr("unknown_frame", "cannot attach iframe target")
		}
		t.eventMu.Lock()
		if t.frameContexts == nil {
			t.frameContexts = map[string]context.Context{}
		}
		if len(t.frameContexts) >= 64 {
			t.eventMu.Unlock()
			cancel()
			return toolErr("limit_reached", "iframe target limit")
		}
		t.frameContexts[frameID] = child
		t.eventMu.Unlock()
		browserMarkDispatch(ctx, method)
		if err := chromedp.FromContext(child).Target.Call(ctx, method, params, result); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return browserTransportFailure(err.Error())
		}
		return nil
	}
	return t.command(ctx, method, params, result)
}
