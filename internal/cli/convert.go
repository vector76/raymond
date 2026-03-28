package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/vector76/raymond/internal/convert"
	"github.com/vector76/raymond/internal/yamlscope"
	"github.com/vector76/raymond/internal/zipscope"
)

// newConvertCmd builds the "convert" subcommand.
func (c *CLI) newConvertCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "convert <path (directory or .zip)>",
		Short: "Convert a folder or zip workflow to YAML format",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.cmdConvert(cmd, args[0])
		},
	}
	cmd.Flags().String("output", "", "Path to write output file")
	return cmd
}

// cmdConvert converts a directory or zip workflow scope to YAML.
func (c *CLI) cmdConvert(cmd *cobra.Command, arg string) error {
	outputFile, _ := cmd.Flags().GetString("output")

	absPath, err := filepath.Abs(arg)
	if err != nil {
		return fmt.Errorf("cannot resolve path %q: %w", arg, err)
	}

	if yamlscope.IsYamlScope(absPath) {
		return fmt.Errorf("already a YAML workflow")
	}

	if zipscope.IsZipScope(absPath) {
		if err := zipscope.VerifyZipHash(absPath); err != nil {
			return fmt.Errorf("zip hash validation failed: %w", err)
		}
		if _, err := zipscope.DetectLayout(absPath); err != nil {
			return fmt.Errorf("zip layout invalid: %w", err)
		}
	} else {
		info, statErr := os.Stat(absPath)
		if statErr != nil {
			return fmt.Errorf("cannot access %q: %w", arg, statErr)
		}
		if !info.IsDir() {
			return fmt.Errorf("%q is not a directory or zip archive", arg)
		}
	}

	yamlStr, warnings, err := convert.Convert(absPath)
	if err != nil {
		return err
	}

	for _, w := range warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
	}

	if outputFile != "" {
		if err := os.WriteFile(outputFile, []byte(yamlStr), 0o644); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
		return nil
	}

	fmt.Fprint(cmd.OutOrStdout(), yamlStr)
	return nil
}
