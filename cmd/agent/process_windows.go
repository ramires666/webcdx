//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strconv"

	"golang.org/x/sys/windows"
)

func configureProcess(cmd *exec.Cmd) {}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return terminatePIDTree(cmd.Process.Pid)
}

func terminatePIDTree(pid int) error {
	kill := exec.Command("taskkill.exe", "/PID", fmt.Sprint(pid), "/T", "/F")
	if output, err := kill.CombinedOutput(); err != nil {
		return fmt.Errorf("taskkill: %v: %s", err, output)
	}
	return nil
}

func processIdentity(pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return "", err
	}
	return strconv.FormatUint(uint64(created.HighDateTime)<<32|uint64(created.LowDateTime), 10), nil
}

func processAliveWithIdentity(pid int, identity string) bool {
	current, err := processIdentity(pid)
	if err != nil || current != identity {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	return windows.GetExitCodeProcess(handle, &exitCode) == nil && exitCode == 259
}

func atomicReplace(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
