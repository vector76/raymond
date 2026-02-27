//go:build windows

package ccwrap

import (
	"os/exec"
	"syscall"
)

// setupClaudeCmd places the child in a new console process group on Windows
// and detaches it from the parent's console.
//
// CREATE_NEW_PROCESS_GROUP (0x00000200) prevents the child from inheriting
// the parent's Ctrl-C handling.
//
// DETACHED_PROCESS (0x00000008) removes the child's console handle entirely,
// mirroring the effect of setsid on Unix. Without this, the child Node.js
// process (claude CLI) retains access to the Windows Console API and calls
// SetConsoleTitleW(), which overwrites raymond's terminal title updates.
// Since raymond already captures claude's stdout/stderr via pipes, the child
// does not need a real console.
func setupClaudeCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000200 | 0x00000008, // CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS
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
