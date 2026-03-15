package state_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vector76/raymond/internal/state"
)

// stateDir creates and returns a temporary state directory for testing.
func stateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".raymond", "state")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

// ----------------------------------------------------------------------------
// WriteState / ReadState round-trip
// ----------------------------------------------------------------------------

func TestWriteStateCreatesFile(t *testing.T) {
	dir := stateDir(t)
	ws := &state.WorkflowState{
		WorkflowID: "test-001",
		ScopeDir:   "workflows/test",
		Agents: []state.AgentState{
			{ID: "main", CurrentState: "START.md", Stack: []state.StackFrame{}},
		},
	}

	require.NoError(t, state.WriteState("test-001", ws, dir))

	_, err := os.Stat(filepath.Join(dir, "test-001.json"))
	require.NoError(t, err)
}

func TestWriteStateJSONIsValid(t *testing.T) {
	dir := stateDir(t)
	ws := &state.WorkflowState{
		WorkflowID:   "test-002",
		ScopeDir:     "workflows/test",
		TotalCostUSD: 0.0,
		BudgetUSD:    10.0,
		Agents: []state.AgentState{
			{ID: "main", CurrentState: "START.md", Stack: []state.StackFrame{}},
		},
	}

	require.NoError(t, state.WriteState("test-002", ws, dir))

	data, err := os.ReadFile(filepath.Join(dir, "test-002.json"))
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "test-002", got["workflow_id"])
	assert.Equal(t, "workflows/test", got["scope_dir"])
}

func TestReadStateReturnsWritten(t *testing.T) {
	dir := stateDir(t)
	sid := "sess-abc"
	ws := &state.WorkflowState{
		WorkflowID:   "test-003",
		ScopeDir:     "workflows/test",
		TotalCostUSD: 1.5,
		BudgetUSD:    10.0,
		Agents: []state.AgentState{
			{ID: "main", CurrentState: "NEXT.md", SessionID: &sid, Stack: []state.StackFrame{}},
		},
	}

	require.NoError(t, state.WriteState("test-003", ws, dir))

	got, err := state.ReadState("test-003", dir)
	require.NoError(t, err)

	assert.Equal(t, "test-003", got.WorkflowID)
	assert.Equal(t, "workflows/test", got.ScopeDir)
	assert.InDelta(t, 1.5, got.TotalCostUSD, 1e-9)
	require.Len(t, got.Agents, 1)
	assert.Equal(t, "main", got.Agents[0].ID)
	assert.Equal(t, "NEXT.md", got.Agents[0].CurrentState)
	require.NotNil(t, got.Agents[0].SessionID)
	assert.Equal(t, "sess-abc", *got.Agents[0].SessionID)
}

func TestReadStateMissingFileError(t *testing.T) {
	dir := stateDir(t)
	_, err := state.ReadState("nonexistent", dir)
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestReadStateMalformedJSON(t *testing.T) {
	dir := stateDir(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "bad.json"),
		[]byte("{not valid json}"), 0o644,
	))

	_, err := state.ReadState("bad", dir)
	require.Error(t, err)
	var sfe *state.StateFileError
	assert.True(t, errors.As(err, &sfe))
}

// ----------------------------------------------------------------------------
// WriteState atomicity: temp file cleaned up on success
// ----------------------------------------------------------------------------

func TestWriteStateNoTempFilesLeft(t *testing.T) {
	dir := stateDir(t)
	ws := &state.WorkflowState{WorkflowID: "atomic", Agents: []state.AgentState{}}
	require.NoError(t, state.WriteState("atomic", ws, dir))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), ".tmp"),
			"temp file left behind: %s", e.Name())
	}
}

func TestWriteStateCreatesDirectoryIfMissing(t *testing.T) {
	// Pass a dir that doesn't exist yet — WriteState must create it.
	dir := filepath.Join(t.TempDir(), "new", "state")
	ws := &state.WorkflowState{WorkflowID: "mkdir-test", Agents: []state.AgentState{}}
	require.NoError(t, state.WriteState("mkdir-test", ws, dir))

	_, err := os.Stat(filepath.Join(dir, "mkdir-test.json"))
	require.NoError(t, err)
}

// ----------------------------------------------------------------------------
// DeleteState
// ----------------------------------------------------------------------------

func TestDeleteStateRemovesFile(t *testing.T) {
	dir := stateDir(t)
	ws := &state.WorkflowState{WorkflowID: "del-1", Agents: []state.AgentState{}}
	require.NoError(t, state.WriteState("del-1", ws, dir))

	require.NoError(t, state.DeleteState("del-1", dir))

	_, err := os.Stat(filepath.Join(dir, "del-1.json"))
	assert.True(t, os.IsNotExist(err))
}

