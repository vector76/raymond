package cli

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/daemon"
	"github.com/vector76/raymond/internal/orchestrator"
)

// fakeOrch is a minimal Orchestrator for tests; RunAllAgents returns nil
// immediately so launches do not spawn real LLM work.
type fakeOrch struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeOrch) RunAllAgents(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
	f.mu.Lock()
	f.calls = append(f.calls, workflowID)
	f.mu.Unlock()
	return nil
}

// writeWorkflowDir creates <root>/<dirName>/ containing a workflow.yaml
// manifest and a START.md entry point so the registry indexes it and
// LaunchRun's ResolveEntryPoint succeeds.
func writeWorkflowDir(t *testing.T, root, dirName, manifestYAML string) {
	t.Helper()
	dir := filepath.Join(root, dirName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.yaml"), []byte(manifestYAML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "START.md"), []byte("# Start\nDo it."), 0o644))
}

// newTestEnv builds a registry over scopeRoot and a RunManager wired to a
// fake orchestrator under stateDir.
func newTestEnv(t *testing.T, scopeRoot string) (*daemon.Registry, *daemon.RunManager) {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), ".raymond", "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))

	reg, err := daemon.NewRegistry([]string{scopeRoot})
	require.NoError(t, err)

	rm, err := daemon.NewRunManagerWithOrchestrator(stateDir, t.TempDir(), &fakeOrch{})
	require.NoError(t, err)

	return reg, rm
}

func TestLaunchStartupRuns_Empty(t *testing.T) {
	reg, rm := newTestEnv(t, t.TempDir())

	var buf bytes.Buffer
	err := launchStartupRuns(context.Background(), reg, rm, 0, nil, &buf)
	require.NoError(t, err)
	assert.Empty(t, buf.String())
	assert.Empty(t, rm.ListRuns())
}

func TestLaunchStartupRuns_SingleValidID(t *testing.T) {
	root := t.TempDir()
	writeWorkflowDir(t, root, "wf-foo", "id: foo\nname: Foo\n")

	reg, rm := newTestEnv(t, root)

	var buf bytes.Buffer
	err := launchStartupRuns(context.Background(), reg, rm, 0, []string{"foo"}, &buf)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Launched run ")
	assert.Contains(t, out, "for workflow foo")
	assert.Equal(t, 1, strings.Count(out, "Launched run "))

	runs := rm.ListRuns()
	require.Len(t, runs, 1)
	assert.Equal(t, "foo", runs[0].WorkflowID)
}

func TestLaunchStartupRuns_MultipleDistinctIDs(t *testing.T) {
	root := t.TempDir()
	writeWorkflowDir(t, root, "wf-a", "id: a\n")
	writeWorkflowDir(t, root, "wf-b", "id: b\n")
	writeWorkflowDir(t, root, "wf-c", "id: c\n")

	reg, rm := newTestEnv(t, root)

	var buf bytes.Buffer
	err := launchStartupRuns(context.Background(), reg, rm, 0, []string{"a", "b", "c"}, &buf)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "for workflow a")
	assert.Contains(t, out, "for workflow b")
	assert.Contains(t, out, "for workflow c")
	assert.Equal(t, 3, strings.Count(out, "Launched run "))

	runs := rm.ListRuns()
	assert.Len(t, runs, 3)
	wfIDs := map[string]int{}
	for _, r := range runs {
		wfIDs[r.WorkflowID]++
	}
	assert.Equal(t, map[string]int{"a": 1, "b": 1, "c": 1}, wfIDs)
}

func TestLaunchStartupRuns_DuplicateIDs(t *testing.T) {
	root := t.TempDir()
	writeWorkflowDir(t, root, "wf-foo", "id: foo\n")

	reg, rm := newTestEnv(t, root)

	var buf bytes.Buffer
	err := launchStartupRuns(context.Background(), reg, rm, 0, []string{"foo", "foo"}, &buf)
	require.NoError(t, err)

	out := buf.String()
	assert.Equal(t, 2, strings.Count(out, "Launched run "))
	assert.Equal(t, 2, strings.Count(out, "for workflow foo"))

	runs := rm.ListRuns()
	require.Len(t, runs, 2)
	assert.NotEqual(t, runs[0].RunID, runs[1].RunID)
}

func TestLaunchStartupRuns_UnknownID(t *testing.T) {
	reg, rm := newTestEnv(t, t.TempDir())

	var buf bytes.Buffer
	err := launchStartupRuns(context.Background(), reg, rm, 0, []string{"does-not-exist"}, &buf)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Failed to launch does-not-exist:")
	assert.NotContains(t, out, "Launched run ")
	assert.Empty(t, rm.ListRuns())
}

func TestLaunchStartupRuns_EmptyStringID(t *testing.T) {
	reg, rm := newTestEnv(t, t.TempDir())

	var buf bytes.Buffer
	err := launchStartupRuns(context.Background(), reg, rm, 0, []string{""}, &buf)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Failed to launch :")
	assert.NotContains(t, out, "Launched run ")
	assert.Empty(t, rm.ListRuns())
}

func TestLaunchStartupRuns_InputRequired(t *testing.T) {
	root := t.TempDir()
	writeWorkflowDir(t, root, "wf-needs-input", "id: needs-input\ninput:\n  mode: required\n")

	reg, rm := newTestEnv(t, root)

	var buf bytes.Buffer
	err := launchStartupRuns(context.Background(), reg, rm, 0, []string{"needs-input"}, &buf)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Failed to launch needs-input:")
	assert.Contains(t, out, "input")
	assert.NotContains(t, out, "Launched run ")
	assert.Empty(t, rm.ListRuns())
}

func TestLaunchStartupRuns_MixedValidAndUnknown(t *testing.T) {
	root := t.TempDir()
	writeWorkflowDir(t, root, "wf-good", "id: good\n")

	reg, rm := newTestEnv(t, root)

	var buf bytes.Buffer
	err := launchStartupRuns(context.Background(), reg, rm, 0, []string{"good", "missing"}, &buf)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Launched run ")
	assert.Contains(t, out, "for workflow good")
	assert.Contains(t, out, "Failed to launch missing:")

	runs := rm.ListRuns()
	require.Len(t, runs, 1)
	assert.Equal(t, "good", runs[0].WorkflowID)
}

func TestLaunchStartupRuns_ConcurrentLogIntegrity(t *testing.T) {
	root := t.TempDir()
	// Mix successes and failures to exercise both log-line shapes under
	// concurrent fan-out.
	writeWorkflowDir(t, root, "wf-a", "id: a\n")
	writeWorkflowDir(t, root, "wf-b", "id: b\n")
	writeWorkflowDir(t, root, "wf-c", "id: c\n")

	reg, rm := newTestEnv(t, root)

	ids := []string{"a", "b", "c", "missing-1", "a", "missing-2", "b", "c"}

	var buf bytes.Buffer
	err := launchStartupRuns(context.Background(), reg, rm, 0, ids, &buf)
	require.NoError(t, err)

	successRe := regexp.MustCompile(`^Launched run \S+ for workflow \S+$`)
	failureRe := regexp.MustCompile(`^Failed to launch \S*: .+$`)

	scanner := bufio.NewScanner(&buf)
	lineCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineCount++
		if !successRe.MatchString(line) && !failureRe.MatchString(line) {
			t.Errorf("line %d does not match either expected format: %q", lineCount, line)
		}
	}
	require.NoError(t, scanner.Err())
	assert.Equal(t, len(ids), lineCount, "expected one log line per id")
}

var _ daemon.Orchestrator = (*fakeOrch)(nil)
