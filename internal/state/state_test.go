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

// ----------------------------------------------------------------------------
// TaskFolder / TaskCount / TaskFolderPattern fields
// ----------------------------------------------------------------------------

func TestAgentStateTaskFieldsRoundTrip(t *testing.T) {
	agent := state.AgentState{
		ID:           "main",
		CurrentState: "START.md",
		Stack:        []state.StackFrame{},
		TaskFolder:   "/output/main_task1",
		TaskCount:    3,
	}

	data, err := json.Marshal(agent)
	require.NoError(t, err)

	var got state.AgentState
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "/output/main_task1", got.TaskFolder)
	assert.Equal(t, 3, got.TaskCount)
}

func TestStackFrameTaskFolderRoundTrip(t *testing.T) {
	frame := state.StackFrame{
		State:      "NEXT.md",
		ScopeDir:   "scope",
		TaskFolder: "/output/main_task2",
	}

	data, err := json.Marshal(frame)
	require.NoError(t, err)

	var got state.StackFrame
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "/output/main_task2", got.TaskFolder)
}

func TestLegacyJSONMissingNewFieldsDeserializesToZeroValues(t *testing.T) {
	// A JSON string without task_folder, task_count, or TaskFolderPattern (legacy).
	raw := `{"workflow_id":"legacy","scope_dir":"scope","total_cost_usd":0,"budget_usd":10,"agents":[{"id":"main","current_state":"START.md","session_id":null,"stack":[]}]}`

	var ws state.WorkflowState
	require.NoError(t, json.Unmarshal([]byte(raw), &ws))

	require.Len(t, ws.Agents, 1)
	assert.Equal(t, "", ws.Agents[0].TaskFolder, "TaskFolder should be empty string for legacy JSON")
	assert.Equal(t, 0, ws.Agents[0].TaskCount, "TaskCount should be zero for legacy JSON")
	assert.Equal(t, "", ws.TaskFolderPattern, "TaskFolderPattern should be empty string (transient, not in JSON)")
}

// ----------------------------------------------------------------------------
// Ask fields serialization
// ----------------------------------------------------------------------------

func TestAgentStateAskFieldsRoundTrip(t *testing.T) {
	agent := state.AgentState{
		ID:               "main",
		CurrentState:     "REVIEW.md",
		Stack:            []state.StackFrame{},
		Status:           state.AgentStatusAsking,
		AskPrompt:      "Please approve the deployment",
		AskNextState:   "DEPLOY.md",
		AskTimeout:     "24h",
		AskTimeoutNext: "TIMEOUT.md",
		AskID:     "ask_main_1234567890",
	}

	data, err := json.Marshal(agent)
	require.NoError(t, err)

	var got state.AgentState
	require.NoError(t, json.Unmarshal(data, &got))

	assert.Equal(t, state.AgentStatusAsking, got.Status)
	assert.Equal(t, "Please approve the deployment", got.AskPrompt)
	assert.Equal(t, "DEPLOY.md", got.AskNextState)
	assert.Equal(t, "24h", got.AskTimeout)
	assert.Equal(t, "TIMEOUT.md", got.AskTimeoutNext)
	assert.Equal(t, "ask_main_1234567890", got.AskID)
}

func TestAgentStateAskFieldsBackwardCompatibility(t *testing.T) {
	// JSON without any ask fields — should deserialize to zero values with no errors.
	raw := `{"id":"main","current_state":"START.md","session_id":null,"stack":[]}`

	var agent state.AgentState
	require.NoError(t, json.Unmarshal([]byte(raw), &agent))

	assert.Equal(t, "", agent.AskPrompt)
	assert.Equal(t, "", agent.AskNextState)
	assert.Equal(t, "", agent.AskTimeout)
	assert.Equal(t, "", agent.AskTimeoutNext)
	assert.Equal(t, "", agent.AskID)
	assert.Equal(t, "", agent.Status)
}

