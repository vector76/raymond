package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/manifest"
	"github.com/vector76/raymond/internal/orchestrator"
	"github.com/vector76/raymond/internal/parsing"
	wfstate "github.com/vector76/raymond/internal/state"
)

// newTestServer creates a Server backed by a registry with one workflow and a
// fake orchestrator. It returns the server, the httptest server, and the fake
// orchestrator for test control.
func newTestServer(t *testing.T) (*Server, *httptest.Server, *fakeOrchestrator) {
	t.Helper()

	// Create a workflow directory with a manifest.
	scopeDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(scopeDir, "START.md"),
		[]byte("# Start\nDo something."),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(scopeDir, "workflow.yaml"),
		[]byte("id: test-workflow\nname: Test Workflow\ndescription: A test workflow\ndefault_budget: 5.0\n"),
		0o644,
	))

	rootDir := filepath.Dir(scopeDir)
	reg, err := NewRegistry([]string{rootDir})
	require.NoError(t, err)

	stateDir := ensureStateDir(t)
	fake := &fakeOrchestrator{}
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	pendingDir := t.TempDir()
	pr, err := NewPendingRegistry(pendingDir)
	require.NoError(t, err)
	rm.SetPendingRegistry(pr)

	srv := NewServer(reg, rm, 0)
	srv.SetPendingRegistry(pr)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return srv, ts, fake
}

func TestListWorkflows(t *testing.T) {
	_, ts, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/workflows")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var workflows []workflowResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&workflows))
	require.Len(t, workflows, 1)
	assert.Equal(t, "test-workflow", workflows[0].ID)
	assert.Equal(t, "Test Workflow", workflows[0].Name)
}

func TestGetWorkflow(t *testing.T) {
	_, ts, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/workflows/test-workflow")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var wf workflowResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&wf))
	assert.Equal(t, "test-workflow", wf.ID)
}

func TestHTTPGetWorkflow_NotFound(t *testing.T) {
	_, ts, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/workflows/nonexistent")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	var errResp errorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	assert.Contains(t, errResp.Error, "not found")
}

func TestCreateRun(t *testing.T) {
	_, ts, _ := newTestServer(t)

	body := `{"workflow_id": "test-workflow", "input": "hello", "budget": 3.0, "model": "sonnet"}`
	resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var cr createRunResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&cr))
	assert.NotEmpty(t, cr.RunID)
	assert.Equal(t, "running", cr.Status)
	assert.False(t, cr.StartedAt.IsZero())
}

func TestResolveBudget(t *testing.T) {
	// Precedence: request > manifest > server > hardcoded constant
	// (daemonDefaultBudgetUSD, currently 0 = unlimited).
	assert.Equal(t, 3.0, ResolveBudget(3.0, 2.0, 1.0))
	assert.Equal(t, 2.0, ResolveBudget(0, 2.0, 1.0))
	assert.Equal(t, 1.0, ResolveBudget(0, 0, 1.0))
	// All levels unset → fall through to the unlimited default.
	assert.Equal(t, daemonDefaultBudgetUSD, ResolveBudget(0, 0, 0))
	// Negative values are treated as unset at every level.
	assert.Equal(t, 2.0, ResolveBudget(-5, 2.0, 1.0))
}

func TestValidateInputMode(t *testing.T) {
	// required: empty rejected, non-empty accepted.
	require.Error(t, ValidateInputMode(manifest.InputModeRequired, ""))
	require.NoError(t, ValidateInputMode(manifest.InputModeRequired, "hello"))

	// optional: both empty and non-empty are accepted.
	require.NoError(t, ValidateInputMode(manifest.InputModeOptional, ""))
	require.NoError(t, ValidateInputMode(manifest.InputModeOptional, "hello"))

	// none: empty accepted, non-empty rejected.
	require.NoError(t, ValidateInputMode(manifest.InputModeNone, ""))
	require.Error(t, ValidateInputMode(manifest.InputModeNone, "hello"))
}

func TestResolveUploadCaps_PerAskOverridesAll(t *testing.T) {
	// Bucket-mode asks with their own caps win over server config and
	// the hardcoded fallback. Per-slot MIME isn't a size cap, so slot mode
	// is exercised separately below.
	fa := &parsing.FileAffordance{
		Mode: parsing.ModeBucket,
		Bucket: parsing.BucketSpec{
			MaxSizePerFile: 7,
			MaxTotalSize:   77,
			MaxCount:       3,
		},
	}
	perFile, total, count := resolveUploadCaps(fa, 50, 500, 20)
	assert.Equal(t, int64(7), perFile)
	assert.Equal(t, int64(77), total)
	assert.Equal(t, 3, count)
}

func TestResolveUploadCaps_ServerConfigUsedWhenAskUnset(t *testing.T) {
	// Bucket ask with all caps unset (zero) inherits the server config.
	fa := &parsing.FileAffordance{Mode: parsing.ModeBucket}
	perFile, total, count := resolveUploadCaps(fa, 42, 420, 9)
	assert.Equal(t, int64(42), perFile)
	assert.Equal(t, int64(420), total)
	assert.Equal(t, 9, count)
}

func TestResolveUploadCaps_HardcodedFallbackWhenServerUnset(t *testing.T) {
	// Both per-ask and server are zero (or negative) — fall through to
	// the daemonDefaultMax* constants.
	perFile, total, count := resolveUploadCaps(nil, 0, 0, 0)
	assert.Equal(t, daemonDefaultMaxFileSize, perFile)
	assert.Equal(t, daemonDefaultMaxTotalSize, total)
	assert.Equal(t, daemonDefaultMaxFileCount, count)

	// Negatives at the server level are treated as unset, same as zero.
	perFile, total, count = resolveUploadCaps(nil, -1, -1, -1)
	assert.Equal(t, daemonDefaultMaxFileSize, perFile)
	assert.Equal(t, daemonDefaultMaxTotalSize, total)
	assert.Equal(t, daemonDefaultMaxFileCount, count)
}

func TestResolveUploadCaps_PartialAskOverridesPerCap(t *testing.T) {
	// An ask may override only some of the three caps; the rest fall
	// through to the server config (or hardcoded fallback for any cap the
	// server also leaves unset). This is the "mix and match" precedence
	// case the design calls out for bucket-mode asks.
	fa := &parsing.FileAffordance{
		Mode: parsing.ModeBucket,
		Bucket: parsing.BucketSpec{
			MaxSizePerFile: 11,
			// MaxTotalSize and MaxCount left unset.
		},
	}
	perFile, total, count := resolveUploadCaps(fa, 99, 999, 5)
	assert.Equal(t, int64(11), perFile) // per-ask override
	assert.Equal(t, int64(999), total)  // server config
	assert.Equal(t, 5, count)           // server config
}

func TestResolveUploadCaps_SlotModeInheritsServerDefaults(t *testing.T) {
	// Slot-mode asks don't carry size/count caps of their own; they must
	// inherit the server-wide defaults so an upload to a slot is bounded
	// by the same ceiling as a bucket upload that also leaves caps unset.
	fa := &parsing.FileAffordance{
		Mode: parsing.ModeSlot,
		Slots: []parsing.SlotSpec{
			{Name: "resume.pdf", MIME: []string{"application/pdf"}},
		},
	}
	perFile, total, count := resolveUploadCaps(fa, 200, 2000, 4)
	assert.Equal(t, int64(200), perFile)
	assert.Equal(t, int64(2000), total)
	assert.Equal(t, 4, count)
}

func TestResolveUploadCaps_NilAffordanceUsesServerThenFallback(t *testing.T) {
	// A nil FileAffordance (e.g. text-only ask misrouted through the
	// upload path) is bounded by the server config, then the hardcoded
	// defaults — never unbounded.
	perFile, total, count := resolveUploadCaps(nil, 33, 333, 6)
	assert.Equal(t, int64(33), perFile)
	assert.Equal(t, int64(333), total)
	assert.Equal(t, 6, count)
}

func TestSetDefaultUploadCapsAffectsServerState(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.SetDefaultUploadCaps(123, 456, 7)
	assert.Equal(t, int64(123), srv.defaultMaxFileSize)
	assert.Equal(t, int64(456), srv.defaultMaxTotalSize)
	assert.Equal(t, 7, srv.defaultMaxFileCount)
}

func TestCreateRun_AppliesServerDefaultBudgetWhenUnspecified(t *testing.T) {
	// Workflow manifest has no default_budget and the request omits budget;
	// the daemon must fall back to the server-wide default that serve.go
	// loads from config.toml. This is what the CLI does when --budget and
	// the workflow manifest are both silent.
	scopeDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(scopeDir, "START.md"),
		[]byte("# Start\nDo something."),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(scopeDir, "workflow.yaml"),
		[]byte("id: no-budget-workflow\nname: No Budget\ndescription: Has no default_budget\n"),
		0o644,
	))

	reg, err := NewRegistry([]string{filepath.Dir(scopeDir)})
	require.NoError(t, err)

	stateDir := ensureStateDir(t)
	fake := &fakeOrchestrator{}
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	srv := NewServer(reg, rm, 0)
	srv.SetDefaultBudget(1000.0) // simulate config.toml budget = 1000
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	body := `{"workflow_id": "no-budget-workflow"}`
	resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var cr createRunResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&cr))

	stateFile := filepath.Join(stateDir, cr.RunID+".json")
	raw, err := os.ReadFile(stateFile)
	require.NoError(t, err)
	var ws struct {
		BudgetUSD float64 `json:"budget_usd"`
	}
	require.NoError(t, json.Unmarshal(raw, &ws))
	assert.Equal(t, 1000.0, ws.BudgetUSD)
}

func TestCreateRun_AppliesDefaultBudgetWhenUnspecified(t *testing.T) {
	// Workflow has no default_budget and the request omits budget; the daemon
	// must fall back to daemonDefaultBudgetUSD (0 = unlimited). The markdown
	// executor's budget-exceeded guard is gated on BudgetUSD > 0, so a
	// fall-through default of zero correctly produces an unbounded run rather
	// than terminating on the first Claude call.
	scopeDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(scopeDir, "START.md"),
		[]byte("# Start\nDo something."),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(scopeDir, "workflow.yaml"),
		[]byte("id: no-budget-workflow\nname: No Budget\ndescription: Has no default_budget\n"),
		0o644,
	))

	reg, err := NewRegistry([]string{filepath.Dir(scopeDir)})
	require.NoError(t, err)

	stateDir := ensureStateDir(t)
	fake := &fakeOrchestrator{}
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)

	srv := NewServer(reg, rm, 0)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	body := `{"workflow_id": "no-budget-workflow"}`
	resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var cr createRunResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&cr))

	// Read the persisted budget from the state file to confirm the fallback.
	stateFile := filepath.Join(stateDir, cr.RunID+".json")
	raw, err := os.ReadFile(stateFile)
	require.NoError(t, err)
	var ws struct {
		BudgetUSD float64 `json:"budget_usd"`
	}
	require.NoError(t, json.Unmarshal(raw, &ws))
	assert.Equal(t, daemonDefaultBudgetUSD, ws.BudgetUSD)
}

