package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

const taskGitDiffMax = 4 << 20

var taskGitHashPattern = regexp.MustCompile("^([a-f0-9]{40}|[a-f0-9]{64})$")
var taskGitComponentPattern = regexp.MustCompile("^[a-zA-Z0-9_.-]+$")

// taskGitCommand keeps the selected repository and index independent of the
// parent process's Git environment. Callers supply fixed Git subcommands.
func taskGitCommand(ctx context.Context, gitExe, repo string, args ...string) *exec.Cmd {
	options := []string{
		"--literal-pathspecs",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.attributesFile=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.protectNTFS=true",
		"-c", "core.protectHFS=true",
		"-c", "core.sparseCheckout=false",
		"-c", "core.sparseCheckoutCone=false",
		"-c", "index.sparse=false",
		"-c", "submodule.recurse=false",
	}
	cmd := exec.CommandContext(ctx, gitExe, append(options, args...)...)
	cmd.Dir = repo
	for _, key := range []string{"SystemRoot", "WINDIR", "SystemDrive", "PATH", "TEMP", "TMP", "TMPDIR", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.Env = append(cmd.Env,
		"GIT_ATTR_NOSYSTEM=1", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0",
	)
	cmd.WaitDelay = 3 * time.Second
	return cmd
}

type taskGitOutput struct {
	data bytes.Buffer
	max  int
}

func (w *taskGitOutput) Write(p []byte) (int, error) {
	if len(p) > w.max-w.data.Len() {
		return 0, errors.New("Git output exceeds the configured limit")
	}
	return w.data.Write(p)
}

func runTaskGit(ctx context.Context, gitExe, repo string, limit int, args ...string) ([]byte, error) {
	if !filepath.IsAbs(gitExe) || !filepath.IsAbs(repo) || runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(gitExe), ".exe") {
		return nil, errors.New("Git executable and working directory must be absolute paths")
	}
	cmd := taskGitCommand(ctx, gitExe, repo, args...)
	out := &taskGitOutput{max: limit}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out.data.Bytes(), nil
}

func pinTaskGitHEAD(ctx context.Context, gitExe, repo string) (string, error) {
	root, err := runTaskGit(ctx, gitExe, repo, 32<<10, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return "", err
	}
	gitRoot, err := filepath.EvalSymlinks(strings.TrimRight(string(root), "\r\n"))
	if err != nil {
		return "", err
	}
	requested, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return "", err
	}
	if !taskGitSamePath(gitRoot, requested) {
		return "", errors.New("project path must be the repository root")
	}
	raw, err := runTaskGit(ctx, gitExe, repo, 128, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(raw))
	if !taskGitHashPattern.MatchString(head) {
		return "", errors.New("Git did not return a full commit ID")
	}
	return head, nil
}

func taskGitSamePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func taskGitOutside(repo, path string) error {
	if !filepath.IsAbs(repo) || !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return errors.New("Git worktree and artifact paths must be clean and absolute")
	}
	root, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	target := filepath.Join(parent, filepath.Base(path))
	for current := target; ; current = filepath.Dir(current) {
		if taskGitSamePath(root, current) {
			return errors.New("Git worktree or artifact path is inside the source directory")
		}
		if next := filepath.Dir(current); next == current {
			return nil
		}
	}
}

func checkNoTaskGitFilters(ctx context.Context, gitExe, dir string) error {
	_, err := runTaskGit(ctx, gitExe, dir, 32<<10, "config", "--includes", "--name-only", "--get-regexp", "^filter\\.")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check repository Git filters: %w", err)
	}
	return errors.New("repositories with configured Git filters are unavailable for code tasks")
}

func createTaskGitWorktree(ctx context.Context, gitExe, repo, head, dir string) error {
	if !taskGitHashPattern.MatchString(head) {
		return errors.New("full commit ID required for code task worktree")
	}
	if err := taskGitOutside(repo, dir); err != nil {
		return err
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		return errors.New("code task worktree path must not exist")
	}
	if _, err := runTaskGit(ctx, gitExe, repo, 8<<10, "worktree", "add", "--detach", "--no-checkout", "--", dir, head); err != nil {
		if _, statErr := os.Lstat(dir); statErr == nil {
			return cleanFailedTaskGitWorktree(ctx, gitExe, repo, dir, err)
		}
		return err
	}
	if err := populateTaskGitWorktree(ctx, gitExe, dir, head); err != nil {
		return cleanFailedTaskGitWorktree(ctx, gitExe, repo, dir, err)
	}
	return nil
}

func cleanFailedTaskGitWorktree(ctx context.Context, gitExe, repo, dir string, createErr error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return errors.Join(createErr, removeTaskGitWorktree(cleanupCtx, gitExe, repo, dir))
}