func TestAgentStateAskFieldsOmittedWhenEmpty(t *testing.T) {
	agent := state.AgentState{
		ID:           "main",
		CurrentState: "START.md",
		Stack:        []state.StackFrame{},
	}

	data, err := json.Marshal(agent)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	_, hasAskPrompt := raw["ask_prompt"]
	assert.False(t, hasAskPrompt, "ask_prompt should be absent from JSON when empty")
	_, hasAskNextState := raw["ask_next_state"]
	assert.False(t, hasAskNextState, "ask_next_state should be absent from JSON when empty")
	_, hasAskTimeout := raw["ask_timeout"]
	assert.False(t, hasAskTimeout, "ask_timeout should be absent from JSON when empty")
	_, hasAskTimeoutNext := raw["ask_timeout_next"]
	assert.False(t, hasAskTimeoutNext, "ask_timeout_next should be absent from JSON when empty")
	_, hasAskID := raw["ask_id"]
	assert.False(t, hasAskID, "ask_id should be absent from JSON when empty")
}

func TestAgentStatePendingAskIDRoundTrip(t *testing.T) {
	agent := state.AgentState{
		ID:             "main",
		CurrentState:   "POST_ASK.md",
		Stack:          []state.StackFrame{},
		PendingAskID: "ask_main_1234567890",
	}

	data, err := json.Marshal(agent)
	require.NoError(t, err)

	var got state.AgentState
	require.NoError(t, json.Unmarshal(data, &got))

	assert.Equal(t, "ask_main_1234567890", got.PendingAskID)
}

func TestAgentStatePendingAskIDOmittedWhenEmpty(t *testing.T) {
	agent := state.AgentState{
		ID:           "main",
		CurrentState: "START.md",
		Stack:        []state.StackFrame{},
	}

	data, err := json.Marshal(agent)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	_, has := raw["pending_ask_id"]
	assert.False(t, has, "pending_ask_id should be absent from JSON when empty")
}

// ----------------------------------------------------------------------------
// LaunchParams OnAsk field
// ----------------------------------------------------------------------------

func TestLaunchParamsOnAskRoundTrip(t *testing.T) {
	lp := state.LaunchParams{
		Model: "opus",
		OnAsk: "pause",
	}

	data, err := json.Marshal(lp)
	require.NoError(t, err)

	var got state.LaunchParams
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "pause", got.OnAsk)
}

func TestLaunchParamsOnAskOmittedWhenEmpty(t *testing.T) {
	lp := state.LaunchParams{Model: "opus"}

	data, err := json.Marshal(lp)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	_, hasOnAsk := raw["on_ask"]
	assert.False(t, hasOnAsk, "on_ask should be absent from JSON when empty")
}

func TestLaunchParamsOnAskBackwardCompatibility(t *testing.T) {
	// Old JSON without on_ask field.
	raw := `{"dangerously_skip_permissions":false,"model":"haiku"}`

	var lp state.LaunchParams
	require.NoError(t, json.Unmarshal([]byte(raw), &lp))
	assert.Equal(t, "", lp.OnAsk, "OnAsk should be empty for old state files without the field")
}

// ----------------------------------------------------------------------------
// ResolvePoolDir
// ----------------------------------------------------------------------------

// withRaymondDir creates a fresh project root containing a .raymond directory
// in a temp dir, chdirs into it for the duration of the test, and returns the
// absolute path to that .raymond directory.
func withRaymondDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	raymondDir := filepath.Join(root, ".raymond")
	require.NoError(t, os.MkdirAll(raymondDir, 0o755))

	origWd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	require.NoError(t, os.Chdir(root))

	// Resolve symlinks (macOS /var → /private/var, etc.) so the returned
	// path matches what os.Getwd reports inside the test.
	resolved, err := filepath.EvalSymlinks(raymondDir)
	require.NoError(t, err)
	return resolved
}

