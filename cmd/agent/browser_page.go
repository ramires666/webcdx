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
		return nil, toolErr("unknown_frame", "frame does not belong to this tab")
	}
	u, _ := frame["url"].(string)
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
	if err := t.commandFrame(ctx, id, "Runtime.evaluate", map[string]any{"expression": browserEvalExpression(expression), "contextId": world, "returnByValue": true, "awaitPromise": true, "silent": true, "userGesture": ctx.Value(browserUserGesture{}) == true}, &response); err != nil {
		var coded *codedError
		if errors.As(err, &coded) && coded.code == "frame_changed" {
			t.eventMu.Lock()
			delete(t.worlds, id)
			t.snapshot = ""
			t.eventMu.Unlock()
		}
		return err
	}
	if len(response.Exception) > 0 {
		return toolErr("page_evaluation_failed", browserFailureMessage("page_evaluation_failed"))
	}
	return browserEvalResult(response.Result.Value, result)
}

const browserDOM = `
const all=[];
function walk(root){for(const el of root.querySelectorAll('*')){if(all.length>=10000)break;all.push(el);if(el.shadowRoot)walk(el.shadowRoot)}}
walk(document);
function role(el){return el.getAttribute('role')||({A:'link',BUTTON:'button',SELECT:'combobox',TEXTAREA:'textbox'}[el.tagName])||(el.tagName==='INPUT'?(el.type==='checkbox'?'checkbox':el.type==='radio'?'radio':el.type==='password'?'textbox':'textbox'):'')}
function name(el){return (el.getAttribute('aria-label')||(el.getAttribute('aria-labelledby')||'').split(/\s+/).map(id=>document.getElementById(id)?.textContent||'').join(' ').trim()||(el.labels&&[...el.labels].map(x=>x.innerText).join(' '))||el.getAttribute('placeholder')||el.innerText||el.getAttribute('alt')||'').trim().slice(0,256)}
`

func browserJS(value any) string { data, _ := json.Marshal(value); return string(data) }

func (b *browserManager) snapshot(ctx context.Context, t *browserTab, args map[string]any) (any, error) {
	frameID, _ := optionalString(args, "frame_id")
	id := browserID("snapshot_")
	expression := `(()=>{` + browserDOM + `
const id=` + browserJS(id) + `,nodes=all.filter(el=>role(el)&&el.getClientRects().length).slice(0,300);
globalThis.__webcodex={id,nodes,document,url:location.href};
return {snapshot_id:id,url:location.href,title:document.title,viewport_width:innerWidth,viewport_height:innerHeight,text:(document.body?.innerText||'').slice(0,12000),elements:nodes.map((el,i)=>({element_id:String(i),role:role(el),name:name(el),tag:el.tagName.toLowerCase(),disabled:!!el.disabled,href:el.tagName==='A'?el.href.split(/[?#]/)[0]:'',value:el.type==='password'?'':String(el.value??'').slice(0,256)})),truncated:all.length>=10000||nodes.length>=300||(document.body?.innerText||'').length>12000};})()`
	var result map[string]any
	if err := b.eval(ctx, t, frameID, expression, &result); err != nil {
		return nil, err
	}
	u, _ := result["url"].(string)
	result["url"] = safeBrowserURL(u)
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
	if err := t.command(ctx, "Accessibility.getFullAXTree", axParams, &ax); err == nil {
		for _, node := range ax.Nodes[:min(200, len(ax.Nodes))] {
			name, _ := truncateUTF8(node.Name.Value, 256)
			accessibility = append(accessibility, map[string]any{"node_id": node.ID, "role": node.Role.Value, "name": name, "children": node.Children})
		}
	}
	result["accessibility"] = accessibility
	t.eventMu.Lock()
	t.snapshot = id
	t.snapshotFrame = frameID
	t.eventMu.Unlock()
	for {
		encoded, _ := json.Marshal(result)
		if len(encoded) <= min(b.executor.maxResponse/4, 65536) {
			break
		}
		result["truncated"] = true
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
		t.eventMu.Lock()
		stale := snapshot == "" || snapshot != t.snapshot || frame != t.snapshotFrame
		t.eventMu.Unlock()
		if stale {
			return "", toolErr("stale_snapshot", browserFailureMessage("stale_snapshot"))
		}
		return browserDOMFailure + `const snap=globalThis.__webcodex;if(!snap||snap.id!==` + browserJS(snapshot) + `||snap.document!==document||snap.url!==location.href)wcFail('stale_snapshot');const el=snap.nodes[Number(` + browserJS(element) + `)];if(!el||!el.isConnected)wcFail('element_detached');`, nil
	}
	query := `all.filter(el=>role(el)===` + browserJS(role) + `&&name(el)===` + browserJS(name) + `)`
	if css != "" {
		query = `all.filter(el=>el.matches(` + browserJS(css) + `))`
	}
	return browserDOMFailure + browserDOM + `let matches;try{matches=` + query + `}catch(e){wcFail('invalid_selector')}if(!matches.length)wcFail('element_not_found',0);if(matches.length!==1)wcFail('ambiguous_locator',matches.length);const el=matches[0];`, nil
}

