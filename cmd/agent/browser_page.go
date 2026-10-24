package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type browserUserGesture struct{}

func (b *browserManager) pageFrame(ctx context.Context, t *browserTab, wanted string) (map[string]any, error) {
	frames, err := t.frames(ctx)
	if err != nil {
		return nil, err
	}
	var frame map[string]any
	for _, candidate := range frames {
		if wanted == "" || candidate["id"] == wanted {
			frame = candidate
			break
		}
	}
	if frame == nil {
		if wanted == "" {
			return nil, toolErr("navigation_pending", browserFailureMessage("navigation_pending"))
		}
		return nil, toolErr("unknown_frame", "frame does not belong to this tab")
	}
	u, _ := frame["url"].(string)
	if binding, _ := ctx.Value(browserTargetContext{}).(*browserTargetBinding); binding != nil && wanted == "" {
		observed, _ := canonicalBrowserExistingURL(u)
		expected, _ := canonicalBrowserExistingURL(binding.URL)
		if observed != expected {
			return nil, toolErr("binding_changed", browserFailureMessage("binding_changed"))
		}
	}
	if u != "about:blank" {
		if err := b.checkURL(u); err != nil {
			return nil, err
		}
	}
	return frame, nil
}

func (b *browserManager) eval(ctx context.Context, t *browserTab, frameID, expression string, result any) (evalErr error) {
	frame, err := b.pageFrame(ctx, t, frameID)
	if err != nil {
		return err
	}
	id, _ := frame["id"].(string)
	defer func() {
		if evalErr != nil {
			failure := browserErrorContext(evalErr, nil).(*codedError)
			if len(id) <= 128 {
				failure.details["frame_id"] = id
			}
			evalErr = failure
		}
	}()
	t.eventMu.Lock()
	world := t.worlds[id]
	epoch := t.frameEpoch
	t.eventMu.Unlock()
	if world == 0 {
		var res struct {
			ID int `json:"executionContextId"`
		}
		if err := t.commandFrame(ctx, id, "Page.createIsolatedWorld", map[string]any{"frameId": id, "worldName": "webcodex"}, &res); err != nil {
			return err
		}
		world = res.ID
		t.eventMu.Lock()
		t.worlds[id] = world
		t.eventMu.Unlock()
	}
	var response struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	url, _ := frame["url"].(string)
	guarded := `(()=>{if(location.href!==` + browserJS(url) + `){const e=new Error();e.webcodexCode='frame_changed';throw e}return (` + expression + `)})()`
	if err := t.commandFrame(ctx, id, "Runtime.evaluate", map[string]any{"expression": browserEvalExpression(guarded), "contextId": world, "returnByValue": true, "awaitPromise": true, "silent": true, "userGesture": ctx.Value(browserUserGesture{}) == true}, &response); err != nil {
		var coded *codedError
		if errors.As(err, &coded) && coded.code == "frame_changed" {
			t.eventMu.Lock()
			delete(t.worlds, id)
			t.snapshot = ""
			t.eventMu.Unlock()
		}
		return err
	}
	if browserFrameEpoch(t) != epoch {
		return toolErr("frame_changed", browserFailureMessage("frame_changed"))
	}
	if len(response.Exception) > 0 {
		return toolErr("page_evaluation_failed", browserFailureMessage("page_evaluation_failed"))
	}
	return browserEvalResult(response.Result.Value, result)
}

