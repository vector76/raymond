package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"
)

// Server is the HTTP REST API server for the Raymond daemon. It exposes
// workflow discovery, run management, and SSE event streaming endpoints.
type Server struct {
	registry   *Registry
	runManager *RunManager
	httpServer *http.Server
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

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: corsMiddleware(mux),
	}

	return s
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

	budget := req.Budget
	if budget <= 0 {
		budget = entry.DefaultBudget
	}

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
		if strings.Contains(err.Error(), "not found") {
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
		if strings.Contains(err.Error(), "not found") {
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

// --- Helpers ---

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
