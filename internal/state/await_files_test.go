package state_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/state"
)

// ----------------------------------------------------------------------------
// File-affordance and resolved-input persistence on AgentState / WorkflowState
// ----------------------------------------------------------------------------

func TestAgentStateAwaitFileAffordanceRoundTrip(t *testing.T) {
	dir := stateDir(t)

	fa := &parsing.FileAffordance{
		Mode: parsing.ModeBucket,
		Bucket: parsing.BucketSpec{
			MaxCount:       3,
			MaxSizePerFile: 1024 * 1024,
			MIME:           []string{"image/png", "image/jpeg"},
		},
		DisplayFiles: []parsing.DisplaySpec{
			{SourcePath: "out/report.pdf", DisplayName: "Final Report"},
		},
	}
	entered := time.Date(2026, 4, 25, 12, 30, 45, 0, time.UTC)

	ws := &state.WorkflowState{
		WorkflowID: "fa-rtrip",
		ScopeDir:   "scope",
		Agents: []state.AgentState{
			{
				ID:                  "main",
				CurrentState:        "REVIEW.md",
				Stack:               []state.StackFrame{},
				Status:              state.AgentStatusAwaiting,
				AwaitPrompt:         "Upload corrected dataset",
				AwaitNextState:      "PROCESS.md",
				AwaitInputID:        "inp_main_42",
				AwaitFileAffordance: fa,
				AwaitStagedFiles: []state.FileRecord{
					{Name: "Final Report", Size: 12345, ContentType: "application/pdf", Source: "display"},
				},
				AwaitEnteredAt: entered,
			},
		},
	}

	require.NoError(t, state.WriteState("fa-rtrip", ws, dir))
	got, err := state.ReadState("fa-rtrip", dir)
	require.NoError(t, err)

	require.Len(t, got.Agents, 1)
	a := got.Agents[0]

	require.NotNil(t, a.AwaitFileAffordance)
	assert.Equal(t, parsing.ModeBucket, a.AwaitFileAffordance.Mode)
	assert.Equal(t, 3, a.AwaitFileAffordance.Bucket.MaxCount)
	assert.Equal(t, int64(1024*1024), a.AwaitFileAffordance.Bucket.MaxSizePerFile)
	assert.Equal(t, []string{"image/png", "image/jpeg"}, a.AwaitFileAffordance.Bucket.MIME)
	require.Len(t, a.AwaitFileAffordance.DisplayFiles, 1)
	assert.Equal(t, "out/report.pdf", a.AwaitFileAffordance.DisplayFiles[0].SourcePath)
	assert.Equal(t, "Final Report", a.AwaitFileAffordance.DisplayFiles[0].DisplayName)

	require.Len(t, a.AwaitStagedFiles, 1)
	assert.Equal(t, "Final Report", a.AwaitStagedFiles[0].Name)
	assert.Equal(t, int64(12345), a.AwaitStagedFiles[0].Size)
	assert.Equal(t, "application/pdf", a.AwaitStagedFiles[0].ContentType)
	assert.Equal(t, "display", a.AwaitStagedFiles[0].Source)

	assert.True(t, a.AwaitEnteredAt.Equal(entered),
		"AwaitEnteredAt should round-trip (got %v want %v)", a.AwaitEnteredAt, entered)
}

func TestAgentStateAwaitFileAffordanceOmittedWhenZero(t *testing.T) {
	agent := state.AgentState{
		ID:           "main",
		CurrentState: "START.md",
		Stack:        []state.StackFrame{},
	}

	data, err := json.Marshal(agent)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	_, hasFA := raw["await_file_affordance"]
	assert.False(t, hasFA, "await_file_affordance should be absent from JSON when nil")
	_, hasStaged := raw["await_staged_files"]
	assert.False(t, hasStaged, "await_staged_files should be absent from JSON when empty")
	// Note: time.Time always serializes (omitempty has no effect on struct types),
	// matching the convention already used by WorkflowState.StartedAt.
}

func TestAgentStateAwaitEnteredAtZeroValueRoundTrip(t *testing.T) {
	// A zero AwaitEnteredAt round-trips through ReadState/WriteState as a zero
	// time, satisfying the "writes tolerate empty descriptors" guarantee.
	dir := stateDir(t)
	ws := &state.WorkflowState{
		WorkflowID: "ent-zero",
		ScopeDir:   "scope",
		Agents: []state.AgentState{
			{ID: "main", CurrentState: "START.md", Stack: []state.StackFrame{}},
		},
	}
	require.NoError(t, state.WriteState("ent-zero", ws, dir))

	got, err := state.ReadState("ent-zero", dir)
	require.NoError(t, err)
	require.Len(t, got.Agents, 1)
	assert.True(t, got.Agents[0].AwaitEnteredAt.IsZero(),
		"AwaitEnteredAt should round-trip as zero time")
}

