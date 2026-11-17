package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

func optionalTaskStringArray(args map[string]any, name string, limit int) ([]string, error) {
	value, ok := args[name]
	if !ok {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok || len(items) > limit {
		return nil, toolErr("invalid_arguments", fmt.Sprintf("%s must be an array with at most %d strings", name, limit))
	}
	var result []string
	for _, item := range items {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, toolErr("invalid_arguments", name+" must contain nonempty strings")
		}
		result = append(result, s)
	}
	return result, nil
}

func validateTaskDependencies(specs []taskSpec) error {
	byKey := make(map[string]taskSpec, len(specs))
	for _, spec := range specs {
		byKey[spec.Key] = spec
	}
	state := make(map[string]uint8, len(specs))
	var visit func(string) error
	visit = func(key string) error {
		switch state[key] {
		case 1:
			return toolErr("invalid_arguments", "task dependencies form a cycle at "+key)
		case 2:
			return nil
		}
		state[key] = 1
		seen := map[string]bool{}
		for _, dep := range byKey[key].DependsOn {
			if _, exists := byKey[dep]; !exists || seen[dep] {
				return toolErr("invalid_arguments", "task "+key+" has an unknown or repeated dependency")
			}
			seen[dep] = true
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[key] = 2
		return nil
	}
	for _, spec := range specs {
		if err := visit(spec.Key); err != nil {
			return err
		}
	}
	return nil
}

func validateTaskFileScopes(specs []taskSpec) ([]string, error) {
	writers := map[string]string{}
	selected := map[string]string{}
	spelling := map[string]string{}
	for _, spec := range specs {
		ws := spec.Workspace
		if ws == nil {
			continue
		}
		paths := append(slices.Clone(ws.Files), ws.ContextFiles...)
		if len(paths) > taskFileLimit {
			return nil, toolErr("invalid_arguments", "each task can select at most 16 files")
		}
		if _, err := checkTaskGitPathSet(paths); err != nil {
			return nil, toolErr("invalid_arguments", err.Error())
		}
		for _, path := range paths {
			selected[strings.ToLower(path)] = path
			for prefix := path; prefix != ""; prefix = taskPathParent(prefix) {
				folded := strings.ToLower(prefix)
				if previous, ok := spelling[folded]; ok && previous != prefix {
					return nil, toolErr("task_scope_conflict", "selected paths use different case for the same file or directory")
				}
				spelling[folded] = prefix
			}
		}
		for _, path := range ws.Files {
			lower := strings.ToLower(path)
			if other := writers[lower]; other != "" {
				return nil, toolErr("task_scope_conflict", "tasks "+other+" and "+spec.Key+" both write "+path)
			}
			writers[lower] = spec.Key
		}
	}
	paths := make([]string, 0, len(selected))
	for lower, path := range selected {
		for parent := taskPathParent(lower); parent != ""; parent = taskPathParent(parent) {
			if _, exists := selected[parent]; exists {
				return nil, toolErr("task_scope_conflict", "a selected file is also an ancestor of "+path)
			}
		}
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths, nil
}

func taskPathParent(path string) string {
	i := strings.LastIndexByte(path, '/')
	if i < 0 {
		return ""
	}
	return path[:i]
}

func (e *nativeExecutor) startTaskGroup(ctx context.Context, owner, key, project string, specs []taskSpec) (any, error) {
	s := e.tasks
	paths, err := validateTaskFileScopes(specs)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		if project != "" {
			return nil, toolErr("invalid_arguments", "project_root requires tasks with files or context_files")
		}
		return s.start(owner, key, specs)
	}
	if s.workspaces == nil {
		return nil, toolErr("task_workspace_disabled", "Git workspaces are disabled on this agent")
	}
	repo, err := e.resolvePath(project)
	if err != nil {
		return nil, err
	}
	if !s.workspaces.allows(repo) {
		return nil, toolErr("task_workspace_denied", "project_root is not configured for Git workspaces")
	}
	for _, spec := range specs {
		if spec.Workspace != nil {
			spec.Workspace.Repo = repo
		}
	}
	if previous, ok, err := s.retryTaskGroup(owner, key, specs); ok || err != nil {
		return previous, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	head, err := pinTaskGitHEAD(ctx, s.workspaces.git, repo)
	if err != nil {
		return nil, toolErr("invalid_task_repository", "project_root must be the root of a Git repository with a commit")
	}
	tracked, err := inspectTaskGitPaths(ctx, s.workspaces.git, repo, head, paths)
	if err != nil {
		return nil, toolErr("invalid_task_paths", "selected paths must be safe regular files in the pinned Git tree", err)
	}
	for _, spec := range specs {
		if spec.Workspace == nil {
			continue
		}
		spec.Workspace.Head = head
		for _, path := range spec.Workspace.ContextFiles {
			if !tracked[path] {
				return nil, toolErr("invalid_task_paths", "context file is missing at the pinned commit: "+path)
			}
		}
	}
	return s.start(owner, key, specs)
}

func (s *taskService) retryTaskGroup(owner, key string, specs []taskSpec) (any, bool, error) {
	id := commandHash([]string{owner, key})
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, err := s.load(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, toolErr("task_store_unavailable", "task queue is unavailable")
	}
	if batch.Owner != owner || batch.Fingerprint != commandHash(specs) {
		return nil, true, toolErr("request_key_conflict", "request_key was already used with different tasks")
	}
	return taskBatchStatus(batch), true, nil
}
