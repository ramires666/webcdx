package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type taskPatchSelection struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
}

type taskIntegrationView struct {
	ID    string               `json:"integration_id"`
	Patch taskPatchView        `json:"patch"`
	Tasks []taskPatchSelection `json:"tasks"`
}

func parseTaskPatchSelection(raw any) ([]taskPatchSelection, error) {
	rows, ok := raw.([]any)
	if !ok || len(rows) == 0 || len(rows) > taskBatchMax {
		return nil, toolErr("invalid_arguments", "patches must select 1 to 100 task patches")
	}
	seen := make(map[string]bool, len(rows))
	selected := make([]taskPatchSelection, 0, len(rows))
	for _, row := range rows {
		args, ok := row.(map[string]any)
		if !ok {
			return nil, toolErr("invalid_arguments", "each patch selection must be an object")
		}
		if err := onlyArgs(args, "key", "sha256"); err != nil {
			return nil, err
		}
		key, _ := args["key"].(string)
		sha, _ := args["sha256"].(string)
		if !taskNamePattern.MatchString(key) || !taskIDPattern.MatchString(sha) || seen[key] {
			return nil, toolErr("invalid_arguments", "patches require unique task keys and lowercase SHA-256 digests")
		}
		seen[key] = true
		selected = append(selected, taskPatchSelection{Key: key, SHA256: sha})
	}
	slices.SortFunc(selected, func(a, b taskPatchSelection) int { return strings.Compare(a.Key, b.Key) })
	return selected, nil
}

func (s *taskService) ownedTaskViews(owner, id string) ([]taskView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, err := s.load(id)
	if errors.Is(err, os.ErrNotExist) || err == nil && batch.Owner != owner {
		return nil, toolErr("unknown_task_batch", "task batch not found")
	}
	if err != nil {
		return nil, toolErr("task_store_unavailable", "could not read task batch")
	}
	return slices.Clone(batch.Jobs), nil
}

func taskViewByKey(jobs []taskView, key string) (taskView, bool) {
	for _, job := range jobs {
		if job.Key == key {
			return job, true
		}
	}
	return taskView{}, false
}

func (s *taskService) taskPatchPath(batch, key string) string {
	return filepath.Join(s.root, batch+"-"+key+".patch")
}

func (s *taskService) integrationPaths(batch, id string) (string, string) {
	prefix := filepath.Join(s.root, batch+"-integration-"+id)
	return prefix + ".json", prefix + ".patch"
}

func loadTaskIntegration(path, id string) (taskIntegrationView, error) {
	var v taskIntegrationView
	raw, err := readTaskFile(path, taskPatchBytes)
	if err != nil {
		return v, err
	}
	if json.Unmarshal(raw, &v) != nil || v.ID != id || !taskIDPattern.MatchString(v.Patch.SHA256) {
		return v, errors.New("invalid integration metadata")
	}
	return v, nil
}

func verifyTaskPatch(path string, info taskPatchView) ([]byte, error) {
	data, err := readTaskPatchFile(path)
	if err != nil || len(data) != info.Bytes || hashBytes(data) != info.SHA256 {
		return nil, toolErr("task_patch_unavailable", "task patch is missing or differs from its recorded digest")
	}
	return data, nil
}

func (s *taskService) readPatch(owner, batchID, key, integrationID string, offset, limit int) (any, error) {
	jobs, err := s.ownedTaskViews(owner, batchID)
	if err != nil {
		return nil, err
	}
	var info taskPatchView
	var path string
	if integrationID != "" {
		manifest, artifact := s.integrationPaths(batchID, integrationID)
		entry, err := loadTaskIntegration(manifest, integrationID)
		if err != nil {
			return nil, toolErr("unknown_task_integration", "integration result not found")
		}
		info = entry.Patch
		path = artifact
	} else {
		job, ok := taskViewByKey(jobs, key)
		if !ok || job.Status != "completed" || job.Patch == nil {
			return nil, toolErr("unknown_task_patch", "the task has no completed patch")
		}
		info = *job.Patch
		path = s.taskPatchPath(batchID, key)
	}
	data, err := verifyTaskPatch(path, info)
	if err != nil {
		return nil, err
	}
	if offset > len(data) || offset < len(data) && !utf8.RuneStart(data[offset]) {
		return nil, toolErr("invalid_arguments", "offset must be at a UTF-8 boundary within the patch")
	}
	next := min(offset+limit, len(data))
	for next < len(data) && !utf8.RuneStart(data[next]) {
		next--
	}
	return map[string]any{"patch": info, "content": string(data[offset:next]),
		"next_offset": next, "complete": next == len(data)}, nil
}

