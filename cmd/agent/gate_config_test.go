package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestAllowedRootsLocalOverrideAndGateDefaults(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/agent/config" || r.Header.Get("Authorization") != "Bearer worker" {
			t.Error("config request was not authenticated")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"allowed_roots":"C:\\projects;W:\\"}`))
	}))
	defer server.Close()
	t.Setenv("WEBCODEX_ALLOWED_ROOTS", "")
	roots, err := loadAllowedRoots(context.Background(), server.Client(), server.URL, "worker")
	if err != nil || roots != `C:\projects;W:\` || requests != 1 {
		t.Fatalf("gate roots: %q %v", roots, err)
	}
	local := filepath.Join(t.TempDir(), "local")
	t.Setenv("WEBCODEX_ALLOWED_ROOTS", local)
	roots, err = loadAllowedRoots(context.Background(), server.Client(), server.URL, "worker")
	if err != nil || roots != local || requests != 1 {
		t.Fatalf("local override: %q %v requests=%d", roots, err, requests)
	}
}

func TestAllowedRootsDiscoveryAndMissingDrive(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "unmounted")
	executor, err := newNativeExecutorWithConfig(executorConfig{AllowedRoots: []string{root, missing}, LogDir: filepath.Join(root, "logs")})
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	result := callTool(t, executor, "list_allowed_roots", map[string]any{})
	roots := result["roots"].([]any)
	if len(roots) != 2 || roots[0].(map[string]any)["available"] != true || roots[1].(map[string]any)["available"] != false || result["unrestricted"] != false {
		t.Fatalf("root discovery: %v", result)
	}
	if _, err := executor.resolvePath(filepath.Join(t.TempDir(), "outside")); err == nil {
		t.Fatal("root discovery widened file access")
	}
}

func TestAllowedRootsConfigFailuresDoNotWidenAccess(t *testing.T) {
	t.Setenv("WEBCODEX_ALLOWED_ROOTS", "")
	for _, body := range []string{`{}`, `{"allowed_roots":""}`, `{"allowed_roots":1}`, `not json`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		roots, err := loadAllowedRoots(context.Background(), server.Client(), server.URL, "worker")
		server.Close()
		if err == nil || roots != "" {
			t.Fatalf("invalid config opened roots: %q %v", roots, err)
		}
	}
}
