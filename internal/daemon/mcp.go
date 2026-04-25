package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vector76/raymond/internal/manifest"
	"github.com/vector76/raymond/internal/parsing"
)

// --- JSON-RPC 2.0 types ---

type jsonrpcRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Result  any              `json:"result,omitempty"`
	Error   *jsonrpcError    `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Standard JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// --- MCP protocol types ---

type mcpToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// --- MCPServer ---

// MCPServer handles MCP protocol communication over JSON-RPC 2.0 on stdio.
// It exposes launcher-phase tools and input delivery for the Raymond daemon.
type MCPServer struct {
	registry        *Registry
	runManager      *RunManager
	pendingRegistry *PendingRegistry
	hasElicitation  bool
	// defaultBudget mirrors Server.defaultBudget: a server-wide budget
	// fallback applied when the raymond_launch tool call and the workflow
	// manifest both leave it unset.
	defaultBudget float64

	// baseURL is the absolute prefix used to construct file-content URLs
	// embedded in display-only elicitation prompts. Empty means "no URL
	// prefix"; staged-file URLs are then omitted from the prompt.
	baseURL string

	// Encoder for writing to the output stream (set by Serve).
	enc   *json.Encoder
	encMu sync.Mutex

	// Server-to-client request tracking (for elicitation).
	pendingResponses sync.Map   // request ID string → chan json.RawMessage
	nextReqID        atomic.Int64

	// Active raymond_await calls that support elicitation (runID → struct{}).
	activeAwaits sync.Map
}

// NewMCPServer creates an MCPServer wired to the given registry and run manager.
func NewMCPServer(reg *Registry, rm *RunManager) *MCPServer {
	return &MCPServer{
		registry:   reg,
		runManager: rm,
	}
}

// SetPendingRegistry configures the pending input registry for input-related
// tools.
func (s *MCPServer) SetPendingRegistry(pr *PendingRegistry) {
	s.pendingRegistry = pr
}

// SetDefaultBudget configures the server-wide fallback budget for MCP launch
// tool calls. See Server.SetDefaultBudget for semantics.
func (s *MCPServer) SetDefaultBudget(budget float64) {
	s.defaultBudget = budget
}

// SetBaseURL configures the absolute URL prefix used when embedding
// file-content URLs in display-only elicitation prompts. The prefix should
// not end with a slash (e.g. "http://localhost:8080"). When empty, staged
// file URLs are omitted from the prompt.
func (s *MCPServer) SetBaseURL(u string) {
	s.baseURL = strings.TrimRight(u, "/")
}

// HasElicitation reports whether the MCP client declared elicitation support
// during initialization.
func (s *MCPServer) HasElicitation() bool {
	return s.hasElicitation
}

// HandleAwaitNotification is called by the RunManager's await notifier when
// an agent enters <await>. If there is an active raymond_await call with
// elicitation support for the run, it issues an elicitation request to the
// MCP client and delivers the response.
//
// Awaits that declare upload affordance (slot or bucket mode) are not
// delivered via MCP: the elicitation channel cannot asymmetrically carry a
// rejection of a text-only response, so we leave the input pending and let
// the user complete it via the HTTP UI. A warning is logged for visibility.
//
// Display-only awaits are delivered via elicitation with the prompt
// augmented by absolute URLs for the staged files so an MCP client can
// fetch them out of band.
func (s *MCPServer) HandleAwaitNotification(runID, inputID, prompt string) {
	if _, ok := s.activeAwaits.Load(runID); !ok {
		return
	}

	pi, ok := s.lookupPendingInput(inputID)
	if ok && requiresUpload(pi.FileAffordance) {
		log.Printf("mcp: skipping elicitation for input %q (run %q): upload affordance present, deliver via HTTP UI", inputID, runID)
		return
	}

	finalPrompt := prompt
	if ok {
		finalPrompt = s.augmentPromptWithFileURLs(prompt, pi)
	}

	go func() {
		response, err := s.sendElicitation(finalPrompt)
		if err != nil {
			return // elicitation failed; input stays pending for manual delivery
		}
		s.runManager.DeliverInput(runID, inputID, response, nil)
	}()
}

// lookupPendingInput returns the registered PendingInput for the given input
// ID, if the registry is configured and the input is still pending.
func (s *MCPServer) lookupPendingInput(inputID string) (*PendingInput, bool) {
	if s.pendingRegistry == nil {
		return nil, false
	}
	return s.pendingRegistry.Get(inputID)
}

// requiresUpload reports whether the affordance includes any non-zero upload
// affordance (slot mode with declared slots, or bucket mode). Display-only
// and text-only awaits do not require uploads.
func requiresUpload(fa *parsing.FileAffordance) bool {
	if fa == nil {
		return false
	}
	switch fa.Mode {
	case parsing.ModeSlot, parsing.ModeBucket:
		return true
	}
	return false
}

// augmentPromptWithFileURLs appends absolute URLs for the input's staged
// files to the prompt so an MCP client can fetch them out of band. Returns
// the prompt unchanged when there are no staged files or no base URL is
// configured.
func (s *MCPServer) augmentPromptWithFileURLs(prompt string, pi *PendingInput) string {
	if s.baseURL == "" || len(pi.StagedFiles) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	if !strings.HasSuffix(prompt, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\nAttached files:\n")
	for _, f := range pi.StagedFiles {
		b.WriteString("- ")
		b.WriteString(f.Name)
		b.WriteString(": ")
		b.WriteString(s.fileContentURL(pi.RunID, pi.InputID, f.Name))
		b.WriteString("\n")
	}
	return b.String()
}

// fileContentURL builds the absolute URL for the file content endpoint.
func (s *MCPServer) fileContentURL(runID, inputID, name string) string {
	return fmt.Sprintf("%s/runs/%s/inputs/%s/files/%s",
		s.baseURL,
		url.PathEscape(runID),
		url.PathEscape(inputID),
		url.PathEscape(name),
	)
}

// Serve reads JSON-RPC 2.0 requests from in (newline-delimited) and writes
// responses to out. It blocks until ctx is cancelled or in reaches EOF.
//
// The loop also handles responses to server-originated requests (e.g.
// elicitation/create) by routing them to the appropriate pending channel.
func (s *MCPServer) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	s.encMu.Lock()
	s.enc = json.NewEncoder(out)
	s.encMu.Unlock()

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Peek to distinguish client requests from responses to our requests.
		var peek struct {
			ID     *json.RawMessage `json:"id,omitempty"`
			Method string           `json:"method,omitempty"`
		}
		if err := json.Unmarshal(line, &peek); err != nil {
			s.sendJSON(jsonrpcResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &jsonrpcError{Code: codeParseError, Message: "parse error"},
			})
			continue
		}

		// A message with an id but no method is a response to a
		// server-originated request (e.g. elicitation/create).
		if peek.Method == "" && peek.ID != nil {
			s.routeResponse(peek.ID, line)
			continue
		}

		var req jsonrpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.sendJSON(jsonrpcResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &jsonrpcError{Code: codeParseError, Message: "parse error"},
			})
			continue
		}

		// Notifications (no id field) don't get a response.
		if req.ID == nil {
			continue
		}

		resp := s.dispatch(ctx, &req)
		if resp != nil {
			s.sendJSON(*resp)
		}
	}

	return scanner.Err()
}

func (s *MCPServer) dispatch(ctx context.Context, req *jsonrpcRequest) *jsonrpcResponse {
	switch req.Method {
	case "initialize":
		resp := s.handleInitialize(req)
		return &resp
	case "tools/list":
		resp := s.handleToolsList(req)
		return &resp
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	default:
		return &jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonrpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}
}

// --- initialize ---

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
	Capabilities    struct {
		Elicitation *struct{} `json:"elicitation,omitempty"`
	} `json:"capabilities"`
	ClientInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

func (s *MCPServer) handleInitialize(req *jsonrpcRequest) jsonrpcResponse {
	var params initializeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return jsonrpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &jsonrpcError{Code: codeInvalidParams, Message: "invalid initialize params"},
			}
		}
	}

	s.hasElicitation = params.Capabilities.Elicitation != nil

	result := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "raymond",
			"version": "0.1.0",
		},
	}

	return jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

// --- tools/list ---

func (s *MCPServer) handleToolsList(req *jsonrpcRequest) jsonrpcResponse {
	return jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"tools": s.toolDefinitions()},
	}
}

func (s *MCPServer) toolDefinitions() []mcpToolDef {
	return []mcpToolDef{
		{
			Name:        "raymond_list_workflows",
			Description: "List all available workflows in the Raymond daemon registry.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "raymond_run",
			Description: "Launch a new workflow run. Returns the run ID and initial status immediately without waiting for completion.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workflow_id": map[string]any{
						"type":        "string",
						"description": "ID of the workflow to run.",
					},
					"input": map[string]any{
						"type":        "string",
						"description": "Input text for the workflow.",
					},
					"budget": map[string]any{
						"type":        "number",
						"description": "Budget in USD for the run. Defaults to the workflow's default budget.",
					},
					"model": map[string]any{
						"type":        "string",
						"description": "Model to use for the run.",
					},
					"working_directory": map[string]any{
						"type":        "string",
						"description": "Working directory for the run.",
					},
					"environment": map[string]any{
						"type":                 "object",
						"description":          "Environment variables for the run.",
						"additionalProperties": map[string]any{"type": "string"},
					},
				},
				"required": []string{"workflow_id"},
			},
		},
		{
			Name:        "raymond_status",
			Description: "Get the current status of a workflow run.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"run_id": map[string]any{
						"type":        "string",
						"description": "ID of the run to query.",
					},
				},
				"required": []string{"run_id"},
			},
		},
		{
			Name:        "raymond_await",
			Description: "Wait for a workflow run to complete. Blocks until the run reaches a terminal state or the timeout expires.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"run_id": map[string]any{
						"type":        "string",
						"description": "ID of the run to wait for.",
					},
					"timeout_seconds": map[string]any{
						"type":        "number",
						"description": "Maximum time to wait in seconds. 0 or omitted means wait indefinitely.",
					},
				},
				"required": []string{"run_id"},
			},
		},
		{
			Name:        "raymond_cancel",
			Description: "Cancel a running workflow.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"run_id": map[string]any{
						"type":        "string",
						"description": "ID of the run to cancel.",
					},
				},
				"required": []string{"run_id"},
			},
		},
		{
			Name:        "raymond_list_pending_inputs",
			Description: "List all pending human-input requests across all runs.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "raymond_provide_input",
			Description: "Provide a response to a pending human-input request.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input_id": map[string]any{
						"type":        "string",
						"description": "ID of the pending input to respond to.",
					},
					"response": map[string]any{
						"type":        "string",
						"description": "The response text to deliver.",
					},
				},
				"required": []string{"input_id", "response"},
			},
		},
	}
}

// --- tools/call ---

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *MCPServer) handleToolsCall(ctx context.Context, req *jsonrpcRequest) *jsonrpcResponse {
	var params toolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonrpcError{Code: codeInvalidParams, Message: "invalid tools/call params"},
		}
	}

	// raymond_await with elicitation support is dispatched asynchronously so
	// the Serve loop can continue reading elicitation responses.
	if params.Name == "raymond_await" && s.hasElicitation {
		reqID := req.ID
		go func() {
			result := s.toolAwaitWithElicitation(ctx, params.Arguments)
			s.sendJSON(jsonrpcResponse{
				JSONRPC: "2.0",
				ID:      reqID,
				Result:  result,
			})
		}()
		return nil // response sent asynchronously
	}

	var result mcpToolResult
	switch params.Name {
	case "raymond_list_workflows":
		result = s.toolListWorkflows()
	case "raymond_run":
		result = s.toolRun(ctx, params.Arguments)
	case "raymond_status":
		result = s.toolStatus(params.Arguments)
	case "raymond_await":
		result = s.toolAwait(params.Arguments)
	case "raymond_cancel":
		result = s.toolCancel(params.Arguments)
	case "raymond_list_pending_inputs":
		result = s.toolListPendingInputs()
	case "raymond_provide_input":
		result = s.toolProvideInput(params.Arguments)
	default:
		result = mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("unknown tool: %s", params.Name)}},
			IsError: true,
		}
	}

	return &jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

// --- Tool implementations ---

func (s *MCPServer) toolListWorkflows() mcpToolResult {
	entries := s.registry.ListWorkflows()

	type wfItem struct {
		ID                 string             `json:"id"`
		Name               string             `json:"name"`
		Description        string             `json:"description"`
		Input              manifest.InputSpec `json:"input"`
		DefaultBudget      float64            `json:"default_budget"`
		RequiresHumanInput bool               `json:"requires_human_input"`
	}

	items := make([]wfItem, len(entries))
	for i, e := range entries {
		items[i] = wfItem{
			ID:                 e.ID,
			Name:               e.Name,
			Description:        e.Description,
			Input:              e.Input,
			DefaultBudget:      e.DefaultBudget,
			RequiresHumanInput: e.RequiresHumanInput,
		}
	}

	data, _ := json.Marshal(items)
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: string(data)}},
	}
}

func (s *MCPServer) toolRun(ctx context.Context, args json.RawMessage) mcpToolResult {
	var params struct {
		WorkflowID       string            `json:"workflow_id"`
		Input            string            `json:"input"`
		Budget           float64           `json:"budget"`
		Model            string            `json:"model"`
		WorkingDirectory string            `json:"working_directory"`
		Environment      map[string]string `json:"environment"`
	}
	if err := unmarshalArgs(args, &params); err != nil {
		return toolError("invalid arguments: " + err.Error())
	}

	if params.WorkflowID == "" {
		return toolError("workflow_id is required")
	}

	entry, ok := s.registry.GetWorkflow(params.WorkflowID)
	if !ok {
		return toolError(fmt.Sprintf("workflow %q not found", params.WorkflowID))
	}

	// Reject workflows requiring human input when client lacks elicitation.
	if entry.RequiresHumanInput && !s.hasElicitation {
		return toolError(fmt.Sprintf(
			"workflow %q requires human input but the MCP client does not support elicitation",
			params.WorkflowID,
		))
	}

	if err := validateInputMode(entry.Input.Mode, params.Input); err != nil {
		return toolError(err.Error())
	}

	budget := resolveBudget(params.Budget, entry.DefaultBudget, s.defaultBudget)

	runID, err := s.runManager.LaunchRun(
		context.Background(),
		*entry,
		params.Input,
		budget,
		params.Model,
		params.WorkingDirectory,
		params.Environment,
	)
	if err != nil {
		return toolError("failed to launch run: " + err.Error())
	}

	result := map[string]any{
		"run_id": runID,
		"status": RunStatusRunning,
	}
	data, _ := json.Marshal(result)
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: string(data)}},
	}
}

func (s *MCPServer) toolStatus(args json.RawMessage) mcpToolResult {
	var params struct {
		RunID string `json:"run_id"`
	}
	if err := unmarshalArgs(args, &params); err != nil {
		return toolError("invalid arguments: " + err.Error())
	}

	if params.RunID == "" {
		return toolError("run_id is required")
	}

	info, ok := s.runManager.GetRun(params.RunID)
	if !ok {
		return toolError(fmt.Sprintf("run %q not found", params.RunID))
	}

	agents := make([]map[string]string, len(info.Agents))
	for i, a := range info.Agents {
		agents[i] = map[string]string{
			"id":            a.ID,
			"current_state": a.CurrentState,
			"status":        a.Status,
		}
	}

	result := map[string]any{
		"run_id":          info.RunID,
		"status":          info.Status,
		"current_state":   info.CurrentState,
		"agents":          agents,
		"cost_usd":        info.CostUSD,
		"elapsed_seconds": info.ElapsedDuration.Seconds(),
	}
	data, _ := json.Marshal(result)
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: string(data)}},
	}
}

func (s *MCPServer) toolAwait(args json.RawMessage) mcpToolResult {
	var params struct {
		RunID          string  `json:"run_id"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if err := unmarshalArgs(args, &params); err != nil {
		return toolError("invalid arguments: " + err.Error())
	}

	if params.RunID == "" {
		return toolError("run_id is required")
	}

	var timeout time.Duration
	if params.TimeoutSeconds > 0 {
		timeout = time.Duration(params.TimeoutSeconds * float64(time.Second))
	}

	info, err := s.runManager.WaitForCompletion(params.RunID, timeout)
	if err != nil {
		return toolError(err.Error())
	}

	result := map[string]any{
		"run_id":   info.RunID,
		"status":   info.Status,
		"result":   info.Result,
		"cost_usd": info.CostUSD,
	}
	data, _ := json.Marshal(result)
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: string(data)}},
	}
}