func TestAgentStateAwaitFileAffordanceBackwardCompatibility(t *testing.T) {
	// Pre-bead state JSON has none of the new file-affordance fields.
	dir := stateDir(t)
	raw := `{
		"workflow_id": "old-fa",
		"scope_dir": "scope",
		"total_cost_usd": 0,
		"budget_usd": 10,
		"agents": [
			{"id":"main","current_state":"START.md","session_id":null,"stack":[]}
		]
	}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old-fa.json"), []byte(raw), 0o644))

	got, err := state.ReadState("old-fa", dir)
	require.NoError(t, err)
	require.Len(t, got.Agents, 1)
	a := got.Agents[0]

	assert.Nil(t, a.AwaitFileAffordance, "AwaitFileAffordance should be nil for old state files")
	assert.Empty(t, a.AwaitStagedFiles, "AwaitStagedFiles should be empty for old state files")
	assert.True(t, a.AwaitEnteredAt.IsZero(), "AwaitEnteredAt should be zero for old state files")
}

func TestWorkflowStateResolvedInputsRoundTrip(t *testing.T) {
	dir := stateDir(t)
	entered := time.Date(2026, 4, 25, 12, 30, 45, 0, time.UTC)
	resolved := entered.Add(2 * time.Minute)

	ws := &state.WorkflowState{
		WorkflowID: "ri-rtrip",
		ScopeDir:   "scope",
		Agents:     []state.AgentState{},
		ResolvedInputs: []state.ResolvedInput{
			{
				InputID:      "inp_main_1",
				AgentID:      "main",
				Prompt:       "Please review",
				NextState:    "AFTER.md",
				ResponseText: "looks good",
				StagedFiles: []state.FileRecord{
					{Name: "report.pdf", Size: 4096, ContentType: "application/pdf", Source: "display"},
				},
				UploadedFiles: []state.FileRecord{
					{Name: "annotated.pdf", Size: 5120, ContentType: "application/pdf", Source: "upload"},
				},
				EnteredAt:  entered,
				ResolvedAt: resolved,
			},
		},
	}

	require.NoError(t, state.WriteState("ri-rtrip", ws, dir))
	got, err := state.ReadState("ri-rtrip", dir)
	require.NoError(t, err)

	require.Len(t, got.ResolvedInputs, 1)
	r := got.ResolvedInputs[0]
	assert.Equal(t, "inp_main_1", r.InputID)
	assert.Equal(t, "main", r.AgentID)
	assert.Equal(t, "Please review", r.Prompt)
	assert.Equal(t, "AFTER.md", r.NextState)
	assert.Equal(t, "looks good", r.ResponseText)

	require.Len(t, r.StagedFiles, 1)
	assert.Equal(t, "report.pdf", r.StagedFiles[0].Name)
	assert.Equal(t, "display", r.StagedFiles[0].Source)
	require.Len(t, r.UploadedFiles, 1)
	assert.Equal(t, "annotated.pdf", r.UploadedFiles[0].Name)
	assert.Equal(t, "upload", r.UploadedFiles[0].Source)

	assert.True(t, r.EnteredAt.Equal(entered))
	assert.True(t, r.ResolvedAt.Equal(resolved))
}

func TestWorkflowStateResolvedInputsOmittedWhenEmpty(t *testing.T) {
	ws := &state.WorkflowState{
		WorkflowID: "ri-empty",
		Agents:     []state.AgentState{},
	}

	data, err := json.Marshal(ws)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	_, has := raw["resolved_inputs"]
	assert.False(t, has, "resolved_inputs should be absent from JSON when empty")
}

func TestWorkflowStateResolvedInputsBackwardCompatibility(t *testing.T) {
	// Old state JSON without the resolved_inputs field.
	dir := stateDir(t)
	raw := `{
		"workflow_id": "old-ri",
		"scope_dir": "scope",
		"total_cost_usd": 0,
		"budget_usd": 10,
		"agents": []
	}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old-ri.json"), []byte(raw), 0o644))

	got, err := state.ReadState("old-ri", dir)
	require.NoError(t, err)
	assert.Nil(t, got.ResolvedInputs, "ResolvedInputs should be nil for old state files")
}

func TestResolvedInputEmptyFileSlicesOmittedFromJSON(t *testing.T) {
	r := state.ResolvedInput{
		InputID: "inp_x",
		AgentID: "main",
	}
	data, err := json.Marshal(r)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	_, hasStaged := raw["staged_files"]
	assert.False(t, hasStaged, "staged_files should be absent when empty")
	_, hasUploaded := raw["uploaded_files"]
	assert.False(t, hasUploaded, "uploaded_files should be absent when empty")
}
