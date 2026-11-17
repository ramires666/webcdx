package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskProviderReadsOpenMCPKeyOnly(t *testing.T) {
	dotenv := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(dotenv, []byte("\uFEFF# config\r\nexport OPENCODE_API_KEY='fixture-key#1' # comment\r\nWEBCODEX_TASK_MODEL=wrong-model\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEBCODEX_TASK_KEY_FILE", "")
	t.Setenv("WEBCODEX_TASK_OPENMCP_ENV", dotenv)
	key, err := configuredTaskProviderKey()
	if err != nil || key != "fixture-key#1" {
		t.Fatalf("failed to read configured key: %v", err)
	}
	if err := os.WriteFile(dotenv, []byte("OPENCODE_API_KEY=sensitive-duplicate\nOPENCODE_API_KEY=second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := configuredTaskProviderKey(); err == nil || strings.Contains(err.Error(), "sensitive-duplicate") {
		t.Fatal("expected an opaque duplicate key error")
	}
}
