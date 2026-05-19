package executors_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vector76/raymond/internal/events"
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
		env map[string]string, cwd string, onChunk func(string, []byte),
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
		env map[string]string, cwd string, onChunk func(string, []byte),
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
	if capturedEnv["RAYMOND_INPUT"] != "prev-result" {
		t.Errorf("RAYMOND_INPUT = %q, want prev-result", capturedEnv["RAYMOND_INPUT"])
	}
}

func TestScriptExecutor_YamlScope_TaskFolderEnvVar(t *testing.T) {
	yamlPath := makeYamlFile(t, `states:
  NEXT:
    prompt: "next"
  CHECK:
    sh: |
      #!/bin/sh
      echo '<goto>NEXT.md</goto>'
`)

	ws := &wfstate.WorkflowState{
		WorkflowID: "yaml-tf-test",
		ScopeDir:   yamlPath,
		BudgetUSD:  10.0,
		Agents: []wfstate.AgentState{{
			ID:           "agent-B",
			CurrentState: "CHECK.sh",
			ScopeDir:     yamlPath,
			TaskFolder:   "/output/agent-B_task2",
			Stack:        []wfstate.StackFrame{},
		}},
	}

	b := newBus()
	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: ws.WorkflowID}

	var capturedEnv map[string]string
	executors.SetRunScriptFn(func(
		ctx context.Context, scriptPath string, timeout float64,
		env map[string]string, cwd string, onChunk func(string, []byte),
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

	if capturedEnv["RAYMOND_TASK_FOLDER"] != "/output/agent-B_task2" {
		t.Errorf("RAYMOND_TASK_FOLDER = %q, want /output/agent-B_task2", capturedEnv["RAYMOND_TASK_FOLDER"])
	}
}

// TestScriptExecutor_TimeoutError_IncludesStateAndSource verifies the timeout
// error message names the state (not the temp script path) and attributes the
// timeout to its configured source.
func TestScriptExecutor_TimeoutError_IncludesStateAndSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CHECK.sh"), []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "NEXT.md"), []byte("next"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name           string
		source         string
		wantSubstrings []string
	}{
		{
			name:   "cli flag source",
			source: "--timeout flag",
			wantSubstrings: []string{
				"Script 'CHECK.sh' produced no output for",
				"600 seconds",
				"(timeout from --timeout flag)",
			},
		},
		{
			name:   "per-state yaml source",
			source: "per-state timeout in workflow YAML",
			wantSubstrings: []string{
				"Script 'CHECK.sh' produced no output for",
				"(timeout from per-state timeout in workflow YAML)",
			},
		},
		{
			name:   "empty source falls back to unknown",
			source: "",
			wantSubstrings: []string{
				"Script 'CHECK.sh' produced no output for",
				"(timeout from unknown source)",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := &wfstate.WorkflowState{
				WorkflowID: "timeout-test",
				ScopeDir:   dir,
				BudgetUSD:  10.0,
				Agents: []wfstate.AgentState{{
					ID:           "main",
					CurrentState: "CHECK.sh",
					ScopeDir:     dir,
					Stack:        []wfstate.StackFrame{},
				}},
			}

			b := newBus()
			execCtx := &executors.ExecutionContext{
				Bus:           b,
				WorkflowID:    ws.WorkflowID,
				Timeout:       600,
				TimeoutSource: tc.source,
			}

			executors.SetRunScriptFn(func(
				ctx context.Context, scriptPath string, timeout float64,
				env map[string]string, cwd string, onChunk func(string, []byte),
			) (*platform.ScriptResult, error) {
				return nil, &platform.ScriptTimeoutError{ScriptPath: scriptPath, Timeout: timeout}
			})
			defer executors.ResetRunScriptFn()

			_, err := executors.NewScriptExecutor().Execute(
				context.Background(), &ws.Agents[0], ws, execCtx,
			)
			if err == nil {
				t.Fatal("expected timeout error, got nil")
			}
			msg := err.Error()
			for _, want := range tc.wantSubstrings {
				if !strings.Contains(msg, want) {
					t.Errorf("error message missing %q\ngot: %s", want, msg)
				}
			}
			// The script path (the temp dir holding CHECK.sh, or any extracted
			// temp script) should not appear in the user-facing message — the
			// state name is enough.
			if strings.Contains(msg, dir) || strings.Contains(msg, "raymond-script-") {
				t.Errorf("error message should not expose script path\ngot: %s", msg)
			}
		})
	}
}

// --------------------------------------------------------------------------
// ScriptExecutor — PrintOutput wiring tests
// --------------------------------------------------------------------------

