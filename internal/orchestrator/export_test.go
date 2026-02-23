package orchestrator

// This file is compiled only during testing. It exposes package-level
// variables for test injection so the external test package can override
// the executor factory and subscribe to the bus before agents run.

import (
	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/executors"
)

// original function values for reset.
var (
	origExecutorFactory = executorFactory
	origBusHook         = busHook
)

// SetExecutorFactory replaces the executor factory used by RunAllAgents.
// Call ResetExecutorFactory in a defer to restore the original.
func SetExecutorFactory(f func(string) executors.StateExecutor) {
	executorFactory = f
}

// ResetExecutorFactory restores the original executor factory.
func ResetExecutorFactory() {
	executorFactory = origExecutorFactory
}

// SetBusHook sets a callback that is invoked with the Bus immediately after
// it is created (before WorkflowStarted is emitted). Use it in tests to
// subscribe to events before the workflow runs.
func SetBusHook(hook func(*bus.Bus)) {
	busHook = hook
}

// ResetBusHook removes the bus hook.
func ResetBusHook() {
	busHook = origBusHook
}
