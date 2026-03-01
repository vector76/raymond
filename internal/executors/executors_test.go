package executors_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/ccwrap"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/executors"
	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/platform"
	wfstate "github.com/vector76/raymond/internal/state"
)

// --------------------------------------------------------------------------
// Test helpers
// --------------------------------------------------------------------------

func newBus() *bus.Bus { return bus.New() }

// collectEvents subscribes to event type T on b and collects all instances
// until the returned cancel is called.
func collectEvents[T any](b *bus.Bus) (*[]T, func()) {
	var collected []T
	cancel := bus.Subscribe(b, func(e T) {
		collected = append(collected, e)
	})
	return &collected, cancel
}

// makeWorkflow creates a minimal temp directory workflow for testing.
func makeWorkflow(t *testing.T) (scopeDir string, wfState *wfstate.WorkflowState) {
	t.Helper()
	dir := t.TempDir()

	// Create prompt file
	write(t, filepath.Join(dir, "START.md"), "Test prompt for {{result}}")
	// Create a target state file
	write(t, filepath.Join(dir, "NEXT.md"), "Next prompt")

	ws := &wfstate.WorkflowState{
		WorkflowID:   "test-001",
		ScopeDir:     dir,
		TotalCostUSD: 0.0,
		BudgetUSD:    10.0,
		Agents:       []wfstate.AgentState{{ID: "main", CurrentState: "START.md", ScopeDir: dir, Stack: []wfstate.StackFrame{}}},
	}
	return dir, ws
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeMockStream returns a <-chan ccwrap.StreamItem that yields the provided
// JSON objects.
func makeMockStream(objects []map[string]any) <-chan ccwrap.StreamItem {
	ch := make(chan ccwrap.StreamItem, len(objects)+1)
	for _, obj := range objects {
		ch <- ccwrap.StreamItem{Object: obj}
	}
	close(ch)
	return ch
}

// makeMockRunScript returns a runScriptFn replacement that always returns sr.
func makeMockRunScript(sr *platform.ScriptResult, err error) func(context.Context, string, float64, map[string]string, string) (*platform.ScriptResult, error) {
	return func(context.Context, string, float64, map[string]string, string) (*platform.ScriptResult, error) {
		return sr, err
	}
}

// --------------------------------------------------------------------------
// ExecutionContext tests
// --------------------------------------------------------------------------

func TestExecutionContext_Creation(t *testing.T) {
	b := newBus()
	ctx := &executors.ExecutionContext{
		Bus:        b,
		WorkflowID: "test-001",
	}

	if ctx.Bus != b {
		t.Error("Bus not set")
	}
	if ctx.WorkflowID != "test-001" {
		t.Error("WorkflowID not set")
	}
	if ctx.DebugDir != "" {
		t.Error("DebugDir should default to empty string")
	}
	if ctx.StateDir != "" {
		t.Error("StateDir should default to empty string")
	}
	if ctx.DefaultModel != "" {
		t.Error("DefaultModel should default to empty string")
	}
	if ctx.Timeout != 0 {
		t.Error("Timeout should default to 0")
	}
	if ctx.DangerouslySkipPermissions {
		t.Error("DangerouslySkipPermissions should default to false")
	}
	if ctx.StepCounters != nil {
		t.Error("StepCounters should default to nil")
	}
}

func TestExecutionContext_WithAllFields(t *testing.T) {
	b := newBus()
	debugDir := t.TempDir()

	ctx := &executors.ExecutionContext{
		Bus:                        b,
		WorkflowID:                 "test-002",
		DebugDir:                   debugDir,
		StateDir:                   "/path/to/state",
		DefaultModel:               "sonnet",
		DefaultEffort:              "high",
		Timeout:                    300.0,
		DangerouslySkipPermissions: true,
		StepCounters:               map[string]int{"main": 5},
	}

	if ctx.DebugDir != debugDir {
		t.Errorf("DebugDir = %q, want %q", ctx.DebugDir, debugDir)
	}
	if ctx.StateDir != "/path/to/state" {
		t.Errorf("StateDir = %q", ctx.StateDir)
	}
	if ctx.DefaultModel != "sonnet" {
		t.Errorf("DefaultModel = %q", ctx.DefaultModel)
	}
	if ctx.DefaultEffort != "high" {
		t.Errorf("DefaultEffort = %q", ctx.DefaultEffort)
	}
	if ctx.Timeout != 300.0 {
		t.Errorf("Timeout = %v", ctx.Timeout)
	}
	if !ctx.DangerouslySkipPermissions {
		t.Error("DangerouslySkipPermissions should be true")
	}
	if ctx.StepCounters["main"] != 5 {
		t.Errorf("StepCounters[main] = %d, want 5", ctx.StepCounters["main"])
	}
}

func TestExecutionContext_GetNextStepNumber_First(t *testing.T) {
	ctx := &executors.ExecutionContext{Bus: newBus()}
	if n := ctx.GetNextStepNumber("main"); n != 1 {
		t.Errorf("first step = %d, want 1", n)
	}
}

func TestExecutionContext_GetNextStepNumber_Increments(t *testing.T) {
	ctx := &executors.ExecutionContext{Bus: newBus()}
	if n := ctx.GetNextStepNumber("main"); n != 1 {
		t.Errorf("step 1 = %d", n)
	}
	if n := ctx.GetNextStepNumber("main"); n != 2 {
		t.Errorf("step 2 = %d", n)
	}
	if n := ctx.GetNextStepNumber("main"); n != 3 {
		t.Errorf("step 3 = %d", n)
	}
}

func TestExecutionContext_GetNextStepNumber_PerAgent(t *testing.T) {
	ctx := &executors.ExecutionContext{Bus: newBus()}
	if n := ctx.GetNextStepNumber("main"); n != 1 {
		t.Errorf("main step 1 = %d", n)
	}
	if n := ctx.GetNextStepNumber("worker"); n != 1 {
		t.Errorf("worker step 1 = %d", n)
	}
	if n := ctx.GetNextStepNumber("main"); n != 2 {
		t.Errorf("main step 2 = %d", n)
	}
	if n := ctx.GetNextStepNumber("worker"); n != 2 {
		t.Errorf("worker step 2 = %d", n)
	}
}

// --------------------------------------------------------------------------
// ExecutionResult tests
// --------------------------------------------------------------------------

func TestExecutionResult_Creation(t *testing.T) {
	sid := "sess-123"
	r := executors.ExecutionResult{
		SessionID: &sid,
		CostUSD:   0.05,
	}
	if r.SessionID == nil || *r.SessionID != "sess-123" {
		t.Error("SessionID not set correctly")
	}
	if r.CostUSD != 0.05 {
		t.Errorf("CostUSD = %v", r.CostUSD)
	}
}

func TestExecutionResult_WithNilSession(t *testing.T) {
	r := executors.ExecutionResult{
		SessionID: nil,
		CostUSD:   0.0,
	}
	if r.SessionID != nil {
		t.Error("SessionID should be nil")
	}
}

// --------------------------------------------------------------------------
// ExtractStateName tests
// --------------------------------------------------------------------------

func TestExtractStateName_Md(t *testing.T) {
	if got := executors.ExtractStateName("START.md"); got != "START" {
		t.Errorf("got %q", got)
	}
}

func TestExtractStateName_Sh(t *testing.T) {
	if got := executors.ExtractStateName("CHECK.sh"); got != "CHECK" {
		t.Errorf("got %q", got)
	}
}

func TestExtractStateName_Bat(t *testing.T) {
	if got := executors.ExtractStateName("SCRIPT.bat"); got != "SCRIPT" {
		t.Errorf("got %q", got)
	}
}

func TestExtractStateName_Ps1(t *testing.T) {
	if got := executors.ExtractStateName("SCRIPT.ps1"); got != "SCRIPT" {
		t.Errorf("got %q", got)
	}
}

func TestExtractStateName_CaseInsensitive(t *testing.T) {
	if got := executors.ExtractStateName("START.MD"); got != "START" {
		t.Errorf("got %q", got)
	}
	if got := executors.ExtractStateName("CHECK.SH"); got != "CHECK" {
		t.Errorf("got %q", got)
	}
	if got := executors.ExtractStateName("SCRIPT.PS1"); got != "SCRIPT" {
		t.Errorf("got %q", got)
	}
}

func TestExtractStateName_NoExtension(t *testing.T) {
	if got := executors.ExtractStateName("NOEXT"); got != "NOEXT" {
		t.Errorf("got %q", got)
	}
	if got := executors.ExtractStateName("file.txt"); got != "file.txt" {
		t.Errorf("got %q", got)
	}
}

// --------------------------------------------------------------------------
// ResolveTransitionTargets tests
// --------------------------------------------------------------------------

func TestResolveTransitionTargets_Result(t *testing.T) {
	from := parsing.Transition{Tag: "result", Payload: "done"}
	got, err := executors.ResolveTransitionTargets(from, "/some/path")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "result" || got.Payload != "done" {
		t.Errorf("result transition altered: %+v", got)
	}
}

func TestResolveTransitionTargets_Goto(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "NEXT.md"), "prompt")

	from := parsing.Transition{Tag: "goto", Target: "NEXT", Attributes: map[string]string{}}
	got, err := executors.ResolveTransitionTargets(from, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != "NEXT.md" {
		t.Errorf("target = %q, want NEXT.md", got.Target)
	}
}