func TestCreateRun_PropagatesServerDangerouslySkipPermissions(t *testing.T) {
	// SetDefaultDangerouslySkipPermissions must reach the run's RunOptions
	// and be persisted into LaunchParams. Run with both true and false to
	// confirm the daemon honours the value end-to-end and does not silently
	// hardcode either direction.
	for _, dsp := range []bool{true, false} {
		t.Run(fmt.Sprintf("dsp=%v", dsp), func(t *testing.T) {
			srv, ts, fake := newTestServer(t)
			srv.SetDefaultDangerouslySkipPermissions(dsp)

			body := `{"workflow_id": "test-workflow"}`
			resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusCreated, resp.StatusCode)

			var cr createRunResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&cr))

			require.Eventually(t, func() bool {
				return fake.callCount() == 1
			}, time.Second, 10*time.Millisecond)

			fake.mu.Lock()
			gotOpts := fake.calls[0].Opts
			fake.mu.Unlock()
			assert.Equal(t, dsp, gotOpts.DangerouslySkipPermissions,
				"server-wide skip-perms must propagate into RunOptions")

			ws, err := wfstate.ReadState(cr.RunID, srv.runManager.stateDir)
			require.NoError(t, err)
			require.NotNil(t, ws.LaunchParams)
			assert.Equal(t, dsp, ws.LaunchParams.DangerouslySkipPermissions,
				"LaunchParams must record the server-wide skip-perms used at launch")
		})
	}
}

func TestCreateRun_InvalidJSON(t *testing.T) {
	_, ts, _ := newTestServer(t)

	resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader("{bad json"))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var errResp errorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	assert.Contains(t, errResp.Error, "invalid JSON")
}

func TestCreateRun_MissingWorkflowID(t *testing.T) {
	_, ts, _ := newTestServer(t)

	body := `{"input": "hello"}`
	resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestCreateRun_UnknownWorkflow(t *testing.T) {
	_, ts, _ := newTestServer(t)

	body := `{"workflow_id": "nonexistent"}`
	resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// newTestServerWithManifest builds a server backed by a registry with one
// workflow whose workflow.yaml is the given yaml. Used by input-mode tests.
func newTestServerWithManifest(t *testing.T, manifestYAML string) *httptest.Server {
	t.Helper()
	scopeDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(scopeDir, "START.md"),
		[]byte("# Start\nDo something."),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(scopeDir, "workflow.yaml"),
		[]byte(manifestYAML),
		0o644,
	))
	reg, err := NewRegistry([]string{filepath.Dir(scopeDir)})
	require.NoError(t, err)
	stateDir := ensureStateDir(t)
	fake := &fakeOrchestrator{}
	rm, err := NewRunManagerWithOrchestrator(stateDir, "/tmp", fake)
	require.NoError(t, err)
	srv := NewServer(reg, rm, 0)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestCreateRun_InputModeRequired_RejectsEmpty(t *testing.T) {
	ts := newTestServerWithManifest(t, "id: req-wf\ninput:\n  mode: required\n")

	body := `{"workflow_id": "req-wf"}`
	resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var errResp errorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	assert.Contains(t, errResp.Error, "requires non-empty input")
}

func TestCreateRun_InputModeRequired_AcceptsNonEmpty(t *testing.T) {
	ts := newTestServerWithManifest(t, "id: req-wf\ninput:\n  mode: required\n")

	body := `{"workflow_id": "req-wf", "input": "hello"}`
	resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestCreateRun_InputModeNone_RejectsNonEmpty(t *testing.T) {
	ts := newTestServerWithManifest(t, "id: none-wf\ninput:\n  mode: none\n")

	body := `{"workflow_id": "none-wf", "input": "should not be here"}`
	resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var errResp errorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	assert.Contains(t, errResp.Error, "does not accept input")
}

func TestCreateRun_InputModeNone_AcceptsEmpty(t *testing.T) {
	ts := newTestServerWithManifest(t, "id: none-wf\ninput:\n  mode: none\n")

	body := `{"workflow_id": "none-wf"}`
	resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestListWorkflows_ExposesInputSpec(t *testing.T) {
	ts := newTestServerWithManifest(t, `
id: labeled-wf
input:
  mode: required
  label: Vendor name
  description: The vendor to evaluate
`)

	resp, err := http.Get(ts.URL + "/workflows")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var workflows []workflowResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&workflows))
	require.Len(t, workflows, 1)
	assert.Equal(t, "required", workflows[0].Input.Mode)
	assert.Equal(t, "Vendor name", workflows[0].Input.Label)
	assert.Equal(t, "The vendor to evaluate", workflows[0].Input.Description)
}

func TestGetRun(t *testing.T) {
	_, ts, _ := newTestServer(t)

	// Create a run first.
	body := `{"workflow_id": "test-workflow", "input": "hello"}`
	createResp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	var cr createRunResponse
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&cr))

	// Get the run.
	resp, err := http.Get(ts.URL + "/runs/" + cr.RunID)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var run runResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&run))
	assert.Equal(t, cr.RunID, run.RunID)
	assert.Equal(t, "test-workflow", run.WorkflowID)
	assert.Equal(t, "running", run.Status)
	assert.NotEmpty(t, run.Agents)
}

func TestHTTPGetRun_NotFound(t *testing.T) {
	_, ts, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/runs/nonexistent")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestListRuns(t *testing.T) {
	_, ts, _ := newTestServer(t)

	// Initially empty.
	resp, err := http.Get(ts.URL + "/runs")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var runs []runResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&runs))
	assert.Empty(t, runs)

	// Create a run.
	body := `{"workflow_id": "test-workflow"}`
	createResp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	createResp.Body.Close()

	// Now should have one run.
	resp2, err := http.Get(ts.URL + "/runs")
	require.NoError(t, err)
	defer resp2.Body.Close()

	var runs2 []runResponse
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&runs2))
	assert.Len(t, runs2, 1)
}

func TestCancelRun(t *testing.T) {
	_, ts, _ := newTestServer(t)

	// Create a run.
	body := `{"workflow_id": "test-workflow"}`
	createResp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer createResp.Body.Close()

	var cr createRunResponse
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&cr))

	// Give the orchestrator goroutine a moment to start.
	time.Sleep(50 * time.Millisecond)

	// Cancel the run.
	resp, err := http.Post(ts.URL+"/runs/"+cr.RunID+"/cancel", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var cancelResp cancelRunResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&cancelResp))
	assert.Equal(t, cr.RunID, cancelResp.RunID)
	assert.Equal(t, "cancelled", cancelResp.Status)
}

func TestDeleteRun_HTTP(t *testing.T) {
	_, ts, fake := newTestServer(t)

	// Completes immediately so the run is deletable.
	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		b := bus.New()
		opts.ObserverSetup(b)
		b.Emit(events.WorkflowCompleted{
			WorkflowID: workflowID, TotalCostUSD: 0, Timestamp: time.Now(),
		})
		return nil
	}

	body := `{"workflow_id": "test-workflow"}`
	createResp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer createResp.Body.Close()

	var cr createRunResponse
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&cr))

	// Wait for the run to reach terminal status.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		getResp, getErr := http.Get(ts.URL + "/runs/" + cr.RunID)
		require.NoError(t, getErr)
		var r runResponse
		json.NewDecoder(getResp.Body).Decode(&r)
		getResp.Body.Close()
		if r.Status == "completed" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/runs/"+cr.RunID, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Subsequent GET should 404.
	getResp, err := http.Get(ts.URL + "/runs/" + cr.RunID)
	require.NoError(t, err)
	defer getResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, getResp.StatusCode)
}

func TestDeleteRun_HTTP_Active(t *testing.T) {
	_, ts, _ := newTestServer(t)

	body := `{"workflow_id": "test-workflow"}`
	createResp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer createResp.Body.Close()

	var cr createRunResponse
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&cr))

	// Let the orchestrator start so the run is "running".
	time.Sleep(50 * time.Millisecond)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/runs/"+cr.RunID, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestDeleteRun_HTTP_NotFound(t *testing.T) {
	_, ts, _ := newTestServer(t)

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/runs/nonexistent", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHTTPCancelRun_NotFound(t *testing.T) {
	_, ts, _ := newTestServer(t)

	resp, err := http.Post(ts.URL+"/runs/nonexistent/cancel", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestRunOutput_SSE(t *testing.T) {
	_, ts, fake := newTestServer(t)

	// busReady signals that ObserverSetup has been called and the bus is stored.
	// emitNow signals the orchestrator to emit an event (after the SSE client
	// has connected and subscribed).
	busReady := make(chan struct{})
	emitNow := make(chan struct{})

	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		b := bus.New()
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(b)
		}
		close(busReady)

		// Wait for signal to emit event.
		select {
		case <-emitNow:
		case <-ctx.Done():
			return ctx.Err()
		}

		b.Emit(events.StateStarted{
			AgentID:   "main",
			StateName: "START",
			StateType: "markdown",
			Timestamp: time.Now(),
		})

		// Block until cancelled.
		<-ctx.Done()
		return ctx.Err()
	}

	// Create a run.
	body := `{"workflow_id": "test-workflow"}`
	createResp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	var cr createRunResponse
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&cr))

	// Wait for the bus to be ready.
	select {
	case <-busReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for bus setup")
	}

	// Connect to the SSE stream.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/runs/"+cr.RunID+"/output", nil)
	require.NoError(t, err)

	sseResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer sseResp.Body.Close()

	assert.Equal(t, http.StatusOK, sseResp.StatusCode)
	assert.Equal(t, "text/event-stream", sseResp.Header.Get("Content-Type"))

	// Give the SSE handler a moment to subscribe to the bus, then emit.
	time.Sleep(50 * time.Millisecond)
	close(emitNow)

	// Read at least one SSE data line.
	scanner := bufio.NewScanner(sseResp.Body)
	var gotEvent bool
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			var evt map[string]any
			require.NoError(t, json.Unmarshal([]byte(payload), &evt))
			assert.Equal(t, "state_started", evt["type"])
			gotEvent = true
			break
		}
	}
	assert.True(t, gotEvent, "expected at least one SSE event")
}

func TestRunOutput_NotFound(t *testing.T) {
	_, ts, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/runs/nonexistent/output")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestCORS_Preflight(t *testing.T) {
	_, ts, _ := newTestServer(t)

	req, err := http.NewRequest("OPTIONS", ts.URL+"/workflows", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "GET")
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "POST")
}

