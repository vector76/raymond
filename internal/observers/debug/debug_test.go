package debug_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/observers/debug"
)

// setupDebug creates a new Bus, DebugObserver, and a temp debug directory,
// emitting WorkflowStarted to activate the observer.
func setupDebug(t *testing.T) (*bus.Bus, string) {
	t.Helper()
	b := bus.New()
	obs := debug.New(b)
	t.Cleanup(obs.Close)

	dir := t.TempDir()
	b.Emit(events.WorkflowStarted{
		WorkflowID: "wf1",
		DebugDir:   dir,
		Timestamp:  time.Now(),
	})
	return b, dir
}

// ----------------------------------------------------------------------------
// JSONL step files
// ----------------------------------------------------------------------------

func TestDebugCreatesJSONLFile(t *testing.T) {
	b, dir := setupDebug(t)

	b.Emit(events.ClaudeStreamOutput{
		AgentID:    "main",
		StateName:  "START.md",
		StepNumber: 1,
		JSONObject: map[string]any{"type": "result", "total_cost_usd": 0.05},
		Timestamp:  time.Now(),
	})

	path := filepath.Join(dir, "main_START_001.jsonl")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 1)

	var obj map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &obj))
	assert.Equal(t, "result", obj["type"])
}

func TestDebugAppendsMultipleLinesPerStep(t *testing.T) {
	b, dir := setupDebug(t)

	b.Emit(events.ClaudeStreamOutput{
		AgentID: "main", StateName: "START.md", StepNumber: 1,
		JSONObject: map[string]any{"seq": 1.0}, Timestamp: time.Now(),
	})
	b.Emit(events.ClaudeStreamOutput{
		AgentID: "main", StateName: "START.md", StepNumber: 1,
		JSONObject: map[string]any{"seq": 2.0}, Timestamp: time.Now(),
	})

	path := filepath.Join(dir, "main_START_001.jsonl")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assert.Len(t, lines, 2)
}

func TestDebugStepNumberZeroPadded(t *testing.T) {
	b, dir := setupDebug(t)

	b.Emit(events.ClaudeStreamOutput{
		AgentID: "main", StateName: "REVIEW.md", StepNumber: 12,
		JSONObject: map[string]any{}, Timestamp: time.Now(),
	})

	path := filepath.Join(dir, "main_REVIEW_012.jsonl")
	_, err := os.ReadFile(path)
	require.NoError(t, err, "file must exist with zero-padded step number")
}

func TestDebugSeparateStepFiles(t *testing.T) {
	b, dir := setupDebug(t)

	b.Emit(events.ClaudeStreamOutput{
		AgentID: "main", StateName: "A.md", StepNumber: 1,
		JSONObject: map[string]any{"step": "A"}, Timestamp: time.Now(),
	})
	b.Emit(events.ClaudeStreamOutput{
		AgentID: "main", StateName: "B.md", StepNumber: 2,
		JSONObject: map[string]any{"step": "B"}, Timestamp: time.Now(),
	})

	_, err := os.ReadFile(filepath.Join(dir, "main_A_001.jsonl"))
	require.NoError(t, err)
	_, err = os.ReadFile(filepath.Join(dir, "main_B_002.jsonl"))
	require.NoError(t, err)
}

func TestDebugWorkerAgentFiles(t *testing.T) {
	b, dir := setupDebug(t)

	b.Emit(events.ClaudeStreamOutput{
		AgentID: "main_worker1", StateName: "ANALYZE.md", StepNumber: 1,
		JSONObject: map[string]any{"type": "result"}, Timestamp: time.Now(),
	})

	path := filepath.Join(dir, "main_worker1_ANALYZE_001.jsonl")
	_, err := os.ReadFile(path)
	require.NoError(t, err)
}