func TestResolveTransitionTargets_WithReturn(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "FUNC.md"), "prompt")
	write(t, filepath.Join(dir, "CALLER.md"), "prompt")

	from := parsing.Transition{Tag: "function", Target: "FUNC", Attributes: map[string]string{"return": "CALLER"}}
	got, err := executors.ResolveTransitionTargets(from, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != "FUNC.md" {
		t.Errorf("target = %q, want FUNC.md", got.Target)
	}
	if got.Attributes["return"] != "CALLER.md" {
		t.Errorf("return = %q, want CALLER.md", got.Attributes["return"])
	}
}

func TestResolveTransitionTargets_WithNext(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "FORK.md"), "prompt")
	write(t, filepath.Join(dir, "AFTER.md"), "prompt")

	from := parsing.Transition{Tag: "fork", Target: "FORK", Attributes: map[string]string{"next": "AFTER"}}
	got, err := executors.ResolveTransitionTargets(from, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != "FORK.md" {
		t.Errorf("target = %q, want FORK.md", got.Target)
	}
	if got.Attributes["next"] != "AFTER.md" {
		t.Errorf("next = %q, want AFTER.md", got.Attributes["next"])
	}
}

func TestResolveTransitionTargets_NotFound(t *testing.T) {
	dir := t.TempDir()
	from := parsing.Transition{Tag: "goto", Target: "MISSING", Attributes: map[string]string{}}
	_, err := executors.ResolveTransitionTargets(from, dir)
	if err == nil {
		t.Fatal("expected error for missing target")
	}
}

func TestResolveTransitionTargets_WorkflowTagsPassThroughTarget(t *testing.T) {
	// Workflow tags have targets that are paths to other workflows (may contain
	// path separators). The target must NOT be passed through ResolveState, but
	// the return/next attributes (which reference states in the current workflow)
	// must still be resolved.
	dir := t.TempDir()
	write(t, filepath.Join(dir, "AFTER.md"), "prompt")

	cases := []struct {
		tag      string
		attrKey  string // the resume-state attribute for this tag
	}{
		{"fork-workflow", "next"},
		{"call-workflow", "return"},
		{"function-workflow", "return"},
	}

	for _, tc := range cases {
		attrs := map[string]string{tc.attrKey: "AFTER"}
		from := parsing.Transition{Tag: tc.tag, Target: "../other-workflow/", Attributes: attrs}
		got, err := executors.ResolveTransitionTargets(from, dir)
		if err != nil {
			t.Fatalf("tag %q: unexpected error: %v", tc.tag, err)
		}
		if got.Target != "../other-workflow/" {
			t.Errorf("tag %q: target = %q, want %q", tc.tag, got.Target, "../other-workflow/")
		}
		if got.Attributes[tc.attrKey] != "AFTER.md" {
			t.Errorf("tag %q: %s = %q, want AFTER.md", tc.tag, tc.attrKey, got.Attributes[tc.attrKey])
		}
	}
}

