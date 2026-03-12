package executors

// This file is intentionally separate from errors.go to keep concerns clear.
// See errors.go for error types, script.go and markdown.go for executors.

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/prompts"
	wfstate "github.com/vector76/raymond/internal/state"
)

// ExecutionResult holds the outcome of executing a single state.
type ExecutionResult struct {
	Transition  parsing.Transition   // selected single transition (zero value when Transitions is set)
	Transitions []parsing.Transition // full list for multi-fork outputs; nil for single-transition outputs
	SessionID   *string              // nil for script states (session preserved externally)
	CostUSD     float64              // 0.0 for script states
}

// ExecutionContext holds shared configuration and mutable step-counters passed
// to every executor invocation.
type ExecutionContext struct {
	Bus                        *bus.Bus
	WorkflowID                 string
	DebugDir                   string  // empty string = debug disabled
	StateDir                   string  // empty string = use default
	DefaultModel               string  // empty = no override; policy takes precedence
	DefaultEffort              string  // empty = no override
	Timeout                    float64 // ≤ 0 = no timeout
	DangerouslySkipPermissions bool

	stepMu       sync.Mutex
	StepCounters map[string]int // per-agent step counter for debug filenames; guarded by stepMu
}

// GetNextStepNumber increments and returns the step counter for agentID.
// Step numbers are 1-indexed and tracked separately per agent.
// Safe for concurrent calls.
func (c *ExecutionContext) GetNextStepNumber(agentID string) int {
	c.stepMu.Lock()
	defer c.stepMu.Unlock()
	if c.StepCounters == nil {
		c.StepCounters = make(map[string]int)
	}
	c.StepCounters[agentID]++
	return c.StepCounters[agentID]
}

// StateExecutor is the common interface for MarkdownExecutor and ScriptExecutor.
//
// Execute runs a single state, emits the appropriate events, and returns the
// resolved transition and cost. The caller (orchestrator) is responsible for
// applying the transition and persisting updated state.
//
// SessionID contract: ExecutionResult.SessionID uses nil to mean "no change"
// (preserve the agent's existing session ID). A non-nil pointer — even one
// pointing to an empty string — replaces the agent's session ID. Script
// executors always return nil because scripts do not own or modify sessions;
// markdown executors return the new session ID obtained from Claude.
type StateExecutor interface {
	Execute(
		ctx context.Context,
		agent *wfstate.AgentState,
		wfState *wfstate.WorkflowState,
		execCtx *ExecutionContext,
	) (ExecutionResult, error)
}

// Singleton executor instances.
var (
	markdownSingleton StateExecutor = &MarkdownExecutor{}
	scriptSingleton   StateExecutor = &ScriptExecutor{}
)

// GetExecutor returns the singleton executor appropriate for filename.
// Markdown (.md) → MarkdownExecutor; scripts (.sh, .bat, .ps1) → ScriptExecutor.
// Unknown extensions fall back to MarkdownExecutor.
func GetExecutor(filename string) StateExecutor {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".sh", ".bat", ".ps1":
		return scriptSingleton
	default:
		return markdownSingleton
	}
}

// --------------------------------------------------------------------------
// Shared utilities
// --------------------------------------------------------------------------

// ExtractStateName strips the recognized state file extension (.md, .sh, .bat, .ps1)
// from filename, case-insensitively. If no recognized extension is present the
// filename is returned unchanged. Delegates to parsing.ExtractStateName.
func ExtractStateName(filename string) string {
	return parsing.ExtractStateName(filename)
}

// ResolveTransitionTargets resolves abstract state names in t to concrete
// filenames using prompts.ResolveState.
//
//   - result tags pass through unchanged (no state references to resolve).
//   - Workflow tags (call-workflow, function-workflow, fork-workflow) have a
//     target that is a path to another workflow, resolved later by
//     specifier.Resolve; the target is passed through unchanged. Their
//     "return" and "next" attributes (which reference states in the current
//     workflow) are still resolved.
//   - For all other tags the main target is also resolved.
func ResolveTransitionTargets(t parsing.Transition, scopeDir string) (parsing.Transition, error) {
	if t.Tag == "result" {
		return t, nil
	}

	resolvedTarget := t.Target
	if !parsing.IsWorkflowTag(t.Tag) {
		var err error
		resolvedTarget, err = prompts.ResolveState(scopeDir, t.Target)
		if err != nil {
			return parsing.Transition{}, err
		}
	}

	resolvedAttrs := make(map[string]string, len(t.Attributes))
	for k, v := range t.Attributes {
		resolvedAttrs[k] = v
	}

	if ret, ok := resolvedAttrs["return"]; ok {
		r, err := prompts.ResolveState(scopeDir, ret)
		if err != nil {
			return parsing.Transition{}, err
		}
		resolvedAttrs["return"] = r
	}

	if next, ok := resolvedAttrs["next"]; ok {
		r, err := prompts.ResolveState(scopeDir, next)
		if err != nil {
			return parsing.Transition{}, err
		}
		resolvedAttrs["next"] = r
	}

	return parsing.Transition{
		Tag:        t.Tag,
		Target:     resolvedTarget,
		Attributes: resolvedAttrs,
		Payload:    t.Payload,
	}, nil
}

// ExtractCostFromResults searches the Claude stream results (in reverse order)
// for a "total_cost_usd" field and returns it as float64. Returns 0.0 when not
// found.
func ExtractCostFromResults(results []map[string]any) float64 {
	for i := len(results) - 1; i >= 0; i-- {
		obj := results[i]
		cost, ok := obj["total_cost_usd"]
		if !ok {
			continue
		}
		switch v := cost.(type) {
		case float64:
			return v
		case int:
			return float64(v)
		case int64:
			return float64(v)
		}
	}
	return 0.0
}

// ExtractTokensFromResults searches the Claude stream results (in reverse order)
// for a "usage" field. When found, it extracts cache_creation_input_tokens,
// cache_read_input_tokens, and input_tokens, sums them, and returns a pointer
// to the sum. Returns nil when no result has a "usage" key or when "usage" is
// not a map[string]any.
func ExtractTokensFromResults(results []map[string]any) *int64 {
	for i := len(results) - 1; i >= 0; i-- {
		obj := results[i]
		usageRaw, ok := obj["usage"]
		if !ok {
			continue
		}
		usage, ok := usageRaw.(map[string]any)
		if !ok {
			return nil
		}
		var sum int64
		for _, key := range []string{"cache_creation_input_tokens", "cache_read_input_tokens", "input_tokens"} {
			val, exists := usage[key]
			if !exists {
				continue
			}
			switch v := val.(type) {
			case float64:
				sum += int64(v)
			case int:
				sum += int64(v)
			case int64:
				sum += v
			}
		}
		return &sum
	}
	return nil
}
