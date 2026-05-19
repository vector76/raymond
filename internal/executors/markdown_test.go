package executors_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vector76/raymond/internal/backend"
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
// {{input}} and {{workflow_id}} are both substituted in the same prompt.
func TestMarkdownExecutor_WorkflowIDAndResultBothSubstituted(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "START.md"), "{{input}} in {{workflow_id}}")
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
// is replaced even when no {{input}} placeholder is present.
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

// TestMarkdownExecutor_AgentIDSubstitutedInBody verifies that {{agent_id}}
// in the prompt body is replaced with the agent's ID.
func TestMarkdownExecutor_AgentIDSubstitutedInBody(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "START.md"), "Agent: {{agent_id}}")
	write(t, filepath.Join(dir, "NEXT.md"), "next")

	ws := &wfstate.WorkflowState{
		WorkflowID: "wf-agent-test",
		ScopeDir:   dir,
		BudgetUSD:  10.0,
		Agents:     []wfstate.AgentState{{ID: "test-agent-42", CurrentState: "START.md", ScopeDir: dir, Stack: []wfstate.StackFrame{}}},
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

	if !strings.Contains(capturedPrompt, "test-agent-42") {
		t.Errorf("prompt = %q, want it to contain %q", capturedPrompt, "test-agent-42")
	}
	if strings.Contains(capturedPrompt, "{{agent_id}}") {
		t.Errorf("prompt still contains literal {{agent_id}}: %q", capturedPrompt)
	}
}

// TestMarkdownExecutor_AgentIDSubstitutedInImplicitInput verifies that
// {{agent_id}} in an implicit transition's input attribute is substituted.
func TestMarkdownExecutor_AgentIDSubstitutedInImplicitInput(t *testing.T) {
	dir := t.TempDir()

	frontmatter := "---\nallowed_transitions:\n  - { tag: goto, target: NEXT.md, input: \"id={{agent_id}}\" }\n---\n"
	write(t, filepath.Join(dir, "START.md"), frontmatter+"Process the task.")
	write(t, filepath.Join(dir, "NEXT.md"), "next")

	ws := &wfstate.WorkflowState{
		WorkflowID: "wf-agent-test",
		ScopeDir:   dir,
		BudgetUSD:  10.0,
		Agents:     []wfstate.AgentState{{ID: "test-agent-42", CurrentState: "START.md", ScopeDir: dir, Stack: []wfstate.StackFrame{}}},
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
	if got != "id=test-agent-42" {
		t.Errorf("input attribute = %q, want %q", got, "id=test-agent-42")
	}
}

// TestMarkdownExecutor_TaskFolderSubstitutedInBody verifies that {{task_folder}}
// in the prompt body is replaced with the agent's TaskFolder value.
func TestMarkdownExecutor_TaskFolderSubstitutedInBody(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "START.md"), "Output dir: {{task_folder}}")
	write(t, filepath.Join(dir, "NEXT.md"), "next")

	ws := &wfstate.WorkflowState{
		WorkflowID: "wf-tf-1",
		ScopeDir:   dir,
		BudgetUSD:  10.0,
		Agents: []wfstate.AgentState{{
			ID:           "main",
			CurrentState: "START.md",
			ScopeDir:     dir,
			TaskFolder:   "/output/main_task1",
			Stack:        []wfstate.StackFrame{},
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

	if !strings.Contains(capturedPrompt, "Output dir: /output/main_task1") {
		t.Errorf("prompt = %q, want it to contain %q", capturedPrompt, "Output dir: /output/main_task1")
	}
	if strings.Contains(capturedPrompt, "{{task_folder}}") {
		t.Errorf("prompt still contains literal {{task_folder}}: %q", capturedPrompt)
	}
}

// TestMarkdownExecutor_AskIDSubstitutedInImmediatelyFollowingState verifies
// that {{ask_id}} is substituted with the agent's PendingAskID when the
// state runs immediately after an ask resolves.
func TestMarkdownExecutor_AskIDSubstitutedInImmediatelyFollowingState(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "START.md"), "Resumed by input {{ask_id}}")
	write(t, filepath.Join(dir, "NEXT.md"), "next")

	ws := &wfstate.WorkflowState{
		WorkflowID: "wf-input-id",
		ScopeDir:   dir,
		BudgetUSD:  10.0,
		Agents: []wfstate.AgentState{{
			ID:             "main",
			CurrentState:   "START.md",
			ScopeDir:       dir,
			Stack:          []wfstate.StackFrame{},
			PendingAskID: "input-abc-123",
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

	if !strings.Contains(capturedPrompt, "Resumed by input input-abc-123") {
		t.Errorf("prompt = %q, want it to contain %q", capturedPrompt, "Resumed by input input-abc-123")
	}
	if strings.Contains(capturedPrompt, "{{ask_id}}") {
		t.Errorf("prompt still contains literal {{ask_id}}: %q", capturedPrompt)
	}
}

// TestMarkdownExecutor_PrintOutputEventEmitted verifies that a <print> tag in
// the assistant stream causes a PrintOutput event to be emitted on the bus with
// the correct content and agent ID, and that the real transition is unaffected.
func TestMarkdownExecutor_PrintOutputEventEmitted(t *testing.T) {
	_, wfState := makeWorkflow(t)

	b := newBus()
	printOutputs, cancel := collectEvents[events.PrintOutput](b)
	defer cancel()

	execCtx := &executors.ExecutionContext{Bus: b, WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string, bool) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "assistant", "message": map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": "<print>hello from print</print><goto>NEXT.md</goto>"},
				},
			}},
			{"session_id": "sess-print", "total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(*printOutputs) != 1 {
		t.Fatalf("got %d PrintOutput events, want 1", len(*printOutputs))
	}
	ev := (*printOutputs)[0]
	if ev.Content != "hello from print" {
		t.Errorf("Content = %q, want %q", ev.Content, "hello from print")
	}
	if ev.AgentID != "main" {
		t.Errorf("AgentID = %q, want main", ev.AgentID)
	}
	if result.Transition.Tag != "goto" || result.Transition.Target != "NEXT.md" {
		t.Errorf("unexpected transition: %+v", result.Transition)
	}
}

