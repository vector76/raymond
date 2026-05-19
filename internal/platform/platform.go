// Package platform provides cross-platform script execution for workflow states.
//
// Shell selection by platform and file extension:
//
//	Unix  (!windows): .sh → bash; .bat/.ps1 → error
//	Windows:          .bat → cmd.exe /c; .ps1 → powershell.exe -ExecutionPolicy Bypass -File; .sh → error
//	Both:             unsupported extension → error
//
// RunScript merges the provided env map over the current process environment;
// the child process inherits all parent env vars with supplied keys overriding
// any that already exist.
//
// stdin is set to /dev/null (os.DevNull) so the child cannot put the terminal
// in raw mode or interfere with Ctrl-C handling.
package platform

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	return fmt.Sprintf("Script %q produced no output for %.6g seconds", e.ScriptPath, e.Timeout)
}

// BuildScriptEnv constructs the environment variable map to pass to RunScript.
//
// Always sets:
//   - RAYMOND_WORKFLOW_ID
//   - RAYMOND_AGENT_ID
//   - RAYMOND_TASK_FOLDER
//
// Sets RAYMOND_INPUT only when input is non-nil (including empty string).
// Fork attributes are added directly as environment variables (key → value).
func BuildScriptEnv(workflowID, agentID, taskFolder string, input *string, forkAttributes map[string]string) map[string]string {
	env := map[string]string{
		"RAYMOND_WORKFLOW_ID": workflowID,
		"RAYMOND_AGENT_ID":    agentID,
		"RAYMOND_TASK_FOLDER": taskFolder,
	}
	if input != nil {
		env["RAYMOND_INPUT"] = *input
	}
	for k, v := range forkAttributes {
		env[k] = v
	}
	return env
}

// RunScript executes the script at scriptPath, streaming output to onChunk,
// and returns the accumulated output.
//
//   - ctx is used as the parent context; cancelling it terminates the script.
//   - timeout ≤ 0 means no inactivity timeout (only ctx cancellation applies).
//   - env is merged over the current process environment (supplied keys win).
//   - cwd == "" means the child inherits the caller's working directory.
//   - onChunk is called for each chunk of output (pipe="stdout"/"stderr"); nil is a no-op.
//     onChunk may be called concurrently from the stdout and stderr reader goroutines.
//
// Returns *ScriptTimeoutError when the inactivity timer fires, os.ErrNotExist-wrapping
// error when the script file doesn't exist, and a plain error for other failures.
// Non-zero exit codes are NOT errors; they are captured in ScriptResult.ExitCode.
func RunScript(ctx context.Context, scriptPath string, timeout float64, env map[string]string, cwd string, onChunk func(pipe string, data []byte)) (*ScriptResult, error) {
	// Check the script exists before trying to exec it.
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("Script not found: %s: %w", scriptPath, os.ErrNotExist)
	}

	// Convert to absolute path so that cmd.Dir (cwd) does not cause the
	// interpreter to misinterpret a relative script path.
	abs, absErr := filepath.Abs(scriptPath)
	if absErr != nil {
		return nil, fmt.Errorf("failed to resolve absolute path for script %q: %w", scriptPath, absErr)
	}
	scriptPath = abs

	ext := strings.ToLower(filepath.Ext(scriptPath))
	cmd, err := buildScriptCmd(scriptPath, ext)
	if err != nil {
		return nil, err
	}

	// Merge env over the parent's environment (supplied keys take precedence).
	merged := mergeEnv(env)

	// innerCtx is cancelled by the inactivity timer; rooted in caller's ctx so
	// external cancellation also propagates to the child process.
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	execCmd := exec.CommandContext(innerCtx, cmd[0], cmd[1:]...)
	execCmd.Env = merged
	execCmd.Stdin = nil // /dev/null per exec.Cmd docs
	if cwd != "" {
		execCmd.Dir = cwd
	}

	// Platform-specific: process group setup + Cancel + WaitDelay.
	setupCmd(execCmd)

	stdoutPipe, err := execCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderrPipe, err := execCmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := execCmd.Start(); err != nil {
		return nil, fmt.Errorf("script execution failed: %w", err)
	}

	// timerFiredCh is closed if the inactivity timer fires.
	timerFiredCh := make(chan struct{})

	// signalActivity resets the inactivity timer; no-op when timeout <= 0.
	signalActivity := func() {}

	if timeout > 0 {
		activityCh := make(chan struct{}, 1)
		signalActivity = func() {
			select {
			case activityCh <- struct{}{}:
			default:
			}
		}
		duration := time.Duration(float64(time.Second) * timeout)
		go func() {
			timer := time.NewTimer(duration)
			for {
				select {
				case <-timer.C:
					close(timerFiredCh)
					innerCancel()
					return
				case <-activityCh:
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(duration)
				case <-innerCtx.Done():
					timer.Stop()
					return
				}
			}
		}()
	}

	var stdoutBuf, stderrBuf []byte
	var wg sync.WaitGroup
	wg.Add(2)

	readPipe := func(pipe io.ReadCloser, pipeName string, buf *[]byte) {
		defer wg.Done()
		scratch := make([]byte, 32*1024)
		for {
			n, readErr := pipe.Read(scratch)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, scratch[:n])
				*buf = append(*buf, chunk...)
				signalActivity()
				if onChunk != nil {
					onChunk(pipeName, chunk)
				}
			}
			if readErr != nil {
				break
			}
		}
	}

	go readPipe(stdoutPipe, "stdout", &stdoutBuf)
	go readPipe(stderrPipe, "stderr", &stderrBuf)

	wg.Wait()
	runErr := execCmd.Wait()
	innerCancel() // stop timer goroutine if still running

	if runErr != nil {
		// Inactivity timeout: our timer fired and the parent ctx is not cancelled.
		select {
		case <-timerFiredCh:
			if ctx.Err() == nil {
				return nil, &ScriptTimeoutError{ScriptPath: scriptPath, Timeout: timeout}
			}
		default:
		}

		// External context cancellation.
		if ctx.Err() != nil {
			return nil, runErr
		}

		if exitErr, ok := runErr.(*exec.ExitError); ok {
			return &ScriptResult{
				Stdout:   string(stdoutBuf),
				Stderr:   string(stderrBuf),
				ExitCode: exitErr.ExitCode(),
			}, nil
		}
		return nil, fmt.Errorf("script execution failed: %w", runErr)
	}

	return &ScriptResult{
		Stdout:   string(stdoutBuf),
		Stderr:   string(stderrBuf),
		ExitCode: 0,
	}, nil
}

// mergeEnv builds a []string environment from os.Environ() overridden by extra.
// CLAUDECODE is stripped so that script subprocesses cannot accidentally treat
// themselves as nested Claude sessions (mirrors the behaviour in ccwrap.BuildClaudeEnv).
func mergeEnv(extra map[string]string) []string {
	base := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			k := kv[:idx]
			if k == "CLAUDECODE" {
				continue
			}
			base[k] = kv[idx+1:]
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