func (e *nativeExecutor) integrateTaskPatches(ctx context.Context, owner, batchID string, selection []taskPatchSelection) (any, error) {
	s := e.tasks
	w := s.workspaces
	if w == nil {
		return nil, toolErr("task_workspace_disabled", "Git workspaces are disabled on this agent")
	}
	jobs, err := s.ownedTaskViews(owner, batchID)
	if err != nil {
		return nil, err
	}
	var project, head string
	scopes := make([]taskSpec, 0, len(selection))
	for _, pick := range selection {
		job, ok := taskViewByKey(jobs, pick.Key)
		if !ok || job.Status != "completed" || job.Patch == nil || job.Patch.SHA256 != pick.SHA256 {
			return nil, toolErr("task_patch_mismatch", "select a completed task patch by its exact SHA-256: "+pick.Key)
		}
		if project == "" {
			project, head = job.Patch.ProjectRoot, job.Patch.BaseCommit
		}
		if project != job.Patch.ProjectRoot || head != job.Patch.BaseCommit {
			return nil, toolErr("task_patch_mismatch", "selected patches must share a repository and base commit")
		}
		scopes = append(scopes, taskSpec{Key: pick.Key, Workspace: &taskWorkspaceSpec{Files: job.Patch.Paths}})
	}
	resolved, err := e.resolvePath(project)
	if err != nil || !taskGitSamePath(project, resolved) || !w.allows(resolved) {
		return nil, toolErr("task_workspace_denied", "the patch repository is no longer configured for this connection")
	}
	paths, err := validateTaskFileScopes(scopes)
	if err != nil || len(paths) == 0 {
		return nil, toolErr("task_scope_conflict", "selected patches overlap or contain invalid paths")
	}

	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	id := commandHash(selection)
	manifest, artifact := s.integrationPaths(batchID, id)
	if !w.integrateMu.TryLock() {
		return nil, toolErr("task_integration_busy", "another integration is in progress; retry this selection")
	}
	defer w.integrateMu.Unlock()
	if v, err := loadTaskIntegration(manifest, id); err == nil {
		if _, err := verifyTaskPatch(artifact, v.Patch); err != nil {
			return nil, err
		}
		return v, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, toolErr("task_store_unavailable", "could not read the integration result")
	}
	dir, err := os.MkdirTemp(s.root, "integration-")
	if err != nil {
		return nil, toolErr("task_store_unavailable", "could not create the private integration directory")
	}
	worktree := filepath.Join(dir, "worktree")
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := createTaskGitWorktree(ctx, w.git, project, head, worktree); err != nil {
		return nil, toolErr("task_integration_failed", "could not create the integration worktree")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		keep = removeTaskGitWorktree(cleanup, w.git, project, worktree) != nil
	}()

	for _, pick := range selection {
		job, _ := taskViewByKey(jobs, pick.Key)
		data, err := verifyTaskPatch(s.taskPatchPath(batchID, pick.Key), *job.Patch)
		if err != nil {
			return nil, err
		}
		cmd := taskGitCommand(ctx, w.git, worktree, "apply", "--index", "--whitespace=nowarn", "-")
		cmd.Stdin = bytes.NewReader(data)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := cmd.Run(); err != nil {
			return nil, toolErr("task_integration_conflict", "could not apply patch in the integration worktree: "+pick.Key)
		}
	}
	temp := filepath.Join(dir, "combined.patch")
	if err := writeStagedTaskGitDiff(ctx, w.git, worktree, head, paths, temp); err != nil {
		return nil, toolErr("task_integration_failed", "could not produce the combined patch")
	}
	data, err := readTaskPatchFile(temp)
	if err != nil || len(data) == 0 {
		return nil, toolErr("task_integration_failed", "integration did not produce a readable patch")
	}
	result := taskIntegrationView{ID: id, Tasks: selection, Patch: taskPatchView{
		SHA256: hashBytes(data), Bytes: len(data), Paths: paths, BaseCommit: head, ProjectRoot: project,
	}}
	if err := atomicCreate(artifact, data); err != nil {
		// An earlier attempt can have persisted the patch before its manifest.
		if _, verifyErr := verifyTaskPatch(artifact, result.Patch); verifyErr != nil {
			return nil, toolErr("task_store_unavailable", "could not persist the combined patch")
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil || atomicCreate(manifest, encoded) != nil {
		return nil, toolErr("task_store_unavailable", "could not persist the integration result")
	}
	return result, nil
}
