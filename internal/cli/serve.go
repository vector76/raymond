package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/vector76/raymond/internal/config"
	"github.com/vector76/raymond/internal/daemon"
)

// newServeCmd builds the "serve" subcommand that starts the Raymond daemon.
func (c *CLI) newServeCmd() *cobra.Command {
	var (
		roots   []string
		port    int
		mcp     bool
		noHTTP  bool
		workdir string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Raymond workflow daemon",
		Long: `Start the Raymond daemon which exposes discovered workflows via an HTTP API
and/or MCP tool interface.

The daemon scans the configured --root directories for workflow directories
and zip archives containing workflow.yaml manifests, then serves them to
clients.

Defaults for --root, --port, --mcp, --no-http, and --workdir may also be set
in .raymond/config.toml under the [raymond.serve] section. CLI --root values
are appended to (not replacing) the config file's root.`,
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
				Roots:   roots,
				MCP:     mcp,
				NoHTTP:  noHTTP,
				Workdir: workdir,
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

			rm, err := daemon.NewRunManager("", cwd)
			if err != nil {
				return fmt.Errorf("initializing run manager: %w", err)
			}

			// Wire up the pending-input registry so that <await> transitions
			// run in daemon mode: the orchestrator registers each pending
			// input via AwaitCallback and the HTTP layer exposes it for the
			// UI to surface and answer. Without this, awaits fall back to
			// the CLI pause path (return AwaitingInputError, wait for
			// --resume on the next process), which never triggers in a
			// long-running daemon.
			raymondDir, err := config.FindRaymondDir(cwd, true)
			if err != nil {
				return fmt.Errorf("resolving .raymond dir for pending registry: %w", err)
			}
			pr, err := daemon.NewPendingRegistry(raymondDir)
			if err != nil {
				return fmt.Errorf("initializing pending registry: %w", err)
			}
			rm.SetPendingRegistry(pr)

			// Extract the configured budget. Validated values are always
			// positive floats (see config.ValidateConfig); any other shape
			// or absence means "unset" and we leave the server default at
			// 0, which lets the resolver fall back to the hardcoded
			// constant.
			var configBudget float64
			if v, ok := raymondCfg["budget"]; ok {
				if f, ok := v.(float64); ok && f > 0 {
					configBudget = f
				}
			}

			if !merged.NoHTTP {
				srv := daemon.NewServer(reg, rm, merged.Port)
				srv.SetPendingRegistry(pr)
				srv.SetDefaultBudget(configBudget)
				fmt.Fprintf(logOut, "HTTP server listening on port %d\n", merged.Port)
				go func() {
					if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
						fmt.Fprintf(cmd.ErrOrStderr(), "HTTP server error: %v\n", err)
					}
				}()
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

			// Block until interrupted or MCP transport closes.
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			select {
			case sig := <-sigCh:
				fmt.Fprintf(logOut, "\nReceived %v, shutting down...\n", sig)
			case <-mcpDone:
				fmt.Fprintf(logOut, "\nMCP transport closed, shutting down...\n")
			}

			return nil
		},
	}

	f := cmd.Flags()
	f.StringArrayVar(&roots, "root", nil, "scope root directory (may be specified multiple times; appended to [raymond.serve].root from config)")
	f.IntVar(&port, "port", config.DefaultServePort, "HTTP server port")
	f.BoolVar(&mcp, "mcp", false, "enable MCP transport")
	f.BoolVar(&noHTTP, "no-http", false, "disable HTTP server (requires --mcp)")
	f.StringVar(&workdir, "workdir", "", "default working directory for workflow runs")

	return cmd
}
