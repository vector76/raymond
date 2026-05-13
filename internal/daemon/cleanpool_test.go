package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfstate "github.com/vector76/raymond/internal/state"
)

// writeServeStateFile writes a state file directly into serveStateDir.
// Used by the --clean tests to seed mixed pre-startup pool contents
// without going through LaunchRun. terminal == true writes a state with
// zero agents (the "completed" lingering-artifact case the recovery
// path treats as terminal); terminal == false writes a single asking
// agent so the recovery path treats it as non-terminal.
func writeServeStateFile(t *testing.T, serveStateDir, runID string, terminal bool) []byte {
	t.Helper()
	ws := &wfstate.WorkflowState{
		WorkflowID: runID,
		ScopeDir:   "/some/scope",
	}
	if !terminal {
		ws.Agents = []wfstate.AgentState{
			{
				ID:           "main",
				CurrentState: "WAIT.md",
				Status:       wfstate.AgentStatusAsking,
				Stack:        []wfstate.StackFrame{},
			},
		}
	}
	data, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(serveStateDir, runID+".json"), data, 0o644))
	return data
}

// TestArchiveNonTerminalServeState_BackToBackCollision is test (b) of
// bead bd-z68c: two `--clean` invocations triggered in immediate
// succession must produce two distinct abandoned/<ts>/ subdirectories.
// The clock is injected so the test does not rely on the real clock
// ticking between the two calls — the only guarantee we want to assert
// is that nanosecond precision in the timestamp format is enough to
// distinguish back-to-back invocations.
func TestArchiveNonTerminalServeState_BackToBackCollision(t *testing.T) {
	serveStateDir := filepath.Join(t.TempDir(), "serve-state")
	require.NoError(t, os.MkdirAll(serveStateDir, 0o755))

	// Two non-terminal state files; we'll archive one before each clean
	// so each invocation actually has something to move (and therefore
	// actually creates its <ts>/ subdirectory).
	writeServeStateFile(t, serveStateDir, "run-a", false)

	base := time.Date(2026, 5, 12, 18, 30, 45, 123456789, time.UTC)
	first := func() time.Time { return base }
	second := func() time.Time { return base.Add(1) } // exactly one ns later

	dir1, archived1, err := ArchiveNonTerminalServeState(serveStateDir, first)
	require.NoError(t, err)
	require.Equal(t, []string{"run-a"}, archived1)

	writeServeStateFile(t, serveStateDir, "run-b", false)

	dir2, archived2, err := ArchiveNonTerminalServeState(serveStateDir, second)
	require.NoError(t, err)
	require.Equal(t, []string{"run-b"}, archived2)

	assert.NotEqual(t, dir1, dir2, "back-to-back --clean invocations must not collide on the abandoned subdir name")

	// Both subdirectories exist with the right tenant.
	_, err = os.Stat(filepath.Join(dir1, "run-a.json"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir2, "run-b.json"))
	require.NoError(t, err)

	// And the timestamps embed the exact nanosecond components we passed
	// in — the format is `2006-01-02T15-04-05.000000000Z`, so a 1-ns
	// gap shows up as the last digit of the fractional seconds.
	assert.Contains(t, filepath.Base(dir1), ".123456789Z")
	assert.Contains(t, filepath.Base(dir2), ".123456790Z")
}

// TestArchiveNonTerminalServeState_RecoveryDoesNotDescend is test (c)
// of bead bd-z68c: after a `--clean` startup moves non-terminal files
// into abandoned/<ts>/, an immediate (no-`--clean`) restart must NOT
// pick them up — the active set comes back empty. The recovery path
// lists the pool non-recursively (state.ListWorkflowsIn skips dir
// entries), so abandoned/<ts>/<file>.json is intentionally inert.
func TestArchiveNonTerminalServeState_RecoveryDoesNotDescend(t *testing.T) {
	raymondDir := filepath.Join(t.TempDir(), ".raymond")
	serveStateDir := filepath.Join(raymondDir, "serve-state")
	require.NoError(t, os.MkdirAll(serveStateDir, 0o755))

	writeServeStateFile(t, serveStateDir, "abandoned-run", false)

	// First --clean: archive it.
	_, archived, err := ArchiveNonTerminalServeState(serveStateDir, time.Now)
	require.NoError(t, err)
	require.Equal(t, []string{"abandoned-run"}, archived)

	// Sanity: the file is no longer at the top of the pool.
	_, err = os.Stat(filepath.Join(serveStateDir, "abandoned-run.json"))
	require.True(t, os.IsNotExist(err))

	// Second startup, NO --clean. Recovery must not descend into
	// abandoned/<ts>/, so the active set is empty and no orchestrator
	// goroutine is spawned for the archived id.
	fake := &fakeOrchestrator{}
	pr, err := NewPendingRegistry(raymondDir)
	require.NoError(t, err)
	rm, err := NewRunManagerForServe(serveStateDir, "/tmp", fake, pr)
	require.NoError(t, err)

	_, ok := rm.GetRun("abandoned-run")
	assert.False(t, ok, "recovery must not descend into abandoned/<ts>/")
	assert.Empty(t, rm.SnapshotActive(), "active set should be empty after a --clean restart")
	assert.Equal(t, 0, fake.callCount(), "orchestrator must not be invoked for archived runs")
}

