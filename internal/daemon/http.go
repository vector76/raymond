package daemon

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/vector76/raymond/internal/manifest"
	"github.com/vector76/raymond/internal/parsing"
	wfstate "github.com/vector76/raymond/internal/state"
)

//go:embed static
var staticFiles embed.FS

// daemonDefaultBudgetUSD is the ultimate fallback budget when no other
// source (request, workflow manifest, or server-wide config) supplies one.
// Matches the CLI default (internal/cli/cli.go defaultBudgetUSD) so
// daemon-launched runs don't halt immediately on the zero-budget guard
// in the markdown executor.
const daemonDefaultBudgetUSD = 10.0

// Upload-cap fallbacks applied when neither the await nor the server
// configuration supplies a value. These are intentionally conservative; an
// operator who needs higher limits sets them via CLI flag or config file,
// and a workflow can raise its own ceiling per-await on a bucket-mode <await>.
const (
	daemonDefaultMaxFileSize  int64 = 10 * 1024 * 1024  // 10 MiB per file
	daemonDefaultMaxTotalSize int64 = 100 * 1024 * 1024 // 100 MiB per submission
	daemonDefaultMaxFileCount       = 10
)

// validateInputMode enforces the manifest's input.mode constraint at launch.
// mode: required rejects an empty input; mode: none rejects a non-empty input.
// mode: optional (or any unknown value, defensively) accepts anything.
func validateInputMode(mode, input string) error {
	switch mode {
	case manifest.InputModeRequired:
		if input == "" {
			return fmt.Errorf("workflow requires non-empty input")
		}
	case manifest.InputModeNone:
		if input != "" {
			return fmt.Errorf("workflow does not accept input")
		}
	}
	return nil
}

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

// resolveUploadCaps picks the effective upload caps for an await, mirroring
// resolveBudget's precedence ladder: per-await override (from
// FileAffordance.Bucket) > server config > hardcoded constant. Zero and
// negative values are treated as "unset" at every level.
//
// Bucket mode is the only level at which an await may raise or lower the
// caps; slot-mode and display-only awaits leave the per-await values at zero
// and therefore inherit the server-wide defaults for size and count. Per-slot
// MIME allowlists ride on parsing.SlotSpec and are enforced at upload time
// independently of these size caps.
//
// perAwait may be nil for awaits that did not declare a file affordance; the
// caller still gets the server-wide caps so a misuse of the upload endpoint
// is bounded by the same defaults as a declared bucket await.
func resolveUploadCaps(perAwait *parsing.FileAffordance, serverPerFile, serverTotal int64, serverCount int) (perFile, total int64, count int) {
	var awaitPerFile, awaitTotal int64
	var awaitCount int
	if perAwait != nil && perAwait.Mode == parsing.ModeBucket {
		awaitPerFile = perAwait.Bucket.MaxSizePerFile
		awaitTotal = perAwait.Bucket.MaxTotalSize
		awaitCount = perAwait.Bucket.MaxCount
	}
	return pickInt64Cap(awaitPerFile, serverPerFile, daemonDefaultMaxFileSize),
		pickInt64Cap(awaitTotal, serverTotal, daemonDefaultMaxTotalSize),
		pickIntCap(awaitCount, serverCount, daemonDefaultMaxFileCount)
}

func pickInt64Cap(awaitVal, serverVal, fallback int64) int64 {
	if awaitVal > 0 {
		return awaitVal
	}
	if serverVal > 0 {
		return serverVal
	}
	return fallback
}

