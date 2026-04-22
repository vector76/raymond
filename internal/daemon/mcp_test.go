package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/events"
	"github.com/vector76/raymond/internal/orchestrator"
)

// mcpTestClient manages pipes for communicating with an MCPServer under test.
type mcpTestClient struct {
	t        *testing.T
	inWriter *io.PipeWriter
	scanner  *bufio.Scanner
	cancel   context.CancelFunc
}

func newMCPTestClient(t *testing.T, srv *MCPServer) *mcpTestClient {
	t.Helper()

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		srv.Serve(ctx, inReader, outWriter)
		outWriter.Close()
	}()

	c := &mcpTestClient{
		t:        t,
		inWriter: inWriter,
		scanner:  bufio.NewScanner(outReader),
		cancel:   cancel,
	}
	t.Cleanup(c.close)
	return c
}

func (c *mcpTestClient) close() {
	c.cancel()
	c.inWriter.Close()
}

// send writes a JSON-RPC request and reads the response.
func (c *mcpTestClient) send(req map[string]any) map[string]any {
	c.t.Helper()
	data, err := json.Marshal(req)
	require.NoError(c.t, err)
	data = append(data, '\n')
	_, err = c.inWriter.Write(data)
	require.NoError(c.t, err)

	ok := c.scanner.Scan()
	require.True(c.t, ok, "expected response from MCP server, err: %v", c.scanner.Err())

	var resp map[string]any
	require.NoError(c.t, json.Unmarshal(c.scanner.Bytes(), &resp))
	return resp
}

// initialize sends the initialize request.
func (c *mcpTestClient) initialize(withElicitation bool) map[string]any {
	c.t.Helper()
	caps := map[string]any{}
	if withElicitation {
		caps["elicitation"] = map[string]any{}
	}
	return c.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    caps,
			"clientInfo": map[string]any{
				"name":    "test-client",
				"version": "1.0.0",
			},
		},
	})
}

// writeRequest writes a JSON-RPC request without reading the response.
func (c *mcpTestClient) writeRequest(req map[string]any) {
	c.t.Helper()
	data, err := json.Marshal(req)
	require.NoError(c.t, err)
	data = append(data, '\n')
	_, err = c.inWriter.Write(data)
	require.NoError(c.t, err)
}

// readMessage reads the next JSON message from the server.
func (c *mcpTestClient) readMessage() map[string]any {
	c.t.Helper()
	ok := c.scanner.Scan()
	require.True(c.t, ok, "expected message from MCP server, err: %v", c.scanner.Err())
	var msg map[string]any
	require.NoError(c.t, json.Unmarshal(c.scanner.Bytes(), &msg))
	return msg
}

// callTool sends a tools/call request.
func (c *mcpTestClient) callTool(id int, name string, args map[string]any) map[string]any {
	c.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	return c.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	})
}

// --- Result helpers ---

// toolResultJSON extracts the tool result text and unmarshals it as a JSON object.
func toolResultJSON(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	text := toolResultText(t, resp)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &parsed))
	return parsed
}

// toolResultArray extracts the tool result text and unmarshals it as a JSON array.
func toolResultArray(t *testing.T, resp map[string]any) []map[string]any {
	t.Helper()
	text := toolResultText(t, resp)
	var parsed []map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &parsed))
	return parsed
}

// toolResultText extracts the text from the first content item.
func toolResultText(t *testing.T, resp map[string]any) string {
	t.Helper()
	result, ok := resp["result"].(map[string]any)
	require.True(t, ok, "expected result in response, got: %v", resp)
	content, ok := result["content"].([]any)
	require.True(t, ok, "expected content array")
	require.NotEmpty(t, content)
	first := content[0].(map[string]any)
	return first["text"].(string)
}

// toolIsError returns true if the tool result has isError set.
func toolIsError(t *testing.T, resp map[string]any) bool {
	t.Helper()
	result := resp["result"].(map[string]any)
	isErr, _ := result["isError"].(bool)
	return isErr
}

// --- Setup helpers ---

func newMCPTestSetup(t *testing.T) (*mcpTestClient, *fakeOrchestrator) {
	return newMCPTestSetupOpts(t, false)
}

