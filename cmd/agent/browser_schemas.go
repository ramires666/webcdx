package main

import "strings"

func browserToolSchemas() []mcpToolDefinition {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func(minimum, maximum int) map[string]any {
		return map[string]any{"type": "integer", "minimum": minimum, "maximum": maximum}
	}
	enum := func(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}
	common := map[string]any{"browser_session_id": str(), "tab_id": str(), "frame_id": str(), "binding_revision": str(), "request_key": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}
	locator := map[string]any{"selector": str(), "role": str(), "name": str(), "snapshot_id": str(), "element_id": str(), "context": str(), "container_selector": str()}
	option := object(map[string]any{"label": str(), "value": str(), "disabled": boolean(), "selected": boolean()}, "label", "value", "disabled", "selected")
	attachment := object(map[string]any{"attachment_id": str(), "id_source": enum("stable_id", "ordinal"), "filename": str(), "url": str(), "media_type": str()}, "attachment_id", "id_source", "filename", "url", "media_type")
	record := object(map[string]any{"record_id": str(), "text": str(), "author": str(), "timestamp": str(), "attachments": map[string]any{"type": "array", "items": attachment}, "text_truncated": boolean(), "attachments_truncated": boolean()}, "record_id", "text", "attachments")
	output := object(map[string]any{
		"sha256": str(), "verified": boolean(), "source_original_verified": boolean(), "duplicate": boolean(), "width": integer(1, 2147483647), "height": integer(1, 2147483647),
		"scroll_top": map[string]any{"type": "number"}, "scroll_left": map[string]any{"type": "number"}, "scroll_moved": boolean(), "dom_changed": boolean(), "first_id": str(), "last_id": str(), "visible_count": integer(0, 2000), "new_ids": map[string]any{"type": "array", "items": str(), "maxItems": 100}, "at_scroll_start": boolean(), "at_scroll_end": boolean(), "history_end_verified": boolean(),
		"records": map[string]any{"type": "array", "items": record}, "dom_revision": str(), "page_token": str(), "next_page_token": str(), "has_more": boolean(), "total_records": integer(0, 2147483647), "collection_complete": boolean(),
		"agent_version": str(),
		"health":        enum("healthy", "degraded", "disabled", "unavailable"), "health_reason": enum("", "journal_corrupt", "storage_unavailable", "cleanup_pending", "shutdown"),
		"binding_revision": str(), "binding_verified": boolean(), "readable": boolean(), "window_id": integer(0, 2147483647), "selection_reason": str(), "matching_tabs": integer(0, 128), "auth_state": enum("unverified"),
		"browser_session_id": str(), "tab_id": str(), "operation_id": str(), "mode": enum("clean", "persistent", "personal"),
		"status": enum("ready", "running", "waiting_user", "paused", "completed", "failed", "cancelled", "disconnected"),
		"url":    str(), "title": str(), "text": str(), "reason": str(), "snapshot_id": str(), "truncated": boolean(),
		"phase":          enum("unknown", "reserved", "queued", "executing", "dispatch_started", "not_dispatched", "completed"),
		"confirmation":   enum("action_dispatched", "condition_matched", "state_verified", "control_value_verified"),
		"selected_value": str(), "selected_label": str(),
		"profile_id": str(), "browser_alias": str(), "connection_id": str(), "product": enum("chrome", "edge", "chromium", "unknown"),
		"browsers":       map[string]any{"type": "array", "items": object(map[string]any{"connection_id": str(), "profile_id": str(), "alias": str(), "product": enum("chrome", "edge", "chromium", "unknown"), "paused": boolean(), "identified": boolean(), "extension_version": str(), "capabilities": map[string]any{"type": "array", "items": str(), "maxItems": 16}, "tabs": map[string]any{"type": "array", "items": object(map[string]any{"url": str(), "title": str(), "window_id": integer(0, 2147483647), "active": boolean()}, "url", "title", "window_id", "active")}}, "connection_id", "profile_id", "alias", "product", "paused", "identified", "tabs")},
		"dropped_events": integer(0, 2147483647), "tracked_requests": integer(0, 256),
		"frame_id": str(), "observed_at": str(), "document_generation": str(), "ready_state": enum("loading", "interactive", "complete"),
		"accessibility_status": enum("available", "unavailable", "omitted_for_scope", "omitted_by_request"),
		"counts":               object(map[string]any{"dom_visited": integer(0, 10000), "elements_found": integer(0, 10000), "elements_returned": integer(0, 300), "ax_found": integer(0, 1000000), "ax_returned": integer(0, 200)}),
		"truncation_reasons":   map[string]any{"type": "array", "items": enum("node_limit", "element_limit", "text_limit", "accessibility_limit", "response_budget", "options_limit", "record_limit", "attachment_limit")},
		"tabs":                 map[string]any{"type": "array", "items": object(map[string]any{"tab_id": str(), "url": str(), "title": str(), "window_id": integer(0, 2147483647), "active": boolean()}, "tab_id", "url")},
		"elements":             map[string]any{"type": "array", "items": object(map[string]any{"element_id": str(), "role": str(), "name": str(), "tag": str(), "disabled": boolean(), "href": str(), "value": str(), "context": str(), "options": map[string]any{"type": "array", "items": option}, "options_truncated": boolean()}, "element_id", "role", "name", "tag", "disabled", "href", "value")},
		"frames":               map[string]any{"type": "array", "items": object(map[string]any{"frame_id": str(), "url": str()}, "frame_id", "url")},
		"events":               map[string]any{"type": "array", "items": object(map[string]any{"event": str(), "at": str(), "url": str(), "detail": str(), "request_id": str(), "operation_id": str(), "duration_ms": integer(0, 120000), "http_status": integer(400, 599), "canceled": boolean(), "failure_type": enum("timeout", "dns", "connection", "blocked", "cancelled", "other")}, "event", "at")},
		"mime_type":            str(), "artifact_path": str(), "path": str(), "download_id": str(), "bytes": integer(0, 1<<30),
		"viewport_width": integer(1, 100000), "viewport_height": integer(1, 100000),
		"image_width": integer(1, 100000), "image_height": integer(1, 100000),
		"original_image_width": integer(1, 100000), "original_image_height": integer(1, 100000), "image_bytes": integer(0, 1<<30), "image_scaled": boolean(), "image_payload_available": boolean(), "artifact_available": boolean(), "capture_mode": enum("viewport", "full_page"),
		"downloads":     map[string]any{"type": "array", "items": object(map[string]any{"download_id": str(), "status": str(), "bytes": integer(0, 1<<30)}, "download_id", "status", "bytes")},
		"accessibility": map[string]any{"type": "array", "items": object(map[string]any{"node_id": str(), "role": str(), "name": str(), "children": map[string]any{"type": []string{"array", "null"}, "items": str()}}, "node_id", "role", "name", "children")},
	}, "status", "truncated")
	output["properties"].(map[string]any)["error"] = object(map[string]any{
		"code": str(), "message": str(), "details": object(map[string]any{
			"phase":        enum("unknown", "reserved", "queued", "executing", "dispatch_started", "not_dispatched", "completed"),
			"operation_id": str(), "browser_session_id": str(), "tab_id": str(), "frame_id": str(), "snapshot_id": str(), "matches": integer(0, 10000),
			"recovery": enum("refresh_snapshot", "inspect_options", "inspect_locator", "verify_outcome", "inspect_diagnostics", "stop", "wait", "update_extension", "rebind_target"),
			"options":  map[string]any{"type": "array", "items": option}, "verification_error_code": str(),
		}),
	}, "code", "message")
	delete(output, "required")
	output["oneOf"] = []any{map[string]any{"required": []string{"status", "truncated"}}, map[string]any{"required": []string{"error"}}}
	definitions := []struct {
		name, description string
		properties        map[string]any
		required          []string
		locator, readOnly bool
	}{
		{"browser_open", "Open a local browser. For an existing signed-in task use personal, reuse_only=true and exact_url (optionally window_id/profile_id). This never creates a tab/profile or navigates another tab, and returns binding proof after DOM verification. Use binding_revision for subsequent calls. Passwords, MFA and CAPTCHA are completed manually.", map[string]any{"mode": enum("clean", "persistent", "personal"), "url": str(), "reuse_only": boolean(), "exact_url": str(), "window_id": integer(0, 2147483647), "profile_id": str(), "browser_alias": str(), "connection_id": str(), "preferred_browser": enum("chrome", "edge", "chromium", "unknown"), "target_origin": str()}, []string{"mode", "request_key"}, false, false},
		{"browser_tabs", "List, create, select or close tabs in this browser session.", map[string]any{"action": enum("list", "new", "select", "close"), "focus_policy": enum("preserve", "activate")}, []string{"browser_session_id", "action"}, false, false},
		{"browser_navigate", "Navigate HTTP/HTTPS or history. Redirects follow the locally configured URL scope.", map[string]any{"action": enum("url", "back", "forward", "reload"), "url": str()}, []string{"browser_session_id", "action", "request_key"}, false, false},
		{"browser_snapshot", "Read bounded page text and interactive elements. Prefer detail=light for routine reads. Set container_selector and record_selector for stable-ID DOM records with text/author/time/attachment selectors and revision-bound page_token pagination. This reads only mounted visible DOM, never proves full history; collection_complete remains false. On stale_generation restart pagination and deduplicate stable IDs. Full (default) also collects accessibility for ordinary snapshots. Element IDs require this snapshot_id and frame_id.", map[string]any{"container_selector": str(), "detail": enum("light", "full"), "record_selector": str(), "id_attribute": str(), "text_selector": str(), "author_selector": str(), "time_selector": str(), "attachment_selector": str(), "page_size": integer(1, 100), "page_token": str()}, []string{"browser_session_id"}, false, true},
		{"browser_screenshot", "Return a bounded JPEG as MCP image content, optionally saving a local artifact.", map[string]any{"full_page": boolean(), "save": boolean()}, []string{"browser_session_id"}, false, true},
		{"browser_interact", "Click, fill, select, hover, press, scroll, drag, handle a dialog or explicitly click viewport coordinates. Locators must match exactly one element. External changes require the user's authorization.", map[string]any{"action": enum("click", "fill", "select", "hover", "press", "scroll", "drag", "coordinates", "dialog"), "record_selector": str(), "id_attribute": str(), "input_mode": enum("compatibility", "native"), "focus_policy": enum("preserve", "activate"), "text": str(), "value": str(), "select_by": enum("auto", "value", "label"), "verify_selector": str(), "verify_text": str(), "verify_timeout_seconds": integer(1, 20), "key": enum("Enter", "Tab", "Escape", "Backspace", "Delete", "ArrowLeft", "ArrowUp", "ArrowRight", "ArrowDown", "Space"), "accept": boolean(), "x": integer(-100000, 100000), "y": integer(-100000, 100000), "end_x": integer(0, 100000), "end_y": integer(0, 100000), "viewport_width": integer(1, 100000), "viewport_height": integer(1, 100000)}, []string{"browser_session_id", "action", "request_key"}, true, false},
		{"browser_wait", "Wait up to 25 seconds for load, exact URL, text or a unique visible element. Poll browser_control with operation_id if status is running.", map[string]any{"condition": enum("load", "url", "text", "element", "value"), "text": str(), "url": str(), "value": str(), "state": enum("visible", "enabled", "absent"), "timeout_seconds": integer(1, 25)}, []string{"browser_session_id", "condition"}, true, true},
		{"browser_files", "Upload a permitted file, enable downloads, read download status or save a completed download without overwriting.", map[string]any{"action": enum("upload", "enable_downloads", "downloads", "save_download"), "path": str(), "download_id": str()}, []string{"browser_session_id", "action"}, true, false},
		{"browser_diagnostics", "Read bounded redacted failures with operation/request correlation and origin only; no headers, bodies, console text or URL paths.", map[string]any{"operation_id": str()}, []string{"browser_session_id"}, false, true},
		{"browser_control", "Read operation/session status, pause, resume, cancel, wait for manual authorization, or disconnect. Disconnecting personal preserves the user's browser.", map[string]any{"action": enum("status", "pause", "resume", "cancel", "disconnect", "waiting_user", "discover"), "operation_id": str()}, []string{"action"}, false, false},
	}
	result := []mcpToolDefinition{}
	for _, def := range definitions {
		fields := map[string]any{}
		for key, value := range common {
			fields[key] = value
		}
		for key, value := range def.properties {
			fields[key] = value
		}
		if def.locator {
			for key, value := range locator {
				fields[key] = value
			}
		}
		input := object(fields, def.required...)
		if def.name == "browser_interact" || def.name == "browser_tabs" || def.name == "browser_navigate" || def.name == "browser_files" {
			actions := def.properties["action"].(map[string]any)["enum"].([]string)
			variants := []any{}
			for _, action := range actions {
				required := []string{"action"}
				switch action {
				case "fill":
					required = append(required, "text")
				case "select":
					if def.name == "browser_interact" {
						required = append(required, "value")
					} else {
						required = append(required, "tab_id")
					}
				case "url":
					required = append(required, "url")
				case "press":
					required = append(required, "key")
				case "dialog":
					required = append(required, "accept")
				case "coordinates", "drag":
					required = append(required, "x", "y", "viewport_width", "viewport_height")
				case "close":
					required = append(required, "tab_id")
				case "upload":
					required = append(required, "path")
				case "save_download":
					required = append(required, "path", "download_id")
				}
				if action == "drag" {
					required = append(required, "end_x", "end_y")
				}
				if def.name == "browser_tabs" && action != "list" {
					required = append(required, "request_key")
				}
				if def.name == "browser_files" && action != "downloads" {
					required = append(required, "request_key")
				}
				variants = append(variants, map[string]any{"properties": map[string]any{"action": map[string]any{"const": action}}, "required": required})
			}
			input["oneOf"] = variants
		}
		result = append(result, mcpToolDefinition{Name: def.name, Description: def.description, InputSchema: input, OutputSchema: output, Annotations: map[string]any{"readOnlyHint": def.readOnly, "destructiveHint": !def.readOnly, "openWorldHint": true}})
	}
	return result
}