func TestCORS_Headers(t *testing.T) {
	_, ts, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/workflows")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
}

func TestListPendingInputs(t *testing.T) {
	_, ts, fake := newTestServer(t)

	// Set up a fake orchestrator that emits an ask event.
	askReady := make(chan struct{})
	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		b := bus.New()
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(b)
		}

		b.Emit(events.AgentAskStarted{
			AgentID:   "main",
			AskID:   "test-input-1",
			Prompt:    "What should I do?",
			NextState: "NEXT.md",
			Timestamp: time.Now(),
		})

		// In daemon mode the callback registers the pending input.
		if opts.AskCallback != nil {
			opts.AskCallback("main", "test-input-1", "What should I do?", "NEXT.md", nil, nil)
		}

		close(askReady)

		// Block until cancelled.
		<-ctx.Done()
		return ctx.Err()
	}

	// Create a run.
	body := `{"workflow_id": "test-workflow"}`
	createResp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	var cr createRunResponse
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&cr))

	// Wait for ask to be registered.
	select {
	case <-askReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ask")
	}

	// List pending inputs.
	resp, err := http.Get(ts.URL + "/runs/" + cr.RunID + "/pending-asks")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var inputs []pendingInputResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&inputs))
	require.Len(t, inputs, 1)
	assert.Equal(t, cr.RunID, inputs[0].RunID)
	assert.Equal(t, "test-input-1", inputs[0].AskID)
	assert.Equal(t, "What should I do?", inputs[0].Prompt)
}

func TestDeliverInput(t *testing.T) {
	_, ts, fake := newTestServer(t)

	inputDelivered := make(chan string, 1)
	askReady := make(chan struct{})

	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		b := bus.New()
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(b)
		}

		b.Emit(events.AgentAskStarted{
			AgentID:   "main",
			AskID:   "test-input-1",
			Prompt:    "What should I do?",
			NextState: "NEXT.md",
			Timestamp: time.Now(),
		})

		if opts.AskCallback != nil {
			opts.AskCallback("main", "test-input-1", "What should I do?", "NEXT.md", nil, nil)
		}

		close(askReady)

		// In daemon mode, wait for input on the AskInputCh.
		if opts.AskInputCh != nil {
			select {
			case input := <-opts.AskInputCh:
				inputDelivered <- input.Response
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		b.Emit(events.WorkflowCompleted{
			WorkflowID:   workflowID,
			TotalCostUSD: 0.5,
			Timestamp:    time.Now(),
		})
		return nil
	}

	// Create a run.
	body := `{"workflow_id": "test-workflow"}`
	createResp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer createResp.Body.Close()

	var cr createRunResponse
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&cr))

	// Wait for ask.
	select {
	case <-askReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ask")
	}

	// Deliver input.
	inputBody := `{"response": "Do the thing"}`
	resp, err := http.Post(
		ts.URL+"/runs/"+cr.RunID+"/asks/test-input-1",
		"application/json",
		strings.NewReader(inputBody),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var dr deliverInputResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&dr))
	assert.Equal(t, cr.RunID, dr.RunID)
	assert.Equal(t, "test-input-1", dr.AskID)
	assert.Equal(t, "resumed", dr.Status)

	// Verify the input was actually delivered.
	select {
	case delivered := <-inputDelivered:
		assert.Equal(t, "Do the thing", delivered)
	case <-time.After(5 * time.Second):
		t.Fatal("input was not delivered to orchestrator")
	}
}

func TestDeliverInput_InvalidAskID(t *testing.T) {
	_, ts, _ := newTestServer(t)

	// Create a run.
	body := `{"workflow_id": "test-workflow"}`
	createResp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer createResp.Body.Close()

	var cr createRunResponse
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&cr))

	// Try to deliver to non-existent input.
	inputBody := `{"response": "hello"}`
	resp, err := http.Post(
		ts.URL+"/runs/"+cr.RunID+"/asks/nonexistent",
		"application/json",
		strings.NewReader(inputBody),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestDeliverInput_WrongRun(t *testing.T) {
	_, ts, fake := newTestServer(t)

	askReady := make(chan struct{})
	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		b := bus.New()
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(b)
		}
		if opts.AskCallback != nil {
			opts.AskCallback("main", "test-input-1", "prompt", "NEXT.md", nil, nil)
		}
		close(askReady)
		<-ctx.Done()
		return ctx.Err()
	}

	// Create a run.
	body := `{"workflow_id": "test-workflow"}`
	createResp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	select {
	case <-askReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ask")
	}

	// Try to deliver input using wrong run ID.
	inputBody := `{"response": "hello"}`
	resp, err := http.Post(
		ts.URL+"/runs/wrong-run-id/asks/test-input-1",
		"application/json",
		strings.NewReader(inputBody),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// createPendingRunForFiles creates a run via the API and returns the runID.
// The fake orchestrator blocks on ctx.Done() so the run stays active.
func createPendingRunForFiles(t *testing.T, ts *httptest.Server, fake *fakeOrchestrator) string {
	t.Helper()
	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(bus.New())
		}
		<-ctx.Done()
		return ctx.Err()
	}
	createResp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(`{"workflow_id": "test-workflow"}`))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	var cr createRunResponse
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&cr))
	return cr.RunID
}

func TestListInputFiles_PendingReturnsStaged(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	staged := []wfstate.FileRecord{
		{Name: "report.pdf", Size: 2048, ContentType: "application/pdf", Source: "display"},
		{Name: "chart.png", Size: 512, ContentType: "image/png", Source: "display"},
	}
	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:       runID,
		AgentID:     "main",
		AskID:     "inp-1",
		Prompt:      "review",
		CreatedAt:   time.Now(),
		StagedFiles: staged,
	}))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/asks/inp-1/files")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var files []inputFileMetadata
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&files))
	require.Len(t, files, 2)
	assert.Equal(t, "report.pdf", files[0].Name)
	assert.Equal(t, int64(2048), files[0].Size)
	assert.Equal(t, "application/pdf", files[0].ContentType)
	assert.Equal(t, "display", files[0].Source)
	assert.Equal(t, "chart.png", files[1].Name)
}

func TestListInputFiles_ResolvedReturnsStagedPlusUploaded(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	// Write a ResolvedInput record to the workflow state. The pending registry
	// has no entry for this input, so the handler must fall back to state.
	statePath := filepath.Join(srv.runManager.stateDir, runID+".json")
	raw, err := os.ReadFile(statePath)
	require.NoError(t, err)
	var ws wfstate.WorkflowState
	require.NoError(t, json.Unmarshal(raw, &ws))
	ws.ResolvedInputs = append(ws.ResolvedInputs, wfstate.ResolvedInput{
		AskID:      "inp-resolved",
		AgentID:      "main",
		Prompt:       "upload",
		ResponseText: "done",
		StagedFiles: []wfstate.FileRecord{
			{Name: "original.csv", Size: 100, ContentType: "text/csv", Source: "display"},
		},
		UploadedFiles: []wfstate.FileRecord{
			{Name: "fixed.csv", Size: 200, ContentType: "text/csv", Source: "upload"},
			{Name: "notes.txt", Size: 64, ContentType: "text/plain", Source: "upload"},
		},
		EnteredAt:  time.Now().Add(-time.Minute),
		ResolvedAt: time.Now(),
	})
	out, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, out, 0o644))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/asks/inp-resolved/files")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var files []inputFileMetadata
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&files))
	require.Len(t, files, 3)
	assert.Equal(t, "original.csv", files[0].Name)
	assert.Equal(t, "display", files[0].Source)
	assert.Equal(t, "fixed.csv", files[1].Name)
	assert.Equal(t, "upload", files[1].Source)
	assert.Equal(t, "notes.txt", files[2].Name)
}

func TestListInputFiles_ResolvedAfterPendingCleared(t *testing.T) {
	// Register a pending input with staged files, then GetAndRemove it from
	// the registry (simulating delivery). The state file carries a matching
	// ResolvedInput record. The endpoint must still return the catalog by
	// falling back to the state lookup.
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	staged := []wfstate.FileRecord{
		{Name: "spec.pdf", Size: 1024, ContentType: "application/pdf", Source: "display"},
	}
	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:       runID,
		AgentID:     "main",
		AskID:     "inp-cleared",
		Prompt:      "review and reply",
		CreatedAt:   time.Now(),
		StagedFiles: staged,
	}))
	_, ok := srv.pendingRegistry.GetAndRemove("inp-cleared")
	require.True(t, ok)

	statePath := filepath.Join(srv.runManager.stateDir, runID+".json")
	raw, err := os.ReadFile(statePath)
	require.NoError(t, err)
	var ws wfstate.WorkflowState
	require.NoError(t, json.Unmarshal(raw, &ws))
	ws.ResolvedInputs = append(ws.ResolvedInputs, wfstate.ResolvedInput{
		AskID:      "inp-cleared",
		AgentID:      "main",
		Prompt:       "review and reply",
		ResponseText: "ok",
		StagedFiles:  staged,
		UploadedFiles: []wfstate.FileRecord{
			{Name: "answers.txt", Size: 32, ContentType: "text/plain", Source: "upload"},
		},
		ResolvedAt: time.Now(),
	})
	out, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, out, 0o644))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/asks/inp-cleared/files")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var files []inputFileMetadata
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&files))
	require.Len(t, files, 2)
	assert.Equal(t, "spec.pdf", files[0].Name)
	assert.Equal(t, "answers.txt", files[1].Name)
}

func TestListInputFiles_EmptyReturnsArrayNotNull(t *testing.T) {
	// A pending input with no staged files (text-only or slot-mode pre-upload)
	// must serialize to a JSON array `[]`, not `null`, so the UI can iterate
	// without a null check and the response shape stays consistent across
	// pending and resolved branches.
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-empty",
		Prompt:    "no files yet",
		CreatedAt: time.Now(),
	}))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/asks/inp-empty/files")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "[]", strings.TrimSpace(string(body)))
}

func TestListResolvedInputs_UnknownRun(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/runs/no-such-run/resolved-inputs")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestListResolvedInputs_EmptyReturnsEmptyArray(t *testing.T) {
	// A run that hasn't resolved any inputs yet must return `[]` so the UI
	// can iterate without a null check.
	_, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/resolved-inputs")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "[]", strings.TrimSpace(string(body)))
}