func TestDeleteStateMissingFileIsNoop(t *testing.T) {
	dir := stateDir(t)
	// Should not error when file doesn't exist.
	assert.NoError(t, state.DeleteState("does-not-exist", dir))
}

// ----------------------------------------------------------------------------
// ListWorkflows
// ----------------------------------------------------------------------------

func TestListWorkflowsReturnsIDs(t *testing.T) {
	dir := stateDir(t)

	for _, id := range []string{"wf-1", "wf-2", "wf-3"} {
		ws := &state.WorkflowState{WorkflowID: id, Agents: []state.AgentState{}}
		require.NoError(t, state.WriteState(id, ws, dir))
	}
	// Non-JSON file — should be ignored.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ignore.txt"), []byte("x"), 0o644))

	ids, err := state.ListWorkflows(dir)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"wf-1", "wf-2", "wf-3"}, ids)
}

func TestListWorkflowsEmptyDir(t *testing.T) {
	dir := stateDir(t)
	ids, err := state.ListWorkflows(dir)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestListWorkflowsNonexistentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	ids, err := state.ListWorkflows(dir)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

// ----------------------------------------------------------------------------
// CreateInitialState
// ----------------------------------------------------------------------------

func TestCreateInitialStateBasic(t *testing.T) {
	ws := state.CreateInitialState("wf-001", "workflows/test", "START.md", 10.0, nil, "")

	assert.Equal(t, "wf-001", ws.WorkflowID)
	assert.Equal(t, "workflows/test", ws.ScopeDir)
	assert.Equal(t, 10.0, ws.BudgetUSD)
	assert.Equal(t, 0.0, ws.TotalCostUSD)
	require.Len(t, ws.Agents, 1)

	agent := ws.Agents[0]
	assert.Equal(t, "main", agent.ID)
	assert.Equal(t, "START.md", agent.CurrentState)
	assert.Nil(t, agent.SessionID)
	assert.Equal(t, []state.StackFrame{}, agent.Stack)
	assert.Nil(t, agent.PendingResult)
}

func TestCreateInitialStatePopulatesAgentScopeDir(t *testing.T) {
	ws := state.CreateInitialState("wf-scopedir", "workflows/myapp", "START.md", 10.0, nil, "")
	require.Len(t, ws.Agents, 1)
	assert.Equal(t, "workflows/myapp", ws.Agents[0].ScopeDir,
		"agent ScopeDir should be populated from the scopeDir argument")
}

func TestCreateInitialStateWithInitialInput(t *testing.T) {
	input := "hello there"
	ws := state.CreateInitialState("wf-002", "workflows/test", "START.md", 10.0, &input, "")

	require.NotNil(t, ws.Agents[0].PendingResult)
	assert.Equal(t, "hello there", *ws.Agents[0].PendingResult)
}

func TestCreateInitialStateWithEmptyStringInput(t *testing.T) {
	input := ""
	ws := state.CreateInitialState("wf-003", "workflows/test", "START.md", 10.0, &input, "")

	require.NotNil(t, ws.Agents[0].PendingResult)
	assert.Equal(t, "", *ws.Agents[0].PendingResult)
}

func TestCreateInitialStateWithNilInputHasNoPendingResult(t *testing.T) {
	ws := state.CreateInitialState("wf-004", "workflows/test", "START.md", 10.0, nil, "")
	assert.Nil(t, ws.Agents[0].PendingResult)
}

func TestCreateInitialStateStackSerializesAsArray(t *testing.T) {
	ws := state.CreateInitialState("wf-005", "workflows/test", "START.md", 10.0, nil, "")

	data, err := json.Marshal(ws)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	agents := raw["agents"].([]any)
	require.Len(t, agents, 1)
	agent := agents[0].(map[string]any)

	// stack must be [] not null
	stack, ok := agent["stack"]
	assert.True(t, ok, "stack field must be present")
	assert.IsType(t, []any{}, stack)

	// session_id must be null
	_, hasSessionID := agent["session_id"]
	assert.True(t, hasSessionID, "session_id field must be present")
	assert.Nil(t, agent["session_id"])

	// pending_result must be absent
	_, hasPR := agent["pending_result"]
	assert.False(t, hasPR, "pending_result must be absent when nil")
}

// ----------------------------------------------------------------------------
// GenerateWorkflowID
// ----------------------------------------------------------------------------

func TestGenerateWorkflowIDFormat(t *testing.T) {
	dir := stateDir(t)
	id, err := state.GenerateWorkflowID(dir)
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(id, "workflow_"),
		"expected prefix workflow_, got %q", id)
	// Format: workflow_YYYY-MM-DD_HH-MM-SS-ffffff
	parts := strings.SplitN(id, "_", 2)
	require.Len(t, parts, 2)
	rest := parts[1] // YYYY-MM-DD_HH-MM-SS-ffffff
	assert.Len(t, rest, len("2006-01-02_15-04-05-000000"))
}

