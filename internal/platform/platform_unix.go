//go:build !windows

package platform

import (
	"os/exec"
	"syscall"
	"time"
)

// IsWindows reports whether the current platform is Windows.
func IsWindows() bool { return false }

// IsUnix reports whether the current platform is Unix (Linux, macOS, etc.).
func IsUnix() bool { return true }

// buildScriptCmd returns the command+args to execute the given script.
// On Unix, .sh files are run with bash. .bat files are rejected.
func buildScriptCmd(scriptPath, ext string) ([]string, error) {
	switch ext {
	case ".sh":
		return []string{"bash", scriptPath}, nil
	case ".bat":
		return nil, platformError("Cannot execute .bat file on Unix: %s. Use .sh files on Unix.", scriptPath)
	default:
		return nil, unsupportedExt(ext)
	}
}

// setupCmd configures platform-specific process attributes.
//
// On Unix:
//   - Setpgid=true puts the child in its own process group so that a group
//     kill propagates to all descendants (prevents orphaned sleep/child procs
//     from holding the stdout/stderr pipes open after timeout).
//   - Cancel kills the process group rather than just the leader.
//   - WaitDelay acts as a safety net in case Cancel is called before the
//     process has started.
func setupCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			// Kill the entire process group (negative PID = group ID).
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 3 * time.Second
}
