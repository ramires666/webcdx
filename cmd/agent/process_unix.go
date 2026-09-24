//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return terminatePIDTree(cmd.Process.Pid)
}

func terminatePIDTree(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

func processIdentity(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil {
		closing := strings.LastIndexByte(string(data), ')')
		if closing < 0 {
			return "", fmt.Errorf("invalid process stat")
		}
		fields := strings.Fields(string(data[closing+1:]))
		if len(fields) <= 19 {
			return "", fmt.Errorf("incomplete process stat")
		}
		return fields[19], nil
	}
	output, psErr := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if psErr != nil || strings.TrimSpace(string(output)) == "" {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func processAliveWithIdentity(pid int, identity string) bool {
	current, err := processIdentity(pid)
	return err == nil && current == identity
}

func atomicReplace(source, destination string) error {
	return os.Rename(source, destination)
}
