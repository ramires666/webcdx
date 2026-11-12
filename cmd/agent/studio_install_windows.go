package main

import (
	"errors"
	"golang.org/x/sys/windows/registry"
)

func registerStudioHost(path string, remove bool, data []byte) error {
	name := `Software\Google\Chrome\NativeMessagingHosts\` + studioNativeHostName
	key, err := registry.OpenKey(registry.CURRENT_USER, name, registry.QUERY_VALUE|registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		if remove {
			return nil
		}
		key, _, err = registry.CreateKey(registry.CURRENT_USER, name, registry.QUERY_VALUE|registry.SET_VALUE)
	}
	if err != nil {
		return err
	}
	value, _, err := key.GetStringValue("")
	if err == nil && remove && value != path {
		key.Close()
		return errors.New("Studio native host registered at a different path")
	}
	if remove {
		key.Close()
		return registry.DeleteKey(registry.CURRENT_USER, name)
	}
	err = key.SetStringValue("", path)
	key.Close()
	return err
}