func TestListResolvedInputs_ReturnsPromptResponseAndFiles(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	statePath := filepath.Join(srv.runManager.stateDir, runID+".json")
	raw, err := os.ReadFile(statePath)
	require.NoError(t, err)
	var ws wfstate.WorkflowState
	require.NoError(t, json.Unmarshal(raw, &ws))
	entered := time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Second)
	resolved := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	ws.ResolvedInputs = append(ws.ResolvedInputs,
		wfstate.ResolvedInput{
			AskID:      "inp-1",
			AgentID:      "main",
			Prompt:       "review the chart",
			NextState:    "step-2.md",
			ResponseText: "looks good",
			StagedFiles: []wfstate.FileRecord{
				{Name: "chart.png", Size: 512, ContentType: "image/png", Source: "display"},
			},
			EnteredAt:  entered,
			ResolvedAt: resolved,
		},
		wfstate.ResolvedInput{
			AskID:      "inp-2",
			AgentID:      "main",
			Prompt:       "upload corrections",
			ResponseText: "see attached",
			UploadedFiles: []wfstate.FileRecord{
				{Name: "fixed.csv", Size: 200, ContentType: "text/csv", Source: "upload"},
				{Name: "notes.txt", Size: 64, ContentType: "text/plain", Source: "upload"},
			},
			ResolvedAt: resolved.Add(30 * time.Second),
		},
	)
	out, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, out, 0o644))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/resolved-inputs")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []resolvedInputResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Len(t, got, 2)

	assert.Equal(t, "inp-1", got[0].AskID)
	assert.Equal(t, "main", got[0].AgentID)
	assert.Equal(t, "review the chart", got[0].Prompt)
	assert.Equal(t, "step-2.md", got[0].NextState)
	assert.Equal(t, "looks good", got[0].ResponseText)
	require.Len(t, got[0].StagedFiles, 1)
	assert.Equal(t, "chart.png", got[0].StagedFiles[0].Name)
	assert.Equal(t, int64(512), got[0].StagedFiles[0].Size)
	assert.Equal(t, "image/png", got[0].StagedFiles[0].ContentType)
	assert.Equal(t, "display", got[0].StagedFiles[0].Source)
	assert.Empty(t, got[0].UploadedFiles)
	require.NotNil(t, got[0].EnteredAt)
	assert.Equal(t, entered.Format(time.RFC3339), *got[0].EnteredAt)
	require.NotNil(t, got[0].ResolvedAt)
	assert.Equal(t, resolved.Format(time.RFC3339), *got[0].ResolvedAt)

	assert.Equal(t, "inp-2", got[1].AskID)
	assert.Empty(t, got[1].StagedFiles)
	require.Len(t, got[1].UploadedFiles, 2)
	assert.Equal(t, "fixed.csv", got[1].UploadedFiles[0].Name)
	assert.Equal(t, "upload", got[1].UploadedFiles[0].Source)
	assert.Equal(t, "notes.txt", got[1].UploadedFiles[1].Name)
}

func TestListResolvedInputs_OmitsZeroTimestampsAndEmptyFileFields(t *testing.T) {
	// A text-only resolved input (no files, no timestamps recorded) must
	// serialize without staged_files/uploaded_files/entered_at/resolved_at
	// keys so the UI can rely on omitempty for those fields.
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	statePath := filepath.Join(srv.runManager.stateDir, runID+".json")
	raw, err := os.ReadFile(statePath)
	require.NoError(t, err)
	var ws wfstate.WorkflowState
	require.NoError(t, json.Unmarshal(raw, &ws))
	ws.ResolvedInputs = append(ws.ResolvedInputs, wfstate.ResolvedInput{
		AskID:      "inp-text",
		AgentID:      "main",
		Prompt:       "yes or no?",
		ResponseText: "yes",
	})
	out, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, out, 0o644))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/resolved-inputs")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var rawArr []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rawArr))
	require.Len(t, rawArr, 1)
	_, hasStaged := rawArr[0]["staged_files"]
	_, hasUploaded := rawArr[0]["uploaded_files"]
	_, hasEntered := rawArr[0]["entered_at"]
	_, hasResolved := rawArr[0]["resolved_at"]
	assert.False(t, hasStaged, "text-only resolved input should omit staged_files")
	assert.False(t, hasUploaded, "text-only resolved input should omit uploaded_files")
	assert.False(t, hasEntered, "missing EnteredAt should omit entered_at")
	assert.False(t, hasResolved, "missing ResolvedAt should omit resolved_at")
	assert.Equal(t, "inp-text", rawArr[0]["ask_id"])
	assert.Equal(t, "yes", rawArr[0]["response_text"])
}

func TestListPendingInputs_BucketModeReturnsBucketObject(t *testing.T) {
	// Bucket mode must always emit a bucket sub-object so the client can
	// rely on (mode == "bucket") <=> (bucket != null), even when no
	// constraints were declared.
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	affordance := &parsing.FileAffordance{
		Mode: parsing.ModeBucket,
		Bucket: parsing.BucketSpec{
			MaxCount:       3,
			MaxSizePerFile: 1024,
			MIME:           []string{"image/png"},
		},
	}
	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:          runID,
		AgentID:        "main",
		AskID:        "inp-bucket",
		Prompt:         "attach images",
		CreatedAt:      time.Now(),
		FileAffordance: affordance,
	}))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/pending-asks")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var inputs []pendingInputResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&inputs))
	require.Len(t, inputs, 1)
	require.NotNil(t, inputs[0].FileAffordance)
	assert.Equal(t, "bucket", inputs[0].FileAffordance.Mode)
	require.NotNil(t, inputs[0].FileAffordance.Bucket)
	assert.Equal(t, 3, inputs[0].FileAffordance.Bucket.MaxCount)
	assert.Equal(t, int64(1024), inputs[0].FileAffordance.Bucket.MaxSizePerFile)
	assert.Equal(t, []string{"image/png"}, inputs[0].FileAffordance.Bucket.MIME)
	assert.Empty(t, inputs[0].FileAffordance.Slots)
}

func TestListInputFiles_UnknownRun(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/runs/no-such-run/asks/inp-1/files")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestListInputFiles_UnknownInput(t *testing.T) {
	_, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/asks/no-such-input/files")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestListPendingInputs_TextOnlyHasNoFileFields(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-text",
		Prompt:    "yes or no?",
		CreatedAt: time.Now(),
	}))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/pending-asks")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var inputs []pendingInputResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&inputs))
	require.Len(t, inputs, 1)
	assert.Nil(t, inputs[0].FileAffordance)
	assert.Empty(t, inputs[0].StagedFiles)

	// Verify the JSON omits the file fields entirely (omitempty round-trip).
	resp2, err := http.Get(ts.URL + "/runs/" + runID + "/pending-asks")
	require.NoError(t, err)
	defer resp2.Body.Close()
	var raw []map[string]any
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&raw))
	require.Len(t, raw, 1)
	_, hasAffordance := raw[0]["file_affordance"]
	_, hasStaged := raw[0]["staged_files"]
	assert.False(t, hasAffordance, "text-only pending input should omit file_affordance")
	assert.False(t, hasStaged, "text-only pending input should omit staged_files")
}

func TestListPendingInputs_SlotModeReturnsSlots(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	affordance := &parsing.FileAffordance{
		Mode: parsing.ModeSlot,
		Slots: []parsing.SlotSpec{
			{Name: "resume.pdf", MIME: []string{"application/pdf"}},
			{Name: "cover.pdf", MIME: []string{"application/pdf", "text/plain"}},
		},
	}
	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:          runID,
		AgentID:        "main",
		AskID:        "inp-slot",
		Prompt:         "upload your application",
		CreatedAt:      time.Now(),
		FileAffordance: affordance,
	}))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/pending-asks")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var inputs []pendingInputResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&inputs))
	require.Len(t, inputs, 1)
	require.NotNil(t, inputs[0].FileAffordance)
	assert.Equal(t, "slot", inputs[0].FileAffordance.Mode)
	require.Len(t, inputs[0].FileAffordance.Slots, 2)
	assert.Equal(t, "resume.pdf", inputs[0].FileAffordance.Slots[0].Name)
	assert.Equal(t, []string{"application/pdf"}, inputs[0].FileAffordance.Slots[0].MIME)
	assert.Equal(t, "cover.pdf", inputs[0].FileAffordance.Slots[1].Name)
	assert.Equal(t, []string{"application/pdf", "text/plain"}, inputs[0].FileAffordance.Slots[1].MIME)
	assert.Nil(t, inputs[0].FileAffordance.Bucket)
	assert.Empty(t, inputs[0].StagedFiles)
}

func TestListPendingInputs_DisplayOnlyReturnsStagedFiles(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	affordance := &parsing.FileAffordance{
		Mode: parsing.ModeDisplayOnly,
		DisplayFiles: []parsing.DisplaySpec{
			{SourcePath: "out/report.pdf", DisplayName: "Final Report"},
		},
	}
	staged := []wfstate.FileRecord{
		{Name: "Final Report", Size: 4096, ContentType: "application/pdf", Source: "display"},
	}
	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:          runID,
		AgentID:        "main",
		AskID:        "inp-display",
		Prompt:         "review the report",
		CreatedAt:      time.Now(),
		FileAffordance: affordance,
		StagedFiles:    staged,
	}))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/pending-asks")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var inputs []pendingInputResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&inputs))
	require.Len(t, inputs, 1)
	require.NotNil(t, inputs[0].FileAffordance)
	assert.Equal(t, "display_only", inputs[0].FileAffordance.Mode)
	require.Len(t, inputs[0].FileAffordance.DisplayFiles, 1)
	assert.Equal(t, "out/report.pdf", inputs[0].FileAffordance.DisplayFiles[0].SourcePath)
	assert.Equal(t, "Final Report", inputs[0].FileAffordance.DisplayFiles[0].DisplayName)
	require.Len(t, inputs[0].StagedFiles, 1)
	assert.Equal(t, "Final Report", inputs[0].StagedFiles[0].Name)
	assert.Equal(t, int64(4096), inputs[0].StagedFiles[0].Size)
	assert.Equal(t, "application/pdf", inputs[0].StagedFiles[0].ContentType)
	assert.Equal(t, "display", inputs[0].StagedFiles[0].Source)
}

// setupAgentTaskFolder rewrites the run's state file so the named agent's
// TaskFolder points at taskFolder. The handleGetInputFile handler reads this
// to compute the on-disk inputs subdirectory.
func setupAgentTaskFolder(t *testing.T, srv *Server, runID, agentID, taskFolder string) {
	t.Helper()
	statePath := filepath.Join(srv.runManager.stateDir, runID+".json")
	raw, err := os.ReadFile(statePath)
	require.NoError(t, err)
	var ws wfstate.WorkflowState
	require.NoError(t, json.Unmarshal(raw, &ws))
	updated := false
	for i := range ws.Agents {
		if ws.Agents[i].ID == agentID {
			ws.Agents[i].TaskFolder = taskFolder
			updated = true
			break
		}
	}
	if !updated {
		ws.Agents = append(ws.Agents, wfstate.AgentState{ID: agentID, TaskFolder: taskFolder})
	}
	out, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, out, 0o644))
}

