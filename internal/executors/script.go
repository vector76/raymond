package executors

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/platform"
	wfstate "github.com/vector76/raymond/internal/state"
	"github.com/vector76/raymond/internal/zipscope"
)

// runScriptFn is the platform.RunScript implementation used by ScriptExecutor.
// Overridable in tests.
var runScriptFn = platform.RunScript

// ScriptExecutor handles .sh / .bat states by running them as subprocesses.
//
//   - Emits StateStarted and StateCompleted (and ScriptOutput when debug is on).
//   - Parses exactly one transition from stdout; any other count is fatal.
//   - Preserves the agent's existing session_id (scripts do not create sessions).
//   - Cost is always $0.00.
type ScriptExecutor struct{}

// Execute runs the script state and returns the parsed transition.
func (e *ScriptExecutor) Execute(
	ctx context.Context,
	agent *wfstate.AgentState,
	wfState *wfstate.WorkflowState,
	execCtx *ExecutionContext,
) (ExecutionResult, error) {
	agentID := agent.ID
	currentState := agent.CurrentState
	scopeDir := wfState.ScopeDir
	workflowID := wfState.WorkflowID
	sessionID := agent.SessionID // preserved; scripts do not modify it

	// Build full path to the script.
	scriptPath := filepath.Join(scopeDir, currentState)

	// For zip scopes: extract the script to a temp file.
	var tmpPath string
	if zipscope.IsZipScope(scopeDir) {
		var err error
		tmpPath, err = zipscope.ExtractScript(scopeDir, currentState)
		if err != nil {
			return ExecutionResult{}, &ScriptError{
				Msg: fmt.Sprintf("failed to extract script from zip: %v", err),
			}
		}
		defer os.Remove(tmpPath)

		// Make the temp file executable on Unix.
		if runtime.GOOS != "windows" {
			if err := os.Chmod(tmpPath, 0o755); err != nil {
				return ExecutionResult{}, &ScriptError{
					Msg: fmt.Sprintf("failed to chmod extracted script: %v", err),
				}
			}
		}
		scriptPath = tmpPath
	}

	// Build environment variables.
	pendingResult := agent.PendingResult
	forkAttributes := agent.ForkAttributes
	env := platform.BuildScriptEnv(workflowID, agentID, pendingResult, forkAttributes)

	// Emit StateStarted.
	execCtx.Bus.Emit(events.StateStarted{
		AgentID:   agentID,
		StateName: currentState,
		StateType: "script",
		Timestamp: time.Now(),
	})

	// Execute script and measure time.
	start := time.Now()
	sr, err := runScriptFn(ctx, scriptPath, execCtx.Timeout, env, agent.Cwd)
	executionTimeMS := float64(time.Since(start).Milliseconds())

	if err != nil {
		var msg string
		if tErr, ok := err.(*platform.ScriptTimeoutError); ok {
			msg = fmt.Sprintf("Script timeout: %v", tErr)
		} else {
			msg = fmt.Sprintf("Script execution error: %v", err)
		}
		return ExecutionResult{}, &ScriptError{Msg: msg}
	}

	// Save debug output and emit ScriptOutput when debug is enabled.
	if execCtx.DebugDir != "" {
		stepNumber := execCtx.GetNextStepNumber(agentID)
		stateName := ExtractStateName(currentState)
		e.saveDebugFiles(execCtx.DebugDir, agentID, stateName, stepNumber,
			sr.Stdout, sr.Stderr, sr.ExitCode, executionTimeMS, env)

		execCtx.Bus.Emit(events.ScriptOutput{
			AgentID:         agentID,
			StateName:       currentState,
			StepNumber:      stepNumber,
			Stdout:          sr.Stdout,
			Stderr:          sr.Stderr,
			ExitCode:        sr.ExitCode,
			ExecutionTimeMS: executionTimeMS,
			EnvVars:         env,
			Timestamp:       time.Now(),
		})
	}

	// Non-zero exit code is always fatal for scripts.
	if sr.ExitCode != 0 {
		stderr := sr.Stderr
		if len(stderr) > 500 {
			stderr = stderr[:500]
		}
		return ExecutionResult{}, &ScriptError{
			Msg: fmt.Sprintf(
				"Script '%s' failed with exit code %d. stderr: %s",
				currentState, sr.ExitCode, stderr,
			),
		}
	}

	// Parse transitions from stdout.
	transitions, parseErr := parsing.ParseTransitions(sr.Stdout)
	if parseErr != nil {
		return ExecutionResult{}, &ScriptError{
			Msg: fmt.Sprintf("Script '%s' transition parse error: %v", currentState, parseErr),
		}
	}

	if len(transitions) == 0 {
		return ExecutionResult{}, &ScriptError{
			Msg: fmt.Sprintf("Script '%s' produced no transition tag in stdout", currentState),
		}
	}
	if len(transitions) > 1 {
		return ExecutionResult{}, &ScriptError{
			Msg: fmt.Sprintf("Script '%s' produced %d transition tags (expected 1)", currentState, len(transitions)),
		}
	}

	transition := transitions[0]

	// Resolve abstract state names to concrete filenames.
	transition, err = ResolveTransitionTargets(transition, scopeDir)
	if err != nil {
		return ExecutionResult{}, &ScriptError{
			Msg: fmt.Sprintf("Transition target not found: %v", err),
		}
	}

	// Emit StateCompleted.
	execCtx.Bus.Emit(events.StateCompleted{
		AgentID:      agentID,
		StateName:    currentState,
		CostUSD:      0.0,
		TotalCostUSD: wfState.TotalCostUSD,
		SessionID:    sessionIDStr(sessionID),
		DurationMS:   executionTimeMS,
		Timestamp:    time.Now(),
	})

	return ExecutionResult{
		Transition: transition,
		SessionID:  sessionID,
		CostUSD:    0.0,
	}, nil
}

// saveDebugFiles writes stdout, stderr, and a JSON metadata file to debugDir.
func (e *ScriptExecutor) saveDebugFiles(
	debugDir, agentID, stateName string,
	stepNumber int,
	stdout, stderr string,
	exitCode int,
	executionTimeMS float64,
	envVars map[string]string,
) {
	base := fmt.Sprintf("%s_%s_%03d", agentID, stateName, stepNumber)

	writeDebugFile(filepath.Join(debugDir, base+".stdout.txt"), stdout)
	writeDebugFile(filepath.Join(debugDir, base+".stderr.txt"), stderr)

	meta := map[string]any{
		"exit_code":         exitCode,
		"execution_time_ms": executionTimeMS,
		"env_vars":          envVars,
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err == nil {
		writeDebugFile(filepath.Join(debugDir, base+".meta.json"), string(data))
	}
}

// writeDebugFile writes content to path, silently ignoring errors.
func writeDebugFile(path, content string) {
	_ = os.WriteFile(path, []byte(content), 0o644)
}

// sessionIDStr returns the string value of a *string or "" if nil.
func sessionIDStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
