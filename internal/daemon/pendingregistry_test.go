package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingRegistry_RegisterAndGet(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	pi := PendingInput{
		RunID:       "run-1",
		AgentID:     "main",
		InputID:     "inp-001",
		Prompt:      "Enter value",
		NextState:   "NEXT.md",
		CreatedAt:   time.Now().Truncate(time.Millisecond),
		TimeoutAt:   nil,
		TimeoutNext: "",
	}
	require.NoError(t, reg.Register(pi))

	got, ok := reg.Get("inp-001")
	require.True(t, ok)
	assert.Equal(t, "run-1", got.RunID)
	assert.Equal(t, "main", got.AgentID)
	assert.Equal(t, "inp-001", got.InputID)
	assert.Equal(t, "Enter value", got.Prompt)
	assert.Equal(t, "NEXT.md", got.NextState)
}

func TestPendingRegistry_GetNotFound(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	_, ok := reg.Get("nonexistent")
	assert.False(t, ok)
}

func TestPendingRegistry_Remove(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	pi := PendingInput{
		RunID:   "run-1",
		AgentID: "main",
		InputID: "inp-001",
		Prompt:  "Enter value",
	}
	require.NoError(t, reg.Register(pi))

	require.NoError(t, reg.Remove("inp-001"))

	_, ok := reg.Get("inp-001")
	assert.False(t, ok)
}

func TestPendingRegistry_RemoveNonexistent(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	err = reg.Remove("nonexistent")
	assert.Error(t, err)
}

func TestPendingRegistry_ListAll(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	require.NoError(t, reg.Register(PendingInput{
		RunID: "run-1", AgentID: "main", InputID: "inp-001",
	}))
	require.NoError(t, reg.Register(PendingInput{
		RunID: "run-2", AgentID: "worker", InputID: "inp-002",
	}))

	all := reg.ListAll()
	assert.Len(t, all, 2)

	ids := map[string]bool{}
	for _, pi := range all {
		ids[pi.InputID] = true
	}
	assert.True(t, ids["inp-001"])
	assert.True(t, ids["inp-002"])
}

func TestPendingRegistry_ListByRun(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	require.NoError(t, reg.Register(PendingInput{
		RunID: "run-1", AgentID: "main", InputID: "inp-001",
	}))
	require.NoError(t, reg.Register(PendingInput{
		RunID: "run-1", AgentID: "worker", InputID: "inp-002",
	}))
	require.NoError(t, reg.Register(PendingInput{
		RunID: "run-2", AgentID: "main", InputID: "inp-003",
	}))

	run1 := reg.ListByRun("run-1")
	assert.Len(t, run1, 2)

	run2 := reg.ListByRun("run-2")
	assert.Len(t, run2, 1)
	assert.Equal(t, "inp-003", run2[0].InputID)

	run3 := reg.ListByRun("run-3")
	assert.Len(t, run3, 0)
}

func TestPendingRegistry_Persistence(t *testing.T) {
	dir := t.TempDir()

	timeout := time.Now().Add(24 * time.Hour).Truncate(time.Millisecond)
	// Register entries in the first instance.
	{
		reg, err := NewPendingRegistry(dir)
		require.NoError(t, err)

		require.NoError(t, reg.Register(PendingInput{
			RunID:       "run-1",
			AgentID:     "main",
			InputID:     "inp-001",
			Prompt:      "Enter value",
			NextState:   "NEXT.md",
			CreatedAt:   time.Now().Truncate(time.Millisecond),
			TimeoutAt:   &timeout,
			TimeoutNext: "TIMEOUT.md",
		}))
		require.NoError(t, reg.Register(PendingInput{
			RunID:   "run-1",
			AgentID: "worker",
			InputID: "inp-002",
		}))
	}

	// Create a second instance from the same directory — it should replay the log.
	{
		reg, err := NewPendingRegistry(dir)
		require.NoError(t, err)

		all := reg.ListAll()
		assert.Len(t, all, 2)

		got, ok := reg.Get("inp-001")
		require.True(t, ok)
		assert.Equal(t, "run-1", got.RunID)
		assert.Equal(t, "Enter value", got.Prompt)
		assert.Equal(t, "NEXT.md", got.NextState)
		require.NotNil(t, got.TimeoutAt)
		assert.Equal(t, timeout.UTC(), got.TimeoutAt.UTC())
		assert.Equal(t, "TIMEOUT.md", got.TimeoutNext)

		_, ok = reg.Get("inp-002")
		assert.True(t, ok)
	}
}

