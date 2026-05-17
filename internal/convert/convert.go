// Package convert transforms a directory or zip workflow scope into YAML format.
package convert

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/vector76/raymond/internal/backendcfg"
	"github.com/vector76/raymond/internal/manifest"
	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/policy"
	"github.com/vector76/raymond/internal/workflow"
	"github.com/vector76/raymond/internal/zipscope"
)

// parsedState holds parsed data for a single state file.
type parsedState struct {
	filename    string
	transitions []parsing.Transition
	pol         *policy.Policy
	bodyText    string
	rawScript   string // non-empty only for script files
}

// stateGroup groups files that share the same abstract state name.
type stateGroup struct {
	mdFile  *parsedState
	scripts map[string]*parsedState // keyed by lowercase extension (e.g. ".sh")
}

// sortEntry pairs a state name with its BFS distance for ordering.
type sortEntry struct {
	name string
	dist int
}

// Convert reads a directory or zip workflow scope and returns an equivalent
// YAML string, a list of warnings, and any error. The caller is responsible
// for rejecting YAML scopes and performing zip hash/layout validation before
// calling this function.
func Convert(scopeDir string) (string, []string, error) {
	var warnings []string

	// Step 1: Enumerate all files.
	allFiles, err := listAllFiles(scopeDir)
	if err != nil {
		return "", nil, err
	}

	// Step 2: Classify files. workflow.yaml is consumed as the manifest;
	// README.md is silently skipped; anything else with an unrecognised
	// extension produces a warning.
	var stateFiles []string
	var mani *manifest.Manifest
	for _, name := range allFiles {
		if strings.EqualFold(name, "workflow.yaml") {
			m, parseErr := readManifestFromScope(scopeDir, name)
			if parseErr != nil {
				warnings = append(warnings,
					fmt.Sprintf("workflow.yaml: failed to parse manifest, top-level fields will be dropped: %v", parseErr))
			} else if m != nil {
				mani = m
			}
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !workflow.StateExtensions[ext] {
			if !strings.EqualFold(name, "README.md") {
				warnings = append(warnings, fmt.Sprintf("skipping non-state file: %s", name))
			}
			continue
		}
		if strings.EqualFold(name, "README.md") {
			continue
		}
		stateFiles = append(stateFiles, name)
	}

	// Step 3: Resolve entry point (platform-neutral).
	entryFile, err := resolveEntryPoint(stateFiles)
	if err != nil {
		return "", nil, err
	}
	entryName := parsing.ExtractStateName(entryFile)

	// Step 4: Parse all state files.
	var parsed []parsedState
	for _, filename := range stateFiles {
		transitions, pol, fmErr, bodyText, ioErr := workflow.ExtractFileData(scopeDir, filename)
		if ioErr != nil {
			return "", nil, fmt.Errorf("reading %s: %w", filename, ioErr)
		}
		if fmErr != nil {
			warnings = append(warnings, fmt.Sprintf("%s: frontmatter parse error: %v", filename, fmErr))
		}

		var rawScript string
		ext := strings.ToLower(filepath.Ext(filename))
		if ext == ".sh" || ext == ".ps1" || ext == ".bat" {
			content, err := workflow.ReadFileContent(scopeDir, filename)
			if err != nil {
				return "", nil, fmt.Errorf("reading script %s: %w", filename, err)
			}
			rawScript = content
		}

		parsed = append(parsed, parsedState{
			filename:    filename,
			transitions: transitions,
			pol:         pol,
			bodyText:    bodyText,
			rawScript:   rawScript,
		})
	}

	// Step 5: Group by abstract state name.
	groups := make(map[string]*stateGroup)

	for i := range parsed {
		p := &parsed[i]
		name := parsing.ExtractStateName(p.filename)
		g, ok := groups[name]
		if !ok {
			g = &stateGroup{scripts: make(map[string]*parsedState)}
			groups[name] = g
		}
		ext := strings.ToLower(filepath.Ext(p.filename))
		if ext == ".md" {
			g.mdFile = p
		} else {
			g.scripts[ext] = p
		}
	}

	// Check for conflicts: both .md and script files with same stem.
	for name, g := range groups {
		if g.mdFile != nil && len(g.scripts) > 0 {
			files := []string{g.mdFile.filename}
			for _, sp := range g.scripts {
				files = append(files, sp.filename)
			}
			sort.Strings(files)
			return "", nil, fmt.Errorf("state %q has conflicting file types: %s", name, strings.Join(files, ", "))
		}
	}

	// Step 6: Normalize transition targets.
	for _, g := range groups {
		if g.mdFile != nil && g.mdFile.pol != nil {
			normalizeAllowedTransitions(g.mdFile.pol.AllowedTransitions)
		}
	}

	// Step 7: Compute BFS ordering.
	transMap := make(map[string][]parsing.Transition)
	for name, g := range groups {
		if g.mdFile != nil {
			transMap[name] = g.mdFile.transitions
		} else {
			// Collect transitions from all script variants.
			for _, sp := range g.scripts {
				transMap[name] = append(transMap[name], sp.transitions...)
			}
		}
	}

	distances := bfsDistances(entryName, transMap)

	var entries []sortEntry
	for name := range groups {
		d, ok := distances[name]
		if !ok {
			d = math.MaxInt
		}
		entries = append(entries, sortEntry{name: name, dist: d})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].dist != entries[j].dist {
			return entries[i].dist < entries[j].dist
		}
		return entries[i].name < entries[j].name
	})

	// Step 8: Generate YAML output.
	yamlStr, err := buildYAML(entries, groups, mani)
	if err != nil {
		return "", nil, err
	}

	return yamlStr, warnings, nil
}

