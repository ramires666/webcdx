package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

var studioIDPattern = regexp.MustCompile(`^[a-p]{32}$`)

func studioConfigDir() string {
	d, _ := os.UserConfigDir()
	return filepath.Join(d, "webcodex", "studio")
}
func studioConfigPath() string   { return filepath.Join(studioConfigDir(), "native-host.json") }
func studioSettingsPath() string { return filepath.Join(studioConfigDir(), "settings.json") }

type studioSettings struct {
	ExtensionID  string   `json:"extension_id"`
	AllowedRoots []string `json:"allowed_roots"`
}

func studioAllowedExtensionID() string {
	b, e := os.ReadFile(studioSettingsPath())
	if e == nil {
		var v studioSettings
		if json.Unmarshal(b, &v) == nil && studioIDPattern.MatchString(v.ExtensionID) {
			return v.ExtensionID
		}
	}
	return studioExtensionID
}
func studioAllowedRoots() []string {
	if roots := env("WEBCODEX_ALLOWED_ROOTS", ""); roots != "" {
		return splitPathList(roots)
	}
	b, e := os.ReadFile(studioSettingsPath())
	if e == nil {
		var v studioSettings
		if json.Unmarshal(b, &v) == nil && len(v.AllowedRoots) > 0 {
			return v.AllowedRoots
		}
	}
	return nil
}
func installStudioNativeHost(remove bool, extensionID, roots string) error {
	path := studioConfigPath()
	if remove {
		if err := registerStudioHost(path, true, nil); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(studioSettingsPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if extensionID == "" {
		extensionID = studioExtensionID
	}
	if !studioIDPattern.MatchString(extensionID) {
		return errors.New("extension ID must be 32 lowercase letters from a through p")
	}
	if roots == "" {
		roots = env("WEBCODEX_ALLOWED_ROOTS", "")
	}
	if roots == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return e
		}
		roots = home
	}
	allowedRoots := splitPathList(roots)
	if len(allowedRoots) == 0 {
		return errors.New("no allowed roots supplied")
	}
	for _, root := range allowedRoots {
		if !filepath.IsAbs(root) {
			return errors.New("allowed roots must be absolute")
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := privateBrowserDirectory(filepath.Dir(filepath.Dir(path))); err != nil {
		return err
	}
	if err := privateBrowserDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	settings, err := json.Marshal(studioSettings{ExtensionID: extensionID, AllowedRoots: allowedRoots})
	if err != nil {
		return err
	}
	if err = atomicWrite(studioSettingsPath(), settings, false); err != nil {
		return err
	}
	data, err := json.MarshalIndent(map[string]any{"name": studioNativeHostName, "description": "WebCodex local workspace tools for AI Studio", "path": executable, "type": "stdio", "allowed_origins": []string{"chrome-extension://" + extensionID + "/"}}, "", "  ")
	if err != nil {
		return err
	}
	if err = privateBrowserDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if err = atomicWrite(path, data, false); err != nil {
		return err
	}
	return registerStudioHost(path, false, data)
}
