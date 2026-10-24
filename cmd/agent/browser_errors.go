package main

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// These messages contain no page-provided text, stack, field value or URL.
var browserFailureMessages = map[string]string{
	"cancelled":                 "browser operation was cancelled",
	"cancelled_before_dispatch": "operation was cancelled before a browser side effect was sent",
	"scope_changed":             "local browser access changed; obtain fresh state before continuing",
	"protocol_upgrade_required": "update agent and browser extension to command protocol 2 before changing the page",
	"browser_busy":              "browser operation queue is full; retry after current work finishes",
	"command_expired":           "browser command expired before it could complete",
	"browser_not_found":         "no connected personal browser matches; use browser_control action=discover",
	"ambiguous_browser":         "multiple personal browsers match; choose a profile_id or unique local alias from discovery",
	"ambiguous_tab":             "multiple tabs match target_origin; open the profile and explicitly choose from browser_tabs",
	"wait_timeout":              "browser wait ended without matching the requested condition",
	"stale_snapshot":            "snapshot no longer matches this document or frame; obtain a new snapshot",
	"element_detached":          "element was removed from the document; obtain a new snapshot",
	"element_not_found":         "no element matches the locator",
	"ambiguous_locator":         "more than one element matches the locator; specify a container or exact selector",
	"invalid_selector":          "CSS selector is invalid",
	"invalid_option_value":      "no enabled select option matches; inspect the snapshot options",
	"state_verification_failed": "interaction was dispatched but its control state could not be verified; read a fresh snapshot before retrying",
	"invalid_control_type":      "element does not support this interaction",
	"element_disabled":          "element is disabled",
	"element_hidden":            "element is not visible",
	"element_obscured":          "another element covers the interaction point",
	"element_not_editable":      "element is not editable",
	"frame_changed":             "frame execution context changed; obtain a new snapshot",
	"navigation_pending":        "navigation is changing the document; wait for the target state",
	"page_evaluation_failed":    "page evaluation failed; inspect bounded browser diagnostics",
	"outcome_unknown":           "action outcome is unknown; verify the target state before any new mutation",
	"access_paused":             "browser access is paused or was changed locally",
	"scope_denied":              "operation is outside the locally approved tab or URL scope",
	"disconnected":              "browser connection or tab is no longer available",
	"response_too_large":        "browser response exceeds the allowed message budget",
}

func browserFailureMessage(code string) string {
	if message := browserFailureMessages[code]; message != "" {
		return message
	}
	return "browser operation failed; inspect the error code and browser diagnostics"
}

func browserTransportFailure(message string) error {
	code := "browser_failed"
	if len(message) <= 2048 {
		switch message {
		case "Command cancelled":
			code = "cancelled_before_dispatch"
		case "Command deadline expired":
			code = "command_expired"
		case "Command scope changed":
			code = "scope_changed"
		case "Command queue is full":
			code = "browser_busy"
		case "Command protocol is not supported":
			code = "protocol_upgrade_required"
		case "Local control paused", "Local access changed", "Enable permanent access locally":
			code = "access_paused"
		case "Tab URL is outside the selected scope", "Tab left permitted scope", "Navigation URL is outside selected scope", "History URL outside scope", "Tab is not locally attached", "CDP method is not permitted", "New tabs are not locally permitted", "Only tabs created for this task can be closed":
			code = "scope_denied"
		case "Native bridge disconnected":
			code = "disconnected"
		case "Response exceeds native message limit", "Native response exceeds limit":
			code = "response_too_large"
		default:
			for _, marker := range []string{"Cannot find context with specified id", "Execution context was destroyed", "Cannot find execution context", "No frame with given id"} {
				if strings.Contains(message, marker) {
					code = "frame_changed"
					break
				}
			}
		}
	}
	return toolErr(code, browserFailureMessage(code))
}

// Expressions are internal, never arbitrary caller-supplied JavaScript. Catch
// controlled failures in the isolated world instead of returning page stacks.
func browserEvalExpression(expression string) string {
	return `(async()=>{try{return {value:await (` + expression + `)}}catch(e){
const legacy={'not editable':'element_not_editable','contenteditable insert failed':'page_evaluation_failed','not file input':'invalid_control_type'};
const code=e?.webcodexCode||legacy[e?.message]||'page_evaluation_failed';
return {error:{code,matches:e?.webcodexMatches,options:e?.webcodexOptions}};
}})()`
}

