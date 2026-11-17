package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockTaskService(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var location windows.Overlapped
	err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &location)
	if err != nil {
		file.Close()
		return nil, err
	}
	return func() { _ = file.Close() }, nil
}