const browserDOMBase = browserFormDOM + `
const all=[];
function walk(root){for(const el of root.querySelectorAll('*')){if(all.length>=10000)break;all.push(el);if(el.shadowRoot)walk(el.shadowRoot)}}
function role(el){return el.getAttribute('role')||({A:'link',BUTTON:'button',SELECT:'combobox',TEXTAREA:'textbox'}[el.tagName])||(el.tagName==='INPUT'?(el.type==='checkbox'?'checkbox':el.type==='radio'?'radio':'textbox'):el.isContentEditable?'textbox':'')}
function labelText(label){const copy=label.cloneNode(true);for(const child of copy.querySelectorAll('input,select,textarea,button,[contenteditable]'))child.remove();return copy.textContent.trim()}
function name(el){return (el.getAttribute('aria-label')||(el.getAttribute('aria-labelledby')||'').split(/\s+/).map(id=>document.getElementById(id)?.textContent||'').join(' ').trim()||(el.labels&&[...el.labels].map(labelText).join(' '))||el.getAttribute('placeholder')||el.innerText||el.getAttribute('alt')||'').trim().slice(0,256)}
`

const browserDOM = browserDOMBase + `walk(document);`

func browserJS(value any) string { data, _ := json.Marshal(value); return string(data) }

func (b *browserManager) snapshot(ctx context.Context, t *browserTab, args map[string]any) (any, error) {
	ctx = withBrowserFrameReads(ctx)
	frameID, _ := optionalString(args, "frame_id")
	id := browserID("snapshot_")
	container, _ := optionalString(args, "container_selector")
	page, err := b.pageFrame(ctx, t, frameID)
	if err != nil {
		return nil, err
	}
	canonicalFrame, _ := page["id"].(string)
	expression := browserSnapshotExpression(id, container)
	var result map[string]any
	if err := b.eval(ctx, t, frameID, expression, &result); err != nil {
		return nil, err
	}
	u, _ := result["url"].(string)
	result["url"] = safeBrowserURL(u)
	result["frame_id"] = canonicalFrame
	result["observed_at"] = time.Now().UTC().Format(timeFormat)
	frames := []map[string]any{}
	if available, err := t.frames(ctx); err == nil {
		for _, f := range available {
			url, _ := f["url"].(string)
			frames = append(frames, map[string]any{"frame_id": f["id"], "url": safeBrowserURL(url)})
		}
	}
	result["frames"] = frames
	var ax struct {
		Nodes []struct {
			ID   string `json:"nodeId"`
			Role struct {
				Value string `json:"value"`
			} `json:"role"`
			Name struct {
				Value string `json:"value"`
			} `json:"name"`
			Children []string `json:"childIds"`
		} `json:"nodes"`
	}
	accessibility := []map[string]any{}
	axParams := map[string]any{}
	if frameID != "" {
		axParams["frameId"] = frameID
	}
	if container == "" && args["detail"] != "light" {
		if err := t.command(ctx, "Accessibility.getFullAXTree", axParams, &ax); err == nil {
			result["accessibility_status"] = "available"
			if len(ax.Nodes) > 200 {
				snapshotReason(result, "accessibility_limit")
			}
			for _, node := range ax.Nodes[:min(200, len(ax.Nodes))] {
				name, _ := truncateUTF8(node.Name.Value, 256)
				accessibility = append(accessibility, map[string]any{"node_id": node.ID, "role": node.Role.Value, "name": name, "children": node.Children})
			}
		}
	}
	if result["accessibility_status"] == nil {
		result["accessibility_status"] = "unavailable"
		if container != "" {
			result["accessibility_status"] = "omitted_for_scope"
		} else if args["detail"] == "light" {
			result["accessibility_status"] = "omitted_by_request"
		}
	}
	result["accessibility"] = accessibility
	counts := result["counts"].(map[string]any)
	counts["ax_found"] = len(ax.Nodes)
	t.eventMu.Lock()
	t.snapshot = id
	t.snapshotFrame = canonicalFrame
	t.snapshotMain = frameID == ""
	t.eventMu.Unlock()
	for {
		counts["elements_returned"] = len(result["elements"].([]any))
		counts["ax_returned"] = len(accessibility)
		encoded, _ := json.Marshal(result)
		if len(encoded) <= min(b.executor.maxResponse/4, 65536) {
			break
		}
		snapshotReason(result, "response_budget")
		if len(accessibility) > 0 {
			accessibility = accessibility[:len(accessibility)/2]
			result["accessibility"] = accessibility
			continue
		}
		elements, _ := result["elements"].([]any)
		if len(elements) > 0 {
			result["elements"] = elements[:len(elements)/2]
			continue
		}
		text, _ := result["text"].(string)
		if len(text) > 0 {
			result["text"], _ = truncateUTF8(text, len(text)/2)
			continue
		}
		return nil, toolErr("response_too_large", "response budget cannot fit page metadata")
	}
	return result, nil
}

