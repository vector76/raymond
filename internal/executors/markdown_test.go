package executors_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vector76/raymond/internal/ccwrap"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/executors"
	wfstate "github.com/vector76/raymond/internal/state"
)

// TestMarkdownExecutor_StateCompleted_InputTokens checks that InputTokens is
// populated from the usage field of the stream results.
func TestMarkdownExecutor_StateCompleted_InputTokens(t *testing.T) {
	_, wfState := makeWorkflow(t)

	b := newBus()
	completed, cancel := collectEvents[events.StateCompleted](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string, bool) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "Output\n<goto>NEXT.md</goto>"},
			{
				"session_id":     "sess-tok",
				"total_cost_usd": 0.05,
				"usage": map[string]any{
					"input_tokens":                float64(100),
					"cache_read_input_tokens":     float64(50),
					"cache_creation_input_tokens": float64(25),
				},
			},
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
	if ev.InputTokens == nil {
		t.Fatal("InputTokens is nil, want non-nil")
	}
	// Expected sum: 100 + 50 + 25 = 175
	if *ev.InputTokens != 175 {
		t.Errorf("InputTokens = %d, want 175", *ev.InputTokens)
	}
}

// TestMarkdownExecutor_StateCompleted_InputTokens_NoUsage checks that
// InputTokens is nil when the stream results have no "usage" field.
func TestMarkdownExecutor_StateCompleted_InputTokens_NoUsage(t *testing.T) {
	_, wfState := makeWorkflow(t)

	b := newBus()
	completed, cancel := collectEvents[events.StateCompleted](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string, bool) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "Output\n<goto>NEXT.md</goto>"},
			{"session_id": "sess-nousage", "total_cost_usd": 0.01},
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
	if ev.InputTokens != nil {
		t.Errorf("InputTokens = %d, want nil", *ev.InputTokens)
	}
}

// TestMarkdownExecutor_StateCompleted_InputTokens_LastInvocationOnly checks
// that InputTokens reflects only the last invocation when the reminder loop
// fires multiple times (not an accumulated total).
func TestMarkdownExecutor_StateCompleted_InputTokens_LastInvocationOnly(t *testing.T) {
	_, wfState := makeWorkflowWithPolicy(t)

	b := newBus()
	completed, cancel := collectEvents[events.StateCompleted](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	var callCount atomic.Int32
	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string, bool) <-chan ccwrap.StreamItem {
		n := callCount.Add(1)
		if n == 1 {
			// First call: no transition tag → triggers reminder loop retry.
			// Provide usage so we can verify it does NOT appear in final event.
			return makeMockStream([]map[string]any{
				{"type": "content", "text": "Thinking..."},
				{
					"session_id":     "sess-first",
					"total_cost_usd": 0.03,
					"usage": map[string]any{
						"input_tokens": float64(200),
					},
				},
			})
		}
		// Second call: has transition → exits loop.
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{
				"session_id":     "sess-second",
				"total_cost_usd": 0.02,
				"usage": map[string]any{
					"input_tokens": float64(80),
				},
			},
		})
	})
	defer executors.ResetInvokeStreamFn()

	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if callCount.Load() < 2 {
		t.Fatalf("expected at least 2 invocations (reminder loop), got %d", callCount.Load())
	}

	if len(*completed) != 1 {
		t.Fatalf("got %d StateCompleted events, want 1", len(*completed))
	}
	ev := (*completed)[0]
	if ev.InputTokens == nil {
		t.Fatal("InputTokens is nil, want non-nil")
	}
	// Should reflect last invocation (80), not accumulated (200+80=280).
	if *ev.InputTokens != 80 {
		t.Errorf("InputTokens = %d, want 80 (last invocation only)", *ev.InputTokens)
	}
}

// TestMarkdownExecutor_WorkflowIDSubstitutedInBody verifies that {{workflow_id}}
// in the prompt body is replaced with the WorkflowState's WorkflowID.
func TestMarkdownExecutor_WorkflowIDSubstitutedInBody(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "START.md"), "Task ID: {{workflow_id}}")
	write(t, filepath.Join(dir, "NEXT.md"), "next")

	ws := &wfstate.WorkflowState{
		WorkflowID: "wf-abc-123",
		ScopeDir:   dir,
		BudgetUSD:  10.0,
		Agents:     []wfstate.AgentState{{ID: "main", CurrentState: "START.md", ScopeDir: dir, Stack: []wfstate.StackFrame{}}},
	}

	var capturedPrompt string
	executors.SetInvokeStreamFn(func(_ context.Context, prompt string, _ string, _ string, _ string, _ float64, _ bool, _ bool, _ string, _ bool) <-chan ccwrap.StreamItem {
		capturedPrompt = prompt
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: ws.WorkflowID}
	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &ws.Agents[0], ws, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if !strings.Contains(capturedPrompt, "Task ID: wf-abc-123") {
		t.Errorf("prompt = %q, want it to contain %q", capturedPrompt, "Task ID: wf-abc-123")
	}
	if strings.Contains(capturedPrompt, "{{workflow_id}}") {
		t.Errorf("prompt still contains literal {{workflow_id}}: %q", capturedPrompt)
	}
}