func newMCPTestSetupOpts(t *testing.T, requiresHumanInput bool) (*mcpTestClient, *fakeOrchestrator) {
	t.Helper()

	scopeDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(scopeDir, "START.md"),
		[]byte("# Start\nDo something."),
		0o644,
	))

	humanInput := "false"
	if requiresHumanInput {
		humanInput = "true"
	}
	manifest := fmt.Sprintf(
		"id: test-workflow\nname: Test Workflow\ndescription: A test workflow\ndefault_budget: 5.0\nrequires_human_input: %s\n",
		humanInput,
	)
	require.NoError(t, os.WriteFile(
		filepath.Join(scopeDir, "workflow.yaml"),
		[]byte(manifest),
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

	srv := NewMCPServer(reg, rm)
	srv.SetPendingRegistry(pr)
	rm.SetAwaitNotifier(srv.HandleAwaitNotification)

	client := newMCPTestClient(t, srv)
	return client, fake
}

// --- Tests ---

func TestMCPToolsList(t *testing.T) {
	client, _ := newMCPTestSetup(t)
	client.initialize(false)

	resp := client.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})

	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)
	require.Len(t, tools, 7)

	names := make([]string, len(tools))
	for i, tool := range tools {
		td := tool.(map[string]any)
		names[i] = td["name"].(string)
		// Every tool must have an inputSchema.
		assert.NotNil(t, td["inputSchema"], "tool %q should have inputSchema", names[i])
	}

	assert.Contains(t, names, "raymond_list_workflows")
	assert.Contains(t, names, "raymond_run")
	assert.Contains(t, names, "raymond_status")
	assert.Contains(t, names, "raymond_await")
	assert.Contains(t, names, "raymond_cancel")
	assert.Contains(t, names, "raymond_list_pending_inputs")
	assert.Contains(t, names, "raymond_provide_input")
}

func TestMCPListWorkflows(t *testing.T) {
	client, _ := newMCPTestSetup(t)
	client.initialize(false)

	resp := client.callTool(2, "raymond_list_workflows", nil)

	assert.False(t, toolIsError(t, resp))
	workflows := toolResultArray(t, resp)
	require.Len(t, workflows, 1)
	assert.Equal(t, "test-workflow", workflows[0]["id"])
	assert.Equal(t, "Test Workflow", workflows[0]["name"])
	assert.Equal(t, "A test workflow", workflows[0]["description"])
}

func TestMCPRun(t *testing.T) {
	client, _ := newMCPTestSetup(t)
	client.initialize(true)

	resp := client.callTool(2, "raymond_run", map[string]any{
		"workflow_id": "test-workflow",
		"input":       "hello",
		"budget":      3.0,
	})

	assert.False(t, toolIsError(t, resp))
	result := toolResultJSON(t, resp)
	assert.NotEmpty(t, result["run_id"])
	assert.Equal(t, "running", result["status"])
}

func TestMCPStatus(t *testing.T) {
	client, _ := newMCPTestSetup(t)
	client.initialize(true)

	// Launch a run first.
	runResp := client.callTool(2, "raymond_run", map[string]any{
		"workflow_id": "test-workflow",
	})
	runResult := toolResultJSON(t, runResp)
	runID := runResult["run_id"].(string)

	// Query status.
	resp := client.callTool(3, "raymond_status", map[string]any{
		"run_id": runID,
	})

	assert.False(t, toolIsError(t, resp))
	result := toolResultJSON(t, resp)
	assert.Equal(t, runID, result["run_id"])
	assert.Equal(t, "running", result["status"])
	assert.NotNil(t, result["agents"])
	assert.NotNil(t, result["cost_usd"])
	assert.NotNil(t, result["elapsed_seconds"])
}

func TestMCPAwait(t *testing.T) {
	client, fake := newMCPTestSetup(t)
	client.initialize(true)

	// Make the orchestrator complete quickly with a result.
	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		b := bus.New()
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(b)
		}
		b.Emit(events.AgentTerminated{
			AgentID:       "main",
			ResultPayload: "workflow completed successfully",
			Timestamp:     time.Now(),
		})
		b.Emit(events.WorkflowCompleted{
			TotalCostUSD: 1.5,
			Timestamp:    time.Now(),
		})
		return nil
	}

	// Launch a run.
	runResp := client.callTool(2, "raymond_run", map[string]any{
		"workflow_id": "test-workflow",
	})
	runResult := toolResultJSON(t, runResp)
	runID := runResult["run_id"].(string)

	// Await completion.
	resp := client.callTool(3, "raymond_await", map[string]any{
		"run_id": runID,
	})

	assert.False(t, toolIsError(t, resp))
	result := toolResultJSON(t, resp)
	assert.Equal(t, runID, result["run_id"])
	assert.Equal(t, "completed", result["status"])
	assert.Equal(t, "workflow completed successfully", result["result"])
	assert.Equal(t, 1.5, result["cost_usd"])
}

