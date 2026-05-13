package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/vector76/raymond/internal/config"
	"github.com/vector76/raymond/internal/daemon"
	"github.com/vector76/raymond/internal/orchestrator"
	wfstate "github.com/vector76/raymond/internal/state"
)

// cliOrch adapts c.runner to the daemon.Orchestrator interface so that
// tests injecting a no-op runner via NewTestCLI never spawn real LLM work.
type cliOrch struct {
	fn func(context.Context, string, orchestrator.RunOptions) error
}

func (a *cliOrch) RunAllAgents(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error {
	return a.fn(ctx, workflowID, opts)
}

// newServeCmd builds the "serve" subcommand that starts the Raymond daemon.
func (c *CLI) newServeCmd() *cobra.Command {
	var (
		roots                      []string
		launches                   []string
		port                       int
		mcp                        bool
		noHTTP                     bool
		workdir                    string
		maxFileSize                int64
		maxTotalSize               int64
		maxFileCount               int
		dangerouslySkipPermissions bool
		clean                      bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Raymond workflow daemon",
		Long: `Start the Raymond daemon which exposes discovered workflows via an HTTP API
and/or MCP tool interface.

The daemon scans the configured --root directories for workflow directories
and zip archives containing workflow.yaml manifests, then serves them to
clients.

Defaults for --root, --port, --mcp, --no-http, --workdir, --max-file-size,
--max-total-size, and --max-file-count may also be set in .raymond/config.toml
under the [raymond.serve] section. CLI --root values are appended to (not
replacing) the config file's root.

Use --launch <workflow_id> (repeatable) to auto-dispatch one or more
workflows after the HTTP/MCP transports come up. Launches apply only to
this invocation — there is no scheduling, retry, or persistence across
restarts. Each id must already be discoverable via --root, and launch
outcomes are logged on the same channel as other startup status messages
(stdout by default; stderr under --mcp). Workflows whose first state
requires input cannot be auto-launched and must be started via the HTTP
API or web UI.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fileCfg, err := config.LoadServeConfig("")
			if err != nil {
				return err
			}

			// Also load the main [raymond] section so server-wide defaults
			// (budget in particular) match what `ray run` would pick up for
			// the same config.toml. Without this, daemon launches that omit
			// budget fall through to a hardcoded default instead of the
			// project's configured value.
			raymondCfg, err := config.LoadConfig("")
			if err != nil {
				return err
			}

			cliArgs := config.ServeCLIArgs{
				Roots:        roots,
				MCP:          mcp,
				NoHTTP:       noHTTP,
				Workdir:      workdir,
				MaxFileSize:  maxFileSize,
				MaxTotalSize: maxTotalSize,
				MaxFileCount: maxFileCount,
			}
			if cmd.Flags().Changed("port") {
				p := port
				cliArgs.Port = &p
			}

			merged := config.MergeServeConfig(fileCfg, cliArgs)

			if len(merged.Roots) == 0 {
				return fmt.Errorf("at least one --root directory is required (or set [raymond.serve].root in config.toml)")
			}

			if merged.NoHTTP && !merged.MCP {
				return fmt.Errorf("--no-http requires --mcp (at least one transport must be enabled)")
			}

			// When MCP is enabled, stdout is reserved for JSON-RPC.
			// Direct status messages to stderr instead.
			logOut := cmd.OutOrStdout()
			if merged.MCP {
				logOut = cmd.ErrOrStderr()
			}

			reg, err := daemon.NewRegistry(merged.Roots)
			if err != nil {
				return fmt.Errorf("initializing workflow registry: %w", err)
			}

			workflows := reg.ListWorkflows()
			fmt.Fprintf(logOut, "Discovered %d workflow(s)\n", len(workflows))
			for _, wf := range workflows {
				fmt.Fprintf(logOut, "  %s", wf.ID)
				if wf.Name != "" {
					fmt.Fprintf(logOut, " — %s", wf.Name)
				}
				fmt.Fprintln(logOut)
			}

			cwd := merged.Workdir
			if cwd == "" {
				cwd, _ = os.Getwd()
			}

			// Resolve the project's raymond directory once. Both the run
			// manager (state pool + tasks cleanup) and the pending registry
			// (one registry per project, intentionally not per-pool — see
			// below) anchor here.
			raymondDir, err := config.FindRaymondDir(cwd, true)
			if err != nil {
				return fmt.Errorf("resolving .raymond dir: %w", err)
			}

			// `ray serve` is the daemon, so route every read/write/recover
			// through the serve pool (.raymond/serve-state/). Passing the
			// resolved path explicitly keeps the run manager pool-agnostic:
			// it treats stateDir as an opaque override and never falls
			// through to ResolvePoolDir's discovery (which would re-walk
			// the filesystem from cwd at every read/write).
			// FindRaymondDir(cwd, true) guarantees raymondDir is non-empty
			// on success (it either returns a path or errors), so we can
			// join directly without a fallback.
			serveStateDir := filepath.Join(raymondDir, "serve-state")

			// --clean: archive every non-terminal serve-state file before
			// recovery sees them. Runs BEFORE the pending registry replays
			// and BEFORE NewRunManagerForServe so the dangling-record drop
			// policy (bead-5) naturally fires on the next prune for any
			// pending entry whose paired state file we just moved aside.
			// Only the serve pool is touched — the CLI pool at
			// .raymond/state/ is intentionally out of scope.
			if clean {
				abandonDir, archived, err := daemon.ArchiveNonTerminalServeState(serveStateDir, time.Now)
				if err != nil {
					return fmt.Errorf("--clean: %w", err)
				}
				if len(archived) > 0 {
					fmt.Fprintf(logOut, "--clean: archived %d non-terminal serve-state file(s) to %s\n", len(archived), abandonDir)
				} else {
					fmt.Fprintf(logOut, "--clean: no non-terminal serve-state files to archive\n")
				}
			}

			// Build the pending-input registry FIRST so its on-disk
			// pending_inputs.jsonl is replayed before the run manager's
			// recovery launches. The relaunch path consults the registry
			// when wiring askInputCh / AskCallback for recovered
			// asking-state runs, and the dangling-record drop policy
			// (NewRunManagerForServe → PruneDangling) runs against the
			// already-replayed in-memory view. Reversing this order would
			// race the recovery goroutines against an empty registry.
			//
			// The registry deliberately lives at `<raymondDir>/`, NOT under
			// the serve pool: one registry per project, not per pool. A
			// pending ask is a workflow-author/user-facing artifact whose
			// identity (run_id + ask_id) is independent of which run pool
			// the run happens to belong to. Co-locating it with serve-state
			// would force every future tool that inspects pending asks to
			// guess the right pool. See the Phase 2 plan for the recorded
			// rationale.
			pr, err := daemon.NewPendingRegistry(raymondDir)
			if err != nil {
				return fmt.Errorf("initializing pending registry: %w", err)
			}

			rm, err := daemon.NewRunManagerForServe(serveStateDir, cwd, &cliOrch{fn: c.runner}, pr)
			if err != nil {
				return fmt.Errorf("initializing run manager: %w", err)
			}
			// Plumb the raymond directory so DeleteRun can derive
			// `<raymondDir>/tasks/<id>/` without stripping a segment off
			// the (pool-dependent) state path.
			rm.SetRaymondDir(raymondDir)

			// Extract the configured budget. config.ValidateConfig accepts
			// non-negative floats (with 0 meaning unlimited). We forward only
			// strictly positive caps as the server default; absent, malformed,
			// or zero values leave configBudget at 0, which the daemon's
			// ResolveBudget ladder treats as "unset → fall through" and which
			// terminates at daemonDefaultBudgetUSD (also 0 = unlimited). So
			// "unspecified anywhere" naturally yields an unlimited run.
			var configBudget float64
			if v, ok := raymondCfg["budget"]; ok {
				if f, ok := v.(float64); ok && f > 0 {
					configBudget = f
				}
			}

			// Resolve the effective dangerously_skip_permissions for the
			// server: CLI flag wins; else config file value if present;
			// else the global default (true). Mirrors the CLI launcher's
			// resolution so daemon-launched runs honour the same config.
			effectiveServerDSP := defaultDangerouslySkipPermissions
			if v, ok := raymondCfg["dangerously_skip_permissions"].(bool); ok {
				effectiveServerDSP = v
			}
			if cmd.Flags().Changed("dangerously-skip-permissions") {
				effectiveServerDSP = dangerouslySkipPermissions
			}

			// The event sink wired into the coordinator broadcasts
			// ShutdownRequested/ShutdownComplete frames over /events and
			// per-run streams. With no HTTP server it has nowhere to land,
			// so we substitute a no-op (the MCP transport has no equivalent
			// broadcast channel for daemon-wide events).
			eventSink := func(_ any) {}

			var srv *daemon.Server
			if !merged.NoHTTP {
				srv = daemon.NewServer(reg, rm, merged.Port)
				srv.SetPendingRegistry(pr)
				srv.SetDefaultBudget(configBudget)
				srv.SetDefaultDangerouslySkipPermissions(effectiveServerDSP)
				srv.SetDefaultUploadCaps(merged.MaxFileSize, merged.MaxTotalSize, merged.MaxFileCount)
				eventSink = srv.PublishGlobalEvent
			}

			// Construct the coordinator after the server (if any) so we can
			// pass its publish surface as the event sink. The rm is the
			// runFleet — *RunManager satisfies that interface via the
			// compile-time assertion in shutdowncoordinator.go.
			coordinator := daemon.NewShutdownCoordinator(rm, eventSink)
			if srv != nil {
				// Install the coordinator on the server *before*
				// ListenAndServe so an early POST /shutdown or POST /runs
				// can't race in and observe an unconfigured handler:
				// SetShutdownCoordinator wires both the /shutdown handler
				// and the launch-rejection guard (which consults
				// shutdownDriver.InProgress()).
				srv.SetShutdownCoordinator(coordinator)

				fmt.Fprintf(logOut, "HTTP server listening on port %d\n", merged.Port)
				go func() {
					if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
						fmt.Fprintf(cmd.ErrOrStderr(), "HTTP server error: %v\n", err)
					}
				}()
				// Final HTTP shutdown after the coordinator has already
				// drained all runs. Kept at the original short timeout: by
				// the time this defer fires, ListenAndServe only needs to
				// release sockets — every in-flight orchestrator goroutine
				// has already exited via the tier sequence.
				defer func() {
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					srv.Shutdown(shutdownCtx)
				}()
			}

			mcpDone := make(chan struct{})
			if merged.MCP {
				mcpSrv := daemon.NewMCPServer(reg, rm)
				mcpSrv.SetPendingRegistry(pr)
				mcpSrv.SetDefaultBudget(configBudget)
				mcpSrv.SetDefaultDangerouslySkipPermissions(effectiveServerDSP)
				if !merged.NoHTTP {
					mcpSrv.SetBaseURL(fmt.Sprintf("http://localhost:%d", merged.Port))
				}
				go func() {
					defer close(mcpDone)
					if err := mcpSrv.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "MCP server error: %v\n", err)
					}
				}()
				fmt.Fprintf(logOut, "MCP transport enabled on stdio\n")
			}

			if err := launchStartupRuns(cmd.Context(), reg, rm, configBudget, effectiveServerDSP, launches, logOut); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "startup launches aborted: %v\n", err)
			}

			// Long-lived shutdown driver. Unlike the previous single-shot
			// select, this goroutine keeps listening for the lifetime of
			// the serve loop so SIGINT/SIGTERM after the first signal can
			// still drive the coordinator forward (quiesce → cancel). It
			// exits only once the coordinator's WaitComplete channel fires.
			//
			// Signal mapping (see docs/serve-shutdown-signals.md):
			//   - 1st SIGINT   → BeginQuiesce (indefinite quiesce).
			//   - 2nd SIGINT   → EscalateToCancel.
			//   - SIGTERM (any time) → begin-then-escalate. Coordinator's
			//     EscalateToCancel is a strict no-op until PhaseQuiesce
			//     has been emitted, so we call BeginQuiesce first and
			//     rely on its synchronous emit to satisfy that contract.
			//   - any further signal once cancel has been engaged is
			//     dropped (no log, no call).
			//   - MCP transport close: treated like a 1st SIGINT — enter
			//     quiesce. The pump keeps running so a follow-up signal
			//     can still escalate.
			//
			// Signals, MCP close, and the "already escalated" bookkeeping
			// are all serialised through this single goroutine on purpose:
			// the coordinator's strict-no-op policy in EscalateToCancel
			// requires that the caller hold the BeginQuiesce→EscalateToCancel
			// ordering, and BeginQuiesce only blocks the FIRST caller (it
			// returns immediately on subsequent calls without waiting for
			// the in-flight first call to emit PhaseQuiesce). Folding both
			// triggers into one goroutine means any local BeginQuiesce
			// always returns with PhaseQuiesce already emitted before we
			// reach EscalateToCancel.
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			completeCh := coordinator.WaitComplete()
			// mcpTrigger is the channel watched by the select. It is set
			// to mcpDone while we still want to react, and nil'd out after
			// firing so subsequent select iterations don't re-enter the
			// case (reads from a nil channel block forever). When MCP is
			// disabled mcpDone is never closed, so the case is inert.
			mcpTrigger := mcpDone
			go func() {
				escalated := false
				for {
					select {
					case sig := <-sigCh:
						switch sig {
						case syscall.SIGINT:
							switch {
							case !coordinator.InProgress():
								fmt.Fprintln(logOut, "Received SIGINT; entering quiesce — runs will park at their next state transition. Send a second SIGINT or SIGTERM to escalate to cancel.")
								coordinator.BeginQuiesce(context.Background())
							case !escalated:
								fmt.Fprintln(logOut, "Received second SIGINT; escalating to cancel.")
								coordinator.EscalateToCancel()
								escalated = true
							}
							// Else: 3rd+ SIGINT after cancel — drop.
						case syscall.SIGTERM:
							if escalated {
								// Subsequent SIGTERM after cancel — drop.
								break
							}
							fmt.Fprintln(logOut, "Received SIGTERM; cancelling in-flight runs.")
							coordinator.BeginQuiesce(context.Background())
							coordinator.EscalateToCancel()
							escalated = true
						}
					case <-mcpTrigger:
						mcpTrigger = nil
						if !coordinator.InProgress() {
							fmt.Fprintln(logOut, "MCP transport closed; entering quiesce.")
							coordinator.BeginQuiesce(context.Background())
						}
					case <-completeCh:
						return
					}
				}
			}()

			// Main goroutine blocks on the coordinator's terminal channel.
			// Per-run outcomes are broadcast via eventSink at PhaseComplete
			// — there's nothing to consume here, just wait for completion.
			<-completeCh

			return nil
		},
	}

	f := cmd.Flags()
	f.StringArrayVar(&roots, "root", nil, "scope root directory (may be specified multiple times; appended to [raymond.serve].root from config)")
	f.StringArrayVar(&launches, "launch", nil, "workflow id to launch on startup (may be repeated; the workflow must be discoverable via --root)")
	f.IntVar(&port, "port", config.DefaultServePort, "HTTP server port")
	f.BoolVar(&mcp, "mcp", false, "enable MCP transport")
	f.BoolVar(&noHTTP, "no-http", false, "disable HTTP server (requires --mcp)")
	f.StringVar(&workdir, "workdir", "", "default working directory for workflow runs")
	f.Int64Var(&maxFileSize, "max-file-size", 0, "default maximum bytes per uploaded file when an <ask> declares no per-file cap (0 means use [raymond.serve].max_file_size or daemon default)")
	f.Int64Var(&maxTotalSize, "max-total-size", 0, "default maximum total bytes per upload submission when an <ask> declares no total cap (0 means use [raymond.serve].max_total_size or daemon default)")
	f.IntVar(&maxFileCount, "max-file-count", 0, "default maximum file count per upload submission when an <ask> declares no count cap (0 means use [raymond.serve].max_file_count or daemon default)")
	f.BoolVar(&dangerouslySkipPermissions, "dangerously-skip-permissions", defaultDangerouslySkipPermissions,
		"skip Claude permission prompts for daemon-launched runs; pass --dangerously-skip-permissions=false to require permissions")
	f.BoolVar(&clean, "clean", false,
		"archive non-terminal serve-pool runs before startup: move each one to "+
			".raymond/serve-state/abandoned/<timestamp>/ for forensics so the daemon "+
			"starts with no active runs. Only the serve pool is touched; the CLI pool "+
			"at .raymond/state/ is untouched, and no state is deleted.")

	cmd.AddCommand(c.newServeListCmd())

	return cmd
}

// newServeListCmd builds the "serve list" subcommand: a read-only, daemon-free
// inspector that enumerates workflow ids in the serve pool
// (.raymond/serve-state/). It is the serve-pool peer of `ray --list`, which
// stays scoped to the CLI pool. Like `--list`, it MUST work without a running
// daemon — there is no HTTP client involved, only a top-level directory read.
func (c *CLI) newServeListCmd() *cobra.Command {
	var stateDir string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List workflow ids in the serve pool (.raymond/serve-state/)",
		Long: `List workflow ids in the serve pool (.raymond/serve-state/).

This is the serve-pool peer of "ray --list", which lists the CLI pool only.
Output is one id per line, sorted alphanumerically. Subdirectories under
serve-state/ — notably the abandoned/<ts>/ archives created by
"ray serve --clean" — are NOT descended into.

Read-only and daemon-free: this command inspects the on-disk pool directly
and does not require "ray serve" to be running.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.cmdServeList(cmd, stateDir)
		},
	}
	// Hidden test hook: lets cli_test point the serve pool at a temp dir
	// without requiring a real .raymond/serve-state structure. Mirrors the
	// root command's --state-dir flag, which targets the CLI pool.
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "")
	_ = cmd.Flags().MarkHidden("state-dir")
	return cmd
}

// cmdServeList prints the ids of every workflow state file in the serve pool.
//
// Subdirectories (including serve-state/abandoned/<ts>/) are intentionally
// skipped: state.ListWorkflowsIn reads only the top level of the pool, which
// keeps `--clean`'s archive inert by construction — see the regression test
// TestServeListSkipsAbandonedAndCLIPool in cli_test.go.
func (c *CLI) cmdServeList(cmd *cobra.Command, stateDir string) error {
	ids, err := wfstate.ListWorkflowsIn(wfstate.PoolServe, stateDir)
	if err != nil {
		return fmt.Errorf("list workflows: %w", err)
	}
	if len(ids) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No workflows found.")
		return nil
	}
	// ListWorkflowsIn returns ids in os.ReadDir's filename-sorted order —
	// alphanumeric by id, matching `--list`'s deterministic output.
	for _, id := range ids {
		fmt.Fprintln(cmd.OutOrStdout(), id)
	}
	return nil
}