// --------------------------------------------------------------------------
// GetExecutor factory tests
// --------------------------------------------------------------------------

func TestGetExecutor_Markdown(t *testing.T) {
	ex := executors.GetExecutor("START.md")
	if _, ok := ex.(*executors.MarkdownExecutor); !ok {
		t.Errorf("expected *MarkdownExecutor, got %T", ex)
	}
}

func TestGetExecutor_Shell(t *testing.T) {
	ex := executors.GetExecutor("CHECK.sh")
	if _, ok := ex.(*executors.ScriptExecutor); !ok {
		t.Errorf("expected *ScriptExecutor, got %T", ex)
	}
}

func TestGetExecutor_Bat(t *testing.T) {
	ex := executors.GetExecutor("CHECK.bat")
	if _, ok := ex.(*executors.ScriptExecutor); !ok {
		t.Errorf("expected *ScriptExecutor, got %T", ex)
	}
}

func TestGetExecutor_Ps1(t *testing.T) {
	ex := executors.GetExecutor("CHECK.ps1")
	if _, ok := ex.(*executors.ScriptExecutor); !ok {
		t.Errorf("expected *ScriptExecutor, got %T", ex)
	}
}

func TestGetExecutor_CaseInsensitive(t *testing.T) {
	ex := executors.GetExecutor("START.MD")
	if _, ok := ex.(*executors.MarkdownExecutor); !ok {
		t.Errorf("expected *MarkdownExecutor, got %T", ex)
	}
	ex = executors.GetExecutor("CHECK.BAT")
	if _, ok := ex.(*executors.ScriptExecutor); !ok {
		t.Errorf("expected *ScriptExecutor for .BAT, got %T", ex)
	}
	ex = executors.GetExecutor("CHECK.PS1")
	if _, ok := ex.(*executors.ScriptExecutor); !ok {
		t.Errorf("expected *ScriptExecutor for .PS1, got %T", ex)
	}
}

func TestGetExecutor_Singletons(t *testing.T) {
	md1 := executors.GetExecutor("A.md")
	md2 := executors.GetExecutor("B.md")
	if md1 != md2 {
		t.Error("markdown executors should be the same singleton")
	}

	sh1 := executors.GetExecutor("A.sh")
	sh2 := executors.GetExecutor("B.sh")
	if sh1 != sh2 {
		t.Error("script executors should be the same singleton")
	}

	// .bat and .ps1 share the same ScriptExecutor singleton as .sh
	bat := executors.GetExecutor("A.bat")
	ps1 := executors.GetExecutor("A.ps1")
	if bat != sh1 {
		t.Error(".bat should return the same ScriptExecutor singleton as .sh")
	}
	if ps1 != sh1 {
		t.Error(".ps1 should return the same ScriptExecutor singleton as .sh")
	}
}

// TestScriptExecutor_UsesAgentScopeDir verifies that the executor loads the
// script from agent.ScopeDir, not wfState.ScopeDir.
func TestScriptExecutor_UsesAgentScopeDir(t *testing.T) {
	workflowDir := t.TempDir()
	agentDir := t.TempDir()
	// Place CHECK.sh only in agentDir — if executor uses workflowDir it will fail.
	write(t, filepath.Join(agentDir, "CHECK.sh"), "#!/bin/sh\necho '<goto>NEXT.md</goto>'")
	write(t, filepath.Join(agentDir, "NEXT.md"), "next")

	ws := &wfstate.WorkflowState{
		WorkflowID: "test-scope",
		ScopeDir:   workflowDir,
		BudgetUSD:  10.0,
		Agents:     []wfstate.AgentState{{ID: "main", CurrentState: "CHECK.sh", ScopeDir: agentDir, Stack: []wfstate.StackFrame{}}},
	}
	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: ws.WorkflowID}

	executors.SetRunScriptFn(makeMockRunScript(
		&platform.ScriptResult{Stdout: "<goto>NEXT.md</goto>\n", ExitCode: 0}, nil,
	))
	defer executors.ResetRunScriptFn()

	_, err := executors.NewScriptExecutor().Execute(context.Background(), &ws.Agents[0], ws, execCtx)
	if err != nil {
		t.Fatalf("expected executor to load from agent.ScopeDir, got error: %v", err)
	}
}

// --------------------------------------------------------------------------
// ScriptExecutor tests
// --------------------------------------------------------------------------

func makeScriptWorkflow(t *testing.T) (scopeDir string, wfState *wfstate.WorkflowState) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "NEXT.md"), "Next prompt")
	ws := &wfstate.WorkflowState{
		WorkflowID:   "test-001",
		ScopeDir:     dir,
		TotalCostUSD: 0.0,
		BudgetUSD:    10.0,
		Agents:       []wfstate.AgentState{{ID: "main", CurrentState: "CHECK.sh", ScopeDir: dir, Stack: []wfstate.StackFrame{}}},
	}
	return dir, ws
}

func TestScriptExecutor_EmitsStateStarted(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)

	// Create script file on disk (ScriptExecutor checks existence via RunScript).
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh\necho '<goto>NEXT.md</goto>'")

	b := newBus()
	started, cancel := collectEvents[events.StateStarted](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}
	agent := &wfState.Agents[0]

	executors.SetRunScriptFn(makeMockRunScript(
		&platform.ScriptResult{Stdout: "<goto>NEXT.md</goto>", ExitCode: 0},
		nil,
	))
	defer executors.ResetRunScriptFn()

	_, err := executors.NewScriptExecutor().Execute(context.Background(), agent, wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*started) != 1 {
		t.Fatalf("got %d StateStarted events, want 1", len(*started))
	}
	ev := (*started)[0]
	if ev.AgentID != "main" || ev.StateName != "CHECK.sh" || ev.StateType != "script" {
		t.Errorf("unexpected StateStarted: %+v", ev)
	}
}