// writeInputFile writes a file into the on-disk inputs subdirectory for an
// input step, mirroring what the staging code (bead 4) and the upload code
// (later beads) do at runtime.
func writeInputFile(t *testing.T, taskFolder, askID, name string, content []byte) string {
	t.Helper()
	dir := filepath.Join(taskFolder, "asks", askID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return path
}

func TestGetInputFile_ServesStagedDisplayFileForPendingAsk(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)
	content := []byte("\x89PNG\r\n\x1a\nfake-png-bytes")
	writeInputFile(t, taskFolder, "inp-pending", "chart.png", content)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-pending",
		Prompt:    "review",
		CreatedAt: time.Now(),
		StagedFiles: []wfstate.FileRecord{
			{Name: "chart.png", Size: int64(len(content)), ContentType: "image/png", Source: "display"},
		},
	}))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/asks/inp-pending/files/chart.png")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, content, body)
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
}

func TestGetInputFile_ServesUploadedFileForResolvedInput(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)
	content := []byte("name,value\nfoo,1\nbar,2\n")
	writeInputFile(t, taskFolder, "inp-resolved", "data.csv", content)

	statePath := filepath.Join(srv.runManager.stateDir, runID+".json")
	raw, err := os.ReadFile(statePath)
	require.NoError(t, err)
	var ws wfstate.WorkflowState
	require.NoError(t, json.Unmarshal(raw, &ws))
	ws.ResolvedInputs = append(ws.ResolvedInputs, wfstate.ResolvedInput{
		AskID: "inp-resolved",
		AgentID: "main",
		UploadedFiles: []wfstate.FileRecord{
			{Name: "data.csv", Size: int64(len(content)), ContentType: "text/csv", Source: "upload"},
		},
		ResolvedAt: time.Now(),
	})
	out, err := json.Marshal(ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, out, 0o644))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/asks/inp-resolved/files/data.csv")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, content, body)
	assert.Equal(t, "text/csv", resp.Header.Get("Content-Type"))
}

func TestGetInputFile_RejectsPathTraversal(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)
	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-trav",
		CreatedAt: time.Now(),
		StagedFiles: []wfstate.FileRecord{
			{Name: "ok.txt", Size: 0, ContentType: "text/plain", Source: "display"},
		},
	}))
	// Write a sibling file outside the input subdirectory; the handler must
	// not surface it via a "../" traversal.
	siblingDir := filepath.Join(taskFolder, "asks", "other")
	require.NoError(t, os.MkdirAll(siblingDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(siblingDir, "secret.txt"), []byte("nope"), 0o644))

	// %2e%2e%2f decodes to "../" — Go's ServeMux URL-decodes wildcard segments
	// before matching, so the traversal is observed by the handler. Plain "../"
	// is collapsed by the mux's cleanPath redirect before we ever see it, which
	// is also a valid defense; we test the encoded form because that is the
	// path that actually reaches the handler logic.
	for _, encoded := range []string{
		"%2e%2e%2fother%2fsecret.txt", // ../other/secret.txt (encoded slashes)
		"%2e%2e/other/secret.txt",     // .. via encoded dots, plain slashes
	} {
		resp, err := http.Get(ts.URL + "/runs/" + runID + "/asks/inp-trav/files/" + encoded)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, encoded)
		resp.Body.Close()
	}
}

func TestGetInputFile_DefaultDispositionIsAttachment(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)
	writeInputFile(t, taskFolder, "inp-disp", "report.pdf", []byte("%PDF-1.4 fake"))

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-disp",
		CreatedAt: time.Now(),
		StagedFiles: []wfstate.FileRecord{
			{Name: "report.pdf", Size: 13, ContentType: "application/pdf", Source: "display"},
		},
	}))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/asks/inp-disp/files/report.pdf")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cd := resp.Header.Get("Content-Disposition")
	assert.True(t, strings.HasPrefix(cd, "attachment;"), "expected attachment disposition by default, got %q", cd)
	assert.Contains(t, cd, `filename="report.pdf"`)
}

func TestGetInputFile_InlineHonoredForAllowlistedMime(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)
	writeInputFile(t, taskFolder, "inp-inline", "preview.png", []byte("png-bytes"))

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-inline",
		CreatedAt: time.Now(),
		StagedFiles: []wfstate.FileRecord{
			{Name: "preview.png", Size: 9, ContentType: "image/png", Source: "display"},
		},
	}))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/asks/inp-inline/files/preview.png?disposition=inline")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cd := resp.Header.Get("Content-Disposition")
	assert.True(t, strings.HasPrefix(cd, "inline;"), "expected inline for image/png, got %q", cd)
}

func TestGetInputFile_InlineDeniedForNonAllowlistedMime(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	cases := []struct {
		name        string
		recordName  string
		contentType string
	}{
		{"html_forced_attachment", "page.html", "text/html"},
		{"svg_forced_attachment", "icon.svg", "image/svg+xml"},
		{"plain_forced_attachment", "notes.txt", "text/plain"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			askID := fmt.Sprintf("inp-deny-%d", i)
			writeInputFile(t, taskFolder, askID, tc.recordName, []byte("x"))
			require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
				RunID:     runID,
				AgentID:   "main",
				AskID:   askID,
				CreatedAt: time.Now(),
				StagedFiles: []wfstate.FileRecord{
					{Name: tc.recordName, Size: 1, ContentType: tc.contentType, Source: "display"},
				},
			}))

			resp, err := http.Get(ts.URL + "/runs/" + runID + "/asks/" + askID + "/files/" + tc.recordName + "?disposition=inline")
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			cd := resp.Header.Get("Content-Disposition")
			assert.True(t, strings.HasPrefix(cd, "attachment;"),
				"%s: expected attachment despite ?disposition=inline, got %q", tc.contentType, cd)
		})
	}
}

func TestGetInputFile_DefenseHeadersAlwaysPresent(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)
	writeInputFile(t, taskFolder, "inp-headers", "img.png", []byte("png"))
	writeInputFile(t, taskFolder, "inp-headers", "page.html", []byte("<html></html>"))

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-headers",
		CreatedAt: time.Now(),
		StagedFiles: []wfstate.FileRecord{
			{Name: "img.png", Size: 3, ContentType: "image/png", Source: "display"},
			{Name: "page.html", Size: 13, ContentType: "text/html", Source: "display"},
		},
	}))

	for _, urlPath := range []string{
		"/runs/" + runID + "/asks/inp-headers/files/img.png",
		"/runs/" + runID + "/asks/inp-headers/files/img.png?disposition=inline",
		"/runs/" + runID + "/asks/inp-headers/files/page.html",
		"/runs/" + runID + "/asks/inp-headers/files/page.html?disposition=inline",
	} {
		resp, err := http.Get(ts.URL + urlPath)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, urlPath)
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"), urlPath)
		assert.Equal(t, "default-src 'none'; sandbox", resp.Header.Get("Content-Security-Policy"), urlPath)
		resp.Body.Close()
	}
}

func TestGetInputFile_UnknownRun(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/runs/no-such-run/asks/inp/files/foo.txt")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestGetInputFile_UnknownInput(t *testing.T) {
	_, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)
	resp, err := http.Get(ts.URL + "/runs/" + runID + "/asks/no-such-input/files/foo.txt")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestGetInputFile_UnknownFileInCatalog(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)
	// File on disk but not in the catalog — must 404 (catalog is authoritative).
	writeInputFile(t, taskFolder, "inp-cat", "ghost.txt", []byte("hi"))
	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-cat",
		CreatedAt: time.Now(),
	}))

	resp, err := http.Get(ts.URL + "/runs/" + runID + "/asks/inp-cat/files/ghost.txt")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// multipartFile is a single file part for buildMultipartBody. fieldName is
// the form field (slot name in slot mode); filename is the FileHeader name
// (used by the bucket-mode path); content is the body.
type multipartFile struct {
	fieldName string
	filename  string
	content   []byte
}

// buildMultipartBody assembles a multipart/form-data body containing a
// "response" text field followed by file parts. It returns the body and
// the Content-Type header (including the random boundary).
func buildMultipartBody(t *testing.T, response string, files []multipartFile) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	require.NoError(t, w.WriteField("response", response))
	for _, f := range files {
		part, err := w.CreateFormFile(f.fieldName, f.filename)
		require.NoError(t, err)
		_, err = part.Write(f.content)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return body, w.FormDataContentType()
}

// postMultipart posts a multipart submission to the deliver endpoint.
func postMultipart(t *testing.T, ts *httptest.Server, runID, askID, response string, files []multipartFile) *http.Response {
	t.Helper()
	body, ct := buildMultipartBody(t, response, files)
	resp, err := http.Post(ts.URL+"/runs/"+runID+"/asks/"+askID, ct, body)
	require.NoError(t, err)
	return resp
}

// decodeUploadError reads a uploadErrorResponse body.
func decodeUploadError(t *testing.T, body io.Reader) uploadErrorResponse {
	t.Helper()
	var er uploadErrorResponse
	require.NoError(t, json.NewDecoder(body).Decode(&er))
	return er
}

