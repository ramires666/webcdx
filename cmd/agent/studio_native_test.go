package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStudioOriginAndRuntime(t *testing.T) {
	if !studioNativeHostInvocation([]string{"agent", "chrome-extension://" + studioExtensionID + "/"}) {
		t.Fatal("known Studio origin was not selected")
	}
	if studioNativeHostInvocation([]string{"agent", "chrome-extension://other/"}) {
		t.Fatal("untrusted origin selected as Studio host")
	}
	e, _ := testExecutor(t)
	defer e.Close()
	h := &studioOperations{dir: t.TempDir()}
	init, err := handleStudioRequest(context.Background(), e, h, studioRequest{ID: "i", Method: "initialize", Params: map[string]any{"clientInfo": map[string]any{"name": "AI Studio", "version": "test"}}})
	if err != nil || init == nil {
		t.Fatalf("initialize: %v %v", init, err)
	}
	listed, err := handleStudioRequest(context.Background(), e, h, studioRequest{ID: "l", Method: "tools/list", Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(listed)
	if !json.Valid(b) {
		t.Fatal("invalid tools/list response")
	}
	toolNames := map[string]bool{}
	for _, tool := range listed.(map[string]any)["tools"].([]any) {
		if def, ok := tool.(map[string]any); ok {
			toolNames[def["name"].(string)] = true
		}
	}
	for _, name := range []string{"exec_command", "list_commands", "poll_command", "list_agents"} {
		if !toolNames[name] {
			t.Errorf("v5 tool missing: %s", name)
		}
	}
	call, err := handleStudioRequest(context.Background(), e, h, studioRequest{ID: "c", Method: "tools/call", Params: map[string]any{"name": "list_agents"}})
	if err != nil {
		t.Fatal(err)
	}
	if call.(map[string]any)["structuredContent"] == nil {
		t.Fatal("list_agents missing structured content")
	}
}

func TestStudioRootsRequireValidSavedSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("WEBCODEX_ALLOWED_ROOTS", "")
	if roots := studioAllowedRoots(); len(roots) != 0 {
		t.Fatalf("missing settings granted access: %v", roots)
	}
	if err := os.MkdirAll(studioConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(studioSettingsPath(), []byte("invalid JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if roots := studioAllowedRoots(); len(roots) != 0 {
		t.Fatalf("invalid settings granted access: %v", roots)
	}
	data, _ := json.Marshal(studioSettings{ExtensionID: studioExtensionID, AllowedRoots: []string{dir}})
	if err := os.WriteFile(studioSettingsPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if roots := studioAllowedRoots(); len(roots) != 1 || roots[0] != dir {
		t.Fatalf("saved roots were not restored: %v", roots)
	}
}

func TestStudioOperationIdempotencyAndRecovery(t *testing.T) {
	e, _ := testExecutor(t)
	defer e.Close()
	h := &studioOperations{dir: t.TempDir()}
	args := map[string]any{}
	one, err := h.create(context.Background(), e, "same", "list_allowed_roots", args, "")
	if err != nil {
		t.Fatal(err)
	}
	two, err := h.create(context.Background(), e, "same", "list_allowed_roots", args, "")
	if err != nil || two["operation_id"] != one["operation_id"] {
		t.Fatalf("duplicate did not return saved operation: %#v %v", two, err)
	}
	if _, err = h.create(context.Background(), e, "same", "delete_path", map[string]any{"path": "x"}, ""); err == nil {
		t.Fatal("mismatched duplicate accepted")
	}
	local, err := h.create(context.Background(), e, "agents", "list_agents", nil, "local")
	if err != nil || local["status"] != "completed" {
		t.Fatalf("list_agents operation failed: %#v %v", local, err)
	}
	op := studioOperation{Key: "crash", ID: studioOperationID("crash"), Name: "delete_path", Arguments: map[string]any{}, Status: "running"}
	data, _ := json.Marshal(op)
	sumPath := h.path(op.Key)
	if err = os.WriteFile(sumPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := newStudioOperationsAt(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.get(op.ID)
	if err != nil || got["status"] != "outcome_unknown" {
		t.Fatalf("crashed operation not recovered as unknown: %#v %v", got, err)
	}
	_ = filepath.Base(sumPath)
}

func TestStudioChunkFramesStayWithinNativeLimit(t *testing.T) {
	data := map[string]any{"value": string(make([]byte, studioChunkBytes*4))}
	encoded, _ := json.Marshal(data)
	frames := new(testNativeFrames)
	if err := writeStudioResponse(frames, "id", data, nil); err != nil {
		t.Fatal(err)
	}
	for _, frame := range frames.frames {
		if len(frame) > browserBridgeMax {
			t.Fatalf("chunk frame too large: %d", len(frame))
		}
	}
	if len(encoded) == 0 || len(frames.frames) < 2 {
		t.Fatal("expected chunked response")
	}
}

func TestStudioLargeOperationResultPreservesContentAndStatus(t *testing.T) {
	op := studioOperation{ID: studioOperationID("large"), Status: "completed", Result: map[string]any{"text": string(make([]byte, 5<<20))}}
	result := studioOperationResult(op)
	if result["status"] != "completed" {
		t.Fatalf("status changed: %#v", result)
	}
	if result["result"].(map[string]any)["text"] != op.Result.(map[string]any)["text"] {
		t.Fatal("large result content changed")
	}
}

func TestStudioOperationLookupRejectsNonHexIDs(t *testing.T) {
	h := &studioOperations{dir: t.TempDir()}
	if _, err := h.get("op_" + strings.Repeat(".", 64)); err == nil {
		t.Fatal("accepted non-hex operation id")
	}
}

type testNativeFrames struct{ frames [][]byte }

func (w *testNativeFrames) Write(p []byte) (int, error) {
	w.frames = append(w.frames, append([]byte(nil), p...))
	return len(p), nil
}