func TestScriptExecutor_EmitsStateCompleted(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")

	b := newBus()
	completed, cancel := collectEvents[events.StateCompleted](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}
	agent := &wfState.Agents[0]

	executors.SetRunScriptFn(makeMockRunScript(
		&platform.ScriptResult{Stdout: "<goto>NEXT.md</goto>", ExitCode: 0},
		nil,
	))
	defer executors.ResetRunScriptFn()

	_, err := executors.NewScriptExecutor().Execute(context.Background(), agent, wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*completed) != 1 {
		t.Fatalf("got %d StateCompleted events, want 1", len(*completed))
	}
	ev := (*completed)[0]
	if ev.AgentID != "main" || ev.StateName != "CHECK.sh" || ev.CostUSD != 0.0 {
		t.Errorf("unexpected StateCompleted: %+v", ev)
	}
}

func TestScriptExecutor_PreservesSessionID(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")

	sid := "existing-sess"
	wfState.Agents[0].SessionID = &sid

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}
	agent := &wfState.Agents[0]

	executors.SetRunScriptFn(makeMockRunScript(
		&platform.ScriptResult{Stdout: "<goto>NEXT.md</goto>", ExitCode: 0},
		nil,
	))
	defer executors.ResetRunScriptFn()

	result, err := executors.NewScriptExecutor().Execute(context.Background(), agent, wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.SessionID == nil || *result.SessionID != "existing-sess" {
		t.Errorf("SessionID not preserved: %v", result.SessionID)
	}
}

func TestScriptExecutor_ReturnsZeroCost(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetRunScriptFn(makeMockRunScript(
		&platform.ScriptResult{Stdout: "<goto>NEXT.md</goto>", ExitCode: 0},
		nil,
	))
	defer executors.ResetRunScriptFn()

	result, err := executors.NewScriptExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.CostUSD != 0.0 {
		t.Errorf("CostUSD = %v, want 0.0", result.CostUSD)
	}
}

func TestScriptExecutor_ParsesTransitionFromStdout(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetRunScriptFn(makeMockRunScript(
		&platform.ScriptResult{
			Stdout:   "Some debug output\n<goto>NEXT.md</goto>\nMore output",
			ExitCode: 0,
		},
		nil,
	))
	defer executors.ResetRunScriptFn()

	result, err := executors.NewScriptExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Transition.Tag != "goto" || result.Transition.Target != "NEXT.md" {
		t.Errorf("unexpected transition: %+v", result.Transition)
	}
}

func TestScriptExecutor_RaisesErrorOnNonzeroExit(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetRunScriptFn(makeMockRunScript(
		&platform.ScriptResult{Stdout: "", Stderr: "Error occurred", ExitCode: 1},
		nil,
	))
	defer executors.ResetRunScriptFn()

	_, err := executors.NewScriptExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err == nil {
		t.Fatal("expected ScriptError")
	}
	var se *executors.ScriptError
	if !asError(err, &se) {
		t.Errorf("expected *ScriptError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "exit code 1") {
		t.Errorf("error should mention exit code: %v", err)
	}
}

func TestScriptExecutor_RaisesErrorOnNoTransition(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetRunScriptFn(makeMockRunScript(
		&platform.ScriptResult{Stdout: "Just some output without transition", ExitCode: 0},
		nil,
	))
	defer executors.ResetRunScriptFn()

	_, err := executors.NewScriptExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err == nil {
		t.Fatal("expected ScriptError")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "no transition tag") {
		t.Errorf("error should mention no transition tag: %v", err)
	}
}

func TestScriptExecutor_RaisesErrorOnMultipleTransitions(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")
	write(t, filepath.Join(dir, "OTHER.md"), "other")

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetRunScriptFn(makeMockRunScript(
		&platform.ScriptResult{
			Stdout:   "<goto>NEXT.md</goto><result>done</result>",
			ExitCode: 0,
		},
		nil,
	))
	defer executors.ResetRunScriptFn()

	_, err := executors.NewScriptExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err == nil {
		t.Fatal("expected ScriptError")
	}
	if !strings.Contains(err.Error(), "2 transition tags") {
		t.Errorf("error should mention 2 transition tags: %v", err)
	}
}

func TestScriptExecutor_EmitsScriptOutputEventWithDebug(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")
	debugDir := t.TempDir()

	b := newBus()
	scriptOutputs, cancel := collectEvents[events.ScriptOutput](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{
		Bus:        b,
		WorkflowID: wfState.WorkflowID,
		DebugDir:   debugDir,
	}

	executors.SetRunScriptFn(makeMockRunScript(
		&platform.ScriptResult{
			Stdout:   "<goto>NEXT.md</goto>\n",
			Stderr:   "some stderr",
			ExitCode: 0,
		},
		nil,
	))
	defer executors.ResetRunScriptFn()

	_, err := executors.NewScriptExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*scriptOutputs) != 1 {
		t.Fatalf("got %d ScriptOutput events, want 1", len(*scriptOutputs))
	}
	ev := (*scriptOutputs)[0]
	if ev.Stdout != "<goto>NEXT.md</goto>\n" {
		t.Errorf("Stdout = %q", ev.Stdout)
	}
	if ev.Stderr != "some stderr" {
		t.Errorf("Stderr = %q", ev.Stderr)
	}
	if ev.ExitCode != 0 {
		t.Errorf("ExitCode = %d", ev.ExitCode)
	}
}

func TestScriptExecutor_HandlesResultTransition(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetRunScriptFn(makeMockRunScript(
		&platform.ScriptResult{
			Stdout:   "<result>Script finished successfully</result>",
			ExitCode: 0,
		},
		nil,
	))
	defer executors.ResetRunScriptFn()

	result, err := executors.NewScriptExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Transition.Tag != "result" {
		t.Errorf("tag = %q, want result", result.Transition.Tag)
	}
	if result.Transition.Payload != "Script finished successfully" {
		t.Errorf("payload = %q", result.Transition.Payload)
	}
}

func TestScriptExecutor_WritesDebugFiles(t *testing.T) {
	dir, wfState := makeScriptWorkflow(t)
	write(t, filepath.Join(dir, "CHECK.sh"), "#!/bin/sh")
	debugDir := t.TempDir()

	execCtx := &executors.ExecutionContext{
		Bus:      newBus(),
		DebugDir: debugDir,
	}

	executors.SetRunScriptFn(makeMockRunScript(
		&platform.ScriptResult{
			Stdout:   "<goto>NEXT.md</goto>\n",
			Stderr:   "debug info",
			ExitCode: 0,
		},
		nil,
	))
	defer executors.ResetRunScriptFn()

	_, err := executors.NewScriptExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	stdoutFiles, _ := filepath.Glob(filepath.Join(debugDir, "*.stdout.txt"))
	stderrFiles, _ := filepath.Glob(filepath.Join(debugDir, "*.stderr.txt"))
	metaFiles, _ := filepath.Glob(filepath.Join(debugDir, "*.meta.json"))

	if len(stdoutFiles) != 1 || len(stderrFiles) != 1 || len(metaFiles) != 1 {
		t.Errorf("expected 1 of each debug file, got stdout=%d stderr=%d meta=%d",
			len(stdoutFiles), len(stderrFiles), len(metaFiles))
	}

	data, _ := os.ReadFile(stdoutFiles[0])
	if string(data) != "<goto>NEXT.md</goto>\n" {
		t.Errorf("stdout file content = %q", data)
	}
	data, _ = os.ReadFile(stderrFiles[0])
	if string(data) != "debug info" {
		t.Errorf("stderr file content = %q", data)
	}

	var meta map[string]any
	data, _ = os.ReadFile(metaFiles[0])
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("meta.json parse error: %v", err)
	}
	if exitCode, _ := meta["exit_code"].(float64); exitCode != 0 {
		t.Errorf("meta exit_code = %v", meta["exit_code"])
	}
}