func TestPendingRegistry_PersistenceWithRemove(t *testing.T) {
	dir := t.TempDir()

	// Register two entries, remove one.
	{
		reg, err := NewPendingRegistry(dir)
		require.NoError(t, err)

		require.NoError(t, reg.Register(PendingInput{
			RunID: "run-1", AgentID: "main", InputID: "inp-001",
		}))
		require.NoError(t, reg.Register(PendingInput{
			RunID: "run-1", AgentID: "worker", InputID: "inp-002",
		}))
		require.NoError(t, reg.Remove("inp-001"))
	}

	// Replay: only inp-002 should survive.
	{
		reg, err := NewPendingRegistry(dir)
		require.NoError(t, err)

		all := reg.ListAll()
		assert.Len(t, all, 1)
		assert.Equal(t, "inp-002", all[0].InputID)
	}
}

func TestPendingRegistry_CompactOnStartup(t *testing.T) {
	dir := t.TempDir()

	// Build a log with register + remove + register.
	{
		reg, err := NewPendingRegistry(dir)
		require.NoError(t, err)
		require.NoError(t, reg.Register(PendingInput{
			RunID: "run-1", AgentID: "main", InputID: "inp-001",
		}))
		require.NoError(t, reg.Register(PendingInput{
			RunID: "run-1", AgentID: "worker", InputID: "inp-002",
		}))
		require.NoError(t, reg.Remove("inp-001"))
	}

	// Record the log file size before compaction.
	logPath := filepath.Join(dir, pendingLogFile)
	infoBefore, err := os.Stat(logPath)
	require.NoError(t, err)
	sizeBefore := infoBefore.Size()

	// Re-open (triggers compaction on startup).
	{
		reg, err := NewPendingRegistry(dir)
		require.NoError(t, err)
		all := reg.ListAll()
		assert.Len(t, all, 1)
		assert.Equal(t, "inp-002", all[0].InputID)
	}

	// The compacted log should be smaller (or same if only one record).
	infoAfter, err := os.Stat(logPath)
	require.NoError(t, err)
	assert.LessOrEqual(t, infoAfter.Size(), sizeBefore)
}

func TestPendingRegistry_DuplicateRegister(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	pi := PendingInput{
		RunID: "run-1", AgentID: "main", InputID: "inp-001",
	}
	require.NoError(t, reg.Register(pi))

	err = reg.Register(pi)
	assert.Error(t, err)
}

func TestPendingRegistry_WithTimeout(t *testing.T) {
	dir := t.TempDir()
	reg, err := NewPendingRegistry(dir)
	require.NoError(t, err)

	timeout := time.Now().Add(1 * time.Hour).Truncate(time.Millisecond)
	pi := PendingInput{
		RunID:       "run-1",
		AgentID:     "main",
		InputID:     "inp-001",
		Prompt:      "Enter value",
		NextState:   "NEXT.md",
		TimeoutAt:   &timeout,
		TimeoutNext: "TIMEOUT.md",
	}
	require.NoError(t, reg.Register(pi))

	got, ok := reg.Get("inp-001")
	require.True(t, ok)
	require.NotNil(t, got.TimeoutAt)
	assert.Equal(t, timeout.UTC(), got.TimeoutAt.UTC())
	assert.Equal(t, "TIMEOUT.md", got.TimeoutNext)
}