func TestMCPCancel(t *testing.T) {
	client, _ := newMCPTestSetup(t)
	client.initialize(true)

	// Launch a run (fake orchestrator blocks by default).
	runResp := client.callTool(2, "raymond_run", map[string]any{
		"workflow_id": "test-workflow",
	})
	runResult := toolResultJSON(t, runResp)
	runID := runResult["run_id"].(string)

	// Give the orchestrator goroutine a moment to start.
	time.Sleep(50 * time.Millisecond)

	// Cancel the run.
	resp := client.callTool(3, "raymond_cancel", map[string]any{
		"run_id": runID,
	})

	assert.False(t, toolIsError(t, resp))
	result := toolResultJSON(t, resp)
	assert.Equal(t, runID, result["run_id"])
	assert.Equal(t, "cancelled", result["status"])
}

func TestMCPRequiresHumanInput_Rejected(t *testing.T) {
	client, _ := newMCPTestSetupOpts(t, true) // workflow requires human input
	client.initialize(false)                   // client does NOT support elicitation

	resp := client.callTool(2, "raymond_run", map[string]any{
		"workflow_id": "test-workflow",
	})

	assert.True(t, toolIsError(t, resp))
	errText := toolResultText(t, resp)
	assert.Contains(t, errText, "requires human input")
	assert.Contains(t, errText, "does not support elicitation")
}

func TestMCPRequiresHumanInput_Allowed(t *testing.T) {
	client, _ := newMCPTestSetupOpts(t, true) // workflow requires human input
	client.initialize(true)                    // client supports elicitation

	resp := client.callTool(2, "raymond_run", map[string]any{
		"workflow_id": "test-workflow",
	})

	assert.False(t, toolIsError(t, resp))
	result := toolResultJSON(t, resp)
	assert.NotEmpty(t, result["run_id"])
	assert.Equal(t, "running", result["status"])
}

func TestMCPListPendingInputs(t *testing.T) {
	client, fake := newMCPTestSetup(t)
	client.initialize(false)

	awaitReady := make(chan struct{})
	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		b := bus.New()
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(b)
		}
		if opts.AwaitCallback != nil {
			opts.AwaitCallback("main", "test-input-1", "What next?", "NEXT.md")
		}
		close(awaitReady)
		<-ctx.Done()
		return ctx.Err()
	}

	// Launch a run.
	runResp := client.callTool(2, "raymond_run", map[string]any{
		"workflow_id": "test-workflow",
	})
	assert.False(t, toolIsError(t, runResp))
	runResult := toolResultJSON(t, runResp)
	runID := runResult["run_id"].(string)

	// Wait for await callback.
	select {
	case <-awaitReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for await callback")
	}

	// List pending inputs.
	resp := client.callTool(3, "raymond_list_pending_inputs", nil)
	assert.False(t, toolIsError(t, resp))

	inputs := toolResultArray(t, resp)
	require.Len(t, inputs, 1)
	assert.Equal(t, runID, inputs[0]["run_id"])
	assert.Equal(t, "test-input-1", inputs[0]["input_id"])
	assert.Equal(t, "What next?", inputs[0]["prompt"])
	assert.Equal(t, "test-workflow", inputs[0]["workflow_id"])
}