// readManifestFromScope parses workflow.yaml from either a directory scope or
// a zip scope and returns the manifest. Returns (nil, nil) if the file is a
// YAML scope rather than a manifest (manifest.ErrNotManifest).
func readManifestFromScope(scopeDir, name string) (*manifest.Manifest, error) {
	var m *manifest.Manifest
	var err error
	if zipscope.IsZipScope(scopeDir) {
		content, readErr := zipscope.ReadText(scopeDir, name)
		if readErr != nil {
			return nil, readErr
		}
		m, err = manifest.ParseManifestData([]byte(content))
	} else {
		m, err = manifest.ParseManifest(filepath.Join(scopeDir, name))
	}
	if errors.Is(err, manifest.ErrNotManifest) {
		// File looked like a YAML scope, not a manifest — nothing to preserve.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// listAllFiles returns all filenames in the scope.
func listAllFiles(scopeDir string) ([]string, error) {
	if zipscope.IsZipScope(scopeDir) {
		return zipscope.ListFiles(scopeDir)
	}
	entries, err := os.ReadDir(scopeDir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// resolveEntryPoint finds the entry point file among stateFiles (platform-neutral).
// Multiple files for the same state (e.g. START.sh and START.ps1) are allowed.
func resolveEntryPoint(stateFiles []string) (string, error) {
	var startFile, oneStartFile string
	for _, f := range stateFiles {
		stem := parsing.ExtractStateName(f)
		upper := strings.ToUpper(stem)
		switch upper {
		case "1_START":
			if oneStartFile == "" {
				oneStartFile = f
			}
		case "START":
			if startFile == "" {
				startFile = f
			}
		}
	}
	if oneStartFile != "" && startFile != "" {
		return "", fmt.Errorf("ambiguous entry point: both %s and %s exist", oneStartFile, startFile)
	}
	if oneStartFile != "" {
		return oneStartFile, nil
	}
	if startFile != "" {
		return startFile, nil
	}
	return "", fmt.Errorf("no entry point found: need a file named 1_START or START with a recognized extension")
}

// normalizeAllowedTransitions strips file extensions from target, return, and
// next values in policy allowed_transitions entries.
func normalizeAllowedTransitions(entries []map[string]string) {
	for _, entry := range entries {
		tag := entry["tag"]
		isWorkflow := parsing.IsWorkflowTag(tag)

		// Strip extension from target unless it's a cross-workflow tag.
		if !isWorkflow {
			if target, ok := entry["target"]; ok {
				entry["target"] = parsing.ExtractStateName(target)
			}
		}

		// return and next are always local state names — strip extensions.
		if ret, ok := entry["return"]; ok {
			entry["return"] = parsing.ExtractStateName(ret)
		}
		if next, ok := entry["next"]; ok {
			entry["next"] = parsing.ExtractStateName(next)
		}
	}
}

// buildYAML constructs the YAML document string using the yaml.v3 Node API.
// When mani is non-nil, its top-level fields are emitted before "states:" so
// they survive the conversion (id, name, description, default_budget,
// requires_human_input, working_directory, environment, input, backend).
func buildYAML(order []sortEntry, groups map[string]*stateGroup, mani *manifest.Manifest) (string, error) {
	// Root document → mapping with single key "states".
	statesMapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

	for _, entry := range order {
		g := groups[entry.name]

		// State name key.
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: entry.name, Tag: "!!str"}

		// State value mapping.
		valMapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

		if g.mdFile != nil {
			// Markdown state: prompt + optional policy fields.
			addBlockScalar(valMapping, "prompt", g.mdFile.bodyText)

			if g.mdFile.pol != nil && len(g.mdFile.pol.AllowedTransitions) > 0 {
				addAllowedTransitions(valMapping, g.mdFile.pol.AllowedTransitions)
			}
			if g.mdFile.pol != nil && g.mdFile.pol.Model != "" {
				addScalar(valMapping, "model", g.mdFile.pol.Model)
			}
			if g.mdFile.pol != nil && g.mdFile.pol.Effort != "" {
				addScalar(valMapping, "effort", g.mdFile.pol.Effort)
			}
		} else {
			// Script state: platform keys in order sh, ps1, bat.
			for _, ext := range []string{".sh", ".ps1", ".bat"} {
				if sp, ok := g.scripts[ext]; ok {
					platformKey := ext[1:] // strip leading dot
					addBlockScalar(valMapping, platformKey, sp.rawScript)
				}
			}
		}

		statesMapping.Content = append(statesMapping.Content, keyNode, valMapping)
	}

	rootMapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if err := appendManifestFields(rootMapping, mani); err != nil {
		return "", err
	}
	rootMapping.Content = append(rootMapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "states", Tag: "!!str"},
		statesMapping,
	)

	doc := &yaml.Node{Kind: yaml.DocumentNode}
	doc.Content = append(doc.Content, rootMapping)

	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return "", fmt.Errorf("encoding YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("closing YAML encoder: %w", err)
	}

	return buf.String(), nil
}

// appendManifestFields adds the non-zero top-level manifest fields (id, name,
// description, default_budget, requires_human_input, working_directory,
// environment, input, backend) to rootMapping in canonical order. Each field
// is omitted when its zero value matches the manifest's zero value, mirroring
// the rule the convert tool uses for state-level fields (don't emit what the
// user didn't supply).
func appendManifestFields(rootMapping *yaml.Node, mani *manifest.Manifest) error {
	if mani == nil {
		return nil
	}
	if mani.ID != "" {
		addScalar(rootMapping, "id", mani.ID)
	}
	if mani.Name != "" {
		addScalar(rootMapping, "name", mani.Name)
	}
	if mani.Description != "" {
		addScalar(rootMapping, "description", mani.Description)
	}
	if mani.DefaultBudget != 0 {
		// Format with a decimal point so the value is unambiguously a float
		// even for whole numbers (1.0, not 1). The default !!float tag from
		// fmt.Sprintf "%g" produces "1" for 1.0, which the YAML library
		// then has to disambiguate with an explicit !!float tag in output.
		val := fmt.Sprintf("%g", mani.DefaultBudget)
		if !strings.Contains(val, ".") && !strings.Contains(val, "e") {
			val += ".0"
		}
		rootMapping.Content = append(rootMapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "default_budget", Tag: "!!str"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: val},
		)
	}
	// Skip the "auto" default for requires_human_input. The manifest parser
	// fills this in when the field is omitted, so emitting "auto" would add
	// noise to converted output for workflows that didn't declare it.
	if mani.RequiresHumanInput != "" && mani.RequiresHumanInput != "auto" {
		addScalar(rootMapping, "requires_human_input", mani.RequiresHumanInput)
	}
	if mani.WorkingDirectory != "" {
		addScalar(rootMapping, "working_directory", mani.WorkingDirectory)
	}
	if len(mani.Environment) > 0 {
		envNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		var keys []string
		for k := range mani.Environment {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			addScalar(envNode, k, mani.Environment[k])
		}
		rootMapping.Content = append(rootMapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "environment", Tag: "!!str"},
			envNode,
		)
	}
	// Emit `input:` only when the user supplied label/description or a
	// non-default mode. The parser fills mode="optional" when input is
	// absent, so an all-default input block would round-trip noise.
	hasNonDefaultInput := mani.Input.Label != "" || mani.Input.Description != "" ||
		(mani.Input.Mode != "" && mani.Input.Mode != manifest.InputModeOptional)
	if hasNonDefaultInput {
		inputNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if mani.Input.Mode != "" {
			addScalar(inputNode, "mode", mani.Input.Mode)
		}
		if mani.Input.Label != "" {
			addScalar(inputNode, "label", mani.Input.Label)
		}
		if mani.Input.Description != "" {
			addScalar(inputNode, "description", mani.Input.Description)
		}
		rootMapping.Content = append(rootMapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "input", Tag: "!!str"},
			inputNode,
		)
	}
	if mani.Backend.Name != "" {
		// Emit the structured form (name + options) rather than the bare
		// string. BackendSpec.UnmarshalYAML accepts both shapes, so a
		// downstream YAML scope parser handles either; the structured form
		// is preferred because it preserves any options the user declared.
		backendNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		addScalar(backendNode, "name", mani.Backend.Name)
		if err := appendBackendOptions(backendNode, &mani.Backend.Options); err != nil {
			return err
		}
		rootMapping.Content = append(rootMapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "backend", Tag: "!!str"},
			backendNode,
		)
	}
	return nil
}