// TestMarkdownExecutor_PrintTagsInvisibleToTransitionParser verifies that
// <print> tags in the output text are not parsed as transitions — the
// ExecutionResult.Transition reflects only the real transition tag.
func TestMarkdownExecutor_PrintTagsInvisibleToTransitionParser(t *testing.T) {
	_, wfState := makeWorkflow(t)

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string, bool) <-chan ccwrap.StreamItem {
		return makeMockStream([]map[string]any{
			{"type": "assistant", "message": map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": "<print>side channel output</print><goto>NEXT.md</goto>"},
				},
			}},
			{"session_id": "sess-print2", "total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Transition.Tag != "goto" {
		t.Errorf("print tag must not appear as a transition; got tag=%q", result.Transition.Tag)
	}
	if result.Transition.Target != "NEXT.md" {
		t.Errorf("transition target = %q, want NEXT.md", result.Transition.Target)
	}
}

// TestMarkdownExecutor_CustomBackendIsUsed verifies that when execCtx.Backend
// is non-nil, MarkdownExecutor calls that backend's RunTurn rather than
// constructing a default Claude backend.
func TestMarkdownExecutor_CustomBackendIsUsed(t *testing.T) {
	_, wfState := makeWorkflow(t)

	var runTurnCalled bool
	stub := &markdownTestStubBackend{
		runTurnFn: func(_ context.Context, _ backend.TurnSpec, _ backend.Sink) (backend.TurnResult, error) {
			runTurnCalled = true
			return backend.TurnResult{
				OutputText: "<goto>NEXT.md</goto>",
				SessionID:  "stub-sess",
				CostUSD:    0.01,
			}, nil
		},
	}

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: wfState.WorkflowID, Backend: stub}
	_, err := executors.NewMarkdownExecutor().Execute(context.Background(), &wfState.Agents[0], wfState, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !runTurnCalled {
		t.Error("expected stub backend RunTurn to be called, but it was not")
	}
}

type markdownTestStubBackend struct {
	runTurnFn func(ctx context.Context, spec backend.TurnSpec, sink backend.Sink) (backend.TurnResult, error)
}

func (s *markdownTestStubBackend) RunTurn(ctx context.Context, spec backend.TurnSpec, sink backend.Sink) (backend.TurnResult, error) {
	return s.runTurnFn(ctx, spec, sink)
}

