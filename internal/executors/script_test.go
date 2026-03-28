package executors_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vector76/raymond/internal/executors"
	"github.com/vector76/raymond/internal/platform"
	wfstate "github.com/vector76/raymond/internal/state"
)

// --------------------------------------------------------------------------
// ScriptExecutor — YAML scope tests
// --------------------------------------------------------------------------

// makeYamlFile creates a temp YAML file with the given content and returns its path.
func makeYamlFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScriptExecutor_YamlScope_ExtractsAndExecutes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	yamlPath := makeYamlFile(t, `states:
  NEXT:
    prompt: "next"
  CHECK:
    sh: |
      #!/bin/sh
      echo '<goto>NEXT.md</goto>'
`)

	ws := &wfstate.WorkflowState{
		WorkflowID: "yaml-test",
		ScopeDir:   yamlPath,
		BudgetUSD:  10.0,
		Agents: []wfstate.AgentState{{
			ID:           "main",
			CurrentState: "CHECK.sh",
			ScopeDir:     yamlPath,
			Stack:        []wfstate.StackFrame{},
		}},
	}

	b := newBus()
	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: ws.WorkflowID}

	executors.SetRunScriptFn(func(
		ctx context.Context, scriptPath string, timeout float64,
		env map[string]string, cwd string,
	) (*platform.ScriptResult, error) {
		// Verify the script was extracted to a temp file.
		data, err := os.ReadFile(scriptPath)
		if err != nil {
			t.Fatalf("failed to read extracted script: %v", err)
		}
		if !strings.Contains(string(data), "#!/bin/sh") {
			t.Errorf("extracted script should contain shebang, got %q", string(data))
		}
		return &platform.ScriptResult{
			Stdout:   "<goto>NEXT.md</goto>\n",
			ExitCode: 0,
		}, nil
	})
	defer executors.ResetRunScriptFn()

	result, err := executors.NewScriptExecutor().Execute(
		context.Background(), &ws.Agents[0], ws, execCtx,
	)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Transition.Tag != "goto" {
		t.Errorf("expected goto transition, got %q", result.Transition.Tag)
	}
	if result.Transition.Target != "NEXT.md" {
		t.Errorf("expected target NEXT.md, got %q", result.Transition.Target)
	}
}

func TestScriptExecutor_YamlScope_EnvVars(t *testing.T) {
	yamlPath := makeYamlFile(t, `states:
  NEXT:
    prompt: "next"
  CHECK:
    sh: |
      #!/bin/sh
      echo '<goto>NEXT.md</goto>'
`)

	pendingResult := "prev-result"
	ws := &wfstate.WorkflowState{
		WorkflowID: "yaml-env-test",
		ScopeDir:   yamlPath,
		BudgetUSD:  10.0,
		Agents: []wfstate.AgentState{{
			ID:            "agent-A",
			CurrentState:  "CHECK.sh",
			ScopeDir:      yamlPath,
			PendingResult: &pendingResult,
			Stack:         []wfstate.StackFrame{},
		}},
	}

	b := newBus()
	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: ws.WorkflowID}

	var capturedEnv map[string]string
	executors.SetRunScriptFn(func(
		ctx context.Context, scriptPath string, timeout float64,
		env map[string]string, cwd string,
	) (*platform.ScriptResult, error) {
		capturedEnv = env
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

	if capturedEnv["RAYMOND_WORKFLOW_ID"] != "yaml-env-test" {
		t.Errorf("RAYMOND_WORKFLOW_ID = %q, want yaml-env-test", capturedEnv["RAYMOND_WORKFLOW_ID"])
	}
	if capturedEnv["RAYMOND_AGENT_ID"] != "agent-A" {
		t.Errorf("RAYMOND_AGENT_ID = %q, want agent-A", capturedEnv["RAYMOND_AGENT_ID"])
	}
	if capturedEnv["RAYMOND_RESULT"] != "prev-result" {
		t.Errorf("RAYMOND_RESULT = %q, want prev-result", capturedEnv["RAYMOND_RESULT"])
	}
}
