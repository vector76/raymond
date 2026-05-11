package executors_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vector76/raymond/internal/executors"
	"github.com/vector76/raymond/internal/platform"
	wfstate "github.com/vector76/raymond/internal/state"
)

// fakeSig is a minimal stub of the executors.ShutdownSignal interface used to
// drive the three injection cases below.
type fakeSig struct {
	req  bool
	path string
}

func (f *fakeSig) IsRequested() bool    { return f.req }
func (f *fakeSig) SentinelPath() string { return f.path }

// makeShutdownEnvYaml writes a minimal YAML scope with a script state that the
// fake runner short-circuits — we never actually exec the script; we only want
// to capture the env the executor passes to the script runner.
func makeShutdownEnvYaml(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	content := `states:
  NEXT:
    prompt: "next"
  CHECK:
    sh: |
      #!/bin/sh
      echo '<goto>NEXT.md</goto>'
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// captureScriptEnv runs the ScriptExecutor once with the given ExecutionContext
// and returns the env map handed to the script runner.
func captureScriptEnv(t *testing.T, execCtx *executors.ExecutionContext, ws *wfstate.WorkflowState) map[string]string {
	t.Helper()

	var captured map[string]string
	executors.SetRunScriptFn(func(
		ctx context.Context, scriptPath string, timeout float64,
		env map[string]string, cwd string,
	) (*platform.ScriptResult, error) {
		captured = env
		return &platform.ScriptResult{
			Stdout:   "<goto>NEXT.md</goto>\n",
			ExitCode: 0,
		}, nil
	})
	defer executors.ResetRunScriptFn()

	_, err := executors.NewScriptExecutor().Execute(
		context.Background(), &ws.Agents[0], ws, execCtx,
	)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	return captured
}

func newShutdownEnvWorkflow(t *testing.T) *wfstate.WorkflowState {
	t.Helper()
	yamlPath := makeShutdownEnvYaml(t)
	return &wfstate.WorkflowState{
		WorkflowID: "stop-env-test",
		ScopeDir:   yamlPath,
		BudgetUSD:  10.0,
		Agents: []wfstate.AgentState{{
			ID:           "agent-S",
			CurrentState: "CHECK.sh",
			ScopeDir:     yamlPath,
			Stack:        []wfstate.StackFrame{},
		}},
	}
}

func TestScriptExecutor_StopEnv_NilSignal_NotInjected(t *testing.T) {
	ws := newShutdownEnvWorkflow(t)
	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: ws.WorkflowID}
	// ShutdownSignal intentionally left nil — CLI default.

	env := captureScriptEnv(t, execCtx, ws)

	if _, ok := env["RAYMOND_STOP_REQUESTED"]; ok {
		t.Errorf("RAYMOND_STOP_REQUESTED must not be present when signal is nil; got %q", env["RAYMOND_STOP_REQUESTED"])
	}
	if _, ok := env["RAYMOND_STOP_SENTINEL"]; ok {
		t.Errorf("RAYMOND_STOP_SENTINEL must not be present when signal is nil; got %q", env["RAYMOND_STOP_SENTINEL"])
	}
}

func TestScriptExecutor_StopEnv_SignalNotRequested_NotInjected(t *testing.T) {
	ws := newShutdownEnvWorkflow(t)
	execCtx := &executors.ExecutionContext{
		Bus:            newBus(),
		WorkflowID:     ws.WorkflowID,
		ShutdownSignal: &fakeSig{req: false, path: "/tmp/should-not-appear"},
	}

	env := captureScriptEnv(t, execCtx, ws)

	if _, ok := env["RAYMOND_STOP_REQUESTED"]; ok {
		t.Errorf("RAYMOND_STOP_REQUESTED must not be present when IsRequested=false; got %q", env["RAYMOND_STOP_REQUESTED"])
	}
	if _, ok := env["RAYMOND_STOP_SENTINEL"]; ok {
		t.Errorf("RAYMOND_STOP_SENTINEL must not be present when IsRequested=false; got %q", env["RAYMOND_STOP_SENTINEL"])
	}
}

func TestScriptExecutor_StopEnv_SignalRequested_Injected(t *testing.T) {
	ws := newShutdownEnvWorkflow(t)
	sentinel := "/some/raymond/dir/shutdown.sentinel"
	execCtx := &executors.ExecutionContext{
		Bus:            newBus(),
		WorkflowID:     ws.WorkflowID,
		ShutdownSignal: &fakeSig{req: true, path: sentinel},
	}

	env := captureScriptEnv(t, execCtx, ws)

	if got := env["RAYMOND_STOP_REQUESTED"]; got != "1" {
		t.Errorf("RAYMOND_STOP_REQUESTED = %q, want %q", got, "1")
	}
	if got := env["RAYMOND_STOP_SENTINEL"]; got != sentinel {
		t.Errorf("RAYMOND_STOP_SENTINEL = %q, want %q", got, sentinel)
	}
}