// appendBackendOptions emits backend.options under backendNode, skipping
// fields whose values match the BackendOptions zero value (so a workflow that
// only declared `backend: pi` doesn't get noisy default-zero options written
// back). The options key is only added if at least one field has a value.
func appendBackendOptions(backendNode *yaml.Node, opts *backendcfg.BackendOptions) error {
	optsNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

	if opts.Provider != "" {
		addScalar(optsNode, "provider", opts.Provider)
	}
	if opts.Thinking != "" {
		addScalar(optsNode, "thinking", opts.Thinking)
	}
	if len(opts.Tools) > 0 {
		addStringList(optsNode, "tools", opts.Tools)
	}
	if opts.NoBuiltinTools {
		addScalar(optsNode, "no_builtin_tools", "true")
	}
	if opts.NoTools {
		addScalar(optsNode, "no_tools", "true")
	}
	if opts.NoExtensions {
		addScalar(optsNode, "no_extensions", "true")
	}
	if opts.NoSkills {
		addScalar(optsNode, "no_skills", "true")
	}
	if len(opts.Extensions) > 0 {
		addStringList(optsNode, "extensions", opts.Extensions)
	}
	if len(opts.Skills) > 0 {
		addStringList(optsNode, "skills", opts.Skills)
	}
	if opts.SessionDir != "" {
		addScalar(optsNode, "session_dir", opts.SessionDir)
	}

	if len(optsNode.Content) == 0 {
		// No options declared — emit `backend: { name: ... }` without
		// an empty `options:` key.
		return nil
	}
	backendNode.Content = append(backendNode.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "options", Tag: "!!str"},
		optsNode,
	)
	return nil
}

