package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/vector76/raymond/internal/lint"
	"github.com/vector76/raymond/internal/zipscope"
)

// LintFoundErrorsError is returned by cmdLint when the workflow has lint
// errors. It is a sentinel: the CLI exits with code 1 but prints no error
// message (the diagnostics themselves are already printed).
// Exported so external test packages can use errors.As against it.
type LintFoundErrorsError struct{}

func (e LintFoundErrorsError) Error() string { return "" }

// newLintCmd builds the "lint" subcommand.
func (c *CLI) newLintCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lint <path>",
		Short: "Validate workflow definitions and report issues",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.cmdLint(cmd, args[0])
		},
	}
	cmd.Flags().Bool("win", false, "Apply Windows platform filtering")
	cmd.Flags().Bool("json", false, "Output diagnostics as JSON")
	cmd.Flags().String("level", "warning", "Minimum severity to display: info, warning, error")
	return cmd
}

// cmdLint validates a workflow directory or zip archive and reports issues.
func (c *CLI) cmdLint(cmd *cobra.Command, arg string) error {
	win, _ := cmd.Flags().GetBool("win")
	jsonOut, _ := cmd.Flags().GetBool("json")
	levelStr, _ := cmd.Flags().GetString("level")

	level, err := parseLintLevel(levelStr)
	if err != nil {
		return fmt.Errorf("invalid --level value %q: must be info, warning, or error", levelStr)
	}

	absPath, err := filepath.Abs(arg)
	if err != nil {
		return fmt.Errorf("cannot resolve path %q: %w", arg, err)
	}

	// Validate: must be a directory or .zip file.
	info, statErr := os.Stat(absPath)
	if statErr != nil {
		return fmt.Errorf("cannot access %q: %w", arg, statErr)
	}
	if !info.IsDir() {
		if zipscope.IsZipScope(absPath) {
			if err := zipscope.VerifyZipHash(absPath); err != nil {
				return fmt.Errorf("zip hash validation failed: %w", err)
			}
			if _, err := zipscope.DetectLayout(absPath); err != nil {
				return fmt.Errorf("zip layout invalid: %w", err)
			}
		} else {
			return fmt.Errorf("%q is not a directory or zip archive", arg)
		}
	}

	diags, err := lint.Lint(absPath, lint.Options{WindowsMode: win})
	if err != nil {
		return err
	}

	// Determine if any error-severity diagnostics exist.
	hasErrors := false
	for _, d := range diags {
		if d.Severity == lint.Error {
			hasErrors = true
			break
		}
	}

	// Format and write output.
	if jsonOut {
		out, err := formatLintJSON(diags, level)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), out)
	} else {
		fmt.Fprint(cmd.OutOrStdout(), formatLintText(diags, level))
	}

	if hasErrors {
		return LintFoundErrorsError{}
	}
	return nil
}