func pickIntCap(awaitVal, serverVal, fallback int) int {
	if awaitVal > 0 {
		return awaitVal
	}
	if serverVal > 0 {
		return serverVal
	}
	return fallback
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
	// Server-wide upload caps applied when an <await> does not declare its
	// own. Configured by SetDefaultUploadCaps; zero values mean "unset" and
	// let resolveUploadCaps fall through to the daemonDefaultMax* constants.
	defaultMaxFileSize  int64
	defaultMaxTotalSize int64
	defaultMaxFileCount int
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
	mux.HandleFunc("GET /runs/{id}/inputs/{input_id}/files", s.handleListInputFiles)
	mux.HandleFunc("GET /runs/{id}/inputs/{input_id}/files/{path...}", s.handleGetInputFile)
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

// SetDefaultUploadCaps configures the server-wide fallback upload caps used
// when an <await> declares no caps of its own. Any non-positive value is
// treated as "unset" and lets resolveUploadCaps fall through to the
// daemonDefaultMax* constants. Typically called by the serve command after
// loading .raymond/config.toml and merging CLI flags.
func (s *Server) SetDefaultUploadCaps(perFile, total int64, count int) {
	s.defaultMaxFileSize = perFile
	s.defaultMaxTotalSize = total
	s.defaultMaxFileCount = count
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
	ID                 string             `json:"id"`
	Name               string             `json:"name"`
	Description        string             `json:"description"`
	Input              manifest.InputSpec `json:"input"`
	DefaultBudget      float64            `json:"default_budget"`
	RequiresHumanInput bool               `json:"requires_human_input"`
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
	RunID          string                  `json:"run_id"`
	InputID        string                  `json:"input_id"`
	Prompt         string                  `json:"prompt"`
	CreatedAt      string                  `json:"created_at"`
	TimeoutAt      *string                 `json:"timeout_at,omitempty"`
	FileAffordance *fileAffordanceResponse `json:"file_affordance,omitempty"`
	StagedFiles    []inputFileMetadata     `json:"staged_files,omitempty"`
}

// inputFileMetadata is the JSON shape used by the file-listing endpoint, the
// pending-inputs response, and (later) the run-history endpoint to describe a
// single file associated with an input step.
type inputFileMetadata struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
	Source      string `json:"source,omitempty"`
}

type fileSlotResponse struct {
	Name string   `json:"name"`
	MIME []string `json:"mime,omitempty"`
}

type bucketSpecResponse struct {
	MaxCount       int      `json:"max_count,omitempty"`
	MaxSizePerFile int64    `json:"max_size_per_file,omitempty"`
	MaxTotalSize   int64    `json:"max_total_size,omitempty"`
	MIME           []string `json:"mime,omitempty"`
}

type displayFileResponse struct {
	SourcePath  string `json:"source_path"`
	DisplayName string `json:"display_name,omitempty"`
}

type fileAffordanceResponse struct {
	Mode         string                `json:"mode"`
	Slots        []fileSlotResponse    `json:"slots,omitempty"`
	Bucket       *bucketSpecResponse   `json:"bucket,omitempty"`
	DisplayFiles []displayFileResponse `json:"display_files,omitempty"`
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

	if err := validateInputMode(entry.Input.Mode, req.Input); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
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
			RunID:          pi.RunID,
			InputID:        pi.InputID,
			Prompt:         pi.Prompt,
			CreatedAt:      pi.CreatedAt.Format(time.RFC3339),
			FileAffordance: fileAffordanceToResponse(pi.FileAffordance),
			StagedFiles:    fileRecordsToMetadata(pi.StagedFiles),
		}
		if pi.TimeoutAt != nil {
			t := pi.TimeoutAt.Format(time.RFC3339)
			resp[i].TimeoutAt = &t
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleListInputFiles serves GET /runs/{id}/inputs/{input_id}/files. It
// returns the file catalog for an input — staged display files and any
// uploaded files. Pending inputs (still awaiting a response) are resolved
// through the pending registry; once the input has been delivered, its
// catalog is sourced from the workflow state's ResolvedInputs history.
func (s *Server) handleListInputFiles(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	inputID := r.PathValue("input_id")

	if _, ok := s.runManager.GetRun(runID); !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: fmt.Sprintf("run %q not found", runID)})
		return
	}

	if s.pendingRegistry != nil {
		if pi, ok := s.pendingRegistry.Get(inputID); ok {
			if pi.RunID != runID {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: fmt.Sprintf("input %q not found in run %q", inputID, runID)})
				return
			}
			writeJSON(w, http.StatusOK, fileRecordsToMetadata(pi.StagedFiles))
			return
		}
	}

	ri, ok := s.runManager.LookupResolvedInput(runID, inputID)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: fmt.Sprintf("input %q not found in run %q", inputID, runID)})
		return
	}

	all := make([]wfstate.FileRecord, 0, len(ri.StagedFiles)+len(ri.UploadedFiles))
	all = append(all, ri.StagedFiles...)
	all = append(all, ri.UploadedFiles...)
	writeJSON(w, http.StatusOK, fileRecordsToMetadata(all))
}

