//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

func registerBrowserHost(path string, remove bool, data []byte) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	for _, browser := range []string{"google-chrome", "chromium", "microsoft-edge"} {
		dir := filepath.Join(home, ".config", browser, "NativeMessagingHosts")
		target := filepath.Join(dir, browserNativeHostName+".json")
		if remove {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := atomicWrite(target, data, false); err != nil {
			return err
		}
	}
	return nil
}
