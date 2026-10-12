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
	common := map[string]any{"browser_session_id": str(), "tab_id": str(), "frame_id": str(), "request_key": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}
	locator := map[string]any{"selector": str(), "role": str(), "name": str(), "snapshot_id": str(), "element_id": str()}
	output := object(map[string]any{
		"browser_session_id": str(), "tab_id": str(), "operation_id": str(), "mode": enum("clean", "persistent", "personal"),
		"status": enum("ready", "running", "waiting_user", "paused", "completed", "failed", "cancelled", "disconnected"),
		"url":    str(), "title": str(), "text": str(), "reason": str(), "snapshot_id": str(), "truncated": boolean(),
		"tabs":      map[string]any{"type": "array", "items": object(map[string]any{"tab_id": str(), "url": str()}, "tab_id", "url")},
		"elements":  map[string]any{"type": "array", "items": object(map[string]any{"element_id": str(), "role": str(), "name": str(), "tag": str(), "disabled": boolean(), "href": str(), "value": str()}, "element_id", "role", "name", "tag", "disabled", "href", "value")},
		"frames":    map[string]any{"type": "array", "items": object(map[string]any{"frame_id": str(), "url": str()}, "frame_id", "url")},
		"events":    map[string]any{"type": "array", "items": object(map[string]any{"event": str(), "at": str(), "url": str(), "detail": str()}, "event", "at")},
		"mime_type": str(), "artifact_path": str(), "path": str(), "download_id": str(), "bytes": integer(0, 1<<30),
		"viewport_width": integer(1, 100000), "viewport_height": integer(1, 100000),
		"image_width": integer(1, 100000), "image_height": integer(1, 100000),
		"downloads":     map[string]any{"type": "array", "items": object(map[string]any{"download_id": str(), "status": str(), "bytes": integer(0, 1<<30)}, "download_id", "status", "bytes")},
		"accessibility": map[string]any{"type": "array", "items": object(map[string]any{"node_id": str(), "role": str(), "name": str(), "children": map[string]any{"type": []string{"array", "null"}, "items": str()}}, "node_id", "role", "name", "children")},
	}, "status", "truncated")
	definitions := []struct {
		name, description string
		properties        map[string]any
		required          []string
		locator, readOnly bool
	}{
		{"browser_open", "Open a visible local browser. Use clean or persistent; personal requires a locally connected extension tab. Passwords, MFA and CAPTCHA are completed manually.", map[string]any{"mode": enum("clean", "persistent", "personal"), "url": str()}, []string{"mode", "request_key"}, false, false},
		{"browser_tabs", "List, create, select or close tabs in this browser session.", map[string]any{"action": enum("list", "new", "select", "close")}, []string{"browser_session_id", "action"}, false, false},
		{"browser_navigate", "Navigate HTTP/HTTPS or history. Redirects follow the locally configured URL scope.", map[string]any{"action": enum("url", "back", "forward", "reload"), "url": str()}, []string{"browser_session_id", "action", "request_key"}, false, false},
		{"browser_snapshot", "Read bounded page text and interactive elements, including open shadow roots. Element IDs require this snapshot_id and frame_id.", map[string]any{}, []string{"browser_session_id"}, false, true},
		{"browser_screenshot", "Return a bounded JPEG as MCP image content, optionally saving a local artifact.", map[string]any{"full_page": boolean(), "save": boolean()}, []string{"browser_session_id"}, false, true},
		{"browser_interact", "Click, fill, select, hover, press, scroll, drag, handle a dialog or explicitly click viewport coordinates. Locators must match exactly one element. External changes require the user's authorization.", map[string]any{"action": enum("click", "fill", "select", "hover", "press", "scroll", "drag", "coordinates", "dialog"), "text": str(), "value": str(), "key": enum("Enter", "Tab", "Escape", "Backspace", "Delete", "ArrowLeft", "ArrowUp", "ArrowRight", "ArrowDown", "Space"), "accept": boolean(), "x": integer(-100000, 100000), "y": integer(-100000, 100000), "end_x": integer(0, 100000), "end_y": integer(0, 100000), "viewport_width": integer(1, 100000), "viewport_height": integer(1, 100000)}, []string{"browser_session_id", "action", "request_key"}, true, false},
		{"browser_wait", "Wait up to 25 seconds for load, exact URL, text or a unique visible element. Poll browser_control with operation_id if status is running.", map[string]any{"condition": enum("load", "url", "text", "element"), "text": str(), "url": str(), "timeout_seconds": integer(1, 25)}, []string{"browser_session_id", "condition"}, true, true},
		{"browser_files", "Upload a permitted file, enable downloads, read download status or save a completed download without overwriting.", map[string]any{"action": enum("upload", "enable_downloads", "downloads", "save_download"), "path": str(), "download_id": str()}, []string{"browser_session_id", "action"}, true, false},
		{"browser_diagnostics", "Read bounded console errors and failed requests without headers, bodies, cookies or query secrets.", map[string]any{}, []string{"browser_session_id"}, false, true},
		{"browser_control", "Read operation/session status, pause, resume, cancel, wait for manual authorization, or disconnect. Disconnecting personal preserves the user's browser.", map[string]any{"action": enum("status", "pause", "resume", "cancel", "disconnect", "waiting_user"), "operation_id": str()}, []string{"action"}, false, false},
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
