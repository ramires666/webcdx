package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func configuredTaskProviderKey() (string, error) {
	raw := strings.TrimSpace(os.Getenv("WEBCODEX_TASK_KEY_FILE"))
	dotenv := strings.TrimSpace(os.Getenv("WEBCODEX_TASK_OPENMCP_ENV"))
	if raw != "" && dotenv != "" {
		return "", errors.New("choose either WEBCODEX_TASK_KEY_FILE or WEBCODEX_TASK_OPENMCP_ENV")
	}
	if dotenv != "" {
		return readOpenMCPTaskKey(dotenv)
	}
	if !filepath.IsAbs(raw) {
		return "", errors.New("set WEBCODEX_TASK_KEY_FILE or WEBCODEX_TASK_OPENMCP_ENV to an absolute path")
	}
	data, err := readTaskFile(raw, 4096)
	if err != nil || !validTaskProviderKey(strings.TrimSpace(string(data))) {
		return "", errors.New("WEBCODEX_TASK_KEY_FILE must contain one nonempty key line")
	}
	return strings.TrimSpace(string(data)), nil
}

func validTaskProviderKey(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.ContainsAny(value, "\r\n\x00")
}

func readOpenMCPTaskKey(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("WEBCODEX_TASK_OPENMCP_ENV must be an absolute path")
	}
	data, err := readTaskFile(path, 64<<10)
	if err != nil {
		return "", errors.New("WEBCODEX_TASK_OPENMCP_ENV must be a readable local file of at most 64 KiB")
	}
	key := ""
	for _, line := range strings.Split(strings.TrimPrefix(string(data), "\uFEFF"), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "export ")
		name, raw, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != "OPENCODE_API_KEY" {
			continue
		}
		if key != "" {
			return "", errors.New("WEBCODEX_TASK_OPENMCP_ENV contains multiple OPENCODE_API_KEY entries")
		}
		value := strings.TrimSpace(raw)
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			closing := strings.IndexByte(value[1:], value[0]) + 1
			if closing <= 0 {
				return "", errors.New("invalid OPENCODE_API_KEY format in WEBCODEX_TASK_OPENMCP_ENV")
			}
			rest := strings.TrimSpace(value[closing+1:])
			if rest != "" && !strings.HasPrefix(rest, "#") {
				return "", errors.New("invalid OPENCODE_API_KEY format in WEBCODEX_TASK_OPENMCP_ENV")
			}
			value = value[1:closing]
		} else if raw, _, ok := strings.Cut(value, " #"); ok {
			value = strings.TrimSpace(raw)
		}
		if !validTaskProviderKey(value) {
			return "", errors.New("invalid OPENCODE_API_KEY format in WEBCODEX_TASK_OPENMCP_ENV")
		}
		key = value
	}
	if key == "" {
		return "", errors.New("WEBCODEX_TASK_OPENMCP_ENV does not contain OPENCODE_API_KEY")
	}
	return key, nil
}