func populateTaskGitWorktree(ctx context.Context, gitExe, dir, head string) error {
	actual, err := pinTaskGitHEAD(ctx, gitExe, dir)
	if err != nil {
		return err
	}
	if actual != head {
		return errors.New("linked Git worktree did not resolve to its pinned commit")
	}
	// Check effective config inside the linked worktree. Including config can
	// depend on the new Git directory or on its detached HEAD.
	if err := checkNoTaskGitFilters(ctx, gitExe, dir); err != nil {
		return err
	}
	_, err = runTaskGit(ctx, gitExe, dir, 8<<10, "read-tree", "--reset", "-u", head)
	return err
}

func removeTaskGitWorktree(ctx context.Context, gitExe, repo, dir string) error {
	if err := taskGitOutside(repo, dir); err != nil {
		return err
	}
	_, err := runTaskGit(ctx, gitExe, repo, 8<<10, "worktree", "remove", "--force", "--", dir)
	return err
}

func validateTaskGitPath(path string) error {
	if path == "" || len(path) > 1024 {
		return errors.New("code task path must be a nonempty project-relative Git path")
	}
	for _, part := range strings.Split(path, "/") {
		if !taskGitComponentPattern.MatchString(part) || len(part) > 255 || part == "." ||
			strings.HasSuffix(part, ".") || strings.EqualFold(part, ".git") {
			return fmt.Errorf("invalid code task path %q", path)
		}
		stem, _, _ := strings.Cut(strings.ToUpper(part), ".")
		switch stem {
		case "CON", "PRN", "AUX", "NUL", "COM0", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
			"LPT0", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return fmt.Errorf("invalid code task path %q", path)
		}
	}
	return nil
}

func checkTaskGitPathSet(paths []string) (map[string]bool, error) {
	if len(paths) == 0 {
		return nil, errors.New("assign at least one exact project-relative file")
	}
	assigned := make(map[string]bool, len(paths))
	folded := make(map[string]bool, len(paths))
	casing := make(map[string]string)
	for _, path := range paths {
		if err := validateTaskGitPath(path); err != nil {
			return nil, err
		}
		fold := strings.ToLower(path)
		if folded[fold] {
			return nil, fmt.Errorf("duplicate code task path %q", path)
		}
		folded[fold] = true
		assigned[path] = true
		for end := strings.LastIndexByte(path, '/'); end >= 0; end = strings.LastIndexByte(path[:end], '/') {
			directory := path[:end]
			key := strings.ToLower(directory)
			if previous := casing[key]; previous != "" && previous != directory {
				return nil, fmt.Errorf("code task paths disagree on the casing of directory %q", directory)
			}
			casing[key] = directory
		}
	}
	for path := range folded {
		for end := strings.LastIndexByte(path, '/'); end >= 0; end = strings.LastIndexByte(path[:end], '/') {
			if folded[path[:end]] {
				return nil, fmt.Errorf("overlapping code task path %q", path)
			}
		}
	}
	return assigned, nil
}

func checkTaskGitPaths(ctx context.Context, gitExe, repo, head string, paths []string) error {
	_, err := inspectTaskGitPaths(ctx, gitExe, repo, head, paths)
	return err
}

// inspectTaskGitPaths permits new files but prevents a regular file, Git link
// or symlink from serving as an ancestor of an assigned path.
func inspectTaskGitPaths(ctx context.Context, gitExe, repo, head string, paths []string) (map[string]bool, error) {
	if !taskGitHashPattern.MatchString(head) {
		return nil, errors.New("full commit ID required to validate code task files")
	}
	if _, err := checkTaskGitPathSet(paths); err != nil {
		return nil, err
	}
	tracked := make(map[string]bool, len(paths))
	if err := inspectTaskGitTree(ctx, gitExe, repo, head, "", paths, tracked); err != nil {
		return nil, err
	}
	return tracked, nil
}

