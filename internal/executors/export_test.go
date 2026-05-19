package executors

// This file is compiled only during testing. It exports test hooks that allow
// the external test package (executors_test) to override package-level function
// variables and construct types that would otherwise be private.

import (
	"context"

	"github.com/vector76/raymond/internal/backend"
	"github.com/vector76/raymond/internal/ccwrap"
	"github.com/vector76/raymond/internal/platform"
)

// original function values for reset.
var (
	origRunScriptFn = runScriptFn
)

// claudeInvokeRestore is the restore callback returned by the backend
// package when SetInvokeStreamFn last installed an override. Reset
// chains through to it so cross-package test setup remains symmetric.
var claudeInvokeRestore func()

// SetRunScriptFn replaces the platform.RunScript implementation used by
// ScriptExecutor. Call ResetRunScriptFn in a defer to restore the original.
func SetRunScriptFn(fn func(context.Context, string, float64, map[string]string, string, func(string, []byte)) (*platform.ScriptResult, error)) {
	runScriptFn = fn
}

// ResetRunScriptFn restores the original platform.RunScript implementation.
func ResetRunScriptFn() {
	runScriptFn = origRunScriptFn
}

// SetInvokeStreamFn replaces the ccwrap.InvokeStream implementation used by
// the Claude backend. Call ResetInvokeStreamFn in a defer to restore the
// original. The function signature is preserved from the pre-backend
// refactor so existing tests need no change.
func SetInvokeStreamFn(fn func(context.Context, string, string, string, string, float64, bool, bool, string, bool) <-chan ccwrap.StreamItem) {
	// If a previous override is still installed, restore it before stacking
	// the new one — last-writer-wins matches the historical behaviour.
	if claudeInvokeRestore != nil {
		claudeInvokeRestore()
	}
	claudeInvokeRestore = backend.SetClaudeInvokeStreamFnForTest(fn)
}

// ResetInvokeStreamFn restores the original ccwrap.InvokeStream implementation.
func ResetInvokeStreamFn() {
	if claudeInvokeRestore != nil {
		claudeInvokeRestore()
		claudeInvokeRestore = nil
	}
}

// NewScriptExecutor returns a new ScriptExecutor for testing.
func NewScriptExecutor() *ScriptExecutor { return &ScriptExecutor{} }

// NewMarkdownExecutor returns a new MarkdownExecutor for testing.
func NewMarkdownExecutor() *MarkdownExecutor { return &MarkdownExecutor{} }
