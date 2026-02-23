//go:build windows

package ccwrap

import (
	"os/exec"
	"syscall"
)

// setupClaudeCmd places the child in a new console process group on Windows.
// CREATE_NEW_PROCESS_GROUP (0x00000200) prevents the child from inheriting
// the parent's Ctrl-C handling.
func setupClaudeCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000200, // CREATE_NEW_PROCESS_GROUP
	}
}

// killClaudeProcess kills the claude process on Windows. Windows uses
// TerminateProcess (unconditional), so a direct kill is sufficient.
func killClaudeProcess(cmd *exec.Cmd) error {
	if cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}
