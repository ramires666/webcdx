package main

import (
	"errors"
	"golang.org/x/sys/windows/registry"
)

func registerBrowserHost(path string, remove bool, data []byte) error {
	for _, browser := range []string{`Software\Google\Chrome`, `Software\Microsoft\Edge`} {
		name := browser + `\NativeMessagingHosts\` + browserNativeHostName
		key, err := registry.OpenKey(registry.CURRENT_USER, name, registry.QUERY_VALUE|registry.SET_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			if remove {
				continue
			}
			key, _, err = registry.CreateKey(registry.CURRENT_USER, name, registry.QUERY_VALUE|registry.SET_VALUE)
		}
		if err != nil {
			return err
		}
		value, _, err := key.GetStringValue("")
		if err == nil && value != path {
			key.Close()
			return errors.New("native host already registered at a different path")
		}
		if remove {
			key.Close()
			if err := registry.DeleteKey(registry.CURRENT_USER, name); err != nil {
				return err
			}
		} else {
			err := key.SetStringValue("", path)
			key.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}
