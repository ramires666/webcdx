//go:build windows

package main

import (
	"fmt"
	"os/exec"
)

func configureProcess(cmd *exec.Cmd) {}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill.exe", "/PID", fmt.Sprint(cmd.Process.Pid), "/T", "/F")
	if output, err := kill.CombinedOutput(); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("taskkill: %v: %s", err, output)
	}
	return nil
}