func (b *browserManager) locator(t *browserTab, args map[string]any) (string, error) {
	css, _ := optionalString(args, "selector")
	role, _ := optionalString(args, "role")
	name, _ := optionalString(args, "name")
	element, _ := optionalString(args, "element_id")
	snapshot, _ := optionalString(args, "snapshot_id")
	frame, _ := optionalString(args, "frame_id")
	container, _ := optionalString(args, "container_selector")
	fieldContext, _ := optionalString(args, "context")
	if container != "" && fieldContext != "" {
		return "", toolErr("invalid_arguments", "choose container_selector or context")
	}
	count := 0
	for _, value := range []string{css, role, element} {
		if value != "" {
			count++
		}
	}
	if count != 1 || role != "" && name == "" {
		return "", toolErr("invalid_arguments", "choose one exact selector, role/name, or snapshot/element_id")
	}
	if element != "" {
		if container != "" || fieldContext != "" {
			return "", toolErr("invalid_arguments", "snapshot element IDs already identify their exact field")
		}
		t.eventMu.Lock()
		stale := snapshot == "" || snapshot != t.snapshot || (frame != t.snapshotFrame && !(frame == "" && t.snapshotMain))
		t.eventMu.Unlock()
		if stale {
			return "", toolErr("stale_snapshot", browserFailureMessage("stale_snapshot"))
		}
		return browserDOMFailure + `const snap=globalThis.__webcodex;if(!snap||snap.id!==` + browserJS(snapshot) + `||snap.document!==document||snap.url!==location.href)wcFail('stale_snapshot');const el=snap.nodes[Number(` + browserJS(element) + `)];if(!el||!el.isConnected)wcFail('element_detached');`, nil
	}
	query := `all.filter(el=>role(el)===` + browserJS(role) + `&&name(el)===` + browserJS(name) + `&&visible(el))`
	if css != "" {
		query = `all.filter(el=>el.matches(` + browserJS(css) + `))`
	}
	scope := ""
	if container != "" {
		scope = `let groups;try{groups=all.filter(el=>el.matches(` + browserJS(container) + `)&&visible(el))}catch(e){wcFail('invalid_selector')}if(!groups.length)wcFail('element_not_found',0);if(groups.length!==1)wcFail('ambiguous_locator',groups.length);matches=matches.filter(el=>groups[0].contains(el));`
	} else if fieldContext != "" {
		scope = `matches=matches.filter(el=>fieldContext(el)===` + browserJS(fieldContext) + `);`
	}
	return browserDOMFailure + browserDOM + `let matches;try{matches=` + query + `}catch(e){wcFail('invalid_selector')}` + scope + `if(!matches.length)wcFail('element_not_found',0);if(matches.length!==1)wcFail('ambiguous_locator',matches.length);const el=matches[0];`, nil
}