func TestDebugNoOpWhenDebugDirEmpty(t *testing.T) {
	b := bus.New()
	obs := debug.New(b)
	defer obs.Close()

	// WorkflowStarted with empty DebugDir — observer should be a no-op.
	b.Emit(events.WorkflowStarted{WorkflowID: "wf1", DebugDir: "", Timestamp: time.Now()})

	// Emitting a stream event should not panic or write any file.
	b.Emit(events.ClaudeStreamOutput{
		AgentID: "main", StateName: "START.md", StepNumber: 1,
		JSONObject: map[string]any{"type": "result"}, Timestamp: time.Now(),
	})
}

// ----------------------------------------------------------------------------
// transitions.log
// ----------------------------------------------------------------------------

func TestDebugTransitionsLogGoto(t *testing.T) {
	b, dir := setupDebug(t)

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "START.md",
		ToState:        "NEXT.md",
		TransitionType: "goto",
		Metadata:       map[string]any{"state_type": "markdown"},
		Timestamp:      time.Date(2026, 1, 15, 14, 30, 22, 123456000, time.UTC),
	})

	data, err := os.ReadFile(filepath.Join(dir, "transitions.log"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "[main]")
	assert.Contains(t, content, "START.md -> NEXT.md")
	assert.Contains(t, content, "(goto)")
	assert.Contains(t, content, "state_type: markdown")
}

func TestDebugTransitionsLogTermination(t *testing.T) {
	b, dir := setupDebug(t)

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "LAST.md",
		ToState:        "",
		TransitionType: "result",
		Metadata:       map[string]any{"result_payload": "done"},
		Timestamp:      time.Now(),
	})

	data, err := os.ReadFile(filepath.Join(dir, "transitions.log"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "LAST.md -> (result, terminated)")
	assert.Contains(t, content, "result_payload: done")
}

func TestDebugTransitionsLogMultiple(t *testing.T) {
	b, dir := setupDebug(t)

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "A.md",
		ToState:        "B.md",
		TransitionType: "goto",
		Metadata:       map[string]any{},
		Timestamp:      time.Now(),
	})
	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "B.md",
		ToState:        "C.md",
		TransitionType: "goto",
		Metadata:       map[string]any{},
		Timestamp:      time.Now(),
	})

	data, err := os.ReadFile(filepath.Join(dir, "transitions.log"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "A.md -> B.md")
	assert.Contains(t, content, "B.md -> C.md")
}

func TestDebugTransitionsLogMetadataSorted(t *testing.T) {
	b, dir := setupDebug(t)

	b.Emit(events.TransitionOccurred{
		AgentID:        "main",
		FromState:      "A.md",
		ToState:        "B.md",
		TransitionType: "goto",
		Metadata:       map[string]any{"zzz": "last", "aaa": "first", "mmm": "mid"},
		Timestamp:      time.Now(),
	})

	data, err := os.ReadFile(filepath.Join(dir, "transitions.log"))
	require.NoError(t, err)
	content := string(data)

	aaaIdx := strings.Index(content, "aaa")
	mmmIdx := strings.Index(content, "mmm")
	zzzIdx := strings.Index(content, "zzz")
	assert.Greater(t, mmmIdx, aaaIdx, "mmm should follow aaa")
	assert.Greater(t, zzzIdx, mmmIdx, "zzz should follow mmm")
}

func TestDebugCloseUnsubscribes(t *testing.T) {
	b := bus.New()
	obs := debug.New(b)

	dir := t.TempDir()
	b.Emit(events.WorkflowStarted{WorkflowID: "wf1", DebugDir: dir, Timestamp: time.Now()})
	obs.Close()

	b.Emit(events.ClaudeStreamOutput{
		AgentID: "main", StateName: "START.md", StepNumber: 1,
		JSONObject: map[string]any{"x": 1}, Timestamp: time.Now(),
	})

	// No file should have been written after Close.
	_, err := os.ReadFile(filepath.Join(dir, "main_START_001.jsonl"))
	assert.Error(t, err, "no file should be written after Close")
}
