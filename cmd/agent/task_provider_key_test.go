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
	t.Setenv("WEBCODEX_TASK_OPENCODE_AUTH", "")
	t.Setenv("WEBCODEX_TASK_OPENCODE_DB", "")
	t.Setenv("WEBCODEX_TASK_KEY_FILE", "")
	t.Setenv("WEBCODEX_TASK_OPENMCP_ENV", dotenv)
	credentials, err := configuredTaskProviderAuth()
	if err != nil || credentials.key != "fixture-key#1" {
		t.Fatalf("failed to read configured key: %v", err)
	}
	if err := os.WriteFile(dotenv, []byte("OPENCODE_API_KEY=sensitive-duplicate\nOPENCODE_API_KEY=second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := configuredTaskProviderAuth(); err == nil || strings.Contains(err.Error(), "sensitive-duplicate") {
		t.Fatal("expected an opaque duplicate key error")
	}
}

func TestTaskProviderReadsOnlyOpenCodeGoAuth(t *testing.T) {
	auth := filepath.Join(t.TempDir(), "auth.json")
	content := `{"openai":{"type":"oauth","key":"ignored"},"opencode-go":{"type":"api","key":"fixture-go-key"}}`
	if err := os.WriteFile(auth, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEBCODEX_TASK_OPENMCP_ENV", "")
	t.Setenv("WEBCODEX_TASK_KEY_FILE", "")
	t.Setenv("WEBCODEX_TASK_OPENCODE_AUTH", auth)
	t.Setenv("WEBCODEX_TASK_OPENCODE_DB", "")
	credentials, err := configuredTaskProviderAuth()
	if err != nil || credentials.key != "fixture-go-key" {
		t.Fatalf("could not load OpenCode Go authorization: %v", err)
	}
	t.Setenv("WEBCODEX_TASK_KEY_FILE", auth)
	if _, err := configuredTaskProviderAuth(); err == nil {
		t.Fatal("expected ambiguous key sources to be rejected")
	}
}