func (b *browserManager) interact(ctx context.Context, s *browserSession, t *browserTab, args map[string]any) (any, error) {
	ctx = context.WithValue(ctx, browserUserGesture{}, true)
	action, _ := requiredString(args, "action")
	if mode, _ := optionalString(args, "input_mode"); mode != "" {
		if action != "fill" {
			return nil, toolErr("invalid_arguments", "input_mode is only supported for fill")
		}
		if mode == "native" {
			if t.personal != nil && !t.personal.supports("native_text_v1") {
				return nil, toolErr("protocol_upgrade_required", browserFailureMessage("protocol_upgrade_required"))
			}
			return b.fillNative(ctx, t, args)
		}
	}
	frame, _ := optionalString(args, "frame_id")
	if action == "dialog" {
		accept, exists, err := optionalBool(args, "accept")
		if err != nil || !exists {
			return nil, toolErr("invalid_arguments", "accept is required for dialog")
		}
		text, _ := optionalString(args, "text")
		if err := t.command(ctx, "Page.handleJavaScriptDialog", map[string]any{"accept": accept, "promptText": text}, nil); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	}
	if action == "scroll" {
		x, err := optionalInt(args, "x", 0, -100000, 100000)
		if err != nil {
			return nil, err
		}
		y, err := optionalInt(args, "y", 0, -100000, 100000)
		if err != nil {
			return nil, err
		}
		err = b.eval(ctx, t, frame, fmt.Sprintf(`(()=>{window.scrollBy(%d,%d);return true})()`, x, y), nil)
		return map[string]any{}, err
	}
	if action == "coordinates" || action == "drag" {
		if frame != "" {
			return nil, toolErr("invalid_arguments", "coordinate actions require the main viewport")
		}
		x, err := optionalInt(args, "x", -1, 0, 100000)
		if err != nil {
			return nil, err
		}
		y, err := optionalInt(args, "y", -1, 0, 100000)
		if err != nil {
			return nil, err
		}
		width, err := optionalInt(args, "viewport_width", 0, 1, 100000)
		if err != nil {
			return nil, err
		}
		height, err := optionalInt(args, "viewport_height", 0, 1, 100000)
		if err != nil {
			return nil, err
		}
		var viewport struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		}
		if err := b.eval(ctx, t, "", `({width:innerWidth,height:innerHeight})`, &viewport); err != nil {
			return nil, err
		}
		if width != viewport.Width || height != viewport.Height || x >= width || y >= height || x < 0 || y < 0 {
			return nil, toolErr("viewport_changed", "coordinates do not match viewport")
		}
		endX, endY := x, y
		if action == "drag" {
			endX, err = optionalInt(args, "end_x", -1, 0, width-1)
			if err != nil || endX < 0 {
				return nil, toolErr("invalid_arguments", "end_x required")
			}
			endY, err = optionalInt(args, "end_y", -1, 0, height-1)
			if err != nil || endY < 0 {
				return nil, toolErr("invalid_arguments", "end_y required")
			}
		}
		if err := t.command(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": x, "y": y, "button": "left", "clickCount": 1}, nil); err != nil {
			return nil, err
		}
		if action == "drag" {
			for i := 1; i <= 10; i++ {
				if err := t.command(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x + (endX-x)*i/10, "y": y + (endY-y)*i/10, "buttons": 1}, nil); err != nil {
					return nil, err
				}
			}
			x, y = endX, endY
		}
		return map[string]any{}, t.command(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": x, "y": y, "button": "left", "clickCount": 1}, nil)
	}
	locator, err := b.locator(t, args)
	if err != nil {
		return nil, err
	}
	var info struct {
		Href     string `json:"href"`
		Password bool   `json:"password"`
	}
	if err := b.eval(ctx, t, frame, `(()=>{`+locator+`return {href:el.closest('a')?.href||'',password:el.type==='password'};})()`, &info); err != nil {
		return nil, err
	}
	if info.Href != "" {
		if err := b.checkURL(info.Href); err != nil {
			return nil, err
		}
	}
	text, _ := optionalString(args, "text")
	value, _ := optionalString(args, "value")
	script := ""
	switch action {
	case "click":
		if args["focus_policy"] == "activate" {
			if err := t.command(ctx, "Page.bringToFront", map[string]any{}, nil); err != nil {
				return nil, err
			}
		}
		x, y, err := b.elementPoint(ctx, t, frame, locator)
		if err != nil {
			return nil, err
		}
		if err := t.command(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y}, nil); err != nil {
			return nil, err
		}
		if err := t.command(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": x, "y": y, "button": "left", "clickCount": 1}, nil); err != nil {
			return nil, err
		}
		return map[string]any{}, t.command(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": x, "y": y, "button": "left", "clickCount": 1}, nil)
	case "fill":
		if info.Password {
			return map[string]any{"status": "waiting_user", "reason": "password_entry"}, nil
		}
		script = browserFillScript(text)
	case "select":
		by, _ := optionalString(args, "select_by")
		if by == "" {
			by = "auto"
		}
		var selected map[string]any
		if err := b.eval(ctx, t, frame, `(()=>{`+locator+`return `+browserSelectScript(value, by)+`})()`, &selected); err != nil {
			return nil, err
		}
		return selected, nil
	case "press":
		key, err := requiredString(args, "key")
		if err != nil {
			return nil, err
		}
		codes := map[string]int{"Enter": 13, "Tab": 9, "Escape": 27, "Backspace": 8, "Delete": 46, "ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40, "Space": 32}
		code, ok := codes[key]
		if !ok {
			return nil, toolErr("invalid_arguments", "unsupported key")
		}
		if err := b.eval(ctx, t, frame, `(()=>{`+locator+`el.focus({preventScroll:true});return true})()`, nil); err != nil {
			return nil, err
		}
		if err := t.command(ctx, "Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": key, "windowsVirtualKeyCode": code}, nil); err != nil {
			return nil, err
		}
		return map[string]any{}, t.command(ctx, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": key, "windowsVirtualKeyCode": code}, nil)
	case "hover":
		x, y, err := b.elementPoint(ctx, t, frame, locator)
		if err != nil {
			return nil, err
		}
		return map[string]any{}, t.command(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y}, nil)
	default:
		return nil, toolErr("invalid_arguments", "unknown interaction")
	}
	return map[string]any{}, b.eval(ctx, t, frame, `(()=>{`+locator+script+`return true})()`, nil)
}

