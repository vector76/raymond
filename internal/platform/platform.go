// Package platform provides cross-platform script execution for workflow states.
//
// Shell selection by platform and file extension:
//
//	Unix  (!windows): .sh → bash; .bat → error
//	Windows:          .bat → cmd.exe /c; .sh → error
//	Both:             unsupported extension → error
//
// .ps1 / PowerShell support is designed in (add a case in buildScriptCmd in
// platform_windows.go) but deferred to a future release.
//
// RunScript merges the provided env map over the current process environment;
// the child process inherits all parent env vars with supplied keys overriding
// any that already exist.
//
// stdin is set to /dev/null (os.DevNull) so the child cannot put the terminal
// in raw mode or interfere with Ctrl-C handling.
package platform

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ScriptResult holds the captured output of a script execution.
type ScriptResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ScriptTimeoutError is returned when a script exceeds its timeout.
type ScriptTimeoutError struct {
	ScriptPath string
	Timeout    float64
}

func (e *ScriptTimeoutError) Error() string {
	return fmt.Sprintf("Script timeout: %q exceeded %.6g seconds", e.ScriptPath, e.Timeout)
}

// BuildScriptEnv constructs the environment variable map to pass to RunScript.
//
// Always sets:
//   - RAYMOND_WORKFLOW_ID
//   - RAYMOND_AGENT_ID
//
// Sets RAYMOND_RESULT only when result is non-nil (including empty string).
// Fork attributes are added directly as environment variables (key → value).
func BuildScriptEnv(workflowID, agentID string, result *string, forkAttributes map[string]string) map[string]string {
	env := map[string]string{
		"RAYMOND_WORKFLOW_ID": workflowID,
		"RAYMOND_AGENT_ID":    agentID,
	}
	if result != nil {
		env["RAYMOND_RESULT"] = *result
	}
	for k, v := range forkAttributes {
		env[k] = v
	}
	return env
}

// RunScript executes the script at scriptPath and returns its output.
//
//   - ctx is used as the parent for any internal timeout context; cancelling
//     ctx will also cancel a running script.
//   - timeout ≤ 0 means no timeout (only ctx cancellation applies).
//   - env is merged over the current process environment (supplied keys win).
//   - cwd == "" means the child inherits the caller's working directory.
//
// Returns *ScriptTimeoutError on timeout, os.ErrNotExist-wrapping error when
// the script file doesn't exist, and a plain error for other failures.
// Non-zero exit codes are NOT errors; they are captured in ScriptResult.ExitCode.
func RunScript(ctx context.Context, scriptPath string, timeout float64, env map[string]string, cwd string) (*ScriptResult, error) {
	// Check the script exists before trying to exec it.
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("Script not found: %s: %w", scriptPath, os.ErrNotExist)
	}

	ext := strings.ToLower(filepath.Ext(scriptPath))
	cmd, err := buildScriptCmd(scriptPath, ext)
	if err != nil {
		return nil, err
	}

	// Merge env over the parent's environment (supplied keys take precedence).
	merged := mergeEnv(env)

	// Create context with optional timeout, rooted in the caller's ctx so that
	// cancelling ctx also cancels a running script.
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(
			ctx,
			time.Duration(float64(time.Second)*timeout),
		)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	execCmd := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	execCmd.Env = merged
	execCmd.Stdin = nil // /dev/null per exec.Cmd docs
	if cwd != "" {
		execCmd.Dir = cwd
	}

	// Platform-specific: process group setup + Cancel + WaitDelay.
	setupCmd(execCmd)

	var stdoutBuf, stderrBuf bytes.Buffer
	execCmd.Stdout = &stdoutBuf
	execCmd.Stderr = &stderrBuf

	runErr := execCmd.Run()

	// Timeout check must come before the ExitError check because a killed
	// process also produces an ExitError.
	if runErr != nil && ctx.Err() == context.DeadlineExceeded {
		return nil, &ScriptTimeoutError{ScriptPath: scriptPath, Timeout: timeout}
	}
	if runErr != nil && ctx.Err() == context.Canceled {
		return nil, runErr
	}

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("script execution failed: %w", runErr)
		}
	}

	return &ScriptResult{
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
	}, nil
}

// mergeEnv builds a []string environment from os.Environ() overridden by extra.
func mergeEnv(extra map[string]string) []string {
	base := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			base[kv[:idx]] = kv[idx+1:]
		}
	}
	for k, v := range extra {
		base[k] = v
	}
	result := make([]string, 0, len(base))
	for k, v := range base {
		result = append(result, k+"="+v)
	}
	return result
}

// platformError builds an error for cross-platform extension mismatches.
func platformError(format, scriptPath string) error {
	return fmt.Errorf(format, scriptPath)
}

// unsupportedExt builds an error for unsupported script extensions.
func unsupportedExt(ext string) error {
	return fmt.Errorf("Unsupported script extension: %s", ext)
}
