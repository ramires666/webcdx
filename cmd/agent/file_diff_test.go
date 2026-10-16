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

func TestFileDiffExplainsMissingCounterparts(t *testing.T) {
	for _, test := range []struct {
		name, before, after string
		want                []string
	}{
		{"empty before", "", "new\n", []string{"До (пусто)", "(нет строки)", "+ 1 new"}},
		{"empty after", "old\n", "", []string{"После (пусто)", "- 1 old", "│ (нет строки)"}},
		{"insert", "keep\n", "new\nkeep\n", []string{"(нет строки)", "+ 1 new"}},
		{"delete", "old\nkeep\n", "keep\n", []string{"- 1 old", "│ (нет строки)"}},
		{"expand", "old\n", "new\nextra\n", []string{"- 1 old", "+ 1 new", "(нет строки)", "+ 2 extra"}},
		{"contract", "old\nextra\n", "new\n", []string{"- 1 old", "+ 1 new", "- 2 extra", "│ (нет строки)"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			diff := formatFileDiff(test.before, test.after)
			for _, want := range test.want {
				if !strings.Contains(diff, want) {
					t.Fatalf("diff missing %q: %s", want, diff)
				}
			}
			if strings.Contains(diff, "keep") {
				t.Fatalf("unchanged line shown: %s", diff)
			}
		})
	}
}

func TestWriteFileLogsOriginalContents(t *testing.T) {
	var console bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&console)
	t.Cleanup(func() { log.SetOutput(previous) })
	executor, root := testExecutor(t)
	for _, version := range []string{"v3", "v4"} {
		t.Run(version, func(t *testing.T) {
			console.Reset()
			path := filepath.Join(root, version+".py")
			if err := os.WriteFile(path, []byte("keep\nold value\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"path": path, "content": "keep\nnew value\n"}
			if version == "v4" {
				args["expected_sha256"] = hashBytes([]byte("keep\nold value\n"))
				callToolV4(t, executor, "write_file", args)
			} else {
				callTool(t, executor, "write_file", args)
			}
			for _, want := range []string{"Сохранён файл", "- 2 old value", "+ 2 new value"} {
				if !strings.Contains(console.String(), want) {
					t.Fatalf("original contents missing %q: %s", want, console.String())
				}
			}
			if strings.Contains(console.String(), "ранее не существовал") || strings.Contains(console.String(), "keep") {
				t.Fatalf("incorrect replacement preview: %s", console.String())
			}
		})
	}
}

func TestFileChangeLoggingDistinguishesNewAndEmptyFiles(t *testing.T) {
	var console bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&console)
	t.Cleanup(func() { log.SetOutput(previous) })
	executor, root := testExecutor(t)
	path := filepath.Join(root, "empty.py")
	created := callToolV4(t, executor, "write_file", map[string]any{"path": path, "content": "", "if_absent": true})
	if !strings.Contains(console.String(), "Создан файл (ранее не существовал)") || !strings.Contains(console.String(), "Пустой файл") || strings.Contains(console.String(), "Без изменений") {
		t.Fatalf("empty file creation not explained: %s", console.String())
	}
	console.Reset()
	callToolV4(t, executor, "write_file", map[string]any{"path": path, "content": "new\n", "expected_sha256": created["sha256"]})
	if !strings.Contains(console.String(), "Сохранён файл") || !strings.Contains(console.String(), "До (пусто)") || strings.Contains(console.String(), "ранее не существовал") {
		t.Fatalf("existing empty file shown as new: %s", console.String())
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
	if !strings.Contains(console.String(), "Создана папка") || !strings.Contains(console.String(), "Создан файл (ранее не существовал)") || !strings.Contains(console.String(), "До (пусто)") || !strings.Contains(console.String(), "+ 2 old") {
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
