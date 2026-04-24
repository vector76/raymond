package daemon

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"reflect"
	"strings"
	"time"
)

//go:embed static
var staticFiles embed.FS

// daemonDefaultBudgetUSD is the ultimate fallback budget when no other
// source (request, workflow manifest, or server-wide config) supplies one.
// Matches the CLI default (internal/cli/cli.go defaultBudgetUSD) so
// daemon-launched runs don't halt immediately on the zero-budget guard
// in the markdown executor.
const daemonDefaultBudgetUSD = 10.0

// resolveBudget picks the effective budget for a run, walking the
// precedence ladder: explicit request budget > workflow manifest
// default_budget > server-wide config budget > hardcoded constant. Zero
// and negative values are treated as "unset" at every level.
func resolveBudget(reqBudget, manifestBudget, serverBudget float64) float64 {
	if reqBudget > 0 {
		return reqBudget
	}
	if manifestBudget > 0 {
		return manifestBudget
	}
	if serverBudget > 0 {
		return serverBudget
	}
	return daemonDefaultBudgetUSD
}

// Server is the HTTP REST API server for the Raymond daemon. It exposes
// workflow discovery, run management, and SSE event streaming endpoints.
type Server struct {
	registry        *Registry
	runManager      *RunManager
	pendingRegistry *PendingRegistry
	httpServer      *http.Server
	// defaultBudget is the server-wide budget fallback applied when the
	// request body omits budget and the workflow manifest has no
	// default_budget. Configured by SetDefaultBudget; defaults to 0
	// (meaning the handler falls through to daemonDefaultBudgetUSD).
	defaultBudget float64
}

// NewServer creates a Server wired to the given registry and run manager.
// Call ListenAndServe to start serving.
func NewServer(reg *Registry, rm *RunManager, port int) *Server {
	s := &Server{
		registry:   reg,
		runManager: rm,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /workflows", s.handleListWorkflows)
	mux.HandleFunc("GET /workflows/{id}", s.handleGetWorkflow)
	mux.HandleFunc("POST /runs", s.handleCreateRun)
	mux.HandleFunc("GET /runs", s.handleListRuns)
	mux.HandleFunc("GET /runs/{id}", s.handleGetRun)
	mux.HandleFunc("GET /runs/{id}/output", s.handleRunOutput)
	mux.HandleFunc("POST /runs/{id}/cancel", s.handleCancelRun)
	mux.HandleFunc("DELETE /runs/{id}", s.handleDeleteRun)
	mux.HandleFunc("GET /runs/{id}/pending-inputs", s.handleListPendingInputs)
	mux.HandleFunc("POST /runs/{id}/inputs/{input_id}", s.handleDeliverInput)

	// Serve embedded static UI files at root; API routes above take precedence.
	staticSub, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(staticSub)))

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: corsMiddleware(mux),
	}

	return s
}

// SetPendingRegistry configures the pending input registry for input delivery
// endpoints.
func (s *Server) SetPendingRegistry(pr *PendingRegistry) {
	s.pendingRegistry = pr
}

// SetDefaultBudget configures the server-wide fallback budget used when the
// launch request and the workflow manifest both leave it unset. Pass 0 to
// fall back to daemonDefaultBudgetUSD. Typically called by the serve command
// after loading .raymond/config.toml.
func (s *Server) SetDefaultBudget(budget float64) {
	s.defaultBudget = budget
}

// Handler returns the HTTP handler (for testing with httptest).
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// ListenAndServe starts the HTTP server. It blocks until the server is shut
// down or an error occurs.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// --- JSON response/request types ---

type workflowResponse struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Description        string            `json:"description"`
	InputSchema        map[string]string `json:"input_schema"`
	DefaultBudget      float64           `json:"default_budget"`
	RequiresHumanInput bool              `json:"requires_human_input"`
}

type agentResponse struct {
	ID           string `json:"id"`
	CurrentState string `json:"current_state"`
	Status       string `json:"status"`
}

type runResponse struct {
	RunID        string          `json:"run_id"`
	WorkflowID   string          `json:"workflow_id"`
	Status       string          `json:"status"`
	CurrentState string          `json:"current_state,omitempty"`
	Agents       []agentResponse `json:"agents"`
	CostUSD      float64         `json:"cost_usd"`
	StartedAt    time.Time       `json:"started_at"`
	ElapsedSecs  float64         `json:"elapsed_seconds"`
	Result       string          `json:"result,omitempty"`
}

type createRunRequest struct {
	WorkflowID       string            `json:"workflow_id"`
	Input            string            `json:"input"`
	Budget           float64           `json:"budget"`
	Model            string            `json:"model"`
	WorkingDirectory string            `json:"working_directory"`
	Environment      map[string]string `json:"environment"`
}

type createRunResponse struct {
	RunID     string    `json:"run_id"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
}

type cancelRunResponse struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

type pendingInputResponse struct {
	RunID     string  `json:"run_id"`
	InputID   string  `json:"input_id"`
	Prompt    string  `json:"prompt"`
	CreatedAt string  `json:"created_at"`
	TimeoutAt *string `json:"timeout_at,omitempty"`
}

type deliverInputRequest struct {
	Response string `json:"response"`
}

type deliverInputResponse struct {
	RunID   string `json:"run_id"`
	InputID string `json:"input_id"`
	Status  string `json:"status"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// --- Handlers ---

func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	entries := s.registry.ListWorkflows()
	resp := make([]workflowResponse, len(entries))
	for i, e := range entries {
		resp[i] = workflowToResponse(e)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entry, ok := s.registry.GetWorkflow(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: fmt.Sprintf("workflow %q not found", id)})
		return
	}
	writeJSON(w, http.StatusOK, workflowToResponse(*entry))
}

