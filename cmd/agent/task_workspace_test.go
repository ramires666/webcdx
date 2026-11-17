package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskWorkspaceBuildsAndIntegratesPatches(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git is required")
	}
	ctx := context.Background()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repository")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	g := func(args ...string) string {
		t.Helper()
		out, err := runTaskGit(ctx, git, repo, 64<<10, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return string(out)
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, path), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	g("init", "--quiet")
	write("main.txt", "main base\n")
	write("old.txt", "old file\n")
	g("add", "--", "main.txt", "old.txt")
	g("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--quiet", "-m", "base")
	head := strings.TrimSpace(g("rev-parse", "HEAD"))
	write("main.txt", "local uncommitted edit\n")

	w := &taskWorkspaces{git: git, roots: []string{repo}}
	fixture := func(_ context.Context, _ string, spec taskSpec) taskView {
		switch spec.Key {
		case "edit":
			if !strings.Contains(spec.Prompt, "main base") || strings.Contains(spec.Prompt, "local uncommitted edit") {
				return taskFailure("failed", "did not get the pinned snapshot")
			}
			return taskView{Status: "completed", Output: `{"summary":"update","files":[{"path":"main.txt","content":"new text ё\n"}]}`}
		case "files":
			if !strings.Contains(spec.Prompt, `"content":null`) || !strings.Contains(spec.Prompt, `"writable":false`) {
				return taskFailure("failed", "missing context or new-file metadata")
			}
			return taskView{Status: "completed", Output: `{"summary":"replace other file","files":[{"path":"old.txt","delete":true},{"path":"docs/new.txt","content":"new file\n"}]}`}
		default:
			return taskView{Status: "completed", Output: `{"summary":"outside scope","files":[{"path":"main.txt","content":"out of scope"}]}`}
		}
	}
	s, err := newTaskService(filepath.Join(dir, "service"), 2, func(ctx context.Context, dir string, spec taskSpec) taskView {
		return w.run(ctx, dir, spec, fixture)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	s.workspaces = w
	e := &nativeExecutor{tasks: s, allowedRoots: []string{repo}}
	owner := "first"
	specs := []taskSpec{
		{Key: "edit", Prompt: "edit first file", Timeout: 15, Workspace: &taskWorkspaceSpec{Files: []string{"main.txt"}}},
		{Key: "files", Prompt: "add and remove files", Timeout: 15, Workspace: &taskWorkspaceSpec{Files: []string{"old.txt", "docs/new.txt"}, ContextFiles: []string{"main.txt"}}, DependsOn: []string{"edit"}},
		{Key: "bad", Prompt: "propose an out of scope change", Timeout: 15, Workspace: &taskWorkspaceSpec{Files: []string{"other.txt"}}},
	}
	reply, err := e.startTaskGroup(ctx, owner, "req", repo, specs)
	if err != nil {
		t.Fatal(err)
	}
	s.workers.Wait()
	id := reply.(map[string]any)["batch_id"].(string)
	rows, err := s.ownedTaskViews(owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Status != "completed" || rows[0].Patch == nil || rows[1].Status != "completed" ||
		rows[1].Patch == nil || rows[2].Status != "failed" {
		for _, row := range rows {
			t.Logf("%+v", row)
		}
		t.Fatal("unexpected job statuses")
	}
	picks := []taskPatchSelection{{Key: "edit", SHA256: rows[0].Patch.SHA256}, {Key: "files", SHA256: rows[1].Patch.SHA256}}
	if _, err := e.integrateTaskPatches(ctx, "another client", id, picks); err == nil {
		t.Fatal("another client accessed patch artifacts")
	}
	if _, err := e.integrateTaskPatches(ctx, owner, id, []taskPatchSelection{{Key: "edit", SHA256: strings.Repeat("0", 64)}}); err == nil {
		t.Fatal("expected patch digest is required")
	}
	out, err := e.integrateTaskPatches(ctx, owner, id, picks)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(taskIntegrationView)
	if result.Patch.BaseCommit != head || len(result.Patch.Paths) != 3 {
		t.Fatalf("bad integration result: %+v", result)
	}
	retried, err := e.integrateTaskPatches(ctx, owner, id, picks)
	if err != nil || retried.(taskIntegrationView).ID != result.ID {
		t.Fatalf("invalid integration retry: %+v %v", retried, err)
	}
	var patch strings.Builder
	for offset := 0; ; {
		v, err := s.readPatch(owner, id, "", result.ID, offset, 13)
		if err != nil {
			t.Fatal(err)
		}
		chunk := v.(map[string]any)
		patch.WriteString(chunk["content"].(string))
		if chunk["complete"] == true {
			break
		}
		offset = chunk["next_offset"].(int)
	}
	if hashBytes([]byte(patch.String())) != result.Patch.SHA256 || !strings.Contains(patch.String(), "+new text ё") ||
		!strings.Contains(patch.String(), "deleted file mode") || !strings.Contains(patch.String(), "+new file") {
		t.Fatalf("invalid collected patch: %s", patch.String())
	}
	if data, err := os.ReadFile(filepath.Join(repo, "main.txt")); err != nil || string(data) != "local uncommitted edit\n" {
		t.Fatalf("source checkout was modified: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "docs", "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source checkout received a generated file")
	}

	// Changing HEAD after start may not invalidate a retry of the same request.
	g("add", "main.txt")
	g("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--quiet", "-m", "new head")
	previous, err := e.startTaskGroup(ctx, owner, "req", repo, specs)
	if err != nil || previous.(map[string]any)["batch_id"] != id {
		t.Fatalf("changed HEAD invalidated the original request: %#v %v", previous, err)
	}
}

func TestTaskWorkspaceRejectsScopeAndDependencyConflicts(t *testing.T) {
	for _, pair := range [][2]string{{"same.txt", "same.txt"}, {"Folder/a.txt", "folder/b.txt"}, {"docs", "docs/a.txt"}} {
		spec := []taskSpec{{Key: "a", Workspace: &taskWorkspaceSpec{Files: []string{pair[0]}}},
			{Key: "b", Workspace: &taskWorkspaceSpec{Files: []string{pair[1]}}}}
		if _, err := validateTaskFileScopes(spec); err == nil {
			t.Fatalf("accepted overlapping scopes: %q", pair)
		}
	}
	if err := validateTaskDependencies([]taskSpec{{Key: "a", DependsOn: []string{"b"}}, {Key: "b", DependsOn: []string{"a"}}}); err == nil {
		t.Fatal("accepted cyclic dependency graph")
	}
}
