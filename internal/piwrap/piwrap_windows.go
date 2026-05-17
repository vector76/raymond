//go:build windows

package piwrap

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func setupPiCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}

func killPiProcess(cmd *exec.Cmd) error {
	if cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}