func TestDeliverInput_Multipart_SlotMode_Success(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-slot",
		Prompt:    "upload your application",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode: parsing.ModeSlot,
			Slots: []parsing.SlotSpec{
				{Name: "resume.pdf"},
				{Name: "cover.txt"},
			},
		},
	}))

	resp := postMultipart(t, ts, runID, "inp-slot", "all set", []multipartFile{
		{fieldName: "resume.pdf", filename: "resume.pdf", content: []byte("%PDF-1.4 fake-pdf")},
		{fieldName: "cover.txt", filename: "cover.txt", content: []byte("hello cover letter")},
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Files renamed into the input subdirectory.
	inputDir := filepath.Join(taskFolder, "asks", "inp-slot")
	resume, err := os.ReadFile(filepath.Join(inputDir, "resume.pdf"))
	require.NoError(t, err)
	assert.Equal(t, []byte("%PDF-1.4 fake-pdf"), resume)
	cover, err := os.ReadFile(filepath.Join(inputDir, "cover.txt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("hello cover letter"), cover)

	// Staging directory removed.
	entries, err := os.ReadDir(inputDir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".staging-"),
			"staging dir leaked: %s", e.Name())
	}

	// Pending input was claimed.
	_, ok := srv.pendingRegistry.Get("inp-slot")
	assert.False(t, ok, "expected pending input to be claimed after delivery")
}

func TestDeliverInput_Multipart_SlotMode_MissingSlot(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)
	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-miss",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode: parsing.ModeSlot,
			Slots: []parsing.SlotSpec{
				{Name: "resume.pdf"},
				{Name: "cover.txt"},
			},
		},
	}))

	resp := postMultipart(t, ts, runID, "inp-miss", "", []multipartFile{
		{fieldName: "resume.pdf", filename: "resume.pdf", content: []byte("only one slot")},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	er := decodeUploadError(t, resp.Body)
	assert.Equal(t, "slot_missing", er.Constraint)
	assert.Contains(t, er.Error, "cover.txt")
}

func TestDeliverInput_Multipart_SlotMode_ExtraSlot(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)
	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-extra",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode: parsing.ModeSlot,
			Slots: []parsing.SlotSpec{
				{Name: "resume.pdf"},
			},
		},
	}))

	resp := postMultipart(t, ts, runID, "inp-extra", "", []multipartFile{
		{fieldName: "resume.pdf", filename: "resume.pdf", content: []byte("ok")},
		{fieldName: "extra.txt", filename: "extra.txt", content: []byte("nope")},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	er := decodeUploadError(t, resp.Body)
	assert.Equal(t, "slot_extra", er.Constraint)
	assert.Contains(t, er.Error, "extra.txt")
}

func TestDeliverInput_Multipart_SlotMode_DuplicatePartForSameSlot(t *testing.T) {
	// Two parts with the same fieldName matching a declared slot must be
	// rejected as slot_extra (not slot_missing): the slot was provided,
	// just more than once. "Every declared slot once" is the rule.
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)
	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-dup-slot",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode: parsing.ModeSlot,
			Slots: []parsing.SlotSpec{
				{Name: "resume.pdf"},
			},
		},
	}))

	resp := postMultipart(t, ts, runID, "inp-dup-slot", "", []multipartFile{
		{fieldName: "resume.pdf", filename: "first.pdf", content: []byte("a")},
		{fieldName: "resume.pdf", filename: "second.pdf", content: []byte("b")},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	er := decodeUploadError(t, resp.Body)
	assert.Equal(t, "slot_extra", er.Constraint)
	assert.Contains(t, er.Error, "resume.pdf")
}

func TestDeliverInput_Multipart_BucketMode_Success(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)
	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-bucket",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode: parsing.ModeBucket,
			Bucket: parsing.BucketSpec{
				MaxCount:       3,
				MaxSizePerFile: 1024,
				MaxTotalSize:   2048,
			},
		},
	}))

	resp := postMultipart(t, ts, runID, "inp-bucket", "ok", []multipartFile{
		{fieldName: "files", filename: "first.txt", content: []byte("alpha alpha alpha")},
		{fieldName: "files", filename: "second.txt", content: []byte("beta beta")},
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	first, err := os.ReadFile(filepath.Join(taskFolder, "asks", "inp-bucket", "first.txt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("alpha alpha alpha"), first)
	second, err := os.ReadFile(filepath.Join(taskFolder, "asks", "inp-bucket", "second.txt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("beta beta"), second)
}

func TestDeliverInput_Multipart_BucketMode_PerFileSizeExceeded(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)
	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-pf",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode: parsing.ModeBucket,
			Bucket: parsing.BucketSpec{
				MaxSizePerFile: 8,
				MaxTotalSize:   1024,
				MaxCount:       4,
			},
		},
	}))

	resp := postMultipart(t, ts, runID, "inp-pf", "", []multipartFile{
		{fieldName: "files", filename: "big.txt", content: []byte("this is way too long")},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	er := decodeUploadError(t, resp.Body)
	assert.Equal(t, "max_file_size", er.Constraint)
}

func TestDeliverInput_Multipart_BucketMode_TotalSizeExceeded(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)
	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-total",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode: parsing.ModeBucket,
			Bucket: parsing.BucketSpec{
				MaxSizePerFile: 1024,
				// Total cap small enough that two normal files exceed it,
				// but each one alone fits under MaxSizePerFile. The
				// MaxBytesReader wrapper trips before the per-part loop
				// runs, so this is the path that produces the structured
				// "max_total_size" rejection during ParseMultipartForm.
				MaxTotalSize: 64,
				MaxCount:     4,
			},
		},
	}))

	resp := postMultipart(t, ts, runID, "inp-total", "", []multipartFile{
		{fieldName: "files", filename: "a.txt", content: bytes.Repeat([]byte("a"), 60)},
		{fieldName: "files", filename: "b.txt", content: bytes.Repeat([]byte("b"), 60)},
	})
	defer resp.Body.Close()
	require.True(t, resp.StatusCode >= 400 && resp.StatusCode < 500,
		"expected 4xx, got %d", resp.StatusCode)
	er := decodeUploadError(t, resp.Body)
	assert.Equal(t, "max_total_size", er.Constraint)
}

func TestDeliverInput_Multipart_BucketMode_CountExceeded(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)
	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-count",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode: parsing.ModeBucket,
			Bucket: parsing.BucketSpec{
				MaxCount: 1,
			},
		},
	}))

	resp := postMultipart(t, ts, runID, "inp-count", "", []multipartFile{
		{fieldName: "files", filename: "a.txt", content: []byte("a")},
		{fieldName: "files", filename: "b.txt", content: []byte("b")},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	er := decodeUploadError(t, resp.Body)
	assert.Equal(t, "max_count", er.Constraint)
}

func TestDeliverInput_Multipart_BucketMode_MIMEAllowlist(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)
	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-mime",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode: parsing.ModeBucket,
			Bucket: parsing.BucketSpec{
				MIME: []string{"image/png"},
			},
		},
	}))

	resp := postMultipart(t, ts, runID, "inp-mime", "", []multipartFile{
		{fieldName: "files", filename: "notes.txt", content: []byte("plain text not on the list")},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	er := decodeUploadError(t, resp.Body)
	assert.Equal(t, "mime_not_allowed", er.Constraint)
}

func TestDeliverInput_Multipart_FilenameNormalizationRejection(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)
	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-name",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode: parsing.ModeBucket,
			Bucket: parsing.BucketSpec{
				MaxCount: 4,
			},
		},
	}))

	// Leading dot is rejected by NormalizeUploadFilename.
	resp := postMultipart(t, ts, runID, "inp-name", "", []multipartFile{
		{fieldName: "files", filename: ".hidden", content: []byte("x")},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	er := decodeUploadError(t, resp.Body)
	assert.Equal(t, "filename", er.Constraint)
}

func TestDeliverInput_Multipart_DuplicateFilenameInSubmission(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)
	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-dup",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode:   parsing.ModeBucket,
			Bucket: parsing.BucketSpec{MaxCount: 4},
		},
	}))

	resp := postMultipart(t, ts, runID, "inp-dup", "", []multipartFile{
		{fieldName: "files", filename: "data.txt", content: []byte("first")},
		{fieldName: "files", filename: "data.txt", content: []byte("second")},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	er := decodeUploadError(t, resp.Body)
	assert.Equal(t, "duplicate_filename", er.Constraint)
}

func TestDeliverInput_Multipart_CollisionWithStagedFile(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)
	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-coll",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode:   parsing.ModeBucket,
			Bucket: parsing.BucketSpec{MaxCount: 4},
		},
		StagedFiles: []wfstate.FileRecord{
			{Name: "report.pdf", Size: 10, ContentType: "application/pdf", Source: "display"},
		},
	}))

	resp := postMultipart(t, ts, runID, "inp-coll", "", []multipartFile{
		{fieldName: "files", filename: "report.pdf", content: []byte("collides")},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	er := decodeUploadError(t, resp.Body)
	assert.Equal(t, "collision_with_staged", er.Constraint)
}

func TestDeliverInput_Multipart_FailedValidationLeavesInputClaimable(t *testing.T) {
	srv, ts, fake := newTestServer(t)

	// Use a fake that consumes the AskInputCh so we can verify the
	// follow-up JSON delivery actually reaches the orchestrator after a
	// failed multipart validation. The pending entry must remain in the
	// registry across the failed multipart attempt, with no files left in
	// the input subdirectory.
	delivered := make(chan orchestrator.AskInput, 1)
	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(bus.New())
		}
		select {
		case in := <-opts.AskInputCh:
			delivered <- in
		case <-ctx.Done():
			return ctx.Err()
		}
		<-ctx.Done()
		return ctx.Err()
	}
	createResp, err := http.Post(ts.URL+"/runs", "application/json",
		strings.NewReader(`{"workflow_id": "test-workflow"}`))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	var cr createRunResponse
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&cr))
	runID := cr.RunID

	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-recover",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode: parsing.ModeBucket,
			Bucket: parsing.BucketSpec{
				MaxSizePerFile: 4,
				MaxCount:       2,
			},
		},
	}))

	// First request fails validation (per-file size).
	resp := postMultipart(t, ts, runID, "inp-recover", "", []multipartFile{
		{fieldName: "files", filename: "huge.txt", content: []byte("way too big")},
	})
	resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Input subdirectory must be untouched. Either it was never created
	// (handler short-circuits before mkdir) or it exists but is empty —
	// no files, no leftover staging directory.
	inputDir := filepath.Join(taskFolder, "asks", "inp-recover")
	if entries, statErr := os.ReadDir(inputDir); statErr == nil {
		assert.Empty(t, entries, "input subdirectory must be empty after failed validation")
	}

	// Pending entry still claimable: a follow-up JSON delivery succeeds.
	pi, ok := srv.pendingRegistry.Get("inp-recover")
	require.True(t, ok, "pending input must remain after failed multipart")
	assert.Equal(t, "inp-recover", pi.AskID)

	resp2, err := http.Post(
		ts.URL+"/runs/"+runID+"/asks/inp-recover",
		"application/json",
		strings.NewReader(`{"response": "fallback text"}`),
	)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	select {
	case in := <-delivered:
		assert.Equal(t, "fallback text", in.Response)
		assert.Empty(t, in.UploadedFiles)
	case <-time.After(5 * time.Second):
		t.Fatal("input was not delivered to orchestrator after recovery")
	}
}

func TestDeliverInput_Multipart_ConcurrentSecondSubmissionInputNotPending(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)
	taskFolder := t.TempDir()
	setupAgentTaskFolder(t, srv, runID, "main", taskFolder)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-race",
		CreatedAt: time.Now(),
		FileAffordance: &parsing.FileAffordance{
			Mode:   parsing.ModeBucket,
			Bucket: parsing.BucketSpec{MaxCount: 4},
		},
	}))

	// Simulate the winning concurrent submission having already claimed
	// the input via GetAndRemove. The losing submission's Peek then
	// observes "not pending" and must surface the structured 4xx, not 409.
	_, ok := srv.pendingRegistry.GetAndRemove("inp-race")
	require.True(t, ok)

	resp := postMultipart(t, ts, runID, "inp-race", "", []multipartFile{
		{fieldName: "files", filename: "a.txt", content: []byte("late")},
	})
	defer resp.Body.Close()
	require.True(t, resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusConflict,
		"expected 4xx but not 409, got %d", resp.StatusCode)
	er := decodeUploadError(t, resp.Body)
	assert.Equal(t, "input_not_pending", er.Constraint)
}