// inlineAllowedContentTypes is the server-defined allowlist of MIME types for
// which `?disposition=inline` is honored on the file content endpoint. Any
// content type outside this list is forced to `Content-Disposition: attachment`
// regardless of the requested disposition, so script-bearing types such as
// `text/html` and `image/svg+xml` are never rendered inline by the server.
//
// Defense headers are always emitted alongside file responses:
//   - `X-Content-Type-Options: nosniff` prevents browsers from second-guessing
//     the recorded Content-Type and downgrading our allowlist decision.
//   - `Content-Security-Policy: default-src 'none'; sandbox` is the strictest
//     CSP that still lets browsers display the allowlisted image and PDF
//     types inline. `default-src 'none'` blocks any subresource fetches and
//     script execution; `sandbox` (no `allow-*` tokens) drops the response
//     into a unique opaque origin without scripts, forms, popups, or
//     same-origin privileges. Images and the built-in PDF viewer render
//     fine under this policy because they require no subresource loads.
var inlineAllowedContentTypes = map[string]bool{
	"image/png":       true,
	"image/jpeg":      true,
	"image/gif":       true,
	"image/webp":      true,
	"application/pdf": true,
}

// handleGetInputFile serves GET /runs/{id}/inputs/{input_id}/files/{path...}.
// It looks up the recorded FileRecord for `path` (first via the pending
// registry, then via the resolved-input history) and serves the on-disk file
// at `<task folder>/inputs/<input_id>/<path>`. The recorded content type is
// authoritative — the server does not re-sniff at view time. Path traversal
// is rejected by SafeJoinUnderDir.
func (s *Server) handleGetInputFile(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	inputID := r.PathValue("input_id")
	path := r.PathValue("path")

	if _, ok := s.runManager.GetRun(runID); !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: fmt.Sprintf("run %q not found", runID)})
		return
	}

	records, agentID, ok := s.lookupInputCatalog(runID, inputID)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: fmt.Sprintf("input %q not found in run %q", inputID, runID)})
		return
	}

	taskFolder, ok := s.lookupAgentTaskFolder(runID, agentID)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "task folder not assigned for input"})
		return
	}
	inputDir := filepath.Join(taskFolder, "inputs", inputID)

	// Validate the path against the input subdirectory before matching it to
	// the catalog: traversal attempts must return 400 even when the cleaned
	// form would happen to match a catalog name.
	fullPath, err := SafeJoinUnderDir(inputDir, path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	var record *wfstate.FileRecord
	for i := range records {
		if records[i].Name == path {
			record = &records[i]
			break
		}
	}
	if record == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: fmt.Sprintf("file %q not found", path)})
		return
	}

	f, err := os.Open(fullPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: fmt.Sprintf("file %q not found", path)})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "open file: " + err.Error()})
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "stat file: " + err.Error()})
		return
	}

	disposition := "attachment"
	if r.URL.Query().Get("disposition") == "inline" && isInlineAllowed(record.ContentType) {
		disposition = "inline"
	}

	contentType := record.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, record.Name))

	http.ServeContent(w, r, record.Name, info.ModTime(), f)
}