func TestGenerateWorkflowIDUnique(t *testing.T) {
	dir := stateDir(t)
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		id, err := state.GenerateWorkflowID(dir)
		require.NoError(t, err)
		assert.False(t, seen[id], "duplicate ID generated: %s", id)
		seen[id] = true
		// Simulate creating the file so next call sees it.
		ws := &state.WorkflowState{WorkflowID: id, Agents: []state.AgentState{}}
		require.NoError(t, state.WriteState(id, ws, dir))
	}
}

func TestGenerateWorkflowIDCollisionResolution(t *testing.T) {
	dir := stateDir(t)

	// Pre-create a file that would collide with the generated ID.
	// We do this by generating an ID, then making GenerateWorkflowID produce
	// the same base timestamp by having it resolve the collision.
	id1, err := state.GenerateWorkflowID(dir)
	require.NoError(t, err)

	// Place the generated ID in the state dir.
	ws := &state.WorkflowState{WorkflowID: id1, Agents: []state.AgentState{}}
	require.NoError(t, state.WriteState(id1, ws, dir))

	// GenerateWorkflowID with existing entries should produce a different ID.
	id2, err := state.GenerateWorkflowID(dir)
	require.NoError(t, err)
	assert.NotEqual(t, id1, id2)
}

// ----------------------------------------------------------------------------
// RecoverWorkflows
// ----------------------------------------------------------------------------

func TestRecoverWorkflowsFindsInProgress(t *testing.T) {
	dir := stateDir(t)

	// In-progress: has agents.
	ws1 := state.CreateInitialState("wf-active", "scope", "START.md", 10.0, nil, "")
	require.NoError(t, state.WriteState("wf-active", ws1, dir))

	// Completed: empty agents array.
	ws2 := &state.WorkflowState{WorkflowID: "wf-done", Agents: []state.AgentState{}}
	require.NoError(t, state.WriteState("wf-done", ws2, dir))

	ids, err := state.RecoverWorkflows(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"wf-active"}, ids)
}

func TestRecoverWorkflowsEmptyDir(t *testing.T) {
	dir := stateDir(t)
	ids, err := state.RecoverWorkflows(dir)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestRecoverWorkflowsNonexistentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no-such-dir")
	ids, err := state.RecoverWorkflows(dir)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestRecoverWorkflowsSkipsMalformed(t *testing.T) {
	dir := stateDir(t)

	// Good file.
	ws := state.CreateInitialState("good-wf", "scope", "START.md", 10.0, nil, "")
	require.NoError(t, state.WriteState("good-wf", ws, dir))

	// Malformed JSON — should be skipped, not cause an error.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "bad.json"),
		[]byte("{not json}"), 0o644,
	))

	ids, err := state.RecoverWorkflows(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"good-wf"}, ids)
}

// ----------------------------------------------------------------------------
// Session ID null handling round-trip
// ----------------------------------------------------------------------------

func TestSessionIDNullRoundTrip(t *testing.T) {
	dir := stateDir(t)
	ws := state.CreateInitialState("sid-null", "scope", "START.md", 10.0, nil, "")

	require.NoError(t, state.WriteState("sid-null", ws, dir))

	data, err := os.ReadFile(filepath.Join(dir, "sid-null.json"))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	agents := raw["agents"].([]any)
	agent := agents[0].(map[string]any)
	assert.Nil(t, agent["session_id"], "session_id should be JSON null when unset")

	got, err := state.ReadState("sid-null", dir)
	require.NoError(t, err)
	assert.Nil(t, got.Agents[0].SessionID)
}