func validateBrowserArgs(name string, args map[string]any) error {
	for _, def := range browserToolSchemas() {
		if def.Name != name {
			continue
		}
		fields := def.InputSchema["properties"].(map[string]any)
		allowed := []string{}
		for key := range fields {
			allowed = append(allowed, key)
		}
		if err := onlyArgs(args, allowed...); err != nil {
			return err
		}
		for _, key := range def.InputSchema["required"].([]string) {
			if _, err := requiredString(args, key); err != nil {
				return err
			}
		}
		for key, value := range args {
			schema := fields[key].(map[string]any)
			switch schema["type"] {
			case "string":
				text, ok := value.(string)
				if !ok || len(text) > 16384 {
					return toolErr("invalid_arguments", key+" must be a bounded string")
				}
				if enums, ok := schema["enum"].([]string); ok {
					found := false
					for _, candidate := range enums {
						if candidate == text {
							found = true
						}
					}
					if !found {
						return toolErr("invalid_arguments", "invalid "+key)
					}
				}
			case "integer":
				if _, err := optionalInt(args, key, 0, schema["minimum"].(int), schema["maximum"].(int)); err != nil {
					return err
				}
			case "boolean":
				if _, _, err := optionalBool(args, key); err != nil {
					return err
				}
			}
		}
		if variants, ok := def.InputSchema["oneOf"].([]any); ok {
			for _, raw := range variants {
				variant := raw.(map[string]any)
				action := variant["properties"].(map[string]any)["action"].(map[string]any)["const"]
				if action != args["action"] {
					continue
				}
				for _, key := range variant["required"].([]string) {
					if _, exists := args[key]; !exists {
						return toolErr("invalid_arguments", key+" is required")
					}
				}
			}
		}
		return nil
	}
	return toolErr("unknown_tool", "unknown browser tool: "+strings.TrimPrefix(name, "browser_"))
}