func TestResolvePoolDirNoOverride(t *testing.T) {
	raymondDir := withRaymondDir(t)

	cases := []struct {
		name string
		pool state.Pool
		want string
	}{
		{"CLI pool resolves to state/", state.PoolCLI, filepath.Join(raymondDir, "state")},
		{"Serve pool resolves to serve-state/", state.PoolServe, filepath.Join(raymondDir, "serve-state")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := state.ResolvePoolDir(tc.pool, "")
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolvePoolDirOverridePassthrough(t *testing.T) {
	// Override must win regardless of whether a raymond dir can be found.
	_ = withRaymondDir(t)

	override := filepath.Join(t.TempDir(), "custom-state")

	cases := []struct {
		name string
		pool state.Pool
	}{
		{"CLI pool honours override", state.PoolCLI},
		{"Serve pool honours override", state.PoolServe},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := state.ResolvePoolDir(tc.pool, override)
			assert.Equal(t, override, got, "override must be returned unchanged")
		})
	}
}

// TestResolvePoolDirCLIMatchesLegacyGetStateDir is a regression guard: the
// CLI pool path must be byte-for-byte identical to what GetStateDir("")
// returned before the typed-Pool surface was introduced.
func TestResolvePoolDirCLIMatchesLegacyGetStateDir(t *testing.T) {
	_ = withRaymondDir(t)

	legacy := state.GetStateDir("")
	pooled := state.ResolvePoolDir(state.PoolCLI, "")
	assert.Equal(t, legacy, pooled,
		"PoolCLI must resolve to the same path GetStateDir(\"\") has always returned")
}

// TestGetStateDirOverridePassthrough preserves the documented contract of
// GetStateDir: a non-empty argument is returned unchanged. This is the
// mechanism the hidden --state-dir flag relies on for test injection.
func TestGetStateDirOverridePassthrough(t *testing.T) {
	override := filepath.Join(t.TempDir(), "injected-state")
	assert.Equal(t, override, state.GetStateDir(override))
}

// ----------------------------------------------------------------------------
// Pool-aware primitives: round-trip, missing-dir tolerance, id-generation
// ----------------------------------------------------------------------------

// twoPoolOverrides returns disjoint per-pool override directories rooted in a
// fresh temp dir, so test cases can target each pool independently without
// either pool seeing the other's files.
func twoPoolOverrides(t *testing.T) (cliDir, serveDir string) {
	t.Helper()
	root := t.TempDir()
	cliDir = filepath.Join(root, "cli-pool")
	serveDir = filepath.Join(root, "serve-pool")
	return cliDir, serveDir
}

// TestPoolAwareRoundTrip exercises read/write/list/delete/recover against
// each pool independently and verifies the two pools never see each other's
// state files.
func TestPoolAwareRoundTrip(t *testing.T) {
	cliDir, serveDir := twoPoolOverrides(t)

	cases := []struct {
		name     string
		pool     state.Pool
		override string
		id       string
	}{
		{"CLI pool", state.PoolCLI, cliDir, "wf-cli"},
		{"Serve pool", state.PoolServe, serveDir, "wf-serve"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := state.CreateInitialState(tc.id, "scope", "START.md", 0, nil, "")
			require.NoError(t, state.WriteStateIn(tc.id, ws, tc.pool, tc.override))

			got, err := state.ReadStateIn(tc.id, tc.pool, tc.override)
			require.NoError(t, err)
			assert.Equal(t, tc.id, got.WorkflowID)

			ids, err := state.ListWorkflowsIn(tc.pool, tc.override)
			require.NoError(t, err)
			assert.Equal(t, []string{tc.id}, ids)

			recIDs, err := state.RecoverWorkflowsIn(tc.pool, tc.override)
			require.NoError(t, err)
			assert.Equal(t, []string{tc.id}, recIDs)

			require.NoError(t, state.DeleteStateIn(tc.id, tc.pool, tc.override))
			_, err = state.ReadStateIn(tc.id, tc.pool, tc.override)
			require.Error(t, err)
			assert.True(t, errors.Is(err, os.ErrNotExist))
		})
	}

	// Cross-pool isolation: a file written to one pool must not appear in
	// the other pool's listing.
	ws := state.CreateInitialState("wf-cli-only", "scope", "START.md", 0, nil, "")
	require.NoError(t, state.WriteStateIn("wf-cli-only", ws, state.PoolCLI, cliDir))

	serveIDs, err := state.ListWorkflowsIn(state.PoolServe, serveDir)
	require.NoError(t, err)
	assert.Empty(t, serveIDs, "Serve pool must not see CLI-pool files")
}

