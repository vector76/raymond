package executors

// This file is compiled only during testing. It exports test hooks that allow
// the external test package (executors_test) to override package-level function
// variables and construct types that would otherwise be private.

import (
	"context"

	"github.com/vector76/raymond/internal/ccwrap"
	"github.com/vector76/raymond/internal/platform"
)

// original function values for reset.
var (
	origRunScriptFn    = runScriptFn
	origInvokeStreamFn = invokeStreamFn
)

// SetRunScriptFn replaces the platform.RunScript implementation used by
// ScriptExecutor. Call ResetRunScriptFn in a defer to restore the original.
func SetRunScriptFn(fn func(context.Context, string, float64, map[string]string, string) (*platform.ScriptResult, error)) {
	runScriptFn = fn
}

// ResetRunScriptFn restores the original platform.RunScript implementation.
func ResetRunScriptFn() {
	runScriptFn = origRunScriptFn
}

// SetInvokeStreamFn replaces the ccwrap.InvokeStream implementation used by
// MarkdownExecutor. Call ResetInvokeStreamFn in a defer to restore the original.
func SetInvokeStreamFn(fn func(context.Context, string, string, string, string, float64, bool, bool, string) <-chan ccwrap.StreamItem) {
	invokeStreamFn = fn
}

// ResetInvokeStreamFn restores the original ccwrap.InvokeStream implementation.
func ResetInvokeStreamFn() {
	invokeStreamFn = origInvokeStreamFn
}

// NewScriptExecutor returns a new ScriptExecutor for testing.
func NewScriptExecutor() *ScriptExecutor { return &ScriptExecutor{} }

// NewMarkdownExecutor returns a new MarkdownExecutor for testing.
func NewMarkdownExecutor() *MarkdownExecutor { return &MarkdownExecutor{} }