// TestMarkdownExecutor_AskIDUnsubstitutedWhenNotPending verifies that
// {{ask_id}} is left as a literal placeholder when PendingAskID is empty
// (i.e. the state is not the immediately-following state after an ask).
// This matches the missing-key behavior of RenderPrompt.
func TestMarkdownExecutor_AskIDUnsubstitutedWhenNotPending(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "START.md"), "No input here: {{ask_id}}")
	write(t, filepath.Join(dir, "NEXT.md"), "next")

	ws := &wfstate.WorkflowState{
		WorkflowID: "wf-input-id-empty",
		ScopeDir:   dir,
		BudgetUSD:  10.0,
		Agents: []wfstate.AgentState{{
			ID:           "main",
			CurrentState: "START.md",
			ScopeDir:     dir,
			Stack:        []wfstate.StackFrame{},
			// PendingAskID intentionally empty.
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

	if !strings.Contains(capturedPrompt, "{{ask_id}}") {
		t.Errorf("prompt = %q, want literal {{ask_id}} to remain", capturedPrompt)
	}
}

// TestMarkdownExecutor_ForceImplicit_IgnoresDifferentTag verifies that when
// force_implicit is set, the LLM emitting a tag that doesn't match the policy
// (which would normally be a policy violation) is ignored — the implicit
// transition fires from the policy.
func TestMarkdownExecutor_ForceImplicit_IgnoresDifferentTag(t *testing.T) {
	dir := t.TempDir()

	frontmatter := "---\nallowed_transitions:\n  - { tag: goto, target: NEXT.md }\nforce_implicit: true\n---\n"
	write(t, filepath.Join(dir, "START.md"), frontmatter+"Discuss workflows.")
	write(t, filepath.Join(dir, "NEXT.md"), "next")

	ws := &wfstate.WorkflowState{
		WorkflowID: "test-force-implicit",
		ScopeDir:   dir,
		BudgetUSD:  10.0,
		Agents:     []wfstate.AgentState{{ID: "main", CurrentState: "START.md", ScopeDir: dir, Stack: []wfstate.StackFrame{}}},
	}

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string, bool) <-chan ccwrap.StreamItem {
		// LLM emits a reset tag with a different target — would normally be a
		// policy violation. force_implicit suppresses parsing entirely.
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "Here's an example: <reset>SOMEWHERE_ELSE</reset>"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: ws.WorkflowID}
	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &ws.Agents[0], ws, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Transition.Tag != "goto" {
		t.Fatalf("transition tag = %q, want goto", result.Transition.Tag)
	}
	if result.Transition.Target != "NEXT.md" {
		t.Errorf("target = %q, want NEXT.md", result.Transition.Target)
	}
}

// TestMarkdownExecutor_ForceImplicit_IgnoresMultipleTags verifies that
// multiple competing tags in the LLM output don't cause a "multiple
// transitions" retry — force_implicit dispatches the policy transition
// without parsing.
func TestMarkdownExecutor_ForceImplicit_IgnoresMultipleTags(t *testing.T) {
	dir := t.TempDir()

	frontmatter := "---\nallowed_transitions:\n  - { tag: goto, target: NEXT.md }\nforce_implicit: true\n---\n"
	write(t, filepath.Join(dir, "START.md"), frontmatter+"Discuss workflows.")
	write(t, filepath.Join(dir, "NEXT.md"), "next")

	ws := &wfstate.WorkflowState{
		WorkflowID: "test-force-implicit-multi",
		ScopeDir:   dir,
		BudgetUSD:  10.0,
		Agents:     []wfstate.AgentState{{ID: "main", CurrentState: "START.md", ScopeDir: dir, Stack: []wfstate.StackFrame{}}},
	}

	var calls int32
	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string, bool) <-chan ccwrap.StreamItem {
		atomic.AddInt32(&calls, 1)
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "Examples: <goto>A</goto> and <goto>B</goto>"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: ws.WorkflowID}
	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &ws.Agents[0], ws, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Transition.Target != "NEXT.md" {
		t.Errorf("target = %q, want NEXT.md", result.Transition.Target)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("LLM invoked %d times, want 1 (no reminder retries)", got)
	}
}

// TestMarkdownExecutor_ForceImplicit_TemplatesInputAttribute verifies that
// {{input}} substitution in the implicit transition's input attribute still
// works when force_implicit is set.
func TestMarkdownExecutor_ForceImplicit_TemplatesInputAttribute(t *testing.T) {
	dir := t.TempDir()

	frontmatter := "---\nallowed_transitions:\n  - { tag: goto, target: NEXT.md, input: \"wrapped:{{input}}\" }\nforce_implicit: true\n---\n"
	write(t, filepath.Join(dir, "START.md"), frontmatter+"Process.")
	write(t, filepath.Join(dir, "NEXT.md"), "next")

	pending := "payload-value"
	ws := &wfstate.WorkflowState{
		WorkflowID: "test-force-implicit-tmpl",
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

	executors.SetInvokeStreamFn(func(context.Context, string, string, string, string, float64, bool, bool, string, bool) <-chan ccwrap.StreamItem {
		// LLM emits a stray <goto> tag; force_implicit causes it to be ignored.
		return makeMockStream([]map[string]any{
			{"type": "content", "text": "<goto>STRAY</goto>"},
			{"total_cost_usd": 0.01},
		})
	})
	defer executors.ResetInvokeStreamFn()

	execCtx := &executors.ExecutionContext{Bus: newBus(), WorkflowID: ws.WorkflowID}
	result, err := executors.NewMarkdownExecutor().Execute(context.Background(), &ws.Agents[0], ws, execCtx)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := result.Transition.Attributes["input"]; got != "wrapped:payload-value" {
		t.Errorf("input attribute = %q, want %q", got, "wrapped:payload-value")
	}
}