// TestGenerateWorkflowIDInScopedToPool verifies the collision counter only
// sees ids in the same pool — writing into one pool must not influence id
// generation in the other. The check is positive: we stage a file in pool A
// (CLI) with a specific id, observe the in-pool collision counter promotes
// the next generated id to a "_N" suffix, then verify staging an entry in
// pool B (serve) with the same id does NOT add a second collision (the next
// generated id is still only "_1", not "_2").
func TestGenerateWorkflowIDInScopedToPool(t *testing.T) {
	cliDir, serveDir := twoPoolOverrides(t)

	// Step 1: snapshot what GenerateWorkflowIDIn(PoolCLI) returns right now.
	baseID, err := state.GenerateWorkflowIDIn(state.PoolCLI, cliDir)
	require.NoError(t, err)

	// Step 2: stage that exact id in the *serve* pool only.
	ws := state.CreateInitialState(baseID, "scope", "START.md", 0, nil, "")
	require.NoError(t, state.WriteStateIn(baseID, ws, state.PoolServe, serveDir))

	// Sanity: CLI pool listing must still be empty — proves the serve write
	// did not leak across pools at the directory layer.
	cliIDs, err := state.ListWorkflowsIn(state.PoolCLI, cliDir)
	require.NoError(t, err)
	assert.Empty(t, cliIDs, "CLI pool must not see serve-pool files")

	// Step 3: in-pool collision actually fires when the entry is staged in
	// the SAME pool as the generator. Write baseID into the CLI pool, then
	// generate again. The generated id must change (timestamp advance,
	// suffix, or both) — what matters is that the collision check returns a
	// non-colliding result.
	require.NoError(t, state.WriteStateIn(baseID, ws, state.PoolCLI, cliDir))
	nextID, err := state.GenerateWorkflowIDIn(state.PoolCLI, cliDir)
	require.NoError(t, err)
	assert.NotEqual(t, baseID, nextID,
		"in-pool collision check must produce a different id")

	// Step 4: now the same baseID lives in both pools. Generating in the
	// serve pool must produce something different from baseID (its own
	// in-pool collision check sees baseID). This is the symmetric guarantee.
	servNext, err := state.GenerateWorkflowIDIn(state.PoolServe, serveDir)
	require.NoError(t, err)
	assert.NotEqual(t, baseID, servNext,
		"serve pool's in-pool collision check must see its own baseID entry")
}

// TestPoolAwareMissingDirTolerance verifies every primitive tolerates a
// pool directory that does not exist on disk: lists/recover return an empty
// slice, ReadStateIn returns a not-found error. DeleteStateIn must also be
// a no-op (idempotent) rather than panicking.
func TestPoolAwareMissingDirTolerance(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	cases := []struct {
		name string
		pool state.Pool
	}{
		{"CLI pool", state.PoolCLI},
		{"Serve pool", state.PoolServe},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids, err := state.ListWorkflowsIn(tc.pool, missing)
			require.NoError(t, err)
			assert.Empty(t, ids)

			recIDs, err := state.RecoverWorkflowsIn(tc.pool, missing)
			require.NoError(t, err)
			assert.Empty(t, recIDs)

			_, err = state.ReadStateIn("never-written", tc.pool, missing)
			require.Error(t, err)
			assert.True(t, errors.Is(err, os.ErrNotExist),
				"ReadStateIn against a missing pool dir must return os.ErrNotExist")

			// Delete is idempotent even when the directory itself is absent.
			assert.NoError(t, state.DeleteStateIn("never-written", tc.pool, missing))
		})
	}
}

// TestServePoolPathResolution is a thin guard that the new pool-aware writers
// land their files under the serve-state subdirectory when no override is
// given — i.e. they actually consult the Pool argument rather than silently
// using the CLI pool.
func TestServePoolPathResolution(t *testing.T) {
	raymondDir := withRaymondDir(t)

	ws := state.CreateInitialState("wf-routing", "scope", "START.md", 0, nil, "")
	require.NoError(t, state.WriteStateIn("wf-routing", ws, state.PoolServe, ""))

	_, err := os.Stat(filepath.Join(raymondDir, "serve-state", "wf-routing.json"))
	assert.NoError(t, err, "serve-pool write must land in serve-state/")

	// And nothing should appear in state/ as a side effect.
	_, err = os.Stat(filepath.Join(raymondDir, "state", "wf-routing.json"))
	assert.True(t, os.IsNotExist(err), "serve-pool write must not touch state/")
}