func TestMCPProvideInput(t *testing.T) {
	client, fake := newMCPTestSetup(t)
	client.initialize(false)

	inputDelivered := make(chan string, 1)
	awaitReady := make(chan struct{})

	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		b := bus.New()
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(b)
		}
		if opts.AwaitCallback != nil {
			opts.AwaitCallback("main", "test-input-1", "What next?", "NEXT.md")
		}
		close(awaitReady)

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

	// Launch a run.
	runResp := client.callTool(2, "raymond_run", map[string]any{
		"workflow_id": "test-workflow",
	})
	runResult := toolResultJSON(t, runResp)
	runID := runResult["run_id"].(string)

	// Wait for await.
	select {
	case <-awaitReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for await callback")
	}

	// Provide input.
	resp := client.callTool(3, "raymond_provide_input", map[string]any{
		"input_id": "test-input-1",
		"response": "Do the thing",
	})
	assert.False(t, toolIsError(t, resp))

	result := toolResultJSON(t, resp)
	assert.Equal(t, runID, result["run_id"])
	assert.Equal(t, "test-input-1", result["input_id"])
	assert.Equal(t, "resumed", result["status"])

	// Verify delivery.
	select {
	case delivered := <-inputDelivered:
		assert.Equal(t, "Do the thing", delivered)
	case <-time.After(5 * time.Second):
		t.Fatal("input not delivered")
	}
}

func TestMCPElicitation(t *testing.T) {
	client, fake := newMCPTestSetup(t)
	client.initialize(true) // enable elicitation

	// startAwait signals the orchestrator to hit <await>. This lets us
	// send raymond_await first so the active-await is registered before
	// the callback fires.
	startAwait := make(chan struct{})
	inputDelivered := make(chan string, 1)

	fake.behaviour = func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
		b := bus.New()
		if opts.ObserverSetup != nil {
			opts.ObserverSetup(b)
		}

		// Wait for signal before hitting <await>.
		select {
		case <-startAwait:
		case <-ctx.Done():
			return ctx.Err()
		}

		if opts.AwaitCallback != nil {
			opts.AwaitCallback("main", "elicit-input-1", "What should I do?", "NEXT.md")
		}

		// Wait for input delivery.
		if opts.AwaitInputCh != nil {
			select {
			case input := <-opts.AwaitInputCh:
				inputDelivered <- input.Response
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		b.Emit(events.AgentTerminated{
			AgentID:       "main",
			ResultPayload: "done with input",
			Timestamp:     time.Now(),
		})
		b.Emit(events.WorkflowCompleted{
			WorkflowID:   workflowID,
			TotalCostUSD: 1.0,
			Timestamp:    time.Now(),
		})
		return nil
	}

	// Launch a run.
	runResp := client.callTool(2, "raymond_run", map[string]any{
		"workflow_id": "test-workflow",
	})
	runResult := toolResultJSON(t, runResp)
	runID := runResult["run_id"].(string)

	// Send raymond_await asynchronously (dispatched in a goroutine because
	// the client has elicitation). Give the goroutine a moment to register
	// the active await before signalling the orchestrator.
	client.writeRequest(map[string]any{
		"jsonrpc": "2.0",
		"id":      10,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "raymond_await",
			"arguments": map[string]any{
				"run_id": runID,
			},
		},
	})
	time.Sleep(50 * time.Millisecond)

	// Signal the orchestrator to hit <await>.
	close(startAwait)

	// Read the elicitation/create request from the server.
	elicitReq := client.readMessage()
	assert.Equal(t, "elicitation/create", elicitReq["method"])
	assert.NotNil(t, elicitReq["id"])

	params := elicitReq["params"].(map[string]any)
	assert.Equal(t, "What should I do?", params["message"])

	// Send elicitation response.
	elicitReqID := elicitReq["id"]
	client.writeRequest(map[string]any{
		"jsonrpc": "2.0",
		"id":      elicitReqID,
		"result": map[string]any{
			"action": "accept",
			"content": map[string]any{
				"response": "Please proceed",
			},
		},
	})

	// Verify the input was delivered to the orchestrator.
	select {
	case delivered := <-inputDelivered:
		assert.Equal(t, "Please proceed", delivered)
	case <-time.After(5 * time.Second):
		t.Fatal("elicitation input not delivered")
	}

	// Read the raymond_await response (run should complete).
	awaitResp := client.readMessage()
	assert.NotNil(t, awaitResp["result"])
	result := awaitResp["result"].(map[string]any)
	content := result["content"].([]any)
	first := content[0].(map[string]any)
	text := first["text"].(string)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &parsed))
	assert.Equal(t, runID, parsed["run_id"])
	assert.Equal(t, "completed", parsed["status"])
	assert.Equal(t, "done with input", parsed["result"])
}
