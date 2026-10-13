package main

import (
	"path/filepath"
	"testing"
)

func TestBrowserPipeDirectoriesAreIsolatedAndExclusive(t *testing.T) {
	root := t.TempDir()
	firstDir := filepath.Join(root, "first")
	first, firstEndpoint, err := browserListenIPC(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	duplicate, _, err := browserListenIPC(firstDir)
	if err == nil {
		duplicate.Close()
		t.Fatal("two bridges could bind the same user/config directory")
	}
	second, secondEndpoint, err := browserListenIPC(filepath.Join(root, "second"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if firstEndpoint == secondEndpoint {
		t.Fatal("separate config directories used the same pipe")
	}
}