// TestMarkdownExecutor_UsesAgentScopeDir verifies that the executor loads the
// prompt from agent.ScopeDir, not wfState.ScopeDir. This matters for
// cross-workflow transitions where the two differ.
func TestMarkdownExecutor_UsesAgentScopeDir(t *testing.T) {
	workflowDir := t.TempDir()
	agentDir := t.TempDir()
	// Place START.md only in agentDir — if executor uses workflowDir it will fail.
	write(t, filepath.Join(agentDir, "START.md"), "prompt")
	write(t, filepath.Join(agentDir, "NEXT.md"), "next")

	ws := &wfstate.WorkflowState{
		WorkflowID: "test-scope",
		ScopeDir:   workflowDir,
		BudgetUSD:  10.0,
		Agents:     []wfstate.AgentState{{ID: "main", CurrentState: "START.md", ScopeDir: agentDir, Stack: []wfstate.StackFrame{}}},
	}
	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: ws.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"session_id": "s1", "total_cost_usd": 0.0},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &ws.Agents[0], ws, execCtx)
	if err != nil {
		t.Fatalf("expected executor to load from agent.ScopeDir, got error: %v", err)
	}
}

// --------------------------------------------------------------------------
// MarkdownExecutor tests
// --------------------------------------------------------------------------

func TestMarkdownExecutor_EmitsStateStarted(t *testing.T) {
	_, wfState := makeWorkflow(t)

	b := newBus()
	started, cancel := collectEvents[events.StateStarted](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}
	agent := &wfState.Agents[0]

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "Output\n<goto>NEXT.md</goto>"},
			{"session_id": "sess-123", "total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), agent, wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*started) != 1 {
		t.Fatalf("got %d StateStarted events, want 1", len(*started))
	}
	ev := (*started)[0]
	if ev.AgentID != "main" || ev.StateName != "START.md" || ev.StateType != "markdown" {
		t.Errorf("unexpected StateStarted: %+v", ev)
	}
}

func TestMarkdownExecutor_EmitsStateCompleted(t *testing.T) {
	_, wfState := makeWorkflow(t)

	b := newBus()
	completed, cancel := collectEvents[events.StateCompleted](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "Output\n<goto>NEXT.md</goto>"},
			{"session_id": "sess-123", "total_cost_usd": 0.05},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*completed) != 1 {
		t.Fatalf("got %d StateCompleted events, want 1", len(*completed))
	}
	ev := (*completed)[0]
	if ev.AgentID != "main" || ev.StateName != "START.md" || ev.CostUSD != 0.05 {
		t.Errorf("unexpected StateCompleted: %+v", ev)
	}
}

func TestMarkdownExecutor_ReturnsResultWithTransition(t *testing.T) {
	_, wfState := makeWorkflow(t)

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "Output\n<goto>NEXT.md</goto>"},
			{"session_id": "sess-123", "total_cost_usd": 0.02},
		})
	})
	defer executors.ResetInvokeStreamFn()

	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Transition.Tag != "goto" || result.Transition.Target != "NEXT.md" {
		t.Errorf("unexpected transition: %+v", result.Transition)
	}
	if result.SessionID == nil || *result.SessionID != "sess-123" {
		t.Errorf("SessionID = %v, want sess-123", result.SessionID)
	}
	if result.CostUSD != 0.02 {
		t.Errorf("CostUSD = %v, want 0.02", result.CostUSD)
	}
}

func TestMarkdownExecutor_ExtractsSessionID(t *testing.T) {
	_, wfState := makeWorkflow(t)

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"session_id": "new-sess-456", "total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.SessionID == nil || *result.SessionID != "new-sess-456" {
		t.Errorf("SessionID = %v", result.SessionID)
	}
}

func TestMarkdownExecutor_AccumulatesCost(t *testing.T) {
	_, wfState := makeWorkflow(t)
	wfState.TotalCostUSD = 1.00

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"total_cost_usd": 0.10},
		})
	})
	defer executors.ResetInvokeStreamFn()

	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.CostUSD != 0.10 {
		t.Errorf("CostUSD = %v, want 0.10", result.CostUSD)
	}
	if wfState.TotalCostUSD < 1.09 || wfState.TotalCostUSD > 1.11 {
		t.Errorf("TotalCostUSD = %v, want ~1.10", wfState.TotalCostUSD)
	}
}

