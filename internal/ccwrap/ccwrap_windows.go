//go:build windows

package ccwrap

import (
	"os/exec"
	"syscall"
)

// setupClaudeCmd places the child in a new console process group on Windows
// and hides its console window.
//
// CREATE_NEW_PROCESS_GROUP (0x00000200) prevents the child from inheriting
// the parent's Ctrl-C handling.
//
// CREATE_NO_WINDOW (0x08000000) gives the child its own hidden console rather
// than inheriting raymond's console. This means the child Node.js process
// (claude CLI) calls SetConsoleTitleW() on its own hidden console instead of
// raymond's visible console window, so raymond's terminal title updates are
// not overwritten.
//
// Note: DETACHED_PROCESS was tried but causes Node.js to call AllocConsole()
// internally when it finds no console, which briefly pops up a visible
// terminal window for each claude invocation. CREATE_NO_WINDOW avoids that
// by providing a console that is hidden from the start.
func setupClaudeCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000200 | 0x08000000, // CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW
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
