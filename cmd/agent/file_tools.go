package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func (e *nativeExecutor) readFile(args map[string]any) (any, error) {
	if err := onlyArgs(args, "path", "offset", "limit", "delivery"); err != nil {
		return nil, err
	}
	rawPath, err := requiredString(args, "path")
	if err != nil {
		return nil, err
	}
	path, err := e.resolvePath(rawPath)
	if err != nil {
		return nil, err
	}
	data, _, err := readRegularFile(path)
	if err != nil {
		return nil, err
	}
	delivery, err := optionalString(args, "delivery")
	if err != nil {
		return nil, err
	}
	if delivery != "" && delivery != "text" && delivery != "file" {
		return nil, toolErr("invalid_arguments", "delivery must be text or file")
	}
	if isBinary(data) && delivery == "file" {
		if _, ok := args["offset"]; ok {
			return nil, toolErr("invalid_arguments", "line ranges are not available for binary file attachments")
		}
		if _, ok := args["limit"]; ok {
			return nil, toolErr("invalid_arguments", "line ranges are not available for binary file attachments")
		}
		return attachReadFile(map[string]any{"path": path, "sha256": hashBytes(data), "size": len(data), "offset": 1, "line_count": 0, "next_offset": 1, "truncated": false}, data, filepath.Base(path)), nil
	}
	if isBinary(data) {
		return nil, toolErr("binary_file", "binary files are not supported")
	}
	offset, err := optionalInt(args, "offset", 1, 1, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	limit, err := optionalInt(args, "limit", int(^uint(0)>>1), 1, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	lines := strings.SplitAfter(string(data), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	} else if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start := min(offset-1, len(lines))
	requestedEnd := start + min(limit, len(lines)-start)
	end := requestedEnd
	content := strings.Join(lines[start:end], "")
	result := map[string]any{
		"path": path, "content": content, "sha256": hashBytes(data), "size": len(data), "offset": offset,
		"line_count": end - start, "next_offset": end + 1, "truncated": end < len(lines),
	}
	if delivery == "file" {
		name := filepath.Base(path)
		if start != 0 || end != len(lines) {
			name += fmt.Sprintf(".lines-%d-%d.txt", offset, end)
		}
		return attachReadFile(result, []byte(content), name), nil
	}
	return result, nil
}

func (e *nativeExecutor) writeFile(args map[string]any, requirePrecondition bool) (any, error) {
	allowed := []string{"path", "content"}
	if requirePrecondition {
		allowed = append(allowed, "if_absent", "expected_sha256")
	}
	if err := onlyArgs(args, allowed...); err != nil {
		return nil, err
	}
	rawPath, err := requiredString(args, "path")
	if err != nil {
		return nil, err
	}
	path, err := e.resolvePath(rawPath)
	if err != nil {
		return nil, err
	}
	content, ok := args["content"].(string)
	if !ok {
		return nil, toolErr("invalid_arguments", "content must be a string")
	}
	createOnly := false
	if requirePrecondition {
		ifAbsent, hasIfAbsent, err := optionalBool(args, "if_absent")
		if err != nil {
			return nil, err
		}
		expected, err := optionalString(args, "expected_sha256")
		if err != nil {
			return nil, err
		}
		if (hasIfAbsent && ifAbsent && expected == "") == (expected != "" && !hasIfAbsent) {
			return nil, toolErr("invalid_arguments", "exactly one of if_absent=true or expected_sha256 is required")
		}
		if hasIfAbsent && !ifAbsent {
			return nil, toolErr("invalid_arguments", "if_absent must be true when provided")
		}
		if ifAbsent {
			createOnly = true
			if _, err := os.Lstat(path); err == nil {
				return nil, toolErr("already_exists", "destination already exists")
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, toolErr("io_error", "inspect destination", err)
			}
		} else if err := requireFileHash(path, expected); err != nil {
			return nil, err
		}
	}
	data := []byte(content)
	before, _, previewErr := readRegularFile(path)
	_, inspectErr := os.Lstat(path)
	previewOK := previewErr == nil && !isBinary(before) || errors.Is(inspectErr, os.ErrNotExist)
	_, parentErr := os.Stat(filepath.Dir(path))
	if createOnly {
		err = atomicCreate(path, data)
	} else {
		err = atomicWrite(path, data, true)
	}
	if err != nil {
		return nil, err
	}
	if errors.Is(parentErr, os.ErrNotExist) {
		e.logf("✓ Создана папка %s", compactString(filepath.Dir(path), 180))
	}
	if previewOK {
		e.logFileChange(path, before, data, errors.Is(inspectErr, os.ErrNotExist))
	} else {
		e.logf("✓ Сохранён файл %s; сравнение недоступно", compactString(path, 180))
	}
	return map[string]any{"path": path, "bytes_written": len(data), "sha256": hashBytes(data)}, nil
}

func (e *nativeExecutor) editFile(args map[string]any) (any, error) {
	if err := onlyArgs(args, "path", "expected_sha256", "edits"); err != nil {
		return nil, err
	}
	rawPath, err := requiredString(args, "path")
	if err != nil {
		return nil, err
	}
	path, err := e.resolvePath(rawPath)
	if err != nil {
		return nil, err
	}
	expected, err := requiredString(args, "expected_sha256")
	if err != nil {
		return nil, err
	}
	data, _, err := readRegularFile(path)
	if err != nil {
		return nil, err
	}
	if isBinary(data) {
		return nil, toolErr("binary_file", "binary files are not supported")
	}
	oldHash := hashBytes(data)
	if err := validateHash(expected); err != nil {
		return nil, err
	}
	if oldHash != expected {
		return nil, toolErr("stale_hash", "expected_sha256 does not match the current file")
	}
	rawEdits, ok := args["edits"].([]any)
	if !ok || len(rawEdits) == 0 {
		return nil, toolErr("invalid_arguments", "edits must be a non-empty array")
	}
	content := string(data)
	replacements := 0
	for index, rawEdit := range rawEdits {
		edit, ok := rawEdit.(map[string]any)
		if !ok {
			return nil, toolErr("invalid_arguments", fmt.Sprintf("edit %d must be an object", index))
		}
		if err := onlyArgs(edit, "old_text", "new_text", "replace_all"); err != nil {
			return nil, err
		}
		oldText, ok := edit["old_text"].(string)
		if !ok || oldText == "" {
			return nil, toolErr("invalid_arguments", fmt.Sprintf("edit %d old_text must be non-empty", index))
		}
		newText, ok := edit["new_text"].(string)
		if !ok {
			return nil, toolErr("invalid_arguments", fmt.Sprintf("edit %d new_text must be a string", index))
		}
		replaceAll, _, err := optionalBool(edit, "replace_all")
		if err != nil {
			return nil, err
		}
		count := strings.Count(content, oldText)
		if count == 0 {
			return nil, toolErr("text_not_found", fmt.Sprintf("edit %d old_text was not found", index))
		}
		if !replaceAll && count != 1 {
			return nil, toolErr("ambiguous_match", fmt.Sprintf("edit %d old_text occurs %d times", index, count))
		}
		if replaceAll {
			content = strings.ReplaceAll(content, oldText, newText)
			replacements += count
		} else {
			content = strings.Replace(content, oldText, newText, 1)
			replacements++
		}
	}
	newData := []byte(content)
	if err := atomicWrite(path, newData, false); err != nil {
		return nil, err
	}
	e.logFileChange(path, data, newData, false)
	return map[string]any{
		"path": path, "old_sha256": oldHash, "new_sha256": hashBytes(newData),
		"replacements": replacements, "bytes_written": len(newData),
	}, nil
}

func (e *nativeExecutor) listDirectory(args map[string]any) (any, error) {
	if err := onlyArgs(args, "path", "max_entries"); err != nil {
		return nil, err
	}
	rawPath, err := requiredString(args, "path")
	if err != nil {
		return nil, err
	}
	path, err := e.resolvePath(rawPath)
	if err != nil {
		return nil, err
	}
	maxEntries, err := optionalInt(args, "max_entries", int(^uint(0)>>1), 1, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, pathError("list directory", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		left, right := strings.ToLower(entries[i].Name()), strings.ToLower(entries[j].Name())
		if left == right {
			return entries[i].Name() < entries[j].Name()
		}
		return left < right
	})
	total := len(entries)
	if len(entries) > maxEntries {
		entries = entries[:maxEntries]
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{"name": entry.Name(), "type": "unknown", "size": 0, "modified_at": "", "error": nil}
		info, infoErr := entry.Info()
		if infoErr != nil {
			item["error"] = infoErr.Error()
			items = append(items, item)
			continue
		}
		item["type"] = fileType(info)
		item["size"] = info.Size()
		item["modified_at"] = info.ModTime().UTC().Format(timeFormat)
		items = append(items, item)
	}
	return map[string]any{"path": path, "entries": items, "truncated": total > len(entries), "total_entries": total}, nil
}

type searchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func (e *nativeExecutor) searchFiles(ctx context.Context, args map[string]any) (any, error) {
	if err := onlyArgs(args, "path", "pattern", "glob", "regex", "max_results"); err != nil {
		return nil, err
	}
	rawRoot, err := requiredString(args, "path")
	if err != nil {
		return nil, err
	}
	root, err := e.resolvePath(rawRoot)
	if err != nil {
		return nil, err
	}
	pattern, err := requiredString(args, "pattern")
	if err != nil {
		return nil, err
	}
	glob, err := optionalString(args, "glob")
	if err != nil {
		return nil, err
	}
	if glob != "" {
		if _, err := filepath.Match(glob, "probe"); err != nil {
			return nil, toolErr("invalid_arguments", "invalid glob", err)
		}
	}
	maxResults, err := optionalInt(args, "max_results", int(^uint(0)>>1), 1, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	useRegex, _, err := optionalBool(args, "regex")
	if err != nil {
		return nil, err
	}
	var expression *regexp.Regexp
	if useRegex {
		expression, err = regexp.Compile(pattern)
		if err != nil {
			return nil, toolErr("invalid_arguments", "invalid regular expression", err)
		}
	}
	matches := make([]searchMatch, 0, min(maxResults, 200))
	accessErrors := []string{}
	truncated := false
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			appendAccessError(&accessErrors, path, walkErr)
			return nil
		}
		if entry.IsDir() {
			if path != root && (skippedDirectory(entry.Name()) || samePath(path, e.logDir)) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if len(matches) >= maxResults {
			truncated = true
			return fs.SkipAll
		}
		if glob != "" {
			matched, _ := filepath.Match(glob, entry.Name())
			if !matched {
				return nil
			}
		}
		safePath, resolveErr := e.resolvePath(path)
		if resolveErr != nil {
			appendAccessError(&accessErrors, path, resolveErr)
			return nil
		}
		data, readErr := os.ReadFile(safePath)
		if readErr != nil {
			appendAccessError(&accessErrors, safePath, readErr)
			return nil
		}
		if isBinary(data) {
			return nil
		}
		for index, rawLine := range strings.Split(string(data), "\n") {
			lineNumber, line := index+1, strings.TrimSuffix(rawLine, "\r")
			found := strings.Contains(line, pattern)
			if expression != nil {
				found = expression.MatchString(line)
			}
			if !found {
				continue
			}
			matches = append(matches, searchMatch{Path: safePath, Line: lineNumber, Text: line})
			if len(matches) >= maxResults {
				truncated = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, toolErr("io_error", "search files", err)
	}
	return map[string]any{"path": root, "matches": matches, "errors": accessErrors, "truncated": truncated}, nil
}

func (e *nativeExecutor) findFiles(ctx context.Context, args map[string]any) (any, error) {
	if err := onlyArgs(args, "path", "name_glob", "kind", "max_results"); err != nil {
		return nil, err
	}
	rawRoot, err := requiredString(args, "path")
	if err != nil {
		return nil, err
	}
	root, err := e.resolvePath(rawRoot)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, pathError("inspect root", err)
	}
	if !info.IsDir() {
		return nil, toolErr("not_directory", "path is not a directory")
	}
	glob, err := optionalString(args, "name_glob")
	if err != nil {
		return nil, err
	}
	if glob == "" {
		glob = "*"
	}
	if _, err := filepath.Match(glob, "probe"); err != nil {
		return nil, toolErr("invalid_arguments", "invalid name_glob", err)
	}
	kind, err := optionalString(args, "kind")
	if err != nil {
		return nil, err
	}
	if kind == "" {
		kind = "file"
	}
	if kind != "file" && kind != "directory" && kind != "any" {
		return nil, toolErr("invalid_arguments", "kind must be file, directory, or any")
	}
	maxResults, err := optionalInt(args, "max_results", int(^uint(0)>>1), 1, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	entries := []map[string]any{}
	accessErrors := []string{}
	truncated := false
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			appendAccessError(&accessErrors, path, walkErr)
			return nil
		}
		if path == root {
			return nil
		}
		if entry.IsDir() && (skippedDirectory(entry.Name()) || samePath(path, e.logDir)) {
			return filepath.SkipDir
		}
		matched, _ := filepath.Match(glob, entry.Name())
		if !matched {
			return nil
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			appendAccessError(&accessErrors, path, infoErr)
			return nil
		}
		entryType := fileType(entryInfo)
		isDirectory := entryInfo.IsDir()
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			if target, statErr := os.Stat(path); statErr == nil {
				isDirectory = target.IsDir()
			}
		}
		if kind == "file" && (isDirectory || entryType == "symlink") || kind == "directory" && !isDirectory {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			appendAccessError(&accessErrors, path, relErr)
			return nil
		}
		relative = filepath.ToSlash(relative)
		if len(entries) >= maxResults {
			truncated = true
			return fs.SkipAll
		}
		entries = append(entries, map[string]any{
			"path": filepath.Clean(path), "relative_path": relative, "type": entryType,
			"size": entryInfo.Size(), "modified_at": entryInfo.ModTime().UTC().Format(timeFormat),
		})
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, toolErr("io_error", "find files", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i]["relative_path"].(string), entries[j]["relative_path"].(string)
		lowerLeft, lowerRight := strings.ToLower(left), strings.ToLower(right)
		if lowerLeft == lowerRight {
			return left < right
		}
		return lowerLeft < lowerRight
	})
	return map[string]any{"path": root, "entries": entries, "errors": accessErrors, "truncated": truncated}, nil
}