func (s *MCPServer) toolCancel(args json.RawMessage) mcpToolResult {
	var params struct {
		RunID string `json:"run_id"`
	}
	if err := unmarshalArgs(args, &params); err != nil {
		return toolError("invalid arguments: " + err.Error())
	}

	if params.RunID == "" {
		return toolError("run_id is required")
	}

	if err := s.runManager.CancelRun(params.RunID); err != nil {
		return toolError(err.Error())
	}

	result := map[string]any{
		"run_id": params.RunID,
		"status": RunStatusCancelled,
	}
	data, _ := json.Marshal(result)
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: string(data)}},
	}
}

func (s *MCPServer) toolListPendingInputs() mcpToolResult {
	if s.pendingRegistry == nil {
		return toolError("pending input registry not configured")
	}

	inputs := s.pendingRegistry.ListAll()

	type item struct {
		RunID      string  `json:"run_id"`
		InputID    string  `json:"input_id"`
		WorkflowID string  `json:"workflow_id"`
		Prompt     string  `json:"prompt"`
		CreatedAt  string  `json:"created_at"`
		TimeoutAt  *string `json:"timeout_at,omitempty"`
	}

	items := make([]item, len(inputs))
	for i, pi := range inputs {
		items[i] = item{
			RunID:      pi.RunID,
			InputID:    pi.InputID,
			WorkflowID: pi.WorkflowID,
			Prompt:     pi.Prompt,
			CreatedAt:  pi.CreatedAt.Format(time.RFC3339),
		}
		if pi.TimeoutAt != nil {
			t := pi.TimeoutAt.Format(time.RFC3339)
			items[i].TimeoutAt = &t
		}
	}

	data, _ := json.Marshal(items)
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: string(data)}},
	}
}