// addStringList adds a key with a sequence of strings to a mapping node.
func addStringList(mapping *yaml.Node, key string, values []string) {
	seqNode := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range values {
		seqNode.Content = append(seqNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: v, Tag: "!!str"})
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"}
	mapping.Content = append(mapping.Content, keyNode, seqNode)
}

// addBlockScalar adds a key with a literal block scalar (|) value to a mapping node.
func addBlockScalar(mapping *yaml.Node, key, value string) {
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"}
	valNode := &yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: "!!str", Style: yaml.LiteralStyle}
	mapping.Content = append(mapping.Content, keyNode, valNode)
}

// addScalar adds a key-value pair of plain scalars to a mapping node.
func addScalar(mapping *yaml.Node, key, value string) {
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"}
	valNode := &yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: "!!str"}
	mapping.Content = append(mapping.Content, keyNode, valNode)
}

// addAllowedTransitions adds the allowed_transitions sequence to a mapping node.
func addAllowedTransitions(mapping *yaml.Node, entries []map[string]string) {
	seqNode := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}

	for _, entry := range entries {
		entryMapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

		// Emit keys in a stable order: tag first, then target, then
		// remaining keys alphabetically.
		if v, ok := entry["tag"]; ok {
			addScalar(entryMapping, "tag", v)
		}
		if v, ok := entry["target"]; ok {
			addScalar(entryMapping, "target", v)
		}

		// Remaining keys in sorted order.
		var remaining []string
		for k := range entry {
			if k != "tag" && k != "target" {
				remaining = append(remaining, k)
			}
		}
		sort.Strings(remaining)
		for _, k := range remaining {
			addScalar(entryMapping, k, entry[k])
		}

		seqNode.Content = append(seqNode.Content, entryMapping)
	}

	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "allowed_transitions", Tag: "!!str"}
	mapping.Content = append(mapping.Content, keyNode, seqNode)
}
