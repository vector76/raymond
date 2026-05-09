package cli

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/vector76/raymond/internal/daemon"
)

// launchStartupRuns fans out auto-launches for `ray serve --launch <id>`.
// It mirrors the `POST /runs` handler with a body of {"workflow_id": id} and
// no other fields: lookup, input-mode validation, budget resolution, launch.
// serverDSP is the server-wide DangerouslySkipPermissions value (already
// resolved by the serve command), passed through to LaunchRun so startup
// runs honour the same configuration as HTTP- or MCP-launched runs.
//
// Errors per id are logged to logOut and swallowed; the helper itself only
// returns an error for impossible internal conditions (nil reg or nil rm).
func launchStartupRuns(ctx context.Context, reg *daemon.Registry, rm *daemon.RunManager, serverBudget float64, serverDSP bool, ids []string, logOut io.Writer) error {
	if reg == nil {
		return fmt.Errorf("launchStartupRuns: nil registry")
	}
	if rm == nil {
		return fmt.Errorf("launchStartupRuns: nil run manager")
	}
	if len(ids) == 0 {
		return nil
	}

	var (
		wg    sync.WaitGroup
		logMu sync.Mutex
	)

	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		fmt.Fprintf(logOut, format, args...)
	}

	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()

			entry, ok := reg.GetWorkflow(id)
			if !ok {
				logf("Failed to launch %s: %v\n", id, fmt.Errorf("workflow %q not found", id))
				return
			}

			if err := daemon.ValidateInputMode(entry.Input.Mode, ""); err != nil {
				logf("Failed to launch %s: %v\n", id, err)
				return
			}

			budget := daemon.ResolveBudget(0, entry.DefaultBudget, serverBudget)

			runID, err := rm.LaunchRun(ctx, *entry, "", budget, "", serverDSP, "", nil)
			if err != nil {
				logf("Failed to launch %s: %v\n", id, err)
				return
			}

			logf("Launched run %s for workflow %s\n", runID, id)
		}(id)
	}

	wg.Wait()
	return nil
}
