// Package cli implements the cobra-based command-line interface shared by
// both the raymond and ray binaries.
//
// Call Run() from a main package to start the program. For tests, construct
// a CLI with NewTestCLI to inject output writers and a mock workflow runner.
package cli

import (
	"context"
	"encoding/json"
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
	"github.com/vector76/raymond/internal/diagram"
	"github.com/vector76/raymond/internal/observers/console"
	"github.com/vector76/raymond/internal/observers/debug"
	"github.com/vector76/raymond/internal/observers/titlebar"
	"github.com/vector76/raymond/internal/orchestrator"
	"github.com/vector76/raymond/internal/prompts"
	"github.com/vector76/raymond/internal/registry"
	"github.com/vector76/raymond/internal/specifier"
	wfstate "github.com/vector76/raymond/internal/state"
	"github.com/vector76/raymond/internal/version"
	"github.com/vector76/raymond/internal/yamlscope"
	"github.com/vector76/raymond/internal/zipscope"
)

const (
	// defaultBudgetUSD is the budget applied when neither the CLI nor the
	// config file specifies one. Zero means unlimited (matching the
	// 0=no-limit convention used by --timeout). The runtime budget check
	// is skipped entirely when BudgetUSD == 0.
	defaultBudgetUSD = 0.0

	// defaultTimeoutSec is the per-invocation idle timeout when unspecified.
	defaultTimeoutSec = 600.0

	// defaultDangerouslySkipPermissions is the value applied when neither the
	// CLI nor the config file specifies one. When true, raymond passes
	// --dangerously-skip-permissions to Claude; when false, raymond uses
	// --permission-mode=acceptEdits instead.
	defaultDangerouslySkipPermissions = true
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

// NewTestCLIWithRunner creates a CLI with a caller-supplied runner function.
// Exported for use in cli_test tests that need to return specific errors.
func NewTestCLIWithRunner(stdout, stderr io.Writer, runner func(ctx context.Context, workflowID string, opts orchestrator.RunOptions) error) *CLI {
	return &CLI{
		runner: runner,
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
	err := cmd.Execute()
	if err == nil {
		return
	}
	var askErr *orchestrator.PendingAskError
	if errors.As(err, &askErr) {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(askErr)
		os.Exit(2)
	}
	var lintErr LintFoundErrorsError
	if errors.As(err, &lintErr) {
		os.Exit(1)
	}
	fmt.Fprintln(c.stderr, "Error:", err)
	os.Exit(1)
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
		stateDir                   string // hidden; CLI-pool override, for testing
		serveStateDir              string // hidden; serve-pool override, for testing
		workflowID                 string
		continueSession            bool
		onAsk                    string
	)

	root := &cobra.Command{
		Use:           "raymond [WORKFLOW.md]",
		Short:         "raymond workflow orchestrator",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
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
				return c.cmdStatus(cmd, statusID, stateDir, serveStateDir)
			}

			// dspExplicit is true when the effective DangerouslySkipPermissions
			// value has been explicitly chosen by the user — either via CLI
			// flag or restored from saved LaunchParams. When true, the
			// config-file value should not override it.
			dspExplicit := cmd.Flags().Changed("dangerously-skip-permissions")

			// ---- For resume: apply saved launch params for flags not set on CLI ----
			// Load the saved LaunchParams before config merging so that any
			// restored values are treated identically to CLI-specified values.
			if resume != "" {
				if ws, err := wfstate.ReadStateIn(resume, wfstate.PoolCLI, stateDir); err == nil && ws.LaunchParams != nil {
					lp := ws.LaunchParams
					if !cmd.Flags().Changed("dangerously-skip-permissions") {
						// Restore unconditionally — we want both true and
						// false to round-trip across resume.
						dangerouslySkipPermissions = lp.DangerouslySkipPermissions
						dspExplicit = true
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
					if !cmd.Flags().Changed("on-ask") && lp.OnAsk != "" {
						onAsk = lp.OnAsk
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
				Model:   model,
				Effort:  effort,
				Name:    name,
				NoDebug: noDebug,
				NoWait:  noWait,
				Verbose: verbose,
			}
			if dspExplicit {
				cliArgs.DangerouslySkipPermissions = &dangerouslySkipPermissions
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
			if onAsk != "reject" && onAsk != "pause" {
				return fmt.Errorf("invalid --on-ask value %q: must be one of 'reject', 'pause'", onAsk)
			}
			// Reject negative budget/timeout from the CLI: config.ValidateConfig
			// already rejects negatives in TOML, and the executor's budget check
			// is gated on BudgetUSD > 0, so a negative slipping through would
			// silently disable the cap rather than enforcing it.
			if merged.Budget != nil && *merged.Budget < 0 {
				return fmt.Errorf("invalid --budget value %v: must be non-negative (0 = unlimited)", *merged.Budget)
			}
			if merged.Timeout != nil && *merged.Timeout < 0 {
				return fmt.Errorf("invalid --timeout value %v: must be non-negative (0 = no timeout)", *merged.Timeout)
			}

			effectiveBudget := defaultBudgetUSD
			if merged.Budget != nil {
				effectiveBudget = *merged.Budget
			}
			effectiveTimeout := defaultTimeoutSec
			if merged.Timeout != nil {
				effectiveTimeout = *merged.Timeout
			}
			effectiveSkipPerms := defaultDangerouslySkipPermissions
			if merged.DangerouslySkipPermissions != nil {
				effectiveSkipPerms = *merged.DangerouslySkipPermissions
			}

			opts := orchestrator.RunOptions{
				StateDir:                   stateDir,
				DefaultModel:               merged.Model,
				DefaultEffort:              merged.Effort,
				Timeout:                    effectiveTimeout,
				DangerouslySkipPermissions: effectiveSkipPerms,
				Quiet:                      quiet,
				Debug:                      !merged.NoDebug,
				NoWait:                     merged.NoWait,
				TaskFolderPattern:          merged.TaskFolderPattern,
				OnAsk:                    onAsk,
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
				if cmd.Flags().Changed("input") {
					opts.AskInput = input
				}
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
				DangerouslySkipPermissions: effectiveSkipPerms,
				Model:                      merged.Model,
				Effort:                     merged.Effort,
				Timeout:                    effectiveTimeout,
				ContinueAndFork:            continueSession,
				OnAsk:                    onAsk,
			}
			return c.cmdStart(args[0], effectiveBudget, initialInput, opts, lp, workflowID)
		},
	}

	f := root.Flags()
	f.Float64Var(&budget, "budget", defaultBudgetUSD, "cost budget limit in USD (0=unlimited)")
	f.StringVar(&model, "model", "", "Claude model override (opus|sonnet|haiku)")
	f.StringVar(&effort, "effort", "", "effort level (low|medium|high)")
	f.Float64Var(&timeout, "timeout", defaultTimeoutSec, "idle timeout per invocation in seconds (0=none)")
	f.BoolVar(&dangerouslySkipPermissions, "dangerously-skip-permissions", defaultDangerouslySkipPermissions,
		"skip Claude permission prompts; pass --dangerously-skip-permissions=false to require permissions")
	f.BoolVar(&quiet, "quiet", false, "suppress progress messages")
	f.BoolVar(&verbose, "verbose", false, "enable verbose logging")
	f.BoolVar(&noDebug, "no-debug", false, "disable debug observer")
	f.BoolVar(&noWait, "no-wait", false, "don't wait for usage limit reset; pause and exit immediately")
	f.StringVar(&input, "input", "", "initial {{input}} value passed to the first state")
	f.StringVar(&resume, "resume", "", "resume workflow by ID")
	f.BoolVar(&list, "list", false, "list all workflow state files")
	f.StringVar(&statusID, "status", "", "show status of workflow by ID")
	f.BoolVar(&recover, "recover", false, "list in-progress (non-completed) workflows")
	f.BoolVar(&initCfg, "init-config", false, "create a template .raymond/config.toml")

	f.StringVar(&name, "name", "", "prefix label for the terminal title bar")
	f.StringVar(&workflowID, "workflow-id", "", "custom workflow identifier (auto-generated if not provided)")
	f.BoolVar(&continueSession, "continue-session", false, "continue from the most recent interactive Claude session")
	f.StringVar(&onAsk, "on-ask", "reject", "behaviour when workflow uses <ask> (reject|pause)")

	// Hidden flag: allows tests to control the CLI-pool state directory
	// without requiring a real .raymond directory structure.
	f.StringVar(&stateDir, "state-dir", "", "")
	_ = f.MarkHidden("state-dir")

	// Hidden flag: lets cli_test inject a serve-pool directory so the
	// `ray status` CLI-first / serve-fallback path (cmdStatus) can be
	// exercised without colliding with the real .raymond/serve-state. No
	// production caller sets this — production resolves the serve pool via
	// state.ResolvePoolDir(PoolServe, "") from cwd, same as `ray serve`.
	f.StringVar(&serveStateDir, "serve-state-dir", "", "")
	_ = f.MarkHidden("serve-state-dir")

	root.AddCommand(c.newDiagramCmd(), c.newLintCmd(), c.newConvertCmd(), c.newServeCmd())

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

	// For YAML scopes, parse/validate and resolve the entry point or state name.
	if yamlscope.IsYamlScope(scopeDir) {
		if _, err := yamlscope.Parse(scopeDir); err != nil {
			return fmt.Errorf("YAML workflow invalid: %w", err)
		}
		if initialState == "" {
			// Bare "workflow.yaml" — resolve entry point (1_START or START).
			entry, resolveErr := specifier.ResolveEntryPoint(scopeDir)
			if resolveErr != nil {
				return fmt.Errorf("cannot resolve entry point in %q: %w", scopeDir, resolveErr)
			}
			initialState = entry
		} else {
			// "workflow.yaml/STATE" — resolve bare state name to virtual filename.
			resolved, resolveErr := prompts.ResolveState(scopeDir, initialState)
			if resolveErr != nil {
				return fmt.Errorf("cannot resolve state %q in %q: %w", initialState, scopeDir, resolveErr)
			}
			initialState = resolved
		}
	}

	// Resolve once via the CLI pool so the orchestrator can be handed a
	// concrete directory; the pool-aware primitives below get the override
	// directly so the intent ("CLI pool") is explicit at every call site.
	resolvedStateDir := wfstate.ResolvePoolDir(wfstate.PoolCLI, opts.StateDir)

	var workflowID string
	if workflowIDOverride != "" {
		if err := validateWorkflowID(workflowIDOverride); err != nil {
			return err
		}
		// Reject duplicate IDs: Python raises an explicit error rather than silently overwriting.
		if _, readErr := wfstate.ReadStateIn(workflowIDOverride, wfstate.PoolCLI, opts.StateDir); readErr == nil {
			return fmt.Errorf("workflow %q already exists; use --resume to continue it", workflowIDOverride)
		}
		workflowID = workflowIDOverride
	} else {
		var genErr error
		workflowID, genErr = wfstate.GenerateWorkflowIDIn(wfstate.PoolCLI, opts.StateDir)
		if genErr != nil {
			return fmt.Errorf("generate workflow ID: %w", genErr)
		}
	}

	ws := wfstate.CreateInitialState(workflowID, scopeDir, initialState, budgetUSD, initialInput, scopeURL, lp)
	if err := wfstate.WriteStateIn(workflowID, ws, wfstate.PoolCLI, opts.StateDir); err != nil {
		return fmt.Errorf("write initial state: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	opts.StateDir = resolvedStateDir
	return c.runner(ctx, workflowID, opts)
}

// cmdResume resumes an existing workflow by ID.
func (c *CLI) cmdResume(workflowID string, opts orchestrator.RunOptions) error {
	resolvedStateDir := wfstate.ResolvePoolDir(wfstate.PoolCLI, opts.StateDir)

	ws, err := wfstate.ReadStateIn(workflowID, wfstate.PoolCLI, opts.StateDir)
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

	// For YAML scopes, re-validate the file on resume.
	if yamlscope.IsYamlScope(ws.ScopeDir) {
		if _, statErr := os.Stat(ws.ScopeDir); statErr != nil && errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("yaml workflow not found for workflow %q: %s", workflowID, ws.ScopeDir)
		}
		if _, err := yamlscope.Parse(ws.ScopeDir); err != nil {
			return fmt.Errorf("YAML workflow invalid for workflow %q: %w", workflowID, err)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	opts.StateDir = resolvedStateDir
	return c.runner(ctx, workflowID, opts)
}

// cmdList prints the IDs of all workflow state files.
func (c *CLI) cmdList(cmd *cobra.Command, stateDir string) error {
	// --list/--recover enumerate the CLI pool only; serve-pool listing is
	// a separate command (`ray serve list`). Do not widen this to a
	// merged-pool listing — operators rely on the pool boundary being
	// visible at the command surface.
	ids, err := wfstate.ListWorkflowsIn(wfstate.PoolCLI, stateDir)
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
	// --list/--recover enumerate the CLI pool only; serve-pool listing is
	// a separate command (`ray serve list`). The serve pool's recovery
	// equivalent is the daemon's auto-resume on startup, not this CLI.
	ids, err := wfstate.RecoverWorkflowsIn(wfstate.PoolCLI, stateDir)
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
//
// `ray status` is pool-agnostic: the CLI pool is consulted first, and on a
// miss we fall through to the serve pool. When the same id is present in
// both pools (rare but possible — the two pools manage independent id
// namespaces) the CLI pool's copy wins by virtue of being read first; the
// serve copy is never opened in that case. On a miss in BOTH pools we
// return the generic not-found error verbatim so that an operator probing
// for an id can never learn pool layout from the error message.
func (c *CLI) cmdStatus(cmd *cobra.Command, workflowID, stateDir, serveStateDir string) error {
	ws, err := wfstate.ReadStateIn(workflowID, wfstate.PoolCLI, stateDir)
	if errors.Is(err, os.ErrNotExist) {
		// Strict CLI-first / serve-fallback: ONLY a not-found from the
		// CLI pool triggers the fallback. A malformed CLI state file is
		// a corruption signal, not a miss, and must not be silently
		// papered over by a serve-pool copy — the documented precedence
		// rule (CLI wins when both pools hold the id) is only coherent
		// if the CLI pool is treated as authoritative whenever its file
		// is present. The fall-through still preserves the generic
		// not-found error on miss in both pools so an operator probing
		// for an id can never learn pool layout from the response.
		serveWS, serveErr := wfstate.ReadStateIn(workflowID, wfstate.PoolServe, serveStateDir)
		if serveErr != nil {
			return fmt.Errorf("workflow %q not found", workflowID)
		}
		ws = serveWS
	} else if err != nil {
		// Malformed/unreadable CLI state file: surface the same generic
		// not-found that the pre-Phase-7 cmdStatus returned for any
		// read error. No regression in error text, and no leak of the
		// underlying corruption to the operator beyond what was already
		// observable.
		return fmt.Errorf("workflow %q not found", workflowID)
	}

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Workflow: %s\n", ws.WorkflowID)
	fmt.Fprintf(w, "Scope:    %s\n", ws.ScopeDir)
	if ws.BudgetUSD == 0 {
		fmt.Fprintf(w, "Budget:   unlimited (used: $%.4f)\n", ws.TotalCostUSD)
	} else {
		fmt.Fprintf(w, "Budget:   $%.2f (used: $%.4f)\n", ws.BudgetUSD, ws.TotalCostUSD)
	}

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
//   - .yaml/.yml file      → scope=arg, state="" (resolved later in cmdStart)
//   - .yaml/.yml/STATE     → scope=yamlPath, state=stateName
//   - Directory             → scope=arg, state=resolved entry point (1_START or START)
//   - .zip file             → scope=arg, state="" (resolved later after hash/layout validation)
//   - Other file            → scope=dirname(arg), state=basename(arg)
//
// YAML is checked before os.Stat because "workflow.yaml/STATE" is not a real
// filesystem path. The returned scopeDir is always an absolute path.
func parseScopeAndState(arg string) (scopeDir, initialState string, err error) {
	absArg, absErr := filepath.Abs(arg)
	if absErr != nil {
		return "", "", fmt.Errorf("cannot resolve absolute path for %q: %w", arg, absErr)
	}

	// YAML scopes must be checked before os.Stat because the
	// "workflow.yaml/STATE" syntax is not a real filesystem path.
	if yamlscope.IsYamlScope(absArg) {
		// Bare YAML file path (e.g. "workflow.yaml").
		if _, statErr := os.Stat(arg); statErr != nil {
			return "", "", fmt.Errorf("cannot access %q: %w", arg, statErr)
		}
		// Entry point resolution deferred to cmdStart (same pattern as zip).
		return absArg, "", nil
	}

	// Check if the directory component is a YAML file — handles
	// "workflow.yaml/STATE" syntax (parallel to "archive.zip/STATE").
	dirPart := filepath.Dir(absArg)
	if yamlscope.IsYamlScope(dirPart) {
		if _, statErr := os.Stat(dirPart); statErr != nil {
			return "", "", fmt.Errorf("cannot access %q: %w", filepath.Dir(arg), statErr)
		}
		return dirPart, filepath.Base(absArg), nil
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

// newDiagramCmd builds the "diagram" subcommand.
func (c *CLI) newDiagramCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diagram <path (directory, .zip, or .yaml/.yml)>",
		Short: "Generate a Mermaid flowchart of workflow transitions",
		Long: `Scan a workflow directory, zip archive, or YAML file and generate a Mermaid flowchart.

Flags:
  --win     Include Windows scripts (.bat, .ps1) and exclude Unix scripts (.sh).
            By default, Unix scripts (.sh) are included and Windows scripts excluded.
  --html    Write an interactive HTML file instead of printing Mermaid text to stdout.
  --output  Output filename for --html mode (default: diagram.html).
            Requires --html.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.cmdDiagram(cmd, args[0])
		},
	}
	cmd.Flags().Bool("win", false, "Include Windows scripts (.bat, .ps1) and exclude Unix scripts (.sh)")
	cmd.Flags().Bool("html", false, "Write an interactive HTML file instead of printing Mermaid text")
	cmd.Flags().String("output", "diagram.html", "Output filename for --html mode")
	return cmd
}

// cmdDiagram generates a Mermaid diagram from a workflow scope.
func (c *CLI) cmdDiagram(cmd *cobra.Command, arg string) error {
	winMode, _ := cmd.Flags().GetBool("win")
	htmlMode, _ := cmd.Flags().GetBool("html")
	outputFile, _ := cmd.Flags().GetString("output")

	if !htmlMode && cmd.Flags().Changed("output") {
		return fmt.Errorf("--output requires --html")
	}

	absArg, err := filepath.Abs(arg)
	if err != nil {
		return fmt.Errorf("cannot resolve path %q: %w", arg, err)
	}

	if yamlscope.IsYamlScope(absArg) {
		if _, err := yamlscope.Parse(absArg); err != nil {
			return fmt.Errorf("YAML workflow invalid: %w", err)
		}
	} else if zipscope.IsZipScope(absArg) {
		if err := zipscope.VerifyZipHash(absArg); err != nil {
			return fmt.Errorf("zip hash validation failed: %w", err)
		}
		if _, err := zipscope.DetectLayout(absArg); err != nil {
			return fmt.Errorf("zip layout invalid: %w", err)
		}
	} else {
		info, statErr := os.Stat(absArg)
		if statErr != nil {
			return fmt.Errorf("cannot access %q: %w", arg, statErr)
		}
		if !info.IsDir() {
			return fmt.Errorf("%q is not a directory, zip archive, or YAML workflow", arg)
		}
	}

	result, err := diagram.GenerateDiagram(absArg, diagram.Options{WindowsMode: winMode})
	if err != nil {
		return err
	}

	for _, w := range result.Warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
	}

	if htmlMode {
		html := diagram.GenerateHTML(result)
		if err := os.WriteFile(outputFile, []byte(html), 0o644); err != nil {
			return fmt.Errorf("writing HTML output: %w", err)
		}
		return nil
	}

	fmt.Fprint(cmd.OutOrStdout(), result.Mermaid)
	return nil
}
