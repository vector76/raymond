// Package cli implements the cobra-based command-line interface shared by
// both the raymond and ray binaries.
//
// Call Run() from a main package to start the program. For tests, construct
// a CLI with NewTestCLI to inject output writers and a mock workflow runner.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/vector76/raymond/internal/bus"
	"github.com/vector76/raymond/internal/config"
	"github.com/vector76/raymond/internal/observers/console"
	"github.com/vector76/raymond/internal/observers/debug"
	"github.com/vector76/raymond/internal/observers/titlebar"
	"github.com/vector76/raymond/internal/orchestrator"
	"github.com/vector76/raymond/internal/registry"
	"github.com/vector76/raymond/internal/specifier"
	wfstate "github.com/vector76/raymond/internal/state"
	"github.com/vector76/raymond/internal/version"
	"github.com/vector76/raymond/internal/zipscope"
)

const (
	defaultBudgetUSD  = 10.0
	defaultTimeoutSec = 600.0
)

// workflowIDPattern matches valid workflow IDs: alphanumeric, hyphens, underscores only.
var workflowIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// validateWorkflowID returns an error if the given workflow ID is invalid.
func validateWorkflowID(id string) error {
	if id == "" {
		return fmt.Errorf("workflow ID cannot be empty")
	}
	if len(id) > 255 {
		return fmt.Errorf("workflow ID too long (max 255 characters)")
	}
	if !workflowIDPattern.MatchString(id) {
		return fmt.Errorf("workflow ID %q contains invalid characters: only letters, numbers, hyphens, and underscores are allowed", id)
	}
	return nil
}

// CLI holds injectable dependencies. Use newCLI() for production binaries and
// NewTestCLI() in tests.
type CLI struct {
	// runner is the function used to execute workflows.
	// Tests inject a no-op to avoid spawning real Claude processes.
	runner func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error

	stdout io.Writer
	stderr io.Writer
}