func TestMarkdownExecutor_RaisesPromptFileError(t *testing.T) {
	emptyDir := t.TempDir()
	wfState := &wfstate.WorkflowState{
		WorkflowID: "test",
		ScopeDir:   emptyDir,
		BudgetUSD:  10.0,
		Agents:     []wfstate.AgentState{{ID: "main", CurrentState: "START.md", ScopeDir: emptyDir, Stack: []wfstate.StackFrame{}}},
	}

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err == nil {
		t.Fatal("expected PromptFileError")
	}
	var pfe *executors.PromptFileError
	if !asError(err, &pfe) {
		t.Errorf("expected *PromptFileError, got %T: %v", err, err)
	}
}

func TestMarkdownExecutor_RaisesLimitError(t *testing.T) {
	_, wfState := makeWorkflow(t)

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "result", "is_error": true, "result": "You've hit your limit for today"},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err == nil {
		t.Fatal("expected ClaudeCodeLimitError")
	}
	var le *executors.ClaudeCodeLimitError
	if !asError(err, &le) {
		t.Errorf("expected *ClaudeCodeLimitError, got %T: %v", err, err)
	}
}

func TestMarkdownExecutor_EmitsClaudeStreamOutputWithDebug(t *testing.T) {
	_, wfState := makeWorkflow(t)
	debugDir := t.TempDir()

	b := newBus()
	streamOutputs, cancel := collectEvents[events.ClaudeStreamOutput](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{
		Bus:      b,
		DebugDir: debugDir,
	}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "Output\n<goto>NEXT.md</goto>"},
			{"session_id": "sess-123", "total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*streamOutputs) != 2 {
		t.Errorf("got %d ClaudeStreamOutput events, want 2", len(*streamOutputs))
	}
}

func TestMarkdownExecutor_HandlesResultTransition(t *testing.T) {
	_, wfState := makeWorkflow(t)

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "Done\n<result>Task completed</result>"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Transition.Tag != "result" || result.Transition.Payload != "Task completed" {
		t.Errorf("unexpected transition: %+v", result.Transition)
	}
}

func TestMarkdownExecutor_WritesJSONLDebugFile(t *testing.T) {
	_, wfState := makeWorkflow(t)
	debugDir := t.TempDir()

	execCtx := &executors.ExecutionContext{
		Bus:      newBus(),
		DebugDir: debugDir,
	}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"session_id": "sess-123", "total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	jsonlFiles, _ := filepath.Glob(filepath.Join(debugDir, "*.jsonl"))
	if len(jsonlFiles) != 1 {
		t.Fatalf("expected 1 JSONL file, got %d", len(jsonlFiles))
	}

	data, _ := os.ReadFile(jsonlFiles[0])
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 JSONL lines, got %d", len(lines))
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &obj); err != nil {
		t.Fatalf("line 1 not valid JSON: %v", err)
	}
	if obj["type"] != "content" {
		t.Errorf("first line type = %v, want content", obj["type"])
	}
}

// --------------------------------------------------------------------------
// Reminder prompt tests
// --------------------------------------------------------------------------

func makeWorkflowWithPolicy(t *testing.T) (string, *wfstate.WorkflowState) {
	t.Helper()
	dir := t.TempDir()

	promptContent := "---\nallowed_transitions:\n  - { tag: goto, target: NEXT.md }\n  - { tag: result }\n---\nTest prompt\n"
	write(t, filepath.Join(dir, "START.md"), promptContent)
	write(t, filepath.Join(dir, "NEXT.md"), "Next prompt")

	ws := &wfstate.WorkflowState{
		WorkflowID:   "test-001",
		ScopeDir:     dir,
		TotalCostUSD: 0.0,
		BudgetUSD:    10.0,
		Agents:       []wfstate.AgentState{{ID: "main", CurrentState: "START.md", ScopeDir: dir, Stack: []wfstate.StackFrame{}}},
	}
	return dir, ws
}