func (e *nativeExecutor) movePath(args map[string]any) (any, error) {
	if err := onlyArgs(args, "source", "destination", "expected_sha256"); err != nil {
		return nil, err
	}
	rawSource, err := requiredString(args, "source")
	if err != nil {
		return nil, err
	}
	rawDestination, err := requiredString(args, "destination")
	if err != nil {
		return nil, err
	}
	source, err := e.resolvePath(rawSource)
	if err != nil {
		return nil, err
	}
	destination, err := e.resolvePath(rawDestination)
	if err != nil {
		return nil, err
	}
	if samePath(source, destination) {
		return nil, toolErr("same_path", "source and destination must be different")
	}
	if err := rejectFinalSymlink(rawSource); err != nil {
		return nil, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, pathError("inspect source", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		return nil, toolErr("already_exists", "destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, toolErr("io_error", "inspect destination", err)
	}
	kind := ""
	if info.Mode().IsRegular() {
		expected, err := requiredString(args, "expected_sha256")
		if err != nil {
			return nil, err
		}
		if err := requireFileHash(source, expected); err != nil {
			return nil, err
		}
		kind = "file"
	} else if info.IsDir() {
		if err := requireEmptyDirectory(source); err != nil {
			return nil, err
		}
		if _, exists := args["expected_sha256"]; exists {
			return nil, toolErr("invalid_arguments", "expected_sha256 is only valid for regular files")
		}
		kind = "directory"
	} else {
		return nil, toolErr("unsupported_type", "only regular files and empty directories can be moved")
	}
	if _, err := os.Stat(filepath.Dir(destination)); err != nil {
		return nil, pathError("inspect destination parent", err)
	}
	if err := os.Rename(source, destination); err != nil {
		return nil, toolErr("rename_failed", "rename path", err)
	}
	return map[string]any{"source": source, "destination": destination, "type": kind}, nil
}

func (e *nativeExecutor) deletePath(args map[string]any) (any, error) {
	if err := onlyArgs(args, "path", "expected_sha256"); err != nil {
		return nil, err
	}
	rawPath, err := requiredString(args, "path")
	if err != nil {
		return nil, err
	}
	path, err := e.resolvePath(rawPath)
	if err != nil {
		return nil, err
	}
	if err := rejectFinalSymlink(rawPath); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, pathError("inspect path", err)
	}
	kind := ""
	if info.Mode().IsRegular() {
		expected, err := requiredString(args, "expected_sha256")
		if err != nil {
			return nil, err
		}
		if err := requireFileHash(path, expected); err != nil {
			return nil, err
		}
		kind = "file"
	} else if info.IsDir() {
		if err := requireEmptyDirectory(path); err != nil {
			return nil, err
		}
		if _, exists := args["expected_sha256"]; exists {
			return nil, toolErr("invalid_arguments", "expected_sha256 is only valid for regular files")
		}
		kind = "directory"
	} else {
		return nil, toolErr("unsupported_type", "only regular files and empty directories can be deleted")
	}
	if err := os.Remove(path); err != nil {
		return nil, toolErr("delete_failed", "delete path", err)
	}
	return map[string]any{"path": path, "deleted": true, "type": kind}, nil
}

func readRegularFile(path string) ([]byte, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, pathError("inspect file", err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, toolErr("not_file", "path is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, toolErr("io_error", "read file", err)
	}
	return data, info, nil
}

func atomicWrite(path string, data []byte, createParents bool) error {
	if createParents {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return toolErr("io_error", "create parent directory", err)
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".webcodex-*")
	if err != nil {
		return toolErr("io_error", "create temporary file", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err = temporary.Write(data); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return toolErr("io_error", "write temporary file", err)
	}
	if err := atomicReplace(temporaryName, path); err != nil {
		return toolErr("io_error", "replace file", err)
	}
	return nil
}

func atomicCreate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return toolErr("io_error", "create parent directory", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".webcodex-*")
	if err != nil {
		return toolErr("io_error", "create temporary file", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err = temporary.Write(data); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return toolErr("io_error", "write temporary file", err)
	}
	if err := os.Link(temporaryName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return toolErr("already_exists", "destination already exists")
		}
		return toolErr("io_error", "create file", err)
	}
	return nil
}

func requireFileHash(path, expected string) error {
	if err := validateHash(expected); err != nil {
		return err
	}
	data, _, err := readRegularFile(path)
	if err != nil {
		return err
	}
	if hashBytes(data) != expected {
		return toolErr("stale_hash", "expected_sha256 does not match the current file")
	}
	return nil
}

func validateHash(value string) error {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || value != strings.ToLower(value) {
		return toolErr("invalid_arguments", "expected_sha256 must be 64 lowercase hexadecimal characters")
	}
	return nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func requireEmptyDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return pathError("open directory", err)
	}
	defer directory.Close()
	entries, err := directory.Readdirnames(1)
	if err == nil || len(entries) > 0 {
		return toolErr("not_empty", "directory is not empty")
	}
	if !errors.Is(err, io.EOF) {
		return toolErr("io_error", "inspect directory", err)
	}
	return nil
}

func rejectFinalSymlink(rawPath string) error {
	info, err := os.Lstat(filepath.Clean(rawPath))
	if err != nil {
		return pathError("inspect path", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return toolErr("symlink_unsupported", "symlinks are not accepted for this operation")
	}
	return nil
}

func pathError(action string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return toolErr("not_found", action, err)
	}
	if errors.Is(err, os.ErrPermission) {
		return toolErr("access_denied", action, err)
	}
	return toolErr("io_error", action, err)
}

func appendAccessError(errorsList *[]string, path string, err error) {
	if len(*errorsList) < 100 {
		*errorsList = append(*errorsList, fmt.Sprintf("%s: %v", filepath.Clean(path), err))
	}
}

func fileType(info os.FileInfo) string {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return "symlink"
	case info.IsDir():
		return "directory"
	case info.Mode().IsRegular():
		return "file"
	default:
		return "other"
	}
}

func samePath(left, right string) bool {
	if strings.EqualFold(filepath.Clean(left), filepath.Clean(right)) {
		return true
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"
