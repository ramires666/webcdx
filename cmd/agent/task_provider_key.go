package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type taskProviderAuth struct {
	key      string
	database string
}

func configuredTaskProviderAuth() (taskProviderAuth, error) {
	raw := strings.TrimSpace(os.Getenv("WEBCODEX_TASK_KEY_FILE"))
	dotenv := strings.TrimSpace(os.Getenv("WEBCODEX_TASK_OPENMCP_ENV"))
	auth := strings.TrimSpace(os.Getenv("WEBCODEX_TASK_OPENCODE_AUTH"))
	db := strings.TrimSpace(os.Getenv("WEBCODEX_TASK_OPENCODE_DB"))
	sources := 0
	for _, path := range []string{raw, dotenv, auth, db} {
		if path != "" {
			sources++
		}
	}
	if sources != 1 {
		return taskProviderAuth{}, errors.New("set one OpenCode task credential source: WEBCODEX_TASK_OPENCODE_DB, WEBCODEX_TASK_KEY_FILE, WEBCODEX_TASK_OPENMCP_ENV, or WEBCODEX_TASK_OPENCODE_AUTH")
	}
	switch {
	case db != "":
		info, err := os.Stat(db)
		if err != nil || !filepath.IsAbs(db) || !info.Mode().IsRegular() {
			return taskProviderAuth{}, errors.New("WEBCODEX_TASK_OPENCODE_DB must point to an existing OpenCode database by absolute path")
		}
		return taskProviderAuth{database: db}, nil
	case auth != "":
		key, err := readOpenCodeTaskKey(auth)
		return taskProviderAuth{key: key}, err
	case dotenv != "":
		key, err := readOpenMCPTaskKey(dotenv)
		return taskProviderAuth{key: key}, err
	default:
		key, err := readPlainTaskKey(raw)
		return taskProviderAuth{key: key}, err
	}
}

func readPlainTaskKey(raw string) (string, error) {
	if !filepath.IsAbs(raw) {
		return "", errors.New("WEBCODEX_TASK_KEY_FILE must be an absolute path")
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

func readOpenCodeTaskKey(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("WEBCODEX_TASK_OPENCODE_AUTH must be an absolute path")
	}
	data, err := readTaskFile(path, 64<<10)
	var auth struct {
		Go struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		} `json:"opencode-go"`
	}
	if err != nil || json.Unmarshal(data, &auth) != nil || auth.Go.Type != "api" || !validTaskProviderKey(auth.Go.Key) {
		return "", errors.New("WEBCODEX_TASK_OPENCODE_AUTH must contain an OpenCode Go API key")
	}
	return auth.Go.Key, nil
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
