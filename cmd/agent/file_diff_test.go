package main

import (
	"bytes"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileDiffShowsOnlyChangedLines(t *testing.T) {
	diff := formatFileDiff("unchanged\n  old one\nkeep\nold two\nend\n", "unchanged\n    new one\nkeep\nnew two\nend\n")
	for _, want := range []string{"До", "После", "- 2   old one", "+ 2     new one", "old two", "new two"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff missing %q: %s", want, diff)
		}
	}
	for _, unchanged := range []string{"unchanged", "keep", "end"} {
		if strings.Contains(diff, unchanged) {
			t.Fatalf("unchanged line shown: %s", diff)
		}
	}
	if got := formatFileDiff("same", "same"); got != "Без изменений" {
		t.Fatal(got)
	}
	if got := formatFileDiff("a\r\n", "a\n"); !strings.Contains(got, "переводы строк") {
		t.Fatal(got)
	}
	if got := formatFileDiff("", "new\n"); !strings.Contains(got, "+ 1 new") {
		t.Fatal(got)
	}
	if got := formatFileDiff("old\n", ""); !strings.Contains(got, "- 1 old") {
		t.Fatal(got)
	}
	if got := formatFileDiff("", strings.Repeat("x\n", 50)); strings.Count(got, "│") != 41 || !strings.Contains(got, "скрыты") {
		t.Fatalf("preview unbounded: %s", got)
	}
	if got := formatFileDiff("", strings.Repeat("x\n", 300)); !strings.Contains(got, "слишком большие") {
		t.Fatal("large rewrite not bounded")
	}
	if got := formatFileDiff("", "\x1b[2J\n"); strings.Contains(got, "\x1b") {
		t.Fatal("diff executes terminal controls")
	}
}

func TestChangedLinesReconstructRepeatedText(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	for sample := 0; sample < 500; sample++ {
		makeLines := func() []string {
			result := []string{}
			for i := 0; i < random.Intn(16); i++ {
				result = append(result, string(rune('a'+random.Intn(4))))
			}
			return result
		}
		a, b := makeLines(), makeLines()
		changes, ok := changedLines(strings.Join(a, "\n"), strings.Join(b, "\n"))
		if !ok {
			t.Fatal("small diff rejected")
		}
		removed, added := map[int]string{}, map[int]string{}
		for _, line := range changes {
			if line.kind == '-' {
				removed[line.number-1] = line.text
			}
			if line.kind == '+' {
				added[line.number-1] = line.text
			}
		}
		remaining := []string{}
		for i, line := range a {
			if text, ok := removed[i]; ok {
				if text != line {
					t.Fatal("removed wrong line")
				}
			} else {
				remaining = append(remaining, line)
			}
		}
		rebuilt := []string{}
		for i := range b {
			if text, ok := added[i]; ok {
				rebuilt = append(rebuilt, text)
			} else {
				if len(remaining) == 0 {
					t.Fatalf("diff lost lines: %v -> %v", a, b)
				}
				rebuilt = append(rebuilt, remaining[0])
				remaining = remaining[1:]
			}
		}
		if len(remaining) != 0 || strings.Join(rebuilt, "\n") != strings.Join(b, "\n") {
			t.Fatalf("diff changed wrong lines: %v -> %v: %v", a, b, changes)
		}
	}
}

func TestFileChangeLoggingOnlyAfterSuccessfulWrite(t *testing.T) {
	var console bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&console)
	t.Cleanup(func() { log.SetOutput(previous) })
	executor, root := testExecutor(t)
	path := filepath.Join(root, "new-folder", "текст.md")
	created := callToolV4(t, executor, "write_file", map[string]any{"path": path, "content": "unchanged\nold\n", "if_absent": true})
	if !strings.Contains(console.String(), "Создана папка") || !strings.Contains(console.String(), "+ 2 old") {
		t.Fatalf("creation not shown: %s", console.String())
	}
	console.Reset()
	callToolV4(t, executor, "edit_file", map[string]any{"path": path, "expected_sha256": created["sha256"], "edits": []any{map[string]any{"old_text": "old", "new_text": "new"}}})
	if strings.Contains(console.String(), "unchanged") || !strings.Contains(console.String(), "- 2 old") || !strings.Contains(console.String(), "+ 2 new") {
		t.Fatalf("edit preview: %s", console.String())
	}
	console.Reset()
	if _, err := executor.editFile(map[string]any{"path": path, "expected_sha256": created["sha256"], "edits": []any{map[string]any{"old_text": "new", "new_text": "bad"}}}); err == nil {
		t.Fatal("stale edit accepted")
	}
	if console.Len() != 0 {
		t.Fatalf("failed write logged as saved: %s", console.String())
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "unchanged\nnew\n" {
		t.Fatalf("logging changed file behavior: %q %v", data, err)
	}
}