func (s *MCPServer) toolProvideInput(args json.RawMessage) mcpToolResult {
	var params struct {
		InputID  string `json:"input_id"`
		Response string `json:"response"`
	}
	if err := unmarshalArgs(args, &params); err != nil {
		return toolError("invalid arguments: " + err.Error())
	}
	if params.InputID == "" {
		return toolError("input_id is required")
	}
	if params.Response == "" {
		return toolError("response is required")
	}

	// Look up the pending input to get the run_id.
	if s.pendingRegistry == nil {
		return toolError("pending input registry not configured")
	}
	pi, ok := s.pendingRegistry.Get(params.InputID)
	if !ok {
		return toolError(fmt.Sprintf("pending input %q not found", params.InputID))
	}
	runID := pi.RunID

	if err := s.runManager.DeliverInput("", params.InputID, params.Response, nil); err != nil {
		return toolError(err.Error())
	}

	result := map[string]any{
		"run_id":   runID,
		"input_id": params.InputID,
		"status":   "resumed",
	}
	data, _ := json.Marshal(result)
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: string(data)}},
	}
}

// toolAwaitWithElicitation is the elicitation-capable version of toolAwait.
// It registers as an active await so that HandleAwaitNotification can issue
// elicitation requests when the workflow hits <await>.
func (s *MCPServer) toolAwaitWithElicitation(ctx context.Context, args json.RawMessage) mcpToolResult {
	var params struct {
		RunID          string  `json:"run_id"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if err := unmarshalArgs(args, &params); err != nil {
		return toolError("invalid arguments: " + err.Error())
	}
	if params.RunID == "" {
		return toolError("run_id is required")
	}

	var timeout time.Duration
	if params.TimeoutSeconds > 0 {
		timeout = time.Duration(params.TimeoutSeconds * float64(time.Second))
	}

	// Register this run as having an active elicitation-capable await.
	s.activeAwaits.Store(params.RunID, struct{}{})
	defer s.activeAwaits.Delete(params.RunID)

	// Elicit any inputs that were already pending before we registered.
	if s.pendingRegistry != nil {
		for _, pi := range s.pendingRegistry.ListByRun(params.RunID) {
			if requiresUpload(pi.FileAffordance) {
				log.Printf("mcp: skipping elicitation for input %q (run %q): upload affordance present, deliver via HTTP UI", pi.InputID, params.RunID)
				continue
			}
			prompt := s.augmentPromptWithFileURLs(pi.Prompt, &pi)
			inputID := pi.InputID
			go func(inputID, prompt string) {
				response, err := s.sendElicitation(prompt)
				if err != nil {
					return
				}
				s.runManager.DeliverInput(params.RunID, inputID, response, nil)
			}(inputID, prompt)
		}
	}

	info, err := s.runManager.WaitForCompletion(params.RunID, timeout)
	if err != nil {
		return toolError(err.Error())
	}

	result := map[string]any{
		"run_id":   info.RunID,
		"status":   info.Status,
		"result":   info.Result,
		"cost_usd": info.CostUSD,
	}
	data, _ := json.Marshal(result)
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: string(data)}},
	}
}

// --- Elicitation helpers ---

// elicitationTimeout is the maximum time to wait for a client to respond to
// an elicitation/create request.
const elicitationTimeout = 5 * time.Minute

// sendElicitation sends an elicitation/create request to the MCP client and
// blocks until the client responds or the timeout expires.
func (s *MCPServer) sendElicitation(prompt string) (string, error) {
	id := s.nextReqID.Add(1)
	idBytes, _ := json.Marshal(id)
	idKey := string(idBytes)

	ch := make(chan json.RawMessage, 1)
	s.pendingResponses.Store(idKey, ch)
	defer s.pendingResponses.Delete(idKey)

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "elicitation/create",
		"params": map[string]any{
			"message": prompt,
			"requestedSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"response": map[string]any{
						"type":        "string",
						"description": "Your response",
					},
				},
				"required": []string{"response"},
			},
		},
	}

	if err := s.sendJSON(req); err != nil {
		return "", fmt.Errorf("send elicitation request: %w", err)
	}

	var respData json.RawMessage
	select {
	case respData = <-ch:
	case <-time.After(elicitationTimeout):
		return "", fmt.Errorf("elicitation timed out after %v", elicitationTimeout)
	}

	var result struct {
		Action  string `json:"action"`
		Content struct {
			Response string `json:"response"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respData, &result); err != nil {
		return "", fmt.Errorf("unmarshal elicitation response: %w", err)
	}
	if result.Action != "accept" {
		return "", fmt.Errorf("elicitation declined: %s", result.Action)
	}

	return result.Content.Response, nil
}

// routeResponse delivers a client response to the waiting server-originated
// request (identified by the JSON-RPC id).
func (s *MCPServer) routeResponse(id *json.RawMessage, raw []byte) {
	idKey := string(*id)
	if val, ok := s.pendingResponses.Load(idKey); ok {
		ch := val.(chan json.RawMessage)
		var resp struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(raw, &resp); err == nil {
			ch <- resp.Result
		}
	}
}

// sendJSON thread-safely encodes v to the output stream.
func (s *MCPServer) sendJSON(v any) error {
	s.encMu.Lock()
	defer s.encMu.Unlock()
	if s.enc == nil {
		return fmt.Errorf("encoder not initialized")
	}
	return s.enc.Encode(v)
}

// --- Helpers ---

// unmarshalArgs decodes JSON tool arguments, treating nil/empty as an empty object.
func unmarshalArgs(args json.RawMessage, v any) error {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return json.Unmarshal(args, v)
}

func toolError(msg string) mcpToolResult {
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: msg}},
		IsError: true,
	}
}