func (b *browserManager) elementPoint(ctx context.Context, t *browserTab, frame, locator string) (float64, float64, error) {
	ownerFrame := frame
	if frame != "" {
		frames, err := t.frames(ctx)
		if err != nil {
			return 0, 0, err
		}
		if len(frames) > 0 && frames[0]["id"] == frame {
			ownerFrame = ""
		}
	}
	if ownerFrame != "" {
		var owner struct {
			ID int `json:"backendNodeId"`
		}
		if err := t.command(ctx, "DOM.getFrameOwner", map[string]any{"frameId": ownerFrame}, &owner); err != nil {
			return 0, 0, err
		}
		if err := t.command(ctx, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": owner.ID}, nil); err != nil {
			return 0, 0, err
		}
	}
	var point struct{ X, Y float64 }
	if err := b.eval(ctx, t, frame, `(async()=>{`+locator+`if(el.disabled)wcFail('element_disabled');const before=el.getBoundingClientRect();if(before.top<0||before.left<0||before.bottom>innerHeight||before.right>innerWidth)el.scrollIntoView({block:'nearest',inline:'nearest'});await Promise.race([new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))),new Promise(resolve=>setTimeout(resolve,200))]);if(!el.isConnected)wcFail('element_detached');const r=el.getBoundingClientRect();if(!r.width||!r.height)wcFail('element_hidden');const x=r.x+r.width/2,y=r.y+r.height/2;let hit=document.elementFromPoint(x,y);while(hit?.shadowRoot){const inner=hit.shadowRoot.elementFromPoint(x,y);if(!inner||inner===hit)break;hit=inner}let covered=true;for(let node=hit;node;node=node.getRootNode()?.host){if(node===el||el.contains(node)){covered=false;break}}if(covered)wcFail('element_obscured');return {X:x,Y:y}})()`, &point); err != nil {
		return 0, 0, err
	}
	if ownerFrame != "" {
		var owner struct {
			ID int `json:"backendNodeId"`
		}
		if err := t.command(ctx, "DOM.getFrameOwner", map[string]any{"frameId": ownerFrame}, &owner); err != nil {
			return 0, 0, err
		}
		var box struct {
			Model struct {
				Content []float64 `json:"content"`
			} `json:"model"`
		}
		if err := t.command(ctx, "DOM.getBoxModel", map[string]any{"backendNodeId": owner.ID}, &box); err != nil {
			return 0, 0, err
		}
		if len(box.Model.Content) < 2 {
			return 0, 0, toolErr("browser_failed", "iframe geometry unavailable")
		}
		point.X += box.Model.Content[0]
		point.Y += box.Model.Content[1]
	}
	return point.X, point.Y, nil
}

