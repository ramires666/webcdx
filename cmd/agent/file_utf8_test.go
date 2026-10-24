package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBinaryClassificationUTF8Boundaries(t *testing.T) {
	for _, symbol := range []string{"я", "→", "😀"} {
		for start := 8188; start <= 8196; start++ {
			t.Run(fmt.Sprintf("%s-at-%d", symbol, start), func(t *testing.T) {
				data := []byte(strings.Repeat("a", start) + symbol + "\r\nконец")
				if isBinary(data) {
					t.Fatal("valid UTF-8 rejected near former sample boundary")
				}
			})
		}
	}
	for _, data := range [][]byte{nil, []byte("ASCII\n"), []byte("\xef\xbb\xbfрусский\r\n")} {
		if isBinary(data) {
			t.Fatalf("valid text rejected: %q", data)
		}
	}
	for _, suffix := range [][]byte{{0}, {0xff}, {0xd0}, {0xe2, 0x28, 0xa1}} {
		for _, size := range []int{0, 8193} {
			if !isBinary(append(bytes.Repeat([]byte{'a'}, size), suffix...)) {
				t.Fatalf("binary/invalid UTF-8 accepted at %d: %x", size, suffix)
			}
		}
	}
}

func TestUTF8ReportReadEditSearchAndPreview(t *testing.T) {
	var console bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&console)
	t.Cleanup(func() { log.SetOutput(previous) })
	e, root := testExecutor(t)
	path := filepath.Join(root, "отчёт.md")
	content := strings.Repeat("a", 8191) + "я\r\nинженерный отчёт\r\n" + strings.Repeat("данные → 😀\r\n", 2000)
	written := callToolV4(t, e, "write_file", map[string]any{"path": path, "content": content, "if_absent": true})
	read := callToolV4(t, e, "read_file", map[string]any{"path": path})
	if read["content"] != content || read["sha256"] != written["sha256"] || read["truncated"] != false {
		t.Fatal("UTF-8 round trip changed or truncated the report")
	}
	search := callToolV4(t, e, "search_files", map[string]any{"path": root, "pattern": "инженерный отчёт", "glob": "*.md"})
	if len(search["matches"].([]any)) != 1 {
		t.Fatalf("UTF-8 report was silently skipped: %v", search)
	}
	console.Reset()
	edited := callToolV4(t, e, "edit_file", map[string]any{
		"path": path, "expected_sha256": read["sha256"],
		"edits": []any{map[string]any{"old_text": "инженерный отчёт", "new_text": "проверенный отчёт"}},
	})
	updated := strings.Replace(content, "инженерный отчёт", "проверенный отчёт", 1)
	if edited["new_sha256"] != hashBytes([]byte(updated)) || !strings.Contains(console.String(), "- 2 инженерный отчёт") {
		t.Fatal("edit or original-content preview failed")
	}
	console.Reset()
	callToolV4(t, e, "write_file", map[string]any{"path": path, "content": content, "expected_sha256": edited["new_sha256"]})
	if strings.Contains(console.String(), "сравнение недоступно") || !strings.Contains(console.String(), "- 2 проверенный отчёт") {
		t.Fatal("write preview incorrectly classified existing UTF-8 as binary")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatalf("file contents changed unexpectedly: %v", err)
	}
}