func (b *browserManager) interact(ctx context.Context, s *browserSession, t *browserTab, args map[string]any) (any, error) {
	ctx = context.WithValue(ctx, browserUserGesture{}, true)
	action, _ := requiredString(args, "action")
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
		if err := t.command(ctx, "Page.bringToFront", map[string]any{}, nil); err != nil {
			return nil, err
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
		script = `if(!['INPUT','TEXTAREA'].includes(el.tagName)||el.disabled||el.readOnly)throw Error('not editable');const proto=el.tagName==='INPUT'?HTMLInputElement.prototype:HTMLTextAreaElement.prototype;Object.getOwnPropertyDescriptor(proto,'value').set.call(el,` + browserJS(text) + `);el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));`
	case "select":
		script = `if(el.tagName!=='SELECT')wcFail('invalid_control_type');if(el.disabled)wcFail('element_disabled');if(![...el.options].some(x=>x.value===` + browserJS(value) + `&&!x.disabled&&!x.parentElement.disabled))wcFail('invalid_option_value');el.value=` + browserJS(value) + `;el.dispatchEvent(new Event('change',{bubbles:true}));`
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
		if err := b.eval(ctx, t, frame, `(()=>{`+locator+`el.focus();return true})()`, nil); err != nil {
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
	if frame != "" {
		var owner struct {
			ID int `json:"backendNodeId"`
		}
		if err := t.command(ctx, "DOM.getFrameOwner", map[string]any{"frameId": frame}, &owner); err != nil {
			return 0, 0, err
		}
		if err := t.command(ctx, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": owner.ID}, nil); err != nil {
			return 0, 0, err
		}
	}
	var point struct{ X, Y float64 }
	if err := b.eval(ctx, t, frame, `(async()=>{`+locator+`if(el.disabled)wcFail('element_disabled');el.scrollIntoView({block:'center'});await Promise.race([new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))),new Promise(resolve=>setTimeout(resolve,200))]);if(!el.isConnected)wcFail('element_detached');const r=el.getBoundingClientRect();if(!r.width||!r.height)wcFail('element_hidden');const x=r.x+r.width/2,y=r.y+r.height/2;let hit=document.elementFromPoint(x,y);while(hit?.shadowRoot){const inner=hit.shadowRoot.elementFromPoint(x,y);if(!inner||inner===hit)break;hit=inner}let covered=true;for(let node=hit;node;node=node.getRootNode()?.host){if(node===el||el.contains(node)){covered=false;break}}if(covered)wcFail('element_obscured');return {X:x,Y:y}})()`, &point); err != nil {
		return 0, 0, err
	}
	if frame != "" {
		var owner struct {
			ID int `json:"backendNodeId"`
		}
		if err := t.command(ctx, "DOM.getFrameOwner", map[string]any{"frameId": frame}, &owner); err != nil {
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

func (b *browserManager) wait(ctx context.Context, t *browserTab, args map[string]any) (any, error) {
	condition, _ := requiredString(args, "condition")
	frame, _ := optionalString(args, "frame_id")
	seconds, err := optionalInt(args, "timeout_seconds", 10, 1, 25)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		expression := `document.readyState==='complete'`
		switch condition {
		case "text":
			text, err := requiredString(args, "text")
			if err != nil {
				return nil, err
			}
			expression = `(document.body?.innerText||'').includes(` + browserJS(text) + `)`
		case "url":
			raw, err := requiredString(args, "url")
			if err != nil {
				return nil, err
			}
			if err := b.checkURL(raw); err != nil {
				return nil, err
			}
			expression = `location.href===` + browserJS(raw)
		case "element":
			locator, err := b.locator(t, args)
			if err != nil {
				return nil, err
			}
			expression = `(()=>{` + locator + `return !!el.getClientRects().length})()`
		case "load":
		default:
			return nil, toolErr("invalid_arguments", "unknown wait condition")
		}
		var ready bool
		if err := b.eval(ctx, t, frame, expression, &ready); err == nil && ready {
			return map[string]any{"status": "completed"}, nil
		}
		t.eventMu.Lock()
		dialog := t.dialog
		t.eventMu.Unlock()
		if dialog {
			return map[string]any{"status": "waiting_user", "reason": "browser_dialog"}, nil
		}
		select {
		case <-ctx.Done():
			return nil, toolErr("wait_timeout", "browser wait ended without matching condition")
		case <-ticker.C:
		}
	}
}

func (b *browserManager) tabAction(ctx context.Context, name string, s *browserSession, t *browserTab, args map[string]any) (any, error) {
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
		return b.interact(ctx, s, t, args)
	case "browser_wait":
		return b.wait(ctx, t, args)
	case "browser_diagnostics":
		t.eventMu.Lock()
		events := append([]map[string]any{}, t.diagnostics...)
		t.eventMu.Unlock()
		return map[string]any{"url": safeBrowserURL(u), "events": events, "truncated": len(events) == 100}, nil
	case "browser_files":
		return b.files(ctx, s, t, args)
	}
	return nil, toolErr("unknown_tool", "unknown browser tool")
}
