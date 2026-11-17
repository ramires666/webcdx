package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskGitPathValidation(t *testing.T) {
	for _, valid := range []string{"README.md", ".github/workflows/check.yml", "cmd/agent/new_file.go"} {
		if err := validateTaskGitPath(valid); err != nil {
			t.Errorf("valid path %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", ".", "..", "../README", "/tmp/README", "docs\\README", "docs//README", "docs/a/../README",
		"docs/.Git/config", "GIT~1/config", "C:/file", "file:stream", "docs/foo.", "docs/ file", "space here", "docs/glob*",
		"docs/CON.md", "aux/readme", "LPT9", "com1.txt", "lf\nname", "café.txt"} {
		if err := validateTaskGitPath(invalid); err == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
	if err := checkTaskGitWorktreePaths(t.TempDir(), []string{"Docs/a", "docs/A"}); err == nil {
		t.Fatal("accepted case-insensitive assignment collision")
	}
	if err := checkTaskGitWorktreePaths(t.TempDir(), []string{"Docs/a", "docs/b"}); err == nil {
		t.Fatal("accepted inconsistent directory casing")
	}
}

func taskGitTestRepository(t *testing.T) (string, string, string) {
	t.Helper()
	exe, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required for worktree tests")
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if _, err := runTaskGit(context.Background(), exe, repo, 8<<10, args...); err != nil {
			t.Fatal(err)
		}
	}
	git("init")
	git("config", "user.name", "WebCDX Test")
	git("config", "user.email", "test@example.invalid")
	if err := os.Mkdir(filepath.Join(repo, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	taskGitTestWrite(t, repo, "docs/edit.md", "committed version\n")
	taskGitTestWrite(t, repo, "remove.md", "old text\n")
	taskGitTestWrite(t, repo, "untouched.md", "untouched\n")
	git("add", ".")
	git("commit", "-m", "fixture")
	return exe, repo, root
}

func taskGitTestWrite(t *testing.T, dir, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTaskGitPinnedWorktreeDiff(t *testing.T) {
	git, repo, root := taskGitTestRepository(t)
	ctx := context.Background()
	t.Setenv("GIT_DIR", filepath.Join(root, "unrelated.git"))
	t.Setenv("GIT_WORK_TREE", root)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(root, "foreign-index"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "filter.unwanted.smudge")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	t.Setenv("OPENCODE_API_KEY", "not-for-git")
	for _, variable := range taskGitCommand(ctx, git, repo, "status").Environ() {
		if strings.HasPrefix(variable, "OPENCODE_API_KEY=") {
			t.Fatal("Git inherited a provider credential")
		}
	}
	if _, err := pinTaskGitHEAD(ctx, git, filepath.Join(repo, "docs")); err == nil {
		t.Fatal("accepted a subdirectory as the project root")
	}
	head, err := pinTaskGitHEAD(ctx, git, repo)
	if err != nil {
		t.Fatal(err)
	}
	taskGitTestWrite(t, repo, "docs/edit.md", "uncommitted parent version\n")
	paths := []string{"docs/edit.md", "remove.md", "new/readme.md", "never-created.md"}
	if err := checkTaskGitPaths(ctx, git, repo, head, paths); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docs", "remove.md/child"} {
		if err := checkTaskGitPaths(ctx, git, repo, head, []string{name}); err == nil {
			t.Errorf("accepted %q as an assignment", name)
		}
	}
	if err := createTaskGitWorktree(ctx, git, repo, head, filepath.Join(repo, "child")); err == nil {
		t.Fatal("accepted worktree inside its source repository")
	}
	dir := filepath.Join(root, "worker")
	if err := createTaskGitWorktree(ctx, git, repo, head, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := removeTaskGitWorktree(ctx, git, repo, dir); err != nil {
			t.Error(err)
		}
	})
	if err := checkTaskGitWorktreePaths(dir, paths); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "docs", "edit.md"))
	if err != nil || string(before) != "committed version\n" {
		t.Fatalf("worktree was not pinned: %q, %v", before, err)
	}
	taskGitTestWrite(t, dir, "docs/edit.md", "worker version\n")
	taskGitTestWrite(t, dir, "untouched.md", "unassigned working tree change\n")
	if err := os.Remove(filepath.Join(dir, "remove.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "new"), 0o700); err != nil {
		t.Fatal(err)
	}
	taskGitTestWrite(t, dir, "new/readme.md", "new text\n")
	patch := filepath.Join(root, "worker.patch")
	if err := writeTaskGitDiff(ctx, git, dir, head, paths, patch); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(patch)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"diff --git a/docs/edit.md b/docs/edit.md", "-committed version", "+worker version", "-old text", "+new text"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("patch missing %q:\n%s", want, data)
		}
	}
	for _, unexpected := range []string{"untouched.md", "uncommitted parent version", "never-created.md"} {
		if strings.Contains(string(data), unexpected) {
			t.Errorf("patch included %q:\n%s", unexpected, data)
		}
	}
	stagedPatch := filepath.Join(root, "from-staged.patch")
	if err := writeStagedTaskGitDiff(ctx, git, dir, head, paths, stagedPatch); err != nil {
		t.Fatal(err)
	}
	staged, err := os.ReadFile(stagedPatch)
	if err != nil || string(staged) != string(data) {
		t.Fatalf("index artifact changed after staging: %q, %v", staged, err)
	}
	if err := writeStagedTaskGitDiff(ctx, git, dir, head, paths, patch); err == nil {
		t.Fatal("overwrote existing patch artifact")
	}
	if _, err := runTaskGit(ctx, git, dir, 8<<10, "add", "--", "untouched.md"); err != nil {
		t.Fatal(err)
	}
	if err := writeStagedTaskGitDiff(ctx, git, dir, head, paths, filepath.Join(root, "contaminated.patch")); err == nil {
		t.Fatal("accepted a staged change outside the assigned paths")
	}
}