func TestDeliverInput_JSON_TextOnlyAskStillWorks(t *testing.T) {
	// Sanity-check that introducing the multipart branch did not regress
	// the JSON path for text-only asks. This is a focused complement to
	// TestDeliverInput, which exercises the full orchestrator round-trip.
	srv, ts, fake := newTestServer(t)
	runID := createPendingRunForFiles(t, ts, fake)

	require.NoError(t, srv.pendingRegistry.Register(PendingAsk{
		RunID:     runID,
		AgentID:   "main",
		AskID:   "inp-json",
		Prompt:    "yes or no",
		CreatedAt: time.Now(),
	}))

	resp, err := http.Post(
		ts.URL+"/runs/"+runID+"/asks/inp-json",
		"application/json",
		strings.NewReader(`{"response": "yes"}`),
	)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_, ok := srv.pendingRegistry.Get("inp-json")
	assert.False(t, ok, "JSON delivery must claim the pending input")
}

func TestCamelToSnake(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"StateStarted", "state_started"},
		{"WorkflowCompleted", "workflow_completed"},
		{"AgentAskStarted", "agent_ask_started"},
		{"ErrorOccurred", "error_occurred"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.expected, camelToSnake(tt.input), "camelToSnake(%q)", tt.input)
	}
}

// frameSSE wraps marshalled SSE JSON in the on-the-wire `data: …\n\n` frame
// used by the streaming handler so we can assert against the final shape a
// browser would see.
func frameSSE(t *testing.T, event any) (frame string, payload map[string]any) {
	t.Helper()
	data, err := marshalSSEEvent(event)
	require.NoError(t, err)
	frame = fmt.Sprintf("data: %s\n\n", data)
	require.NoError(t, json.Unmarshal(data, &payload))
	return frame, payload
}

func TestMarshalSSEEvent_ShutdownRequested(t *testing.T) {
	requestedAt := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	evt := events.ShutdownRequested{
		ActiveRuns: []events.ActiveRunSnapshot{
			{ID: "run-1", Workflow: "wf-a", Status: "running"},
			{ID: "run-2", Workflow: "wf-b", Status: "paused"},
		},
		RequestedAt: requestedAt,
	}

	frame, payload := frameSSE(t, evt)

	assert.True(t, strings.HasPrefix(frame, "data: {"), "frame should start with `data: {`, got %q", frame)
	assert.True(t, strings.HasSuffix(frame, "\n\n"), "frame should end with `\\n\\n`")

	assert.Equal(t, "shutdown_requested", payload["type"])

	// active_runs: present, an array, with snake_case per-element keys.
	rawRuns, ok := payload["active_runs"].([]any)
	require.True(t, ok, "active_runs should be an array, got %T", payload["active_runs"])
	require.Len(t, rawRuns, 2)
	first, ok := rawRuns[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "run-1", first["id"])
	assert.Equal(t, "wf-a", first["workflow"])
	assert.Equal(t, "running", first["status"])

	// Tier-timeout fields must not appear on the wire.
	_, hasT1 := payload["tier_1_timeout_secs"]
	assert.False(t, hasT1, "tier_1_timeout_secs should not appear in ShutdownRequested JSON")
	_, hasT2 := payload["tier_2_timeout_secs"]
	assert.False(t, hasT2, "tier_2_timeout_secs should not appear in ShutdownRequested JSON")

	// requested_at round-trips as an RFC3339 string.
	rfcStr, ok := payload["requested_at"].(string)
	require.True(t, ok, "requested_at should be a string, got %T", payload["requested_at"])
	parsed, err := time.Parse(time.RFC3339Nano, rfcStr)
	require.NoError(t, err)
	assert.True(t, parsed.Equal(requestedAt), "requested_at should round-trip; got %s", rfcStr)
}

func TestMarshalSSEEvent_ShutdownComplete(t *testing.T) {
	evt := events.ShutdownComplete{
		Outcomes: map[string]string{
			"run-1": "quiesced",
			"run-2": "cancelled",
		},
	}

	frame, payload := frameSSE(t, evt)

	assert.True(t, strings.HasPrefix(frame, "data: {"))
	assert.True(t, strings.HasSuffix(frame, "\n\n"))

	assert.Equal(t, "shutdown_complete", payload["type"])

	outcomes, ok := payload["outcomes"].(map[string]any)
	require.True(t, ok, "outcomes should be an object, got %T", payload["outcomes"])
	assert.Equal(t, "quiesced", outcomes["run-1"])
	assert.Equal(t, "cancelled", outcomes["run-2"])
}

// fakeShutdownDriver is the test double for ShutdownCoordinator. It mirrors
// the production coordinator's subscribe-or-start contract: the first
// BeginQuiesce call counts as the sequence start (started=1); subsequent
// calls are no-ops. Phase emission and the terminal Finish() transition are
// driven explicitly by tests.
type fakeShutdownDriver struct {
	mu sync.Mutex

	started       int32 // number of BeginQuiesce calls that actually started the sequence
	cancelEngaged bool
	cancelCalls   int

	// done closes when the test calls Finish(); WaitComplete returns this.
	done   chan struct{}
	result ShutdownResult

	// Progress fan-out state — mirrors ShutdownCoordinator's design so a
	// late subscriber sees the replay.
	phases       []TierPhase
	subscribers  []chan TierPhase
	phasesClosed bool
}

func newFakeShutdownDriver() *fakeShutdownDriver {
	return &fakeShutdownDriver{done: make(chan struct{})}
}

func (f *fakeShutdownDriver) BeginQuiesce(_ context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.started == 0 {
		f.started = 1
	}
}

func (f *fakeShutdownDriver) EscalateToCancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Strict no-op if not started or already engaged.
	if f.started == 0 || f.cancelEngaged {
		return
	}
	f.cancelEngaged = true
	f.cancelCalls++
}

func (f *fakeShutdownDriver) InProgress() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started > 0
}

func (f *fakeShutdownDriver) WaitComplete() <-chan struct{} {
	return f.done
}

func (f *fakeShutdownDriver) Result() ShutdownResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.result
}

func (f *fakeShutdownDriver) Progress() <-chan TierPhase {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan TierPhase, 16)
	for _, p := range f.phases {
		ch <- p
	}
	if f.phasesClosed {
		close(ch)
		return ch
	}
	f.subscribers = append(f.subscribers, ch)
	return ch
}

// EmitPhase appends p to the replay history and fans it out to every
// current subscriber. Buffer size on each subscriber is large so sends
// never block in tests.
func (f *fakeShutdownDriver) EmitPhase(p TierPhase) {
	f.mu.Lock()
	f.phases = append(f.phases, p)
	subs := append([]chan TierPhase(nil), f.subscribers...)
	f.mu.Unlock()
	for _, s := range subs {
		s <- p
	}
}

// Finish completes the in-flight sequence: WaitComplete unblocks and every
// Progress subscriber's channel closes. Idempotent.
func (f *fakeShutdownDriver) Finish(result ShutdownResult) {
	f.mu.Lock()
	if f.phasesClosed {
		f.mu.Unlock()
		return
	}
	f.result = result
	f.phasesClosed = true
	subs := f.subscribers
	f.subscribers = nil
	f.mu.Unlock()
	close(f.done)
	for _, s := range subs {
		close(s)
	}
}

func (f *fakeShutdownDriver) Started() int32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

// installFakeShutdownDriver wires the fake driver onto the server. Tests
// use this instead of SetShutdownCoordinator (which takes *ShutdownCoordinator)
// so the fake's recorded sequence-start count can be inspected.
func installFakeShutdownDriver(srv *Server, fake *fakeShutdownDriver) {
	srv.shutdownDriver = fake
}

// readShutdownStream reads the full POST /shutdown response body and splits
// it into trimmed non-empty lines. Used by happy-path / summary assertions.
func readShutdownStream(t *testing.T, body io.Reader) []string {
	t.Helper()
	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if s := strings.TrimSpace(l); s != "" {
			lines = append(lines, s)
		}
	}
	return lines
}

