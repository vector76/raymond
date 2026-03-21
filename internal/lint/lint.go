package lint

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/policy"
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

	// Step 3: Parse each file.
	type parsedFile struct {
		transitions []parsing.Transition
		pol         *policy.Policy
		fmErr       error
		bodyText    string
	}
	parsed := make(map[string]parsedFile, len(files))
	for _, filename := range files {
		transitions, pol, fmErr, bodyText, readErr := workflow.ExtractFileData(scopeDir, filename)
		if readErr != nil {
			return nil, readErr
		}
		parsed[filename] = parsedFile{transitions: transitions, pol: pol, fmErr: fmErr, bodyText: bodyText}
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

		// frontmatter-parse-error check.
		if pf.fmErr != nil {
			diags = append(diags, Diagnostic{
				Severity: Error,
				File:     filename,
				Message:  fmt.Sprintf("invalid YAML frontmatter in %s: %v", filename, pf.fmErr),
				Check:    "frontmatter-parse-error",
			})
		}

		// invalid-model check.
		if pf.pol != nil && pf.pol.Model != "" {
			validModels := map[string]bool{"opus": true, "sonnet": true, "haiku": true}
			if !validModels[pf.pol.Model] {
				diags = append(diags, Diagnostic{
					Severity: Error,
					File:     filename,
					Message:  fmt.Sprintf("invalid model %q in %s; must be one of: opus, sonnet, haiku", pf.pol.Model, filename),
					Check:    "invalid-model",
				})
			}
		}

		// invalid-effort check.
		if pf.pol != nil && pf.pol.Effort != "" {
			validEfforts := map[string]bool{"low": true, "medium": true, "high": true}
			if !validEfforts[pf.pol.Effort] {
				diags = append(diags, Diagnostic{
					Severity: Error,
					File:     filename,
					Message:  fmt.Sprintf("invalid effort %q in %s; must be one of: low, medium, high", pf.pol.Effort, filename),
					Check:    "invalid-effort",
				})
			}
		}

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

		// fork-next-mismatch check: if 2+ fork transitions have non-empty next
		// values that differ, emit a warning.
		var forkNextValues []string
		for _, t := range pf.transitions {
			if t.Tag == "fork" && t.Attributes["next"] != "" {
				forkNextValues = append(forkNextValues, t.Attributes["next"])
			}
		}
		if len(forkNextValues) >= 2 {
			first := forkNextValues[0]
			var second string
			for _, v := range forkNextValues[1:] {
				if v != first {
					second = v
					break
				}
			}
			if second != "" {
				diags = append(diags, Diagnostic{
					Severity: Warning,
					File:     filename,
					Message:  fmt.Sprintf("%s has fork tags with conflicting \"next\" values: %q vs %q; all must agree", filename, first, second),
					Check:    "fork-next-mismatch",
				})
			}
		}

		// unused-allowed-transition check: for .md files with a policy, any
		// allowed transition target (except result) not mentioned in the body.
		if pf.pol != nil && len(pf.pol.AllowedTransitions) > 0 {
			for _, entry := range pf.pol.AllowedTransitions {
				if entry["tag"] == "result" {
					continue
				}
				target := entry["target"]
				if target == "" {
					continue
				}
				if !strings.Contains(pf.bodyText, target) {
					diags = append(diags, Diagnostic{
						Severity: Warning,
						File:     filename,
						Message:  fmt.Sprintf("%s: allowed_transitions lists target %q which is not mentioned in the prompt body", filename, target),
						Check:    "unused-allowed-transition",
					})
				}
			}
		}

		// implicit-transition check: single allowed transition that could be
		// applied without an explicit tag.
		if pf.pol != nil && len(pf.pol.AllowedTransitions) > 0 {
			if policy.CanUseImplicitTransition(pf.pol) {
				t, _ := policy.GetImplicitTransition(pf.pol)
				display := t.Target
				if t.Tag == "result" {
					display = t.Payload
				}
				diags = append(diags, Diagnostic{
					Severity: Info,
					File:     filename,
					Message:  fmt.Sprintf("%s has a single allowed transition (<%s> %s); the agent does not need to emit the tag explicitly", filename, t.Tag, display),
					Check:    "implicit-transition",
				})
			}
		}

		// script-state-no-static-analysis check: emit info for script files.
		fileExt := strings.ToLower(filepath.Ext(filename))
		if fileExt == ".sh" || fileExt == ".bat" || fileExt == ".ps1" {
			diags = append(diags, Diagnostic{
				Severity: Info,
				File:     filename,
				Message:  fmt.Sprintf("%s is a script state; transitions are determined at runtime and cannot be fully validated statically", filename),
				Check:    "script-state-no-static-analysis",
			})
		}
	}

	// Step 5: Graph analysis checks.

	// Build fullAdj (all outgoing edges) and statesWithResult.
	fullAdj := make(map[string][]string)
	statesWithResult := make(map[string]bool)
	for _, filename := range files {
		pf := parsed[filename]
		stateName := parsing.ExtractStateName(filename)
		for _, t := range pf.transitions {
			if parsing.IsWorkflowTag(t.Tag) {
				continue
			}
			switch t.Tag {
			case "goto", "reset":
				if t.Target != "" {
					fullAdj[stateName] = append(fullAdj[stateName], parsing.ExtractStateName(t.Target))
				}
			case "call", "function":
				if t.Target != "" {
					fullAdj[stateName] = append(fullAdj[stateName], parsing.ExtractStateName(t.Target))
				}
				if ret := t.Attributes["return"]; ret != "" {
					fullAdj[stateName] = append(fullAdj[stateName], parsing.ExtractStateName(ret))
				}
			case "fork":
				if t.Target != "" {
					fullAdj[stateName] = append(fullAdj[stateName], parsing.ExtractStateName(t.Target))
				}
				if next := t.Attributes["next"]; next != "" {
					fullAdj[stateName] = append(fullAdj[stateName], parsing.ExtractStateName(next))
				}
			case "result":
				statesWithResult[stateName] = true
			}
		}
	}

	// unreachable-state check.
	if epErr == nil {
		reachable := workflow.BFSReachable(entryStateName, fullAdj)
		for _, filename := range files {
			if !reachable[parsing.ExtractStateName(filename)] {
				diags = append(diags, Diagnostic{
					Severity: Warning,
					File:     filename,
					Message:  fmt.Sprintf("%s is unreachable: no transitions lead to this state", filename),
					Check:    "unreachable-state",
				})
			}
		}
	}

	// dead-end-state check: .md files only.
	for _, filename := range files {
		ext := strings.ToLower(filepath.Ext(filename))
		if ext == ".sh" || ext == ".bat" || ext == ".ps1" {
			continue
		}
		if len(parsed[filename].transitions) == 0 {
			diags = append(diags, Diagnostic{
				Severity: Warning,
				File:     filename,
				Message:  fmt.Sprintf("%s has no outgoing transitions; agent will fail to produce a valid transition", filename),
				Check:    "dead-end-state",
			})
		}
	}

	// Build gotoResetAdj (only goto/reset edges) for call-without-result-path.
	gotoResetAdj := make(map[string][]string)
	for _, filename := range files {
		pf := parsed[filename]
		stateName := parsing.ExtractStateName(filename)
		for _, t := range pf.transitions {
			if parsing.IsWorkflowTag(t.Tag) {
				continue
			}
			if (t.Tag == "goto" || t.Tag == "reset") && t.Target != "" {
				gotoResetAdj[stateName] = append(gotoResetAdj[stateName], parsing.ExtractStateName(t.Target))
			}
		}
	}

	// call-without-result-path check.
	for _, filename := range files {
		for _, t := range parsed[filename].transitions {
			if (t.Tag != "call" && t.Tag != "function") || parsing.IsWorkflowTag(t.Tag) {
				continue
			}
			calleeStateName := parsing.ExtractStateName(t.Target)
			if !knownStates[calleeStateName] {
				continue
			}
			reachable := workflow.BFSReachable(calleeStateName, gotoResetAdj)
			hasResult := false
			for state := range reachable {
				if statesWithResult[state] {
					hasResult = true
					break
				}
			}
			if !hasResult {
				diags = append(diags, Diagnostic{
					Severity: Warning,
					File:     filename,
					Message:  fmt.Sprintf("<%s> in %s calls %s, but no reachable state from %s emits <result>; call will never return", t.Tag, filename, calleeStateName, calleeStateName),
					Check:    "call-without-result-path",
				})
			}
		}
	}

	// Step 6: Sort and return.
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