func TestSessionIDNonNullRoundTrip(t *testing.T) {
	dir := stateDir(t)
	sid := "ses-xyz"
	ws := state.CreateInitialState("sid-set", "scope", "START.md", 10.0, nil, "")
	ws.Agents[0].SessionID = &sid

	require.NoError(t, state.WriteState("sid-set", ws, dir))

	got, err := state.ReadState("sid-set", dir)
	require.NoError(t, err)
	require.NotNil(t, got.Agents[0].SessionID)
	assert.Equal(t, "ses-xyz", *got.Agents[0].SessionID)
}

// ----------------------------------------------------------------------------
// LaunchParams persistence
// ----------------------------------------------------------------------------

func TestCreateInitialState_WithLaunchParams(t *testing.T) {
	lp := &state.LaunchParams{
		DangerouslySkipPermissions: true,
		Model:                      "opus",
		Effort:                     "high",
		Timeout:                    300.0,
	}
	ws := state.CreateInitialState("lp-test", "scope", "START.md", 10.0, nil, "", lp)

	require.NotNil(t, ws.LaunchParams)
	assert.Equal(t, true, ws.LaunchParams.DangerouslySkipPermissions)
	assert.Equal(t, "opus", ws.LaunchParams.Model)
	assert.Equal(t, "high", ws.LaunchParams.Effort)
	assert.Equal(t, 300.0, ws.LaunchParams.Timeout)
}

func TestCreateInitialState_WithoutLaunchParams(t *testing.T) {
	ws := state.CreateInitialState("lp-nil", "scope", "START.md", 10.0, nil, "")
	assert.Nil(t, ws.LaunchParams, "LaunchParams should be nil when not provided")
}

func TestCreateInitialState_NilLaunchParamsOmitted(t *testing.T) {
	ws := state.CreateInitialState("lp-explicit-nil", "scope", "START.md", 10.0, nil, "", nil)
	assert.Nil(t, ws.LaunchParams, "LaunchParams should be nil when explicitly nil is passed")
}

func TestLaunchParamsRoundTrip(t *testing.T) {
	dir := stateDir(t)
	lp := &state.LaunchParams{
		DangerouslySkipPermissions: true,
		Model:                      "haiku",
		Effort:                     "low",
		Timeout:                    120.0,
	}
	ws := state.CreateInitialState("lp-rtrip", "scope", "START.md", 10.0, nil, "", lp)
	require.NoError(t, state.WriteState("lp-rtrip", ws, dir))

	got, err := state.ReadState("lp-rtrip", dir)
	require.NoError(t, err)
	require.NotNil(t, got.LaunchParams)
	assert.Equal(t, true, got.LaunchParams.DangerouslySkipPermissions)
	assert.Equal(t, "haiku", got.LaunchParams.Model)
	assert.Equal(t, "low", got.LaunchParams.Effort)
	assert.Equal(t, 120.0, got.LaunchParams.Timeout)
}