// inspectTaskGitTree queries only immediate names in one tree. Keeping the
// component arguments short and chunking them bounds Windows command length.
func inspectTaskGitTree(ctx context.Context, gitExe, repo, object, prefix string, paths []string, tracked map[string]bool) error {
	children := make(map[string][]string)
	for _, path := range paths {
		name, suffix, parent := strings.Cut(path, "/")
		if parent {
			children[name] = append(children[name], suffix)
		} else {
			children[name] = nil
		}
	}
	names := slices.Sorted(maps.Keys(children))
	for chunk := range slices.Chunk(names, 64) {
		args := append([]string{"ls-tree", "-z", "--full-tree", object, "--"}, chunk...)
		raw, err := runTaskGit(ctx, gitExe, repo, 32<<10, args...)
		if err != nil {
			return err
		}
		for _, line := range bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0}) {
			if len(line) == 0 {
				continue
			}
			header, name, ok := strings.Cut(string(line), "\t")
			mode, rest, modeOK := strings.Cut(header, " ")
			kind, hash, hashOK := strings.Cut(rest, " ")
			if !ok || !modeOK || !hashOK || !slices.Contains(chunk, name) || !taskGitHashPattern.MatchString(hash) {
				return errors.New("Git returned an unexpected entry for an assigned path")
			}
			path := prefix + name
			suffixes := children[name]
			if len(suffixes) == 0 {
				if kind != "blob" || mode != "100644" && mode != "100755" {
					return fmt.Errorf("assigned path %q must be a regular file", path)
				}
				tracked[path] = true
				continue
			}
			if kind != "tree" || mode != "040000" {
				return fmt.Errorf("ancestor %q of an assigned path must be a directory", path)
			}
			if err := inspectTaskGitTree(ctx, gitExe, repo, hash, path+"/", suffixes, tracked); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkTaskGitWorktreePaths verifies every existing component before the
// caller reads or writes files in its private worktree.
func checkTaskGitWorktreePaths(dir string, paths []string) error {
	_, err := inspectTaskGitWorktreePaths(dir, paths)
	return err
}

func inspectTaskGitWorktreePaths(dir string, paths []string) (map[string]bool, error) {
	if _, err := checkTaskGitPathSet(paths); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(dir) {
		return nil, errors.New("code task worktree path must be absolute")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("code task worktree must be a real directory")
	}
	present := make(map[string]bool, len(paths))
	for _, path := range paths {
		current := dir
		parts := strings.Split(path, "/")
		for i, part := range parts {
			next := filepath.Join(current, part)
			info, err := os.Lstat(next)
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			leaf := i == len(parts)-1
			if err != nil || !leaf && !info.IsDir() || leaf && !info.Mode().IsRegular() {
				return nil, fmt.Errorf("assigned path %q is not a regular worktree file", path)
			}
			// Filesystems can resolve a case-insensitive alias as a different
			// tracked file. Require that the name stored on disk matches exactly.
			entries, err := os.ReadDir(current)
			if err != nil {
				return nil, err
			}
			exact := false
			for _, entry := range entries {
				if entry.Name() == part {
					exact = true
					break
				}
			}
			if !exact {
				return nil, fmt.Errorf("assigned path %q does not match its worktree casing", path)
			}
			if leaf {
				present[path] = true
			}
			current = next
		}
	}
	return present, nil
}

func writeTaskGitDiff(ctx context.Context, gitExe, dir, head string, paths []string, artifact string) error {
	if err := taskGitOutside(dir, artifact); err != nil {
		return err
	}
	tracked, err := inspectTaskGitPaths(ctx, gitExe, dir, head, paths)
	if err != nil {
		return err
	}
	present, err := inspectTaskGitWorktreePaths(dir, paths)
	if err != nil {
		return err
	}
	if err := checkNoTaskGitFilters(ctx, gitExe, dir); err != nil {
		return err
	}
	stage := []string{"add", "--all", "--force", "--"}
	for _, path := range paths {
		if tracked[path] || present[path] {
			stage = append(stage, path)
		}
	}
	if len(stage) > 4 {
		if _, err := runTaskGit(ctx, gitExe, dir, 8<<10, stage...); err != nil {
			return err
		}
	}
	return writeStagedTaskGitDiff(ctx, gitExe, dir, head, paths, artifact)
}

// writeStagedTaskGitDiff inspects the existing index before making an artifact.
// It never restages files, including deletions already applied to the index.
func writeStagedTaskGitDiff(ctx context.Context, gitExe, dir, head string, paths []string, artifact string) error {
	if err := taskGitOutside(dir, artifact); err != nil {
		return err
	}
	if !taskGitHashPattern.MatchString(head) {
		return errors.New("full commit ID required for the code task patch")
	}
	assigned, err := checkTaskGitPathSet(paths)
	if err != nil {
		return err
	}
	changed, err := runTaskGit(ctx, gitExe, dir, taskGitDiffMax,
		"diff", "--cached", "--raw", "-z", "--no-abbrev", "--no-ext-diff", "--no-textconv", "--no-renames", head, "--")
	if err != nil {
		return err
	}
	if len(changed) > 0 {
		entries := bytes.Split(bytes.TrimSuffix(changed, []byte{0}), []byte{0})
		if len(entries)%2 != 0 {
			return errors.New("Git returned an invalid staged change")
		}
		for i := 0; i < len(entries); i += 2 {
			path := string(entries[i+1])
			if !assigned[path] {
				return errors.New("code task index contains a change outside its assigned files")
			}
			fields := strings.Fields(string(entries[i]))
			if len(fields) != 5 {
				return errors.New("Git returned an invalid staged file mode")
			}
			oldMode, currentMode, change := fields[0], fields[1], fields[4]
			oldRegular := oldMode == ":100644" || oldMode == ":100755"
			currentRegular := currentMode == "100644" || currentMode == "100755"
			if !(change == "A" && oldMode == ":000000" && currentRegular ||
				change == "D" && oldRegular && currentMode == "000000" ||
				change == "M" && oldRegular && currentRegular) {
				return errors.New("code task index contains a non-regular file change")
			}
		}
	}
	data, err := runTaskGit(ctx, gitExe, dir, taskGitDiffMax, "diff", "--cached", "--patch", "--text",
		"--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--no-relative",
		"--src-prefix=a/", "--dst-prefix=b/", "--unified=3", head, "--")
	if err != nil {
		return err
	}
	return atomicCreate(artifact, data)
}