func (b *browserManager) tabAction(ctx context.Context, name string, s *browserSession, t *browserTab, args map[string]any) (any, error) {
	ctx = withBrowserFrameReads(ctx)
	frame, _ := optionalString(args, "frame_id")
	page, err := b.pageFrame(ctx, t, frame)
	if err != nil {
		return nil, err
	}
	u, _ := page["url"].(string)
	switch name {
	case "browser_navigate":
		action, _ := requiredString(args, "action")
		t.eventMu.Lock()
		t.snapshot = ""
		t.worlds = map[string]int{}
		t.eventMu.Unlock()
		switch action {
		case "url":
			raw, err := requiredString(args, "url")
			if err != nil {
				return nil, err
			}
			if err := b.checkURL(raw); err != nil {
				return nil, err
			}
			var nav struct {
				Error string `json:"errorText"`
			}
			err = t.command(ctx, "Page.navigate", map[string]any{"url": raw}, &nav)
			if err != nil {
				return nil, err
			}
			if nav.Error != "" {
				return nil, toolErr("navigation_failed", "browser could not load URL")
			}
		case "reload":
			if err := t.command(ctx, "Page.reload", map[string]any{}, nil); err != nil {
				return nil, err
			}
		case "back", "forward":
			var history struct {
				Index   int `json:"currentIndex"`
				Entries []struct {
					ID  int    `json:"id"`
					URL string `json:"url"`
				} `json:"entries"`
			}
			if err := t.command(ctx, "Page.getNavigationHistory", map[string]any{}, &history); err != nil {
				return nil, err
			}
			index := history.Index - 1
			if action == "forward" {
				index = history.Index + 1
			}
			if index < 0 || index >= len(history.Entries) {
				return nil, toolErr("no_history", "no navigation history entry")
			}
			entry := history.Entries[index]
			if err := b.checkURL(entry.URL); err != nil {
				return nil, err
			}
			if err := t.command(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": entry.ID}, nil); err != nil {
				return nil, err
			}
		default:
			return nil, toolErr("invalid_arguments", "unknown navigation action")
		}
		result, err := b.wait(ctx, t, map[string]any{"condition": "load", "timeout_seconds": float64(20)})
		if err != nil {
			return nil, err
		}
		object := result.(map[string]any)
		page, err := b.pageFrame(ctx, t, "")
		if err != nil {
			return nil, err
		}
		u, _ = page["url"].(string)
		object["url"] = safeBrowserURL(u)
		return object, nil
	case "browser_snapshot":
		return b.snapshot(ctx, t, args)
	case "browser_screenshot":
		return b.screenshot(ctx, s, t, args)
	case "browser_interact":
		return b.verifiedInteract(ctx, s, t, args)
	case "browser_wait":
		return b.wait(ctx, t, args)
	case "browser_diagnostics":
		operation, _ := optionalString(args, "operation_id")
		return browserDiagnostics(t, u, operation), nil
	case "browser_files":
		return b.files(ctx, s, t, args)
	}
	return nil, toolErr("unknown_tool", "unknown browser tool")
}
