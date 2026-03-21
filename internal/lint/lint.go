package lint

import (
	"fmt"
	"path/filepath"
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

// targetExists reports whether the given transition target can be resolved in
// the knownFiles set. If the target has a recognized extension, it must match
// exactly. If extensionless, we try .md first, then the platform script
// extension(s). Returns true if at least one candidate exists.
func targetExists(target string, knownFiles map[string]bool, winMode bool) bool {
	ext := strings.ToLower(filepath.Ext(target))
	if workflow.StateExtensions[ext] {
		return knownFiles[target]
	}
	// Extensionless: try .md then platform script ext(s).
	if knownFiles[target+".md"] {
		return true
	}
	if winMode {
		return knownFiles[target+".bat"] || knownFiles[target+".ps1"]
	}
	return knownFiles[target+".sh"]
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

	// Build knownFiles (exact filename lookup) and knownStates (bare stem lookup).
	knownFiles := make(map[string]bool, len(files))
	knownStates := make(map[string]bool, len(files))
	for _, filename := range files {
		knownFiles[filename] = true
		knownStates[parsing.ExtractStateName(filename)] = true
	}
	_ = knownStates

	var diags []Diagnostic
	var entryStateName string

	// Step 2: Resolve entry point.
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

	// Step 3: Parse each file.
	type parsedFile struct {
		transitions []parsing.Transition
		fmErr       error
	}
	parsed := make(map[string]parsedFile, len(files))
	for _, filename := range files {
		transitions, _, fmErr, _, readErr := workflow.ExtractFileData(scopeDir, filename)
		if readErr != nil {
			return nil, readErr
		}
		parsed[filename] = parsedFile{transitions: transitions, fmErr: fmErr}
	}

	// Step 4: Per-file transition checks.

	// Tags that have a primary target we validate.
	targetTags := map[string]bool{
		"goto": true, "reset": true, "call": true, "function": true, "fork": true,
	}
	// Tags that require a "return" attribute.
	returnTags := map[string]bool{"call": true, "function": true}

	// Deduplicate ambiguous stems across all files.
	seenAmbiguousStems := make(map[string]bool)

	for _, filename := range files {
		pf := parsed[filename]

		// missing-target check
		for _, t := range pf.transitions {
			if !targetTags[t.Tag] || parsing.IsWorkflowTag(t.Tag) {
				continue
			}
			if !targetExists(t.Target, knownFiles, opts.WindowsMode) {
				diags = append(diags, Diagnostic{
					Severity: Error,
					File:     filename,
					Message:  fmt.Sprintf("<%s> in %s references %q which does not exist in this workflow", t.Tag, filename, t.Target),
					Check:    "missing-target",
				})
			}
		}

		// missing-return check
		for _, t := range pf.transitions {
			if !returnTags[t.Tag] || parsing.IsWorkflowTag(t.Tag) {
				continue
			}
			ret := t.Attributes["return"]
			if ret == "" {
				diags = append(diags, Diagnostic{
					Severity: Error,
					File:     filename,
					Message:  fmt.Sprintf("<%s> in %s is missing required \"return\" attribute", t.Tag, filename),
					Check:    "missing-return",
				})
			} else if !targetExists(ret, knownFiles, opts.WindowsMode) {
				diags = append(diags, Diagnostic{
					Severity: Error,
					File:     filename,
					Message:  fmt.Sprintf("<%s> in %s has return=%q which does not exist in this workflow", t.Tag, filename, ret),
					Check:    "missing-return",
				})
			}
		}

		// missing-fork-next check: if file has ≥1 fork and no fork has "next"
		// and there is no goto, emit one diagnostic.
		hasFork := false
		anyForkHasNext := false
		hasGoto := false
		for _, t := range pf.transitions {
			if t.Tag == "fork" {
				hasFork = true
				if t.Attributes["next"] != "" {
					anyForkHasNext = true
				}
			}
			if t.Tag == "goto" {
				hasGoto = true
			}
		}
		if hasFork && !anyForkHasNext && !hasGoto {
			diags = append(diags, Diagnostic{
				Severity: Error,
				File:     filename,
				Message:  fmt.Sprintf("<fork> in %s has no \"next\" attribute and no accompanying <goto>; parent agent has no continuation", filename),
				Check:    "missing-fork-next",
			})
		}

		// ambiguous-state-resolution check: extensionless targets that resolve
		// to both a .md and a platform script file.
		for _, t := range pf.transitions {
			if parsing.IsWorkflowTag(t.Tag) || t.Target == "" {
				continue
			}
			ext := strings.ToLower(filepath.Ext(t.Target))
			if workflow.StateExtensions[ext] {
				continue // has explicit extension, no ambiguity concern
			}
			stem := t.Target
			if seenAmbiguousStems[stem] {
				continue
			}
			hasMd := knownFiles[stem+".md"]
			var scriptExt string
			if opts.WindowsMode {
				if knownFiles[stem+".bat"] {
					scriptExt = "bat"
				} else if knownFiles[stem+".ps1"] {
					scriptExt = "ps1"
				}
			} else {
				if knownFiles[stem+".sh"] {
					scriptExt = "sh"
				}
			}
			if hasMd && scriptExt != "" {
				seenAmbiguousStems[stem] = true
				diags = append(diags, Diagnostic{
					Severity: Error,
					File:     filename,
					Message:  fmt.Sprintf("ambiguous state %q: both %s.md and %s.%s exist; transitions using %q without extension will fail", stem, stem, stem, scriptExt, stem),
					Check:    "ambiguous-state-resolution",
				})
			}
		}
	}

	// Step 5: Sort and return.
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