func (s *Server) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	var req createRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body: " + err.Error()})
		return
	}

	if req.WorkflowID == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "workflow_id is required"})
		return
	}

	entry, ok := s.registry.GetWorkflow(req.WorkflowID)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: fmt.Sprintf("workflow %q not found", req.WorkflowID)})
		return
	}

	budget := resolveBudget(req.Budget, entry.DefaultBudget, s.defaultBudget)

	// Use a background context so the run outlives the HTTP request.
	runID, err := s.runManager.LaunchRun(
		context.Background(),
		*entry,
		req.Input,
		budget,
		req.Model,
		req.WorkingDirectory,
		req.Environment,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	info, _ := s.runManager.GetRun(runID)
	resp := createRunResponse{
		RunID:     runID,
		Status:    info.Status,
		StartedAt: info.StartedAt,
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	runs := s.runManager.ListRuns()
	resp := make([]runResponse, len(runs))
	for i, ri := range runs {
		resp[i] = runInfoToResponse(ri)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	info, ok := s.runManager.GetRun(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: fmt.Sprintf("run %q not found", id)})
		return
	}
	writeJSON(w, http.StatusOK, runInfoToResponse(*info))
}

func (s *Server) handleRunOutput(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	eventCh, cancel, err := s.runManager.SubscribeRunEvents(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrRunNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: fmt.Sprintf("run %q not found", id)})
		} else {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		}
		return
	}
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "streaming not supported"})
		return
	}

	// Flush headers so the client receives them immediately.
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case event, ok := <-eventCh:
			if !ok {
				return // channel closed, run completed
			}
			data, err := marshalSSEEvent(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return // client disconnected
		}
	}
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := s.runManager.CancelRun(id); err != nil {
		if errors.Is(err, ErrRunNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
		} else {
			writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
		}
		return
	}

	info, ok := s.runManager.GetRun(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: fmt.Sprintf("run %q not found", id)})
		return
	}
	writeJSON(w, http.StatusOK, cancelRunResponse{RunID: id, Status: info.Status})
}

func (s *Server) handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := s.runManager.DeleteRun(id); err != nil {
		switch {
		case errors.Is(err, ErrRunNotFound):
			writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
		case errors.Is(err, ErrRunActive):
			writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListPendingInputs(w http.ResponseWriter, r *http.Request) {
	if s.pendingRegistry == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "pending input registry not configured"})
		return
	}

	runID := r.PathValue("id")
	if _, ok := s.runManager.GetRun(runID); !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: fmt.Sprintf("run %q not found", runID)})
		return
	}

	inputs := s.pendingRegistry.ListByRun(runID)
	resp := make([]pendingInputResponse, len(inputs))
	for i, pi := range inputs {
		resp[i] = pendingInputResponse{
			RunID:     pi.RunID,
			InputID:   pi.InputID,
			Prompt:    pi.Prompt,
			CreatedAt: pi.CreatedAt.Format(time.RFC3339),
		}
		if pi.TimeoutAt != nil {
			t := pi.TimeoutAt.Format(time.RFC3339)
			resp[i].TimeoutAt = &t
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDeliverInput(w http.ResponseWriter, r *http.Request) {
	if s.pendingRegistry == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "pending input registry not configured"})
		return
	}

	runID := r.PathValue("id")
	inputID := r.PathValue("input_id")

	var req deliverInputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body: " + err.Error()})
		return
	}

	if err := s.runManager.DeliverInput(runID, inputID, req.Response); err != nil {
		if errors.Is(err, ErrRunNotFound) ||
			errors.Is(err, ErrPendingInputNotFound) ||
			errors.Is(err, ErrPendingInputMismatch) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
		} else {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		}
		return
	}

	writeJSON(w, http.StatusOK, deliverInputResponse{
		RunID:   runID,
		InputID: inputID,
		Status:  "resumed",
	})
}

// --- Helpers ---

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func workflowToResponse(e WorkflowEntry) workflowResponse {
	return workflowResponse{
		ID:                 e.ID,
		Name:               e.Name,
		Description:        e.Description,
		InputSchema:        e.InputSchema,
		DefaultBudget:      e.DefaultBudget,
		RequiresHumanInput: e.RequiresHumanInput,
	}
}

func runInfoToResponse(ri RunInfo) runResponse {
	agents := make([]agentResponse, len(ri.Agents))
	for i, a := range ri.Agents {
		agents[i] = agentResponse{
			ID:           a.ID,
			CurrentState: a.CurrentState,
			Status:       a.Status,
		}
	}
	return runResponse{
		RunID:        ri.RunID,
		WorkflowID:   ri.WorkflowID,
		Status:       ri.Status,
		CurrentState: ri.CurrentState,
		Agents:       agents,
		CostUSD:      ri.CostUSD,
		StartedAt:    ri.StartedAt,
		ElapsedSecs:  ri.ElapsedDuration.Seconds(),
		Result:       ri.Result,
	}
}

// marshalSSEEvent wraps an event value in a JSON envelope with a "type" field
// derived from the struct name (e.g. events.StateStarted → "state_started").
func marshalSSEEvent(event any) ([]byte, error) {
	typeName := reflect.TypeOf(event).Name()
	envelope := map[string]any{
		"type": camelToSnake(typeName),
	}

	// Marshal the event to a map, then merge into the envelope.
	raw, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for k, v := range fields {
		envelope[k] = v
	}

	return json.Marshal(envelope)
}

// camelToSnake converts CamelCase to snake_case.
func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
