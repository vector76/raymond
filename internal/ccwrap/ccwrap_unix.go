//go:build !windows

package ccwrap

import (
	"os/exec"
	"syscall"
)

// setupClaudeCmd puts the child in a new session (setsid) so that claude's
// Node.js/Ink TUI cannot open /dev/tty and emit terminal control sequences
// (e.g. alternate screen buffer) that would silence raymond's console output.
//
// Setsid also makes the child a new process group leader (PID == PGID), which
// allows killClaudeProcess to kill the entire process group (claude + any
// subprocesses it spawns) via a negative-PID signal.
func setupClaudeCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// killClaudeProcess kills the entire process group of cmd (negative PID signal)
// so that any child processes spawned by claude are also terminated. This
// prevents orphaned processes from holding the stdout/stderr pipes open, which
// would cause Wait() to block indefinitely.
func killClaudeProcess(cmd *exec.Cmd) error {
	if cmd.Process != nil {
		// Negative PID targets the process group (PGID == PID for setsid child).
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return nil
}
