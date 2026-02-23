//go:build windows

package platform

import (
	"os/exec"
	"syscall"
	"time"
)

// IsWindows reports whether the current platform is Windows.
func IsWindows() bool { return true }

// IsUnix reports whether the current platform is Unix.
func IsUnix() bool { return false }

// createNewProcessGroup is the Windows process-creation flag that places the
// new process in its own console process group. This allows Ctrl+Break signals
// to be sent to the group, and is the closest Windows equivalent to Unix
// process groups for subprocess management.
// Value: 0x00000200 (CREATE_NEW_PROCESS_GROUP)
const createNewProcessGroup uint32 = 0x00000200

// buildScriptCmd returns the command+args to execute the given script.
// On Windows, .bat files are run with cmd.exe /c. .sh files are rejected.
// TODO: add .ps1 → powershell.exe -File support here when ready.
func buildScriptCmd(scriptPath, ext string) ([]string, error) {
	switch ext {
	case ".bat":
		return []string{"cmd.exe", "/c", scriptPath}, nil
	case ".sh":
		return nil, platformError("Cannot execute .sh file on Windows: %s. Use .bat files on Windows.", scriptPath)
	default:
		return nil, unsupportedExt(ext)
	}
}

// setupCmd configures platform-specific process attributes for Windows.
// Creates a new process group so child processes can be managed as a unit.
func setupCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup,
	}
	cmd.WaitDelay = 3 * time.Second
	// Default Cancel (cmd.Process.Kill) is used for Windows — it calls
	// TerminateProcess which is immediate and does not require a signal.
}
