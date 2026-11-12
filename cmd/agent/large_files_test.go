package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLargeFileWriteReadEditAndSearchPreserveAllBytes(t *testing.T) {
	e, root := testExecutor(t)
	defer e.Close()
	path := filepath.Join(root, "large.html")
	content := "<!doctype html>\n" + strings.Repeat("code я 😀 ", 1200000) + "\nunique-tail\n"
	if len(content) <= 10<<20 {
		t.Fatal("fixture must exceed the removed file ceiling")
	}
	written, err := e.writeFile(map[string]any{"path": path, "content": content, "if_absent": true}, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatal("file write lost data", err)
	}
	read, err := e.readFile(map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	if read.(map[string]any)["content"] != content || read.(map[string]any)["truncated"] != false {
		t.Fatal("file read truncated data")
	}
	rangeRead, rangeErr := e.readFile(map[string]any{"path": path, "offset": float64(2)})
	if rangeErr != nil || rangeRead.(map[string]any)["content"] != strings.TrimPrefix(content, "<!doctype html>\n") {
		t.Fatal("reading an offset without a limit lost data", rangeErr)
	}
	edited, err := e.editFile(map[string]any{"path": path, "expected_sha256": written.(map[string]any)["sha256"], "edits": []any{map[string]any{"old_text": "unique-tail", "new_text": "verified-tail"}}})
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Replace(content, "unique-tail", "verified-tail", 1)
	data, err = os.ReadFile(path)
	if err != nil || string(data) != expected || edited.(map[string]any)["new_sha256"] != hashBytes(data) {
		t.Fatal("file edit lost data", err)
	}
	found, err := e.searchFiles(context.Background(), map[string]any{"path": root, "pattern": "code я"})
	if err != nil {
		t.Fatal(err)
	}
	matches := found.(map[string]any)["matches"].([]searchMatch)
	if len(matches) != 1 || len(matches[0].Text) < 10<<20 {
		t.Fatal("large search line was lost or truncated")
	}
}