func TestMarkdownExecutor_EmitsErrorEventOnRetry(t *testing.T) {
	_, wfState := makeWorkflowWithPolicy(t)

	b := newBus()
	errorEvents, cancel := collectEvents[events.ErrorOccurred](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	callCount := 0
	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		callCount++
		if callCount == 1 {
			return makeMockStream([]map[string]any{
				{"type": "content", "text": "No transition here"},
				{"total_cost_usd": 0.01},
			})
		}
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Transition.Tag != "goto" {
		t.Errorf("unexpected transition: %+v", result.Transition)
	}

	if len(*errorEvents) != 1 {
		t.Fatalf("got %d ErrorOccurred events, want 1", len(*errorEvents))
	}
	ev := (*errorEvents)[0]
	if !ev.IsRetryable || ev.RetryCount != 1 {
		t.Errorf("unexpected ErrorOccurred: %+v", ev)
	}
}

func TestMarkdownExecutor_RaisesAfterMaxRetries(t *testing.T) {
	_, wfState := makeWorkflowWithPolicy(t)

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "No transition"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err == nil {
		t.Fatal("expected error after max retries")
	}
	if !strings.Contains(err.Error(), "3 reminder attempts") {
		t.Errorf("error should mention 3 reminder attempts: %v", err)
	}
}

// --------------------------------------------------------------------------
// Time-based test helper to ensure events have timestamps
// --------------------------------------------------------------------------

func TestStateStarted_HasTimestamp(t *testing.T) {
	_, wfState := makeWorkflow(t)

	b := newBus()
	started, cancel := collectEvents[events.StateStarted](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	before := time.Now()
	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	after := time.Now()
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*started) != 1 {
		t.Fatal("no StateStarted event")
	}
	ts := (*started)[0].Timestamp
	if ts.Before(before) || ts.After(after) {
		t.Errorf("timestamp %v not in [%v, %v]", ts, before, after)
	}
}

// --------------------------------------------------------------------------
// ExtractCostFromResults edge cases
// --------------------------------------------------------------------------

func TestExtractCostFromResults_Empty(t *testing.T) {
	got := executors.ExtractCostFromResults(nil)
	if got != 0.0 {
		t.Errorf("empty results: got %v, want 0.0", got)
	}
	got = executors.ExtractCostFromResults([]map[string]any{})
	if got != 0.0 {
		t.Errorf("empty slice: got %v, want 0.0", got)
	}
}

func TestExtractCostFromResults_MissingKey(t *testing.T) {
	results := []map[string]any{
		{"type": "assistant", "message": "hello"},
	}
	got := executors.ExtractCostFromResults(results)
	if got != 0.0 {
		t.Errorf("missing key: got %v, want 0.0", got)
	}
}

func TestExtractCostFromResults_NonNumericValue(t *testing.T) {
	results := []map[string]any{
		{"total_cost_usd": "not-a-number"},
	}
	got := executors.ExtractCostFromResults(results)
	if got != 0.0 {
		t.Errorf("non-numeric value: got %v, want 0.0", got)
	}
}

func TestExtractCostFromResults_UsesLastEntry(t *testing.T) {
	results := []map[string]any{
		{"total_cost_usd": 0.01},
		{"total_cost_usd": 0.05},
	}
	got := executors.ExtractCostFromResults(results)
	if got != 0.05 {
		t.Errorf("should use last entry: got %v, want 0.05", got)
	}
}

func TestExtractCostFromResults_IntValue(t *testing.T) {
	results := []map[string]any{
		{"total_cost_usd": int(3)},
	}
	got := executors.ExtractCostFromResults(results)
	if got != 3.0 {
		t.Errorf("int value: got %v, want 3.0", got)
	}
}

// --------------------------------------------------------------------------
// Reminder loop detailed tests
// --------------------------------------------------------------------------

// TestMarkdownExecutor_ReminderEmitsIsReminderFlag verifies that on retry
// invocations the ClaudeInvocationStarted event carries IsReminder=true and
// the correct ReminderAttempt counter.
func TestMarkdownExecutor_ReminderEmitsIsReminderFlag(t *testing.T) {
	_, wfState := makeWorkflowWithPolicy(t)

	b := newBus()
	invocations, cancel := collectEvents[events.ClaudeInvocationStarted](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	callCount := 0
	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		callCount++
		if callCount == 1 {
			// First attempt: no transition
			return makeMockStream([]map[string]any{
				{"type": "content", "text": "No transition here"},
				{"total_cost_usd": 0.01},
			})
		}
		// Second attempt: valid transition
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"total_cost_usd": 0.02},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*invocations) != 2 {
		t.Fatalf("expected 2 ClaudeInvocationStarted events, got %d", len(*invocations))
	}

	first := (*invocations)[0]
	if first.IsReminder {
		t.Error("first invocation should not have IsReminder=true")
	}
	if first.ReminderAttempt != 0 {
		t.Errorf("first ReminderAttempt = %d, want 0", first.ReminderAttempt)
	}

	second := (*invocations)[1]
	if !second.IsReminder {
		t.Error("second invocation should have IsReminder=true")
	}
	if second.ReminderAttempt != 1 {
		t.Errorf("second ReminderAttempt = %d, want 1", second.ReminderAttempt)
	}
}

// TestMarkdownExecutor_CostAccumulatesAcrossReminders verifies that the
// StateCompleted.CostUSD reflects the sum of all reminder invocation costs,
// not just the last one.
func TestMarkdownExecutor_CostAccumulatesAcrossReminders(t *testing.T) {
	_, wfState := makeWorkflowWithPolicy(t)

	b := newBus()
	completed, cancel := collectEvents[events.StateCompleted](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	callCount := 0
	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		callCount++
		if callCount == 1 {
			return makeMockStream([]map[string]any{
				{"type": "content", "text": "No transition"},
				{"total_cost_usd": 0.10}, // first attempt cost
			})
		}
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"total_cost_usd": 0.20}, // second attempt cost
		})
	})
	defer executors.ResetInvokeStreamFn()

	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// CostUSD in result should be the sum of both invocations.
	if result.CostUSD < 0.29 || result.CostUSD > 0.31 {
		t.Errorf("result.CostUSD = %v, want ~0.30 (sum of both invocations)", result.CostUSD)
	}

	// StateCompleted event should also report the total state cost.
	if len(*completed) != 1 {
		t.Fatalf("expected 1 StateCompleted event, got %d", len(*completed))
	}
	ev := (*completed)[0]
	if ev.CostUSD < 0.29 || ev.CostUSD > 0.31 {
		t.Errorf("StateCompleted.CostUSD = %v, want ~0.30 (sum of both invocations)", ev.CostUSD)
	}

	// WorkflowState total should also be updated.
	if wfState.TotalCostUSD < 0.29 || wfState.TotalCostUSD > 0.31 {
		t.Errorf("TotalCostUSD = %v, want ~0.30", wfState.TotalCostUSD)
	}
}

// TestMarkdownExecutor_ReminderSuccessOnThirdAttempt verifies the executor
// succeeds when the transition is found on the third (final allowed) attempt.
func TestMarkdownExecutor_ReminderSuccessOnThirdAttempt(t *testing.T) {
	_, wfState := makeWorkflowWithPolicy(t)
	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	callCount := 0
	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		callCount++
		if callCount < 3 {
			return makeMockStream([]map[string]any{
				{"type": "content", "text": "No transition"},
				{"total_cost_usd": 0.01},
			})
		}
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<result>done</result>"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error on attempt 3: %v", err)
	}
	if result.Transition.Tag != "result" {
		t.Errorf("transition tag = %q, want result", result.Transition.Tag)
	}
	if callCount != 3 {
		t.Errorf("expected 3 invocations, got %d", callCount)
	}
}

// --------------------------------------------------------------------------
// Default model regression tests
// --------------------------------------------------------------------------