// TestMarkdownExecutor_WorkflowIDSubstitutedInImplicitInput verifies that
// {{workflow_id}} in an implicit transition's input attribute is substituted.
func TestMarkdownExecutor_WorkflowIDSubstitutedInImplicitInput(t *testing.T) {
	dir := t.TempDir()

	frontmatter := "---\nallowed_transitions:\n  - { tag: goto, target: NEXT.md, input: \"wf={{workflow_id}}\" }\n---\n"
	write(t, filepath.Join(dir, "START.md"), frontmatter+"Process the workflow.")
	write(t, filepath.Join(dir, "NEXT.md"), "next")

	ws := &wfstate.WorkflowState{
		WorkflowID: "wf-abc-123",
		ScopeDir:   dir,
		BudgetUSD:  10.0,
		Agents:     []wfstate.AgentState{{ID: "main", CurrentState: "START.md", ScopeDir: dir, Stack: []wfstate.StackFrame{}}},
	}

	executors.SetInvokeStreamFn(func(_ context.Context, _ string, _ string, _ string, _ string, _ float64, _ bool, _ bool, _ string, _ bool) <-chan ccwrap.StreamItem {
		// No transition tag → implicit transition fires.
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "Analysis complete"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: ws.WorkflowID}
	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &ws.Agents[0], ws, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	got := result.Transition.Attributes["input"]
	if got != "wf=wf-abc-123" {
		t.Errorf("input attribute = %q, want %q", got, "wf=wf-abc-123")
	}
}

// TestMarkdownExecutor_WorkflowIDAndResultBothSubstituted verifies that
// {{result}} and {{workflow_id}} are both substituted in the same prompt.
func TestMarkdownExecutor_WorkflowIDAndResultBothSubstituted(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "START.md"), "{{result}} in {{workflow_id}}")
	write(t, filepath.Join(dir, "NEXT.md"), "next")

	pending := "the-result"
	ws := &wfstate.WorkflowState{
		WorkflowID: "wf-xyz",
		ScopeDir:   dir,
		BudgetUSD:  10.0,
		Agents: []wfstate.AgentState{{
			ID:            "main",
			CurrentState:  "START.md",
			ScopeDir:      dir,
			Stack:         []wfstate.StackFrame{},
			PendingResult: &pending,
		}},
	}

	var capturedPrompt string
	executors.SetInvokeStreamFn(func(_ context.Context, prompt string, _ string, _ string, _ string, _ float64, _ bool, _ bool, _ string, _ bool) <-chan ccwrap.StreamItem {
		capturedPrompt = prompt
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: ws.WorkflowID}
	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &ws.Agents[0], ws, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if !strings.Contains(capturedPrompt, "the-result in wf-xyz") {
		t.Errorf("prompt = %q, want it to contain %q", capturedPrompt, "the-result in wf-xyz")
	}
}

// TestMarkdownExecutor_WorkflowIDAlwaysSubstituted verifies that {{workflow_id}}
// is replaced even when no {{result}} placeholder is present.
func TestMarkdownExecutor_WorkflowIDAlwaysSubstituted(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "START.md"), "Run workflow {{workflow_id}} now.")
	write(t, filepath.Join(dir, "NEXT.md"), "next")

	ws := &wfstate.WorkflowState{
		WorkflowID: "wf-no-result",
		ScopeDir:   dir,
		BudgetUSD:  10.0,
		Agents:     []wfstate.AgentState{{ID: "main", CurrentState: "START.md", ScopeDir: dir, Stack: []wfstate.StackFrame{}}},
	}

	var capturedPrompt string
	executors.SetInvokeStreamFn(func(_ context.Context, prompt string, _ string, _ string, _ string, _ float64, _ bool, _ bool, _ string, _ bool) <-chan ccwrap.StreamItem {
		capturedPrompt = prompt
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>NEXT.md</goto>"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: ws.WorkflowID}
	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &ws.Agents[0], ws, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if strings.Contains(capturedPrompt, "{{workflow_id}}") {
		t.Errorf("prompt still contains literal {{workflow_id}}: %q", capturedPrompt)
	}
	if !strings.Contains(capturedPrompt, "wf-no-result") {
		t.Errorf("prompt = %q, want it to contain %q", capturedPrompt, "wf-no-result")
	}
}