func TestLaunchParamsAbsentInOldStateFiles(t *testing.T) {
	// Simulate an old state file that has no launch_params field.
	dir := stateDir(t)
	raw := `{"workflow_id":"old-wf","scope_dir":"scope","total_cost_usd":0,"budget_usd":10,"agents":[]}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old-wf.json"), []byte(raw), 0o644))

	got, err := state.ReadState("old-wf", dir)
	require.NoError(t, err)
	assert.Nil(t, got.LaunchParams, "LaunchParams should be nil for old state files without the field")
}

// ----------------------------------------------------------------------------
// ScopeDir migration (workflow-level → per-agent)
// ----------------------------------------------------------------------------

func TestScopeDirMigrationPreMigrationJSON(t *testing.T) {
	// Pre-migration: workflow has scope_dir, agents do not.
	dir := stateDir(t)
	raw := `{
		"workflow_id": "pre-mig",
		"scope_dir": "workflows/myapp",
		"total_cost_usd": 0,
		"budget_usd": 10,
		"agents": [
			{"id": "main", "current_state": "START.md", "session_id": null, "stack": []},
			{"id": "worker", "current_state": "WORK.md", "session_id": null, "stack": []}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pre-mig.json"), []byte(raw), 0o644))

	got, err := state.ReadState("pre-mig", dir)
	require.NoError(t, err)
	require.Len(t, got.Agents, 2)
	assert.Equal(t, "workflows/myapp", got.Agents[0].ScopeDir, "main agent should inherit workflow ScopeDir")
	assert.Equal(t, "workflows/myapp", got.Agents[1].ScopeDir, "worker agent should inherit workflow ScopeDir")
}

func TestScopeDirMigrationPostMigrationJSON(t *testing.T) {
	// Post-migration: agents already have their own scope_dir — round-trips cleanly.
	dir := stateDir(t)
	ws := &state.WorkflowState{
		WorkflowID: "post-mig",
		ScopeDir:   "workflows/myapp",
		Agents: []state.AgentState{
			{ID: "main", CurrentState: "START.md", Stack: []state.StackFrame{}, ScopeDir: "workflows/myapp"},
		},
	}
	require.NoError(t, state.WriteState("post-mig", ws, dir))

	got, err := state.ReadState("post-mig", dir)
	require.NoError(t, err)
	require.Len(t, got.Agents, 1)
	assert.Equal(t, "workflows/myapp", got.Agents[0].ScopeDir, "agent ScopeDir should round-trip unchanged")
}

func TestScopeDirMigrationDoesNotOverwriteExistingAgentScopeDir(t *testing.T) {
	// An agent with its own scope_dir must not be overwritten by the workflow-level value.
	dir := stateDir(t)
	raw := `{
		"workflow_id": "no-overwrite",
		"scope_dir": "workflows/default",
		"total_cost_usd": 0,
		"budget_usd": 10,
		"agents": [
			{"id": "main", "current_state": "START.md", "session_id": null, "stack": [], "scope_dir": "workflows/custom"},
			{"id": "worker", "current_state": "WORK.md", "session_id": null, "stack": []}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "no-overwrite.json"), []byte(raw), 0o644))

	got, err := state.ReadState("no-overwrite", dir)
	require.NoError(t, err)
	require.Len(t, got.Agents, 2)
	assert.Equal(t, "workflows/custom", got.Agents[0].ScopeDir, "agent with its own ScopeDir must not be overwritten")
	assert.Equal(t, "workflows/default", got.Agents[1].ScopeDir, "agent without ScopeDir should inherit workflow value")
}

// ----------------------------------------------------------------------------
// ScopeURL field
// ----------------------------------------------------------------------------

func TestScopeURLRoundTripAgentState(t *testing.T) {
	dir := stateDir(t)
	ws := state.CreateInitialState("su-agent", "scope", "START.md", 10.0, nil, "https://example.com/workflow_abc.zip")
	require.NoError(t, state.WriteState("su-agent", ws, dir))

	got, err := state.ReadState("su-agent", dir)
	require.NoError(t, err)
	require.Len(t, got.Agents, 1)
	assert.Equal(t, "https://example.com/workflow_abc.zip", got.Agents[0].ScopeURL)
}

func TestScopeURLEmptyOmittedFromJSON(t *testing.T) {
	dir := stateDir(t)
	ws := state.CreateInitialState("su-empty", "scope", "START.md", 10.0, nil, "")
	require.NoError(t, state.WriteState("su-empty", ws, dir))

	data, err := os.ReadFile(filepath.Join(dir, "su-empty.json"))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	agents := raw["agents"].([]any)
	agent := agents[0].(map[string]any)
	_, hasScopeURL := agent["scope_url"]
	assert.False(t, hasScopeURL, "scope_url should be absent from JSON when empty")
}

func TestScopeURLAbsentInOldJSONDeserializesAsEmpty(t *testing.T) {
	dir := stateDir(t)
	raw := `{"workflow_id":"su-old","scope_dir":"scope","total_cost_usd":0,"budget_usd":10,"agents":[{"id":"main","current_state":"START.md","session_id":null,"stack":[],"scope_dir":"scope"}]}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "su-old.json"), []byte(raw), 0o644))

	got, err := state.ReadState("su-old", dir)
	require.NoError(t, err)
	require.Len(t, got.Agents, 1)
	assert.Equal(t, "", got.Agents[0].ScopeURL, "ScopeURL should be empty for old state files without scope_url")
}

func TestScopeURLRoundTripStackFrame(t *testing.T) {
	frame := state.StackFrame{
		Session:      nil,
		State:        "NEXT.md",
		ScopeDir:     "scope",
		NestingDepth: 1,
		ScopeURL:     "https://example.com/workflow_abc.zip",
	}

	data, err := json.Marshal(frame)
	require.NoError(t, err)

	var got state.StackFrame
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "https://example.com/workflow_abc.zip", got.ScopeURL)
}

func TestScopeURLEmptyOmittedFromStackFrameJSON(t *testing.T) {
	frame := state.StackFrame{
		State:    "NEXT.md",
		ScopeDir: "scope",
	}

	data, err := json.Marshal(frame)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	_, hasScopeURL := raw["scope_url"]
	assert.False(t, hasScopeURL, "scope_url should be absent from JSON when empty")
}