func TestScriptExecutor_PrintOutput_Stdout(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")

	b := newBus()
	printEvents, cancel := collectEvents[events.PrintOutput](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	executors.SetRunScriptFn(func(
		ctx context.Context, _ string, _ float64,
		_ map[string]string, _ string, onChunk func(string, []byte),
	) (*platform.ScriptResult, error) {
		onChunk("stdout", []byte("before <print>hello world</print> after"))
		return &platform.ScriptResult{Stdout: "<goto>NEXT.md</goto>\n", ExitCode: 0}, nil
	})
	defer executors.ResetRunScriptFn()

	_, err := executors.NewScriptExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*printEvents) != 1 {
		t.Fatalf("got %d PrintOutput events, want 1", len(*printEvents))
	}
	ev := (*printEvents)[0]
	if ev.AgentID != "main" {
		t.Errorf("AgentID = %q, want main", ev.AgentID)
	}
	if ev.Content != "hello world" {
		t.Errorf("Content = %q, want \"hello world\"", ev.Content)
	}
}

func TestScriptExecutor_PrintOutput_Stderr(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")

	b := newBus()
	printEvents, cancel := collectEvents[events.PrintOutput](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	executors.SetRunScriptFn(func(
		ctx context.Context, _ string, _ float64,
		_ map[string]string, _ string, onChunk func(string, []byte),
	) (*platform.ScriptResult, error) {
		onChunk("stderr", []byte("<print>from stderr</print>"))
		return &platform.ScriptResult{Stdout: "<goto>NEXT.md</goto>\n", ExitCode: 0}, nil
	})
	defer executors.ResetRunScriptFn()

	_, err := executors.NewScriptExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*printEvents) != 1 {
		t.Fatalf("got %d PrintOutput events, want 1", len(*printEvents))
	}
	if (*printEvents)[0].Content != "from stderr" {
		t.Errorf("Content = %q, want \"from stderr\"", (*printEvents)[0].Content)
	}
}

func TestScriptExecutor_PrintOutput_MultipleTagsOneEventEach(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")

	b := newBus()
	printEvents, cancel := collectEvents[events.PrintOutput](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	executors.SetRunScriptFn(func(
		ctx context.Context, _ string, _ float64,
		_ map[string]string, _ string, onChunk func(string, []byte),
	) (*platform.ScriptResult, error) {
		onChunk("stdout", []byte("<print>first</print><print>second</print><print>third</print>"))
		return &platform.ScriptResult{Stdout: "<goto>NEXT.md</goto>\n", ExitCode: 0}, nil
	})
	defer executors.ResetRunScriptFn()

	_, err := executors.NewScriptExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*printEvents) != 3 {
		t.Fatalf("got %d PrintOutput events, want 3", len(*printEvents))
	}
	want := []string{"first", "second", "third"}
	for i, ev := range *printEvents {
		if ev.Content != want[i] {
			t.Errorf("event[%d].Content = %q, want %q", i, ev.Content, want[i])
		}
	}
}

func TestScriptExecutor_PrintOutput_IncompleteTagProducesNoEvent(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")

	b := newBus()
	printEvents, cancel := collectEvents[events.PrintOutput](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	executors.SetRunScriptFn(func(
		ctx context.Context, _ string, _ float64,
		_ map[string]string, _ string, onChunk func(string, []byte),
	) (*platform.ScriptResult, error) {
		// Complete tag then an incomplete one at the end.
		onChunk("stdout", []byte("<print>complete</print> trailing <print>incomplete"))
		return &platform.ScriptResult{Stdout: "<goto>NEXT.md</goto>\n", ExitCode: 0}, nil
	})
	defer executors.ResetRunScriptFn()

	_, err := executors.NewScriptExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Only the complete tag fires; the incomplete one is silently discarded.
	if len(*printEvents) != 1 {
		t.Fatalf("got %d PrintOutput events, want 1", len(*printEvents))
	}
	if (*printEvents)[0].Content != "complete" {
		t.Errorf("Content = %q, want \"complete\"", (*printEvents)[0].Content)
	}
}

func TestScriptExecutor_PrintOutput_CoexistsWithTransitionTag(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")

	b := newBus()
	printEvents, cancel := collectEvents[events.PrintOutput](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	executors.SetRunScriptFn(func(
		ctx context.Context, _ string, _ float64,
		_ map[string]string, _ string, onChunk func(string, []byte),
	) (*platform.ScriptResult, error) {
		onChunk("stdout", []byte("<print>status update</print>"))
		// ScriptResult.Stdout contains both the print tag and a transition tag.
		return &platform.ScriptResult{
			Stdout:   "<print>status update</print>\n<goto>NEXT.md</goto>\n",
			ExitCode: 0,
		}, nil
	})
	defer executors.ResetRunScriptFn()

	result, err := executors.NewScriptExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Transition parsing must still find the goto tag.
	if result.Transition.Tag != "goto" || result.Transition.Target != "NEXT.md" {
		t.Errorf("unexpected transition: %+v", result.Transition)
	}

	// PrintOutput event must have been emitted from the streaming chunk.
	if len(*printEvents) != 1 {
		t.Fatalf("got %d PrintOutput events, want 1", len(*printEvents))
	}
	if (*printEvents)[0].Content != "status update" {
		t.Errorf("Content = %q, want \"status update\"", (*printEvents)[0].Content)
	}
}
