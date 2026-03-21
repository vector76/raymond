package lint

import (
	"sort"
	"strings"

	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/specifier"
	"github.com/vector76/raymond/internal/workflow"
)

// Severity represents the severity level of a diagnostic.
type Severity int

const (
	Error   Severity = iota // 0
	Warning                 // 1
	Info                    // 2
)

// Diagnostic represents a single lint finding.
type Diagnostic struct {
	Severity Severity
	File     string
	Message  string
	Check    string
}

// Options controls lint behavior.
type Options struct {
	WindowsMode bool
}

// Lint analyzes the workflow in scopeDir and returns diagnostics sorted by
// severity ascending (Error first), then filename ascending, then check name
// ascending.
func Lint(scopeDir string, opts Options) ([]Diagnostic, error) {
	// Step 1: List (and filter) state files.
	files, err := workflow.ListStateFiles(scopeDir, opts.WindowsMode)
	if err != nil {
		return nil, err
	}
	_ = files

	var diags []Diagnostic
	var entryStateName string

	// Step 3: Resolve entry point.
	entryFilename, epErr := specifier.ResolveEntryPoint(scopeDir)
	if epErr != nil {
		errStr := epErr.Error()
		if strings.HasPrefix(errStr, "ambiguous entry point:") {
			diags = append(diags, Diagnostic{
				Severity: Error,
				File:     "",
				Message:  "ambiguous entry point: both 1_START and START exist; remove one",
				Check:    "ambiguous-entry-point",
			})
		} else {
			diags = append(diags, Diagnostic{
				Severity: Error,
				File:     "",
				Message:  "no entry point found: workflow must contain 1_START or START (with .md, .sh, .bat, or .ps1 extension)",
				Check:    "no-entry-point",
			})
		}
	} else {
		entryStateName = parsing.ExtractStateName(entryFilename)
	}
	_ = entryStateName

	// Step 4: Sort and return.
	sort.Slice(diags, func(i, j int) bool {
		if diags[i].Severity != diags[j].Severity {
			return diags[i].Severity < diags[j].Severity
		}
		if diags[i].File != diags[j].File {
			return diags[i].File < diags[j].File
		}
		return diags[i].Check < diags[j].Check
	})
	return diags, nil
}