func newCLI() *CLI {
	return &CLI{
		runner: orchestrator.RunAllAgents,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

// NewTestCLI creates a CLI with injected output writers and a no-op runner.
// Exported so that the cli_test package can construct one.
func NewTestCLI(stdout, stderr io.Writer) *CLI {
	return &CLI{
		runner: func(_ context.Context, _ string, _ orchestrator.RunOptions) error {
			return nil
		},
		stdout: stdout,
		stderr: stderr,
	}
}

// NewTestCLICapturing creates a CLI that records the RunOptions passed to the
// runner on each invocation. The captured values are appended to *captured.
// Exported for use in cli_test tests that need to inspect resolved opts.
func NewTestCLICapturing(stdout, stderr io.Writer, captured *[]orchestrator.RunOptions) *CLI {
	return &CLI{
		runner: func(_ context.Context, _ string, opts orchestrator.RunOptions) error {
			*captured = append(*captured, opts)
			return nil
		},
		stdout: stdout,
		stderr: stderr,
	}
}

// Run is the main entry point for both the raymond and ray binaries.
func Run() {
	c := newCLI()
	cmd := c.NewRootCmd()
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// NewRootCmd builds and returns the cobra root command with all flags wired.
// Exported so tests can call cmd.SetArgs() and cmd.Execute() directly.
func (c *CLI) NewRootCmd() *cobra.Command {
	var (
		budget                     float64
		model                      string
		effort                     string
		name                       string
		timeout                    float64
		dangerouslySkipPermissions bool
		quiet                      bool
		verbose                    bool
		noDebug                    bool
		noWait                     bool
		input                      string
		resume                     string
		list                       bool
		statusID                   string
		recover                    bool
		initCfg                    bool
		stateDir                   string // hidden; for testing
		workflowID                 string
		continueSession            bool
	)

	root := &cobra.Command{
		Use:           "raymond [WORKFLOW.md]",
		Short:         "raymond workflow orchestrator",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			// ---- non-workflow commands ----
			if initCfg {
				return c.cmdInitConfig(cmd)
			}
			if list {
				return c.cmdList(cmd, stateDir)
			}
			if recover {
				return c.cmdRecover(cmd, stateDir)
			}
			if statusID != "" {
				return c.cmdStatus(cmd, statusID, stateDir)
			}

			// ---- For resume: apply saved launch params for flags not set on CLI ----
			// Load the saved LaunchParams before config merging so that any
			// restored values are treated identically to CLI-specified values.
			if resume != "" {
				resolvedDir := wfstate.GetStateDir(stateDir)
				if ws, err := wfstate.ReadState(resume, resolvedDir); err == nil && ws.LaunchParams != nil {
					lp := ws.LaunchParams
					if !cmd.Flags().Changed("dangerously-skip-permissions") && lp.DangerouslySkipPermissions {
						dangerouslySkipPermissions = lp.DangerouslySkipPermissions
					}
					if !cmd.Flags().Changed("model") && lp.Model != "" {
						model = lp.Model
					}
					if !cmd.Flags().Changed("effort") && lp.Effort != "" {
						effort = lp.Effort
					}
					if !cmd.Flags().Changed("timeout") && lp.Timeout > 0 {
						timeout = lp.Timeout
					}
					if !cmd.Flags().Changed("continue-session") && lp.ContinueAndFork {
						continueSession = lp.ContinueAndFork
					}
				}
			}

			// ---- load and merge config file ----
			fileConfig, err := config.LoadConfig("")
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
				fileConfig = map[string]any{}
			}

			cliArgs := config.CLIArgs{
				DangerouslySkipPermissions: dangerouslySkipPermissions,
				Model:                      model,
				Effort:                     effort,
				Name:                       name,
				NoDebug:                    noDebug,
				NoWait:                     noWait,
				Verbose:                    verbose,
			}
			if cmd.Flags().Changed("budget") {
				cliArgs.Budget = &budget
			}
			if cmd.Flags().Changed("timeout") {
				cliArgs.Timeout = &timeout
			}

			merged := config.MergeConfig(fileConfig, cliArgs)

			if merged.Model != "" && merged.Model != "opus" && merged.Model != "sonnet" && merged.Model != "haiku" {
				return fmt.Errorf("invalid --model value %q: must be one of 'opus', 'sonnet', 'haiku'", merged.Model)
			}
			if merged.Effort != "" && merged.Effort != "low" && merged.Effort != "medium" && merged.Effort != "high" {
				return fmt.Errorf("invalid --effort value %q: must be one of 'low', 'medium', 'high'", merged.Effort)
			}

			effectiveBudget := defaultBudgetUSD
			if merged.Budget != nil {
				effectiveBudget = *merged.Budget
			}
			effectiveTimeout := defaultTimeoutSec
			if merged.Timeout != nil {
				effectiveTimeout = *merged.Timeout
			}

			opts := orchestrator.RunOptions{
				StateDir:                   stateDir,
				DefaultModel:               merged.Model,
				DefaultEffort:              merged.Effort,
				Timeout:                    effectiveTimeout,
				DangerouslySkipPermissions: merged.DangerouslySkipPermissions,
				Quiet:                      quiet,
				Debug:                      !merged.NoDebug,
				NoWait:                     merged.NoWait,
			}
			opts.ObserverSetup = func(b *bus.Bus) {
				console.New(b, quiet, 0)
				if !merged.NoDebug {
					debug.New(b)
				}
				titlebar.NewWithWriter(b, c.stdout, merged.Name)
			}

			// ---- resume mode ----
			if resume != "" {
				return c.cmdResume(resume, opts)
			}

			// ---- start mode ----
			if len(args) == 0 {
				return fmt.Errorf("no workflow specified; provide a file, directory, or use --resume ID")
			}

			var initialInput *string
			if cmd.Flags().Changed("input") {
				s := input
				initialInput = &s
			}

			// Build launch params to persist so they can be restored on --resume.
			lp := &wfstate.LaunchParams{
				DangerouslySkipPermissions: merged.DangerouslySkipPermissions,
				Model:                      merged.Model,
				Effort:                     merged.Effort,
				Timeout:                    effectiveTimeout,
				ContinueAndFork:            continueSession,
			}
			return c.cmdStart(args[0], effectiveBudget, initialInput, opts, lp, workflowID)
		},
	}

	f := root.Flags()
	f.Float64Var(&budget, "budget", defaultBudgetUSD, "cost budget limit in USD")
	f.StringVar(&model, "model", "", "Claude model override (opus|sonnet|haiku)")
	f.StringVar(&effort, "effort", "", "effort level (low|medium|high)")
	f.Float64Var(&timeout, "timeout", defaultTimeoutSec, "idle timeout per invocation in seconds (0=none)")
	f.BoolVar(&dangerouslySkipPermissions, "dangerously-skip-permissions", false, "skip Claude permission prompts")
	f.BoolVar(&quiet, "quiet", false, "suppress progress messages")
	f.BoolVar(&verbose, "verbose", false, "enable verbose logging")
	f.BoolVar(&noDebug, "no-debug", false, "disable debug observer")
	f.BoolVar(&noWait, "no-wait", false, "don't wait for usage limit reset; pause and exit immediately")
	f.StringVar(&input, "input", "", "initial {{result}} value passed to the first state")
	f.StringVar(&resume, "resume", "", "resume workflow by ID")
	f.BoolVar(&list, "list", false, "list all workflow state files")
	f.StringVar(&statusID, "status", "", "show status of workflow by ID")
	f.BoolVar(&recover, "recover", false, "list in-progress (non-completed) workflows")
	f.BoolVar(&initCfg, "init-config", false, "create a template .raymond/config.toml")

	f.StringVar(&name, "name", "", "prefix label for the terminal title bar")
	f.StringVar(&workflowID, "workflow-id", "", "custom workflow identifier (auto-generated if not provided)")
	f.BoolVar(&continueSession, "continue-session", false, "continue from the most recent interactive Claude session")

	// Hidden flag: allows tests to control the state directory without
	// requiring a real .raymond directory structure.
	f.StringVar(&stateDir, "state-dir", "", "")
	_ = f.MarkHidden("state-dir")

	root.SetOut(c.stdout)
	root.SetErr(c.stderr)
	return root
}

// --------------------------------------------------------------------------
// Command implementations
// --------------------------------------------------------------------------

// cmdStart creates initial workflow state and invokes the runner.
// workflowIDOverride is the user-specified workflow ID; when empty, one is generated.
func (c *CLI) cmdStart(arg string, budgetUSD float64, initialInput *string, opts orchestrator.RunOptions, lp *wfstate.LaunchParams, workflowIDOverride string) error {
	// ---- URL resolution block ----
	isRemoteURL := registry.IsRemoteWorkflowURL(arg)
	// Guard against unsupported URL schemes.
	if !isRemoteURL && strings.Contains(arg, "://") {
		return fmt.Errorf("unsupported URL scheme in workflow argument %q: only http:// and https:// are accepted", arg)
	}
	var scopeURL string
	if isRemoteURL {
		hash, err := registry.ValidateRemoteURL(arg)
		if err != nil {
			return fmt.Errorf("invalid remote workflow URL: %w", err)
		}

		// Resolve the .raymond directory for the registry.
		var raymondDir string
		if opts.StateDir != "" {
			// --state-dir was provided: .raymond is its parent.
			raymondDir = filepath.Dir(opts.StateDir)
		} else {
			cwd, cwdErr := os.Getwd()
			if cwdErr != nil {
				cwd = "."
			}
			found, findErr := config.FindRaymondDir(cwd, false)
			if findErr != nil || found == "" {
				raymondDir = filepath.Join(cwd, ".raymond")
			} else {
				raymondDir = found
			}
		}

		reg := registry.New(raymondDir)
		localPath, fetchErr := reg.Fetch(arg, hash)
		if fetchErr != nil {
			return fmt.Errorf("failed to fetch remote workflow: %w", fetchErr)
		}
		scopeURL = arg
		arg = localPath
	}

	scopeDir, initialState, err := parseScopeAndState(arg)
	if err != nil {
		return err
	}

	// For zip scopes, validate the hash and layout, then resolve entry point.
	if zipscope.IsZipScope(scopeDir) {
		if err := zipscope.VerifyZipHash(scopeDir); err != nil {
			return fmt.Errorf("zip archive hash validation failed: %w", err)
		}
		if _, err := zipscope.DetectLayout(scopeDir); err != nil {
			return fmt.Errorf("zip archive layout invalid: %w", err)
		}
		entry, resolveErr := specifier.ResolveEntryPoint(scopeDir)
		if resolveErr != nil {
			return fmt.Errorf("cannot resolve entry point in %q: %w", scopeDir, resolveErr)
		}
		initialState = entry
	}

	resolvedStateDir := wfstate.GetStateDir(opts.StateDir)

	var workflowID string
	if workflowIDOverride != "" {
		if err := validateWorkflowID(workflowIDOverride); err != nil {
			return err
		}
		// Reject duplicate IDs: Python raises an explicit error rather than silently overwriting.
		if _, readErr := wfstate.ReadState(workflowIDOverride, resolvedStateDir); readErr == nil {
			return fmt.Errorf("workflow %q already exists; use --resume to continue it", workflowIDOverride)
		}
		workflowID = workflowIDOverride
	} else {
		var genErr error
		workflowID, genErr = wfstate.GenerateWorkflowID(resolvedStateDir)
		if genErr != nil {
			return fmt.Errorf("generate workflow ID: %w", genErr)
		}
	}

	ws := wfstate.CreateInitialState(workflowID, scopeDir, initialState, budgetUSD, initialInput, scopeURL, lp)
	if err := wfstate.WriteState(workflowID, ws, resolvedStateDir); err != nil {
		return fmt.Errorf("write initial state: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	opts.StateDir = resolvedStateDir
	return c.runner(ctx, workflowID, opts)
}

// cmdResume resumes an existing workflow by ID.
func (c *CLI) cmdResume(workflowID string, opts orchestrator.RunOptions) error {
	resolvedStateDir := wfstate.GetStateDir(opts.StateDir)

	ws, err := wfstate.ReadState(workflowID, resolvedStateDir)
	if err != nil {
		return fmt.Errorf("workflow %q not found", workflowID)
	}

	// For zip scopes, re-validate hash and layout on resume.
	if zipscope.IsZipScope(ws.ScopeDir) {
		if err := zipscope.VerifyZipHash(ws.ScopeDir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("zip archive not found for workflow %q: %s", workflowID, ws.ScopeDir)
			}
			return fmt.Errorf("zip archive hash validation failed for workflow %q: %w", workflowID, err)
		}
		if _, err := zipscope.DetectLayout(ws.ScopeDir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("zip archive not found for workflow %q: %s", workflowID, ws.ScopeDir)
			}
			return fmt.Errorf("zip archive layout invalid for workflow %q: %w", workflowID, err)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	opts.StateDir = resolvedStateDir
	return c.runner(ctx, workflowID, opts)
}

// cmdList prints the IDs of all workflow state files.
func (c *CLI) cmdList(cmd *cobra.Command, stateDir string) error {
	ids, err := wfstate.ListWorkflows(stateDir)
	if err != nil {
		return fmt.Errorf("list workflows: %w", err)
	}
	if len(ids) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No workflows found.")
		return nil
	}
	for _, id := range ids {
		fmt.Fprintln(cmd.OutOrStdout(), id)
	}
	return nil
}

// cmdRecover prints the IDs of workflows that have at least one active agent.
func (c *CLI) cmdRecover(cmd *cobra.Command, stateDir string) error {
	ids, err := wfstate.RecoverWorkflows(stateDir)
	if err != nil {
		return fmt.Errorf("recover workflows: %w", err)
	}
	if len(ids) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No recoverable workflows found.")
		return nil
	}
	for _, id := range ids {
		fmt.Fprintln(cmd.OutOrStdout(), id)
	}
	return nil
}

// cmdStatus prints a human-readable status summary for a single workflow.
func (c *CLI) cmdStatus(cmd *cobra.Command, workflowID, stateDir string) error {
	ws, err := wfstate.ReadState(workflowID, stateDir)
	if err != nil {
		return fmt.Errorf("workflow %q not found", workflowID)
	}

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Workflow: %s\n", ws.WorkflowID)
	fmt.Fprintf(w, "Scope:    %s\n", ws.ScopeDir)
	fmt.Fprintf(w, "Budget:   $%.2f (used: $%.4f)\n", ws.BudgetUSD, ws.TotalCostUSD)

	if len(ws.Agents) == 0 {
		fmt.Fprintln(w, "Agents:   (none)")
	} else {
		fmt.Fprintln(w, "Agents:")
		for _, a := range ws.Agents {
			agentStatus := "active"
			switch a.Status {
			case wfstate.AgentStatusPaused:
				if a.Error != "" {
					agentStatus = fmt.Sprintf("paused: %s", truncate(a.Error, 40))
				} else {
					agentStatus = "paused"
				}
			case wfstate.AgentStatusFailed:
				agentStatus = "failed"
			}
			fmt.Fprintf(w, "  %s at %s [%s]\n", a.ID, a.CurrentState, agentStatus)
		}
	}
	return nil
}

// cmdInitConfig creates a template .raymond/config.toml at the project root.
func (c *CLI) cmdInitConfig(cmd *cobra.Command) error {
	if err := config.InitConfig(""); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	projectRoot := config.FindProjectRoot(cwd)
	configPath := filepath.Join(projectRoot, ".raymond", "config.toml")
	fmt.Fprintf(cmd.OutOrStdout(), "Created %s\n", configPath)
	return nil
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// parseScopeAndState resolves a CLI argument to (scopeDir, initialState).
//
//   - Directory  → scope=arg, state=resolved entry point (1_START or START)
//   - .zip file  → scope=arg, state="" (resolved later after hash/layout validation)
//   - Other file → scope=dirname(arg), state=basename(arg)
//
// The returned scopeDir is always an absolute path.
func parseScopeAndState(arg string) (scopeDir, initialState string, err error) {
	absArg, absErr := filepath.Abs(arg)
	if absErr != nil {
		return "", "", fmt.Errorf("cannot resolve absolute path for %q: %w", arg, absErr)
	}

	info, statErr := os.Stat(arg)
	if statErr != nil {
		return "", "", fmt.Errorf("cannot access %q: %w", arg, statErr)
	}

	if info.IsDir() {
		entry, resolveErr := specifier.ResolveEntryPoint(absArg)
		if resolveErr != nil {
			return "", "", fmt.Errorf("cannot resolve entry point in %q: %w", absArg, resolveErr)
		}
		return absArg, entry, nil
	}

	if strings.ToLower(filepath.Ext(arg)) == ".zip" {
		// Entry point resolution for zips is deferred to cmdStart, after
		// hash and layout validation.
		return absArg, "", nil
	}

	// Regular state file.
	return filepath.Dir(absArg), filepath.Base(absArg), nil
}

// truncate returns s capped at maxLen characters with "..." appended if cut.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
