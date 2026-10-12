package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func browserHostManifest(executable string) []byte {
	data, _ := json.MarshalIndent(map[string]any{"name": browserNativeHostName, "description": "WebCodex local browser bridge", "path": executable, "type": "stdio", "allowed_origins": []string{"chrome-extension://" + browserExtensionID + "/"}}, "", "  ")
	return data
}

func installBrowserNativeHost(remove bool) error {
	path := filepath.Join(filepath.Dir(browserBridgePath()), "native-host.json")
	if remove {
		if err := registerBrowserHost(path, true, nil); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := privateBrowserDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	data := browserHostManifest(executable)
	if err := atomicWrite(path, data, false); err != nil {
		return err
	}
	return registerBrowserHost(path, false, data)
}