// TestMarkdownExecutor_DefaultModelIsSonnet verifies that when no model is
// specified in frontmatter or the execution context, the MarkdownExecutor
// passes "sonnet" to InvokeStream rather than leaving the model empty.
func TestMarkdownExecutor_DefaultModelIsSonnet(t *testing.T) {
	_, wfState := makeWorkflow(t)

	// No DefaultModel in the context (simulates no --model CLI flag and no config).
	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	var capturedModel string
	executors.SetInvokeStreamFn(func(_ context.Context, _ string, model, _, _ string, _ float64, _, _ bool, _ string) <-chan ccwrap.StreamItem {
		capturedModel = model
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"session_id": "sess-1", "total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if capturedModel != "sonnet" {
		t.Errorf("expected default model to be \"sonnet\", got %q", capturedModel)
	}
}

// TestMarkdownExecutor_CLIModelOverridesDefault verifies that DefaultModel in
// the ExecutionContext takes precedence over the built-in "sonnet" default.
func TestMarkdownExecutor_CLIModelOverridesDefault(t *testing.T) {
	_, wfState := makeWorkflow(t)

	execCtx := &executors.ExecutionContext{
		Bus:          newBus(),
		WorkflowID:   wfState.WorkflowID,
		DefaultModel: "opus",
	}

	var capturedModel string
	executors.SetInvokeStreamFn(func(_ context.Context, _ string, model, _, _ string, _ float64, _, _ bool, _ string) <-chan ccwrap.StreamItem {
		capturedModel = model
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"session_id": "sess-1", "total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if capturedModel != "opus" {
		t.Errorf("expected model to be \"opus\" (from DefaultModel), got %q", capturedModel)
	}
}

// --------------------------------------------------------------------------
// "user" message tool error tests (Gap 1 parity)
// --------------------------------------------------------------------------

// TestMarkdownExecutor_EmitsErrorEventOnToolError verifies that when the
// Claude stream contains a "user" message with a tool_result item that has
// is_error=true, an ErrorOccurred event with ErrorType=="ToolError" is emitted.
func TestMarkdownExecutor_EmitsErrorEventOnToolError(t *testing.T) {
	_, wfState := makeWorkflow(t)

	b := newBus()
	errorEvents, cancel := collectEvents[events.ErrorOccurred](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			// "user" message with a failing tool_result
			{
				"type": "user",
				"message": map[string]any{
					"content": []any{
						map[string]any{
							"type":     "tool_result",
							"is_error": true,
							"content":  "Permission denied: cannot write to /etc/hosts",
						},
					},
				},
			},
			// Still need a valid transition for Execute to succeed.
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*errorEvents) != 1 {
		t.Fatalf("got %d ErrorOccurred events, want 1", len(*errorEvents))
	}
	ev := (*errorEvents)[0]
	if ev.ErrorType != "ToolError" {
		t.Errorf("ErrorType = %q, want ToolError", ev.ErrorType)
	}
	if ev.IsRetryable {
		t.Error("tool error should not be retryable")
	}
	if !strings.Contains(ev.ErrorMessage, "Permission denied") {
		t.Errorf("error message should contain tool error text, got %q", ev.ErrorMessage)
	}
}

// TestMarkdownExecutor_ExtractsToolUseErrorTags verifies that <tool_use_error>
// XML tags are stripped from the tool error message, matching Python behaviour.
func TestMarkdownExecutor_ExtractsToolUseErrorTags(t *testing.T) {
	_, wfState := makeWorkflow(t)

	b := newBus()
	errorEvents, cancel := collectEvents[events.ErrorOccurred](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{
				"type": "user",
				"message": map[string]any{
					"content": []any{
						map[string]any{
							"type":     "tool_result",
							"is_error": true,
							"content":  "Error: <tool_use_error>file not found</tool_use_error>",
						},
					},
				},
			},
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*errorEvents) != 1 {
		t.Fatalf("got %d ErrorOccurred events, want 1", len(*errorEvents))
	}
	// The message should be extracted from between the tags.
	if (*errorEvents)[0].ErrorMessage != "file not found" {
		t.Errorf("expected extracted message %q, got %q", "file not found", (*errorEvents)[0].ErrorMessage)
	}
}

// TestMarkdownExecutor_NonErrorToolResultIgnored verifies that "user" messages
// with tool_result items where is_error=false do NOT emit ErrorOccurred events.
func TestMarkdownExecutor_NonErrorToolResultIgnored(t *testing.T) {
	_, wfState := makeWorkflow(t)

	b := newBus()
	errorEvents, cancel := collectEvents[events.ErrorOccurred](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{
				"type": "user",
				"message": map[string]any{
					"content": []any{
						map[string]any{
							"type":     "tool_result",
							"is_error": false,
							"content":  "File written successfully",
						},
					},
				},
			},
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*errorEvents) != 0 {
		t.Errorf("got %d ErrorOccurred events, want 0 for non-error tool result", len(*errorEvents))
	}
}

// --------------------------------------------------------------------------
// Budget exceeded termination test
// --------------------------------------------------------------------------

// TestMarkdownExecutor_BudgetExceededTerminatesWorkflow verifies that when
// TotalCostUSD exceeds BudgetUSD after an invocation, the executor returns
// a "result" transition containing a budget-exceeded message rather than
// continuing the workflow.
func TestMarkdownExecutor_BudgetExceededTerminatesWorkflow(t *testing.T) {
	_, wfState := makeWorkflow(t)
	wfState.BudgetUSD = 0.01 // tiny budget
	wfState.TotalCostUSD = 0.0

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "Working..."},
			{"total_cost_usd": 0.05}, // cost exceeds 0.01 budget
		})
	})
	defer executors.ResetInvokeStreamFn()

	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// A budget-exceeded workflow must return a "result" transition.
	if result.Transition.Tag != "result" {
		t.Errorf("expected result transition on budget exceeded, got tag=%q", result.Transition.Tag)
	}
	if !strings.Contains(strings.ToLower(result.Transition.Payload), "budget") {
		t.Errorf("expected budget mention in payload, got %q", result.Transition.Payload)
	}
}

// --------------------------------------------------------------------------
// asError is errors.As without the import (keeps test file self-contained).
// --------------------------------------------------------------------------

func asError[T error](err error, target *T) bool {
	if err == nil {
		return false
	}
	if te, ok := err.(T); ok {
		*target = te
		return true
	}
	return false
}