const browserDOMFailure = `function wcFail(code,matches,options){const e=new Error();e.webcodexCode=code;e.webcodexMatches=matches;e.webcodexOptions=options;throw e}
`

func browserEvalResult(raw json.RawMessage, result any) error {
	var envelope struct {
		Value json.RawMessage `json:"value"`
		Error *struct {
			Code    string                `json:"code"`
			Matches *int                  `json:"matches"`
			Options []browserSelectOption `json:"options"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return toolErr("browser_failed", "invalid page result")
	}
	if envelope.Error != nil {
		code := envelope.Error.Code
		if browserFailureMessages[code] == "" {
			code = "page_evaluation_failed"
		}
		failure := &codedError{code: code, message: browserFailureMessage(code)}
		if count := envelope.Error.Matches; count != nil && *count >= 0 && *count <= 10000 {
			failure.details = map[string]any{"matches": *count}
		}
		if len(envelope.Error.Options) > 0 && len(envelope.Error.Options) <= 100 {
			if failure.details == nil {
				failure.details = map[string]any{}
			}
			failure.details["options"] = envelope.Error.Options
		}
		return failure
	}
	if len(envelope.Value) == 0 {
		return toolErr("browser_failed", "page result is missing")
	}
	if result != nil && json.Unmarshal(envelope.Value, result) != nil {
		return toolErr("browser_failed", "invalid page result")
	}
	return nil
}

var browserContextID = regexp.MustCompile(`^(browser|tab|snapshot|bop)_[0-9a-f]{24}$`)

func browserErrorContext(err error, args map[string]any) error {
	var coded *codedError
	if !errors.As(err, &coded) {
		coded = &codedError{code: "browser_failed", message: browserFailureMessage("browser_failed")}
	}
	details := map[string]any{}
	for key, value := range coded.details {
		details[key] = value
	}
	for _, key := range []string{"browser_session_id", "tab_id", "snapshot_id"} {
		if value, ok := args[key].(string); ok && browserContextID.MatchString(value) {
			details[key] = value
		}
	}
	return &codedError{code: coded.code, message: coded.message, details: details}
}

func (op *browserOperation) fail(err error, args map[string]any) {
	failure := browserErrorContext(err, args).(*codedError)
	// Persist only safe diagnostics and IDs, never raw exception/cause strings.
	op.setFailureCode(failure.code)
	for key, value := range failure.details {
		switch key {
		case "browser_session_id", "tab_id", "frame_id", "snapshot_id", "matches", "verification_error_code":
			op.ErrorDetails[key] = value
		case "options":
			op.errorTransient = map[string]any{"options": value}
		}
	}
}

func (op *browserOperation) setFailureCode(code string) {
	op.ErrorCode = code
	op.ErrorMessage = browserFailureMessage(code)
	if op.ErrorDetails == nil {
		op.ErrorDetails = map[string]any{}
	}
	if _, ok := op.ErrorDetails["operation_id"]; !ok {
		op.ErrorDetails["operation_id"] = op.ID
	}
	op.ErrorDetails["recovery"] = browserErrorRecovery(code)
}

func browserErrorRecovery(code string) string {
	switch code {
	case "cancelled", "cancelled_before_dispatch":
		return "stop"
	case "browser_busy":
		return "wait"
	case "protocol_upgrade_required":
		return "update_extension"
	case "scope_changed", "command_expired":
		return "refresh_snapshot"
	case "stale_snapshot", "element_detached", "frame_changed", "navigation_pending":
		return "refresh_snapshot"
	case "invalid_option_value":
		return "inspect_options"
	case "element_not_found", "ambiguous_locator", "element_disabled", "element_hidden", "element_obscured", "invalid_selector", "invalid_control_type", "element_not_editable":
		return "inspect_locator"
	case "outcome_unknown", "state_verification_failed":
		return "verify_outcome"
	default:
		return "inspect_diagnostics"
	}
}