// TestArchiveNonTerminalServeState_DanglingRegistryRecordOnClean is
// test (d) of bead bd-z68c: when `--clean` moves a state file out from
// under its paired pending-registry entry, the next NewRunManagerForServe
// startup drops the entry via the bead-5 policy and logs the dangling
// run id. No new policy code lives here — this test just asserts that
// `--clean` is a real trigger for the existing policy.
func TestArchiveNonTerminalServeState_DanglingRegistryRecordOnClean(t *testing.T) {
	raymondDir := filepath.Join(t.TempDir(), ".raymond")
	serveStateDir := filepath.Join(raymondDir, "serve-state")
	require.NoError(t, os.MkdirAll(serveStateDir, 0o755))

	const runID = "ask-run"
	const askID = "ask-1"

	writeServeStateFile(t, serveStateDir, runID, false)

	// Seed a pending entry whose paired state file is the one we are
	// about to abandon.
	seedPR, err := NewPendingRegistry(raymondDir)
	require.NoError(t, err)
	require.NoError(t, seedPR.Register(PendingAsk{
		RunID:      runID,
		AgentID:    "main",
		AskID:      askID,
		WorkflowID: runID,
		Prompt:     "what next?",
		NextState:  "NEXT.md",
		CreatedAt:  time.Now(),
	}))

	// --clean: the state file goes to abandoned/<ts>/.
	_, archived, err := ArchiveNonTerminalServeState(serveStateDir, time.Now)
	require.NoError(t, err)
	require.Equal(t, []string{runID}, archived)

	// Fresh registry replay (the on-disk log still has the entry) +
	// fresh run manager. PruneDangling runs before recovery; its stat
	// predicate now misses because the file is under abandoned/<ts>/.
	logBuf := captureLog(t)

	pr, err := NewPendingRegistry(raymondDir)
	require.NoError(t, err)
	if _, ok := pr.Get(askID); !ok {
		t.Fatalf("seed harness failed: registry replay did not surface %q", askID)
	}

	rm, err := NewRunManagerForServe(serveStateDir, "/tmp", &fakeOrchestrator{}, pr)
	require.NoError(t, err)
	require.NotNil(t, rm)

	// The dangling entry is gone from the in-memory registry.
	_, stillThere := pr.Get(askID)
	assert.False(t, stillThere, "dangling pending entry should be dropped after --clean abandons its state file")

	// And the dangling run id surfaces in the log line so an operator
	// can chase it down to the abandoned/<ts>/ subdirectory.
	assert.Contains(t, logBuf.String(), runID,
		"the dropped run id should be named in the log line when --clean triggers the policy")
}

// TestArchiveNonTerminalServeState_LeavesTerminalFiles is a focused
// sub-property of test (a) at the daemon layer: terminal state files
// (zero remaining agents) are NOT swept by `--clean`. The CLI-level
// end-to-end variant in internal/cli/serve_test.go covers the same
// property going through the cobra command.
func TestArchiveNonTerminalServeState_LeavesTerminalFiles(t *testing.T) {
	serveStateDir := filepath.Join(t.TempDir(), "serve-state")
	require.NoError(t, os.MkdirAll(serveStateDir, 0o755))

	terminalBytes := writeServeStateFile(t, serveStateDir, "done-run", true)
	nonTerminalBytes := writeServeStateFile(t, serveStateDir, "live-run", false)

	abandonDir, archived, err := ArchiveNonTerminalServeState(serveStateDir, time.Now)
	require.NoError(t, err)
	require.Equal(t, []string{"live-run"}, archived)

	// Terminal file remains at the top of the pool with its original bytes.
	got, err := os.ReadFile(filepath.Join(serveStateDir, "done-run.json"))
	require.NoError(t, err)
	assert.Equal(t, terminalBytes, got, "terminal state file must be left in place untouched")

	// Non-terminal file is under abandoned/<ts>/ with its original bytes.
	got, err = os.ReadFile(filepath.Join(abandonDir, "live-run.json"))
	require.NoError(t, err)
	assert.Equal(t, nonTerminalBytes, got, "archived file must preserve its original bytes")

	// And it is no longer at the top of the pool.
	_, err = os.Stat(filepath.Join(serveStateDir, "live-run.json"))
	assert.True(t, os.IsNotExist(err), "non-terminal file must be moved out of serve-state/")
}
