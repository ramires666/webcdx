//go:build !windows

package main

import "os"

func privateBrowserDirectory(path string) error { return os.Chmod(path, 0o700) }