func TestTaskGitBatchesTreeLookups(t *testing.T) {
	git, repo, _ := taskGitTestRepository(t)
	ctx := context.Background()
	head, err := pinTaskGitHEAD(ctx, git, repo)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"docs/edit.md", "docs/new", "untouched.md", "UNTOUCHED_CASE.md"}
	for i := range 130 {
		paths = append(paths, fmt.Sprintf("docs/worker-%03d-%s.md", i, strings.Repeat("a", 220)))
	}
	tracked, err := inspectTaskGitPaths(ctx, git, repo, head, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracked) != 2 || !tracked["docs/edit.md"] || !tracked["untouched.md"] {
		t.Fatalf("unexpected tracked paths: %#v", tracked)
	}
}

func TestTaskGitRejectsRepositoryFilters(t *testing.T) {
	git, repo, root := taskGitTestRepository(t)
	ctx := context.Background()
	head, err := pinTaskGitHEAD(ctx, git, repo)
	if err != nil {
		t.Fatal(err)
	}
	include := filepath.Join(root, "included.config")
	if err := os.WriteFile(include, []byte("[filter \"test\"]\n\tsmudge = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runTaskGit(ctx, git, repo, 8<<10, "config", "--local", "--add", "include.path", filepath.ToSlash(include)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "worker")
	err = createTaskGitWorktree(ctx, git, repo, head, dir)
	if err == nil || !strings.Contains(err.Error(), "configured Git filters") {
		t.Fatalf("expected effective Git filter rejection, got %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("failed creation left a worktree behind: %v", err)
	}
}

func TestTaskGitRejectsSymlinkComponent(t *testing.T) {
	git, repo, root := taskGitTestRepository(t)
	ctx := context.Background()
	blob, err := runTaskGit(ctx, git, repo, 128, "rev-parse", "HEAD:remove.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runTaskGit(ctx, git, repo, 8<<10, "update-index", "--add",
		"--cacheinfo", "120000,"+strings.TrimSpace(string(blob))+",link"); err != nil {
		t.Fatal(err)
	}
	if _, err := runTaskGit(ctx, git, repo, 8<<10, "commit", "-m", "add symlink fixture"); err != nil {
		t.Fatal(err)
	}
	head, err := pinTaskGitHEAD(ctx, git, repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link", "link/child"} {
		if err := checkTaskGitPaths(ctx, git, repo, head, []string{name}); err == nil {
			t.Errorf("accepted Git symlink path %q", name)
		}
	}
	dir := filepath.Join(root, "plain")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, filepath.Join(dir, "link")); err != nil {
		t.Skipf("filesystem symlink unavailable: %v", err)
	}
	if err := checkTaskGitWorktreePaths(dir, []string{"link/untouched.md"}); err == nil {
		t.Fatal("accepted filesystem symlink ancestor")
	}
}