// lookupInputCatalog returns the FileRecords associated with an input — first
// trying the pending registry, then the run-state's resolved-input history.
// Also returns the owning agent ID so the caller can resolve the on-disk
// task folder. A pending entry whose run ID does not match runID is treated
// as "not found" (consistent with handleListInputFiles); we never fall
// through to the resolved history in that case, so a cross-run input ID
// collision cannot leak the wrong file catalog.
func (s *Server) lookupInputCatalog(runID, inputID string) ([]wfstate.FileRecord, string, bool) {
	if s.pendingRegistry != nil {
		if pi, ok := s.pendingRegistry.Get(inputID); ok {
			if pi.RunID != runID {
				return nil, "", false
			}
			return pi.StagedFiles, pi.AgentID, true
		}
	}
	ri, ok := s.runManager.LookupResolvedInput(runID, inputID)
	if !ok {
		return nil, "", false
	}
	all := make([]wfstate.FileRecord, 0, len(ri.StagedFiles)+len(ri.UploadedFiles))
	all = append(all, ri.StagedFiles...)
	all = append(all, ri.UploadedFiles...)
	return all, ri.AgentID, true
}

// lookupAgentTaskFolder reads the persisted workflow state and returns the
// TaskFolder assigned to the agent identified by agentID.
func (s *Server) lookupAgentTaskFolder(runID, agentID string) (string, bool) {
	ws, err := wfstate.ReadState(runID, s.runManager.stateDir)
	if err != nil {
		return "", false
	}
	for _, a := range ws.Agents {
		if a.ID == agentID && a.TaskFolder != "" {
			return a.TaskFolder, true
		}
	}
	return "", false
}

func isInlineAllowed(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return inlineAllowedContentTypes[strings.ToLower(mediaType)]
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

	if err := s.runManager.DeliverInput(runID, inputID, req.Response, nil); err != nil {
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
		Input:              e.Input,
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

// fileRecordsToMetadata converts state.FileRecord slices into the JSON
// inputFileMetadata shape shared by the listing and pending-inputs endpoints.
// Always returns a non-nil slice so the listing endpoint serializes empty
// catalogs as `[]` rather than `null`; pending-input responses still elide
// the field via the struct tag's omitempty (encoding/json treats len-0
// slices as empty for that purpose).
func fileRecordsToMetadata(records []wfstate.FileRecord) []inputFileMetadata {
	out := make([]inputFileMetadata, len(records))
	for i, r := range records {
		out[i] = inputFileMetadata{
			Name:        r.Name,
			Size:        r.Size,
			ContentType: r.ContentType,
			Source:      r.Source,
		}
	}
	return out
}

// fileAffordanceToResponse projects a parsing.FileAffordance into the JSON
// shape exposed by the API. Returns nil for a nil affordance and for a
// zero-value (text-only) affordance with no display files, so the caller can
// rely on omitempty.
func fileAffordanceToResponse(fa *parsing.FileAffordance) *fileAffordanceResponse {
	if fa == nil {
		return nil
	}
	if fa.Mode == parsing.ModeTextOnly && len(fa.DisplayFiles) == 0 {
		return nil
	}
	resp := &fileAffordanceResponse{Mode: modeToString(fa.Mode)}
	if len(fa.Slots) > 0 {
		resp.Slots = make([]fileSlotResponse, len(fa.Slots))
		for i, s := range fa.Slots {
			resp.Slots[i] = fileSlotResponse{Name: s.Name, MIME: s.MIME}
		}
	}
	if fa.Mode == parsing.ModeBucket {
		b := fa.Bucket
		resp.Bucket = &bucketSpecResponse{
			MaxCount:       b.MaxCount,
			MaxSizePerFile: b.MaxSizePerFile,
			MaxTotalSize:   b.MaxTotalSize,
			MIME:           b.MIME,
		}
	}
	if len(fa.DisplayFiles) > 0 {
		resp.DisplayFiles = make([]displayFileResponse, len(fa.DisplayFiles))
		for i, d := range fa.DisplayFiles {
			resp.DisplayFiles[i] = displayFileResponse{
				SourcePath:  d.SourcePath,
				DisplayName: d.DisplayName,
			}
		}
	}
	return resp
}

func modeToString(m parsing.Mode) string {
	switch m {
	case parsing.ModeSlot:
		return "slot"
	case parsing.ModeBucket:
		return "bucket"
	case parsing.ModeDisplayOnly:
		return "display_only"
	default:
		return "text_only"
	}
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
