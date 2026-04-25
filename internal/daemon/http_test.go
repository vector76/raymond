package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/orchestrator"
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
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
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
	// Precedence: request > manifest > server > hardcoded constant.
	assert.Equal(t, 3.0, resolveBudget(3.0, 2.0, 1.0))
	assert.Equal(t, 2.0, resolveBudget(0, 2.0, 1.0))
	assert.Equal(t, 1.0, resolveBudget(0, 0, 1.0))
	assert.Equal(t, daemonDefaultBudgetUSD, resolveBudget(0, 0, 0))
	// Negative values are treated as unset at every level.
	assert.Equal(t, 2.0, resolveBudget(-5, 2.0, 1.0))
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
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
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
	// must fall back to daemonDefaultBudgetUSD. Before the fix this landed at
	// $0.00, which caused the markdown executor's budget-exceeded guard to
	// synthesize a terminating <result> on the first Claude call, bypassing
	// the state's await policy and ending the run immediately.
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
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
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
	rm, err := newRunManagerWithOrchestrator(stateDir, "/tmp", fake)
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

	// Set up a fake orchestrator that emits an await event.
	awaitReady := make(chan struct{})
	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		b := bus.New()
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(b)
		}

		b.Emit(events.AgentAwaitStarted{
			AgentID:   "main",
			InputID:   "test-input-1",
			Prompt:    "What should I do?",
			NextState: "NEXT.md",
			Timestamp: time.Now(),
		})

		// In daemon mode the callback registers the pending input.
		if opts.AwaitCallback != nil {
			opts.AwaitCallback("main", "test-input-1", "What should I do?", "NEXT.md")
		}

		close(awaitReady)

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

	// Wait for await to be registered.
	select {
	case <-awaitReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for await")
	}

	// List pending inputs.
	resp, err := http.Get(ts.URL + "/runs/" + cr.RunID + "/pending-inputs")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var inputs []pendingInputResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&inputs))
	require.Len(t, inputs, 1)
	assert.Equal(t, cr.RunID, inputs[0].RunID)
	assert.Equal(t, "test-input-1", inputs[0].InputID)
	assert.Equal(t, "What should I do?", inputs[0].Prompt)
}

func TestDeliverInput(t *testing.T) {
	_, ts, fake := newTestServer(t)

	inputDelivered := make(chan string, 1)
	awaitReady := make(chan struct{})

	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		b := bus.New()
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(b)
		}

		b.Emit(events.AgentAwaitStarted{
			AgentID:   "main",
			InputID:   "test-input-1",
			Prompt:    "What should I do?",
			NextState: "NEXT.md",
			Timestamp: time.Now(),
		})

		if opts.AwaitCallback != nil {
			opts.AwaitCallback("main", "test-input-1", "What should I do?", "NEXT.md")
		}

		close(awaitReady)

		// In daemon mode, wait for input on the AwaitInputCh.
		if opts.AwaitInputCh != nil {
			select {
			case input := <-opts.AwaitInputCh:
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

	// Wait for await.
	select {
	case <-awaitReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for await")
	}

	// Deliver input.
	inputBody := `{"response": "Do the thing"}`
	resp, err := http.Post(
		ts.URL+"/runs/"+cr.RunID+"/inputs/test-input-1",
		"application/json",
		strings.NewReader(inputBody),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var dr deliverInputResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&dr))
	assert.Equal(t, cr.RunID, dr.RunID)
	assert.Equal(t, "test-input-1", dr.InputID)
	assert.Equal(t, "resumed", dr.Status)

	// Verify the input was actually delivered.
	select {
	case delivered := <-inputDelivered:
		assert.Equal(t, "Do the thing", delivered)
	case <-time.After(5 * time.Second):
		t.Fatal("input was not delivered to orchestrator")
	}
}

func TestDeliverInput_InvalidInputID(t *testing.T) {
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
		ts.URL+"/runs/"+cr.RunID+"/inputs/nonexistent",
		"application/json",
		strings.NewReader(inputBody),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestDeliverInput_WrongRun(t *testing.T) {
	_, ts, fake := newTestServer(t)

	awaitReady := make(chan struct{})
	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		b := bus.New()
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(b)
		}
		if opts.AwaitCallback != nil {
			opts.AwaitCallback("main", "test-input-1", "prompt", "NEXT.md")
		}
		close(awaitReady)
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
	case <-awaitReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for await")
	}

	// Try to deliver input using wrong run ID.
	inputBody := `{"response": "hello"}`
	resp, err := http.Post(
		ts.URL+"/runs/wrong-run-id/inputs/test-input-1",
		"application/json",
		strings.NewReader(inputBody),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestCamelToSnake(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"StateStarted", "state_started"},
		{"WorkflowCompleted", "workflow_completed"},
		{"AgentAwaitStarted", "agent_await_started"},
		{"ErrorOccurred", "error_occurred"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.expected, camelToSnake(tt.input), "camelToSnake(%q)", tt.input)
	}
}
