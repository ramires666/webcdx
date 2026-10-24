package main

import (
	"context"
	"errors"
	"time"
)

func (b *browserManager) wait(ctx context.Context, t *browserTab, args map[string]any) (any, error) {
	condition, _ := requiredString(args, "condition")
	frame, _ := optionalString(args, "frame_id")
	seconds, err := optionalInt(args, "timeout_seconds", 10, 1, 25)
	if err != nil {
		return nil, err
	}
	state, _ := optionalString(args, "state")
	expression := `document.readyState==='complete'`
	switch condition {
	case "text":
		text, err := requiredString(args, "text")
		if err != nil {
			return nil, err
		}
		expression = `(document.body?.innerText||'').includes(` + browserJS(text) + `)`
		if container, _ := optionalString(args, "container_selector"); container != "" {
			expression = `(()=>{` + browserDOMFailure + browserFormDOM + `let groups;try{groups=[...document.querySelectorAll(` + browserJS(container) + `)].filter(visible)}catch(e){wcFail('invalid_selector')}if(!groups.length)wcFail('element_not_found');if(groups.length!==1)wcFail('ambiguous_locator',groups.length);return groups[0].innerText.includes(` + browserJS(text) + `)})()`
		}
	case "url":
		raw, err := requiredString(args, "url")
		if err != nil {
			return nil, err
		}
		if err := b.checkURL(raw); err != nil {
			return nil, err
		}
		expression = `location.href===` + browserJS(raw)
	case "element", "value":
		locator, err := b.locator(t, args)
		if err != nil {
			return nil, err
		}
		expression = `(()=>{` + locator + browserFormDOM + `return visible(el)`
		if condition == "value" {
			value, ok := args["value"].(string)
			if !ok {
				return nil, toolErr("invalid_arguments", "value must be a string")
			}
			expression = `(()=>{` + locator + `if(el.type==='password')wcFail('invalid_control_type');return String(el.value??(el.isContentEditable?el.innerText:''))===` + browserJS(value)
		} else if state == "enabled" {
			expression += `&&!el.matches(':disabled')&&el.getAttribute('aria-disabled')!=='true'`
		} else if state == "absent" {
			expression = `(()=>{` + locator + browserFormDOM + `return !visible(el)`
		}
		expression += `})()`
	case "load":
	default:
		return nil, toolErr("invalid_arguments", "unknown wait condition")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var ready bool
		err := b.eval(ctx, t, frame, expression, &ready)
		if err != nil {
			var coded *codedError
			if ctx.Err() == nil {
				if !errors.As(err, &coded) {
					return nil, err
				}
				if coded.code == "element_not_found" && condition == "element" && state == "absent" {
					ready = true
				} else if coded.code != "element_not_found" && coded.code != "frame_changed" && coded.code != "navigation_pending" {
					return nil, err
				}
			}
		}
		if ready {
			return map[string]any{"status": "completed", "confirmation": "condition_matched"}, nil
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
