package main

import (
	"context"
	"errors"
)

func browserVerification(args map[string]any) (map[string]any, error) {
	selector, _ := optionalString(args, "verify_selector")
	text, _ := optionalString(args, "verify_text")
	if selector == "" && text == "" {
		if _, ok := args["verify_timeout_seconds"]; ok {
			return nil, toolErr("invalid_arguments", "verification requires verify_selector or verify_text")
		}
		return nil, nil
	}
	seconds, err := optionalInt(args, "verify_timeout_seconds", 10, 1, 20)
	if err != nil {
		return nil, err
	}
	wait := map[string]any{"frame_id": args["frame_id"], "timeout_seconds": float64(seconds)}
	if wait["frame_id"] == nil {
		delete(wait, "frame_id")
	}
	if text != "" {
		wait["condition"] = "text"
		wait["text"] = text
		if selector != "" {
			wait["container_selector"] = selector
		}
	} else {
		wait["condition"] = "element"
		wait["selector"] = selector
	}
	return wait, nil
}

func (b *browserManager) verifiedInteract(ctx context.Context, s *browserSession, t *browserTab, args map[string]any) (any, error) {
	verification, err := browserVerification(args)
	if err != nil {
		return nil, err
	}
	// Validate syntax before any side effect, even if the target appears later.
	if selector, _ := optionalString(args, "verify_selector"); selector != "" {
		frame, _ := optionalString(args, "frame_id")
		if err := b.eval(ctx, t, frame, `(()=>{`+browserDOMFailure+`try{document.querySelector(`+browserJS(selector)+`)}catch(e){wcFail('invalid_selector')}return true})()`, nil); err != nil {
			return nil, err
		}
	}
	result, err := b.interact(ctx, s, t, args)
	if err != nil {
		return nil, err
	}
	object, _ := result.(map[string]any)
	if verification == nil || object["status"] == "waiting_user" {
		return result, nil
	}
	verified, err := b.wait(ctx, t, verification)
	if err != nil {
		details := map[string]any{}
		var coded *codedError
		if errors.As(err, &coded) {
			details["verification_error_code"] = coded.code
		}
		return nil, &codedError{code: "state_verification_failed", message: browserFailureMessage("state_verification_failed"), details: details}
	}
	if state, _ := verified.(map[string]any); state["status"] == "waiting_user" {
		state["confirmation"] = "action_dispatched"
		return state, nil
	}
	object["confirmation"] = "state_verified"
	return object, nil
}
