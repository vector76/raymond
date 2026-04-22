package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
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
// It exposes five launcher-phase tools for the Raymond daemon.
type MCPServer struct {
	registry       *Registry
	runManager     *RunManager
	hasElicitation bool
}

// NewMCPServer creates an MCPServer wired to the given registry and run manager.
func NewMCPServer(reg *Registry, rm *RunManager) *MCPServer {
	return &MCPServer{
		registry:   reg,
		runManager: rm,
	}
}

// HasElicitation reports whether the MCP client declared elicitation support
// during initialization.
func (s *MCPServer) HasElicitation() bool {
	return s.hasElicitation
}

// Serve reads JSON-RPC 2.0 requests from in (newline-delimited) and writes
// responses to out. It blocks until ctx is cancelled or in reaches EOF.
func (s *MCPServer) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	enc := json.NewEncoder(out)

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

		var req jsonrpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			enc.Encode(jsonrpcResponse{
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
		enc.Encode(resp)
	}

	return scanner.Err()
}

func (s *MCPServer) dispatch(ctx context.Context, req *jsonrpcRequest) jsonrpcResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	default:
		return jsonrpcResponse{
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
	}
}

// --- tools/call ---

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *MCPServer) handleToolsCall(ctx context.Context, req *jsonrpcRequest) jsonrpcResponse {
	var params toolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonrpcError{Code: codeInvalidParams, Message: "invalid tools/call params"},
		}
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
	default:
		result = mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("unknown tool: %s", params.Name)}},
			IsError: true,
		}
	}

	return jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

// --- Tool implementations ---

func (s *MCPServer) toolListWorkflows() mcpToolResult {
	entries := s.registry.ListWorkflows()

	type wfItem struct {
		ID                 string            `json:"id"`
		Name               string            `json:"name"`
		Description        string            `json:"description"`
		InputSchema        map[string]string `json:"input_schema"`
		DefaultBudget      float64           `json:"default_budget"`
		RequiresHumanInput bool              `json:"requires_human_input"`
	}

	items := make([]wfItem, len(entries))
	for i, e := range entries {
		items[i] = wfItem{
			ID:                 e.ID,
			Name:               e.Name,
			Description:        e.Description,
			InputSchema:        e.InputSchema,
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

	budget := params.Budget
	if budget <= 0 {
		budget = entry.DefaultBudget
	}

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