// TestHandleShutdown_HappyPath drives a full POST /shutdown through a real
// *ShutdownCoordinator backed by an empty fake fleet. With no active runs
// the coordinator emits PhaseQuiesce then PhaseComplete; the handler streams
// both markers and there are no per-run summary lines.
func TestHandleShutdown_HappyPath(t *testing.T) {
	srv, ts, _ := newTestServer(t)

	fleet := newFakeFleet()
	coord := NewShutdownCoordinator(fleet, nil)
	srv.SetShutdownCoordinator(coord)

	resp, err := http.Post(ts.URL+"/shutdown", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain"))

	lines := readShutdownStream(t, resp.Body)
	require.GreaterOrEqual(t, len(lines), 2)
	assert.Equal(t, string(PhaseQuiesce), lines[0])
	assert.Equal(t, string(PhaseComplete), lines[1])
	// No active runs → no per-run summary lines after PhaseComplete.
	assert.Len(t, lines, 2, "no active runs should yield no summary lines")
}

// TestHandleShutdown_HappyPath_RunOutcomeSummary covers the summary trailer
// for an active run: it drains during quiesce (before any cancel), so the
// trailer reads `<id>: quiesced`.
func TestHandleShutdown_HappyPath_RunOutcomeSummary(t *testing.T) {
	srv, ts, _ := newTestServer(t)

	fleet := newFakeFleet()
	fleet.addRun("alpha", false, false)
	coord := NewShutdownCoordinator(fleet, nil)
	srv.SetShutdownCoordinator(coord)

	// Drain shortly after the handler starts the sequence; this happens
	// during the quiesce phase since the handler never escalates to cancel.
	go func() {
		time.Sleep(20 * time.Millisecond)
		fleet.drain("alpha")
	}()

	resp, err := http.Post(ts.URL+"/shutdown", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	lines := readShutdownStream(t, resp.Body)
	require.Len(t, lines, 3, "expected quiesce, complete, and one summary line; got %v", lines)
	assert.Equal(t, string(PhaseQuiesce), lines[0])
	assert.Equal(t, string(PhaseComplete), lines[1])
	assert.Equal(t, "alpha: quiesced", lines[2])
}

// TestHandleShutdown_SequentialDoublePOSTEscalates is the HTTP analogue of
// the SIGINT-escalation integration test. A first POST starts BeginQuiesce
// against a run that does not drain on quiesce; the coordinator parks on
// PhaseQuiesce waiting for the run. A second POST issued while the first is
// still streaming observes InProgress()==true and dispatches
// EscalateToCancel, which drains the run via CancelAll. The sequence then
// completes within the patience window, and the per-run trailer classifies
// the run as `cancelled` (cancelEngagedAt was set before the drain). Both
// concurrent streams must see the same [Quiesce, Cancel, Complete] phases
// and the same `<id>: cancelled` summary.
func TestHandleShutdown_SequentialDoublePOSTEscalates(t *testing.T) {
	srv, ts, _ := newTestServer(t)

	fleet := newFakeFleet()
	// drainOnQuiesce=false / drainOnCancel=true: quiesce alone leaves the run
	// hanging, but CancelAll drains it synchronously so the escalated
	// sequence completes well within cancelPatienceWindow.
	fleet.addRun("alpha", false, true)
	coord := NewShutdownCoordinator(fleet, nil)
	srv.SetShutdownCoordinator(coord)

	type streamResult struct {
		lines []string
		err   error
	}

	// POST #1: read the response body line-by-line. The first line gates the
	// second POST so we can prove the escalation path (not the start-quiesce
	// path) is the one that drives the second call.
	firstHeadLine := make(chan string, 1)
	firstDone := make(chan streamResult, 1)
	go func() {
		resp, err := http.Post(ts.URL+"/shutdown", "", nil)
		if err != nil {
			firstDone <- streamResult{err: err}
			return
		}
		defer resp.Body.Close()
		reader := bufio.NewReader(resp.Body)
		head, err := reader.ReadString('\n')
		if err != nil {
			firstDone <- streamResult{err: err}
			return
		}
		firstHeadLine <- strings.TrimSpace(head)
		rest, err := io.ReadAll(reader)
		if err != nil {
			firstDone <- streamResult{err: err}
			return
		}
		lines := []string{strings.TrimSpace(head)}
		for _, l := range strings.Split(string(rest), "\n") {
			if s := strings.TrimSpace(l); s != "" {
				lines = append(lines, s)
			}
		}
		firstDone <- streamResult{lines: lines}
	}()

	// Wait for POST #1 to receive PhaseQuiesce. This guarantees the
	// coordinator has emitted its first phase and that InProgress() will
	// return true when the second POST arrives, so the handler dispatches
	// EscalateToCancel rather than starting a fresh sequence.
	select {
	case got := <-firstHeadLine:
		require.Equal(t, string(PhaseQuiesce), got)
	case <-time.After(2 * time.Second):
		t.Fatal("POST #1 did not emit PhaseQuiesce within 2s")
	}

	// POST #2: must escalate. The full sequence has to complete inside the
	// patience window even if the daemon were stuck (it isn't here, but the
	// timeout enforces the contract).
	ctx, cancel := context.WithTimeout(context.Background(), cancelPatienceWindow+2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/shutdown", nil)
	require.NoError(t, err)
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	secondLines := readShutdownStream(t, resp2.Body)
	require.GreaterOrEqual(t, len(secondLines), 4,
		"expected quiesce, cancel, complete, and one summary line; got %v", secondLines)
	assert.Contains(t, secondLines, string(PhaseQuiesce))
	assert.Contains(t, secondLines, string(PhaseCancel))
	assert.Contains(t, secondLines, string(PhaseComplete))
	assert.Contains(t, secondLines, "alpha: cancelled",
		"the run must be classified as cancelled because the escalation engaged before the drain")

	// POST #1 must observe the same terminal sequence: subscribe-or-start
	// shares one in-flight coordinator across both callers.
	select {
	case got := <-firstDone:
		require.NoError(t, got.err)
		assert.Contains(t, got.lines, string(PhaseQuiesce))
		assert.Contains(t, got.lines, string(PhaseCancel))
		assert.Contains(t, got.lines, string(PhaseComplete))
		assert.Contains(t, got.lines, "alpha: cancelled")
	case <-time.After(2 * time.Second):
		t.Fatal("POST #1 did not finish after the escalation")
	}
}

// TestSetShutdownCoordinator_NilStaysNil: a typed-nil *ShutdownCoordinator
// passed to the setter must store an untyped-nil interface, so the handler's
// nil-guard fires (503) instead of dispatching into a nil pointer and
// panicking on the first method call.
func TestSetShutdownCoordinator_NilStaysNil(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	var c *ShutdownCoordinator // typed nil
	srv.SetShutdownCoordinator(c)

	resp, err := http.Post(ts.URL+"/shutdown", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// TestHandleShutdown_ConcurrentCallersShareSequence: two concurrent POSTs
// both observe the full phase sequence ending in PhaseComplete, and the
// underlying driver started exactly one sequence (subscribe-or-start).
func TestHandleShutdown_ConcurrentCallersShareSequence(t *testing.T) {
	srv, ts, _ := newTestServer(t)

	fake := newFakeShutdownDriver()
	installFakeShutdownDriver(srv, fake)

	// Drive the sequence on a delayed timer so both handlers have time to
	// subscribe to Progress() before phases are emitted.
	go func() {
		time.Sleep(50 * time.Millisecond)
		fake.EmitPhase(PhaseQuiesce)
		fake.EmitPhase(PhaseCancel)
		fake.EmitPhase(PhaseComplete)
		fake.Finish(ShutdownResult{Outcomes: map[string]RunOutcome{
			"a": OutcomeQuiesced,
		}})
	}()

	type result struct {
		status int
		lines  []string
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			resp, err := http.Post(ts.URL+"/shutdown", "", nil)
			if err != nil {
				results <- result{status: 0}
				return
			}
			defer resp.Body.Close()
			results <- result{status: resp.StatusCode, lines: readShutdownStream(t, resp.Body)}
		}()
	}

	got1 := <-results
	got2 := <-results
	assert.Equal(t, http.StatusOK, got1.status)
	assert.Equal(t, http.StatusOK, got2.status)
	for _, g := range []result{got1, got2} {
		require.GreaterOrEqual(t, len(g.lines), 3)
		assert.Equal(t, string(PhaseQuiesce), g.lines[0])
		assert.Equal(t, string(PhaseCancel), g.lines[1])
		assert.Equal(t, string(PhaseComplete), g.lines[2])
		assert.Contains(t, g.lines, "a: quiesced")
	}

	// Subscribe-or-start: the driver counts the first BeginQuiesce as
	// starting the sequence; the second attaches without re-starting.
	assert.Equal(t, int32(1), fake.Started(),
		"the underlying phase sequence must execute exactly once")
}

// TestHandleShutdown_ClientDisconnectDoesNotCancel: closing the connection
// mid-stream must not abort the coordinator. After disconnect we verify the
// driver still completes by observing Progress() close and Finish having
// run.
func TestHandleShutdown_ClientDisconnectDoesNotCancel(t *testing.T) {
	srv, ts, _ := newTestServer(t)

	fake := newFakeShutdownDriver()
	installFakeShutdownDriver(srv, fake)

	// We control phase emission deliberately so we can disconnect between
	// the first marker and the rest of the sequence.
	phaseOneEmitted := make(chan struct{})
	go func() {
		fake.EmitPhase(PhaseQuiesce)
		close(phaseOneEmitted)
	}()

	// Open a request with a cancellable context. The handler must keep
	// driving the coordinator after our cancel.
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/shutdown", nil)
	require.NoError(t, err)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, string(PhaseQuiesce)+"\n", line)
	<-phaseOneEmitted

	// Hang up. The coordinator must run to completion regardless.
	cancel()
	_ = resp.Body.Close()

	// Now finish the sequence; observe via a fresh Progress() subscriber
	// that the channel actually closed — i.e. the Run goroutine inside the
	// handler continued past the disconnect to drive the sequence forward.
	go func() {
		// Brief settle so the handler goroutine has time to be the lone
		// blocker on fake.done.
		time.Sleep(10 * time.Millisecond)
		fake.EmitPhase(PhaseCancel)
		fake.EmitPhase(PhaseComplete)
		fake.Finish(ShutdownResult{Outcomes: map[string]RunOutcome{
			"survivor": OutcomeQuiesced,
		}})
	}()

	late := fake.Progress()
	deadline := time.After(2 * time.Second)
	var seen []TierPhase
loop:
	for {
		select {
		case p, ok := <-late:
			if !ok {
				break loop
			}
			seen = append(seen, p)
		case <-deadline:
			t.Fatalf("Progress channel did not close; coordinator was apparently cancelled by the client disconnect (seen=%v)", seen)
		}
	}
	assert.Contains(t, seen, PhaseComplete,
		"the coordinator must have run to completion after the client disconnected")

	// Driver was started exactly once.
	assert.Equal(t, int32(1), fake.Started())
}

// TestHandleShutdown_NotConfigured returns 503 when SetShutdownCoordinator
// has never been called.
func TestHandleShutdown_NotConfigured(t *testing.T) {
	_, ts, _ := newTestServer(t)
	// Intentionally do not call SetShutdownCoordinator.
	resp, err := http.Post(ts.URL+"/shutdown", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

// TestCreateRun_PreShutdownSucceeds: with a shutdown driver installed but
// BeginQuiesce not yet called (InProgress()==false), POST /runs continues to
// launch runs as today. Regression guard for the nil-check / InProgress()
// short-circuit.
func TestCreateRun_PreShutdownSucceeds(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	driver := newFakeShutdownDriver()
	installFakeShutdownDriver(srv, driver)

	body := `{"workflow_id": "test-workflow", "input": "hello"}`
	resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var cr createRunResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&cr))
	assert.NotEmpty(t, cr.RunID)
	assert.Equal(t, 1, fake.callCount(),
		"orchestrator must be invoked once when the driver is installed but no shutdown is in flight")
}

// TestCreateRun_ReturnsServiceUnavailableDuringShutdown: once the coordinator
// reports InProgress()==true, POST /runs returns 503 with the JSON error
// shape and the orchestrator is never invoked.
func TestCreateRun_ReturnsServiceUnavailableDuringShutdown(t *testing.T) {
	srv, ts, fake := newTestServer(t)
	driver := newFakeShutdownDriver()
	installFakeShutdownDriver(srv, driver)
	driver.BeginQuiesce(context.Background())

	body := `{"workflow_id": "test-workflow", "input": "hello"}`
	resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var er errorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&er))
	assert.Equal(t, "daemon shutting down", er.Error)

	// The orchestrator must not have been invoked: the 503 short-circuits
	// before LaunchRun.
	assert.Equal(t, 0, fake.callCount(),
		"no run should be launched when shutdown is in progress")
}

// TestCreateRun_NoShutdownDriverUnchanged: a Server constructed without a
// shutdown driver continues to accept POST /runs. The nil-check protects
// callers (CLI `ray run`, existing tests) that never install one.
func TestCreateRun_NoShutdownDriverUnchanged(t *testing.T) {
	_, ts, fake := newTestServer(t)
	// Intentionally do not install a shutdown driver.

	body := `{"workflow_id": "test-workflow", "input": "hello"}`
	resp, err := http.Post(ts.URL+"/runs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, 1, fake.callCount())
}

