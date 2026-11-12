//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
)

func registerStudioHost(path string, remove bool, data []byte) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	paths := []string{filepath.Join(home, ".config", "google-chrome", "NativeMessagingHosts"), filepath.Join(home, ".config", "chromium", "NativeMessagingHosts"), filepath.Join(home, ".config", "microsoft-edge", "NativeMessagingHosts")}
	if runtime.GOOS == "darwin" {
		paths = []string{filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "NativeMessagingHosts"), filepath.Join(home, "Library", "Application Support", "Chromium", "NativeMessagingHosts"), filepath.Join(home, "Library", "Application Support", "Microsoft Edge", "NativeMessagingHosts")}
	}
	for _, dir := range paths {
		target := filepath.Join(dir, studioNativeHostName+".json")
		if remove {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := atomicWrite(target, data, false); err != nil {
			return err
		}
	}
	return nil
}
