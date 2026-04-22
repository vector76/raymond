package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/vector76/raymond/internal/daemon"
)

// newServeCmd builds the "serve" subcommand that starts the Raymond daemon.
func (c *CLI) newServeCmd() *cobra.Command {
	var (
		roots  []string
		port   int
		mcp    bool
		noHTTP bool
		workdir string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Raymond workflow daemon",
		Long: `Start the Raymond daemon which exposes discovered workflows via an HTTP API
and/or MCP tool interface.

The daemon scans the configured --root directories for workflow directories
and zip archives containing workflow.yaml manifests, then serves them to
clients.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(roots) == 0 {
				return fmt.Errorf("at least one --root directory is required")
			}

			if noHTTP && !mcp {
				return fmt.Errorf("--no-http requires --mcp (at least one transport must be enabled)")
			}

			reg, err := daemon.NewRegistry(roots)
			if err != nil {
				return fmt.Errorf("initializing workflow registry: %w", err)
			}

			workflows := reg.ListWorkflows()
			fmt.Fprintf(cmd.OutOrStdout(), "Discovered %d workflow(s)\n", len(workflows))
			for _, wf := range workflows {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s", wf.ID)
				if wf.Name != "" {
					fmt.Fprintf(cmd.OutOrStdout(), " — %s", wf.Name)
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}

			if workdir != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Working directory: %s\n", workdir)
			}

			// Placeholder: actual HTTP and MCP server startup is implemented
			// in later beads (16-17). For now, report what would be started.
			if !noHTTP {
				fmt.Fprintf(cmd.OutOrStdout(), "HTTP server would listen on port %d\n", port)
			}
			if mcp {
				fmt.Fprintln(cmd.OutOrStdout(), "MCP transport would be enabled")
			}

			return nil
		},
	}

	f := cmd.Flags()
	f.StringArrayVar(&roots, "root", nil, "scope root directory (may be specified multiple times)")
	f.IntVar(&port, "port", 8080, "HTTP server port")
	f.BoolVar(&mcp, "mcp", false, "enable MCP transport")
	f.BoolVar(&noHTTP, "no-http", false, "disable HTTP server (requires --mcp)")
	f.StringVar(&workdir, "workdir", "", "default working directory for workflow runs")

	return cmd
}
