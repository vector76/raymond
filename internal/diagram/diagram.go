// Package diagram generates Mermaid flowchart diagrams from Raymond workflow
// definitions. It scans a workflow scope (directory or zip archive) for state
// files, extracts transitions from YAML frontmatter and body text, and renders
// a Mermaid flowchart showing states as nodes and transitions as edges.
package diagram

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vector76/raymond/internal/parsing"
	"github.com/vector76/raymond/internal/specifier"
	"github.com/vector76/raymond/internal/workflow"
)

// Options configures diagram generation behaviour.
type Options struct {
	WindowsMode bool // if false (default), include .sh; if true, include .bat/.ps1
}

// NodeContent holds the raw content and type of a state file.
type NodeContent struct {
	Content    string // raw file text
	IsMarkdown bool
}

// Result holds the output of GenerateDiagram.
type Result struct {
	Mermaid      string                 // Mermaid flowchart text
	Warnings     []string               // diagnostic messages for stderr
	FileContents map[string]NodeContent // keyed by sanitized node ID (sanitizeID(ExtractStateName(filename)))
}

// nodeInfo tracks metadata for a single node in the diagram.
type nodeInfo struct {
	id         string // extensionless state name
	filename   string // original filename with extension (empty for missing/external)
	isScript   bool   // true for .sh/.bat/.ps1
	isMissing  bool   // referenced but no file exists
	isExternal bool   // cross-workflow reference
}

// edge represents a directed edge in the diagram.
type edge struct {
	from  string
	to    string
	tag   string // semantic tag type: "goto", "reset", "call", "fork", etc.
	label string // display label
	style string // "solid", "dashed", "dotted"
}

// callSite records a call or function transition for result-return tracing.
type callSite struct {
	caller      string // node that emitted the call/function
	callee      string // target of the call
	returnState string // state to return to
}

// GenerateDiagram scans the workflow scope at scopeDir and returns a Mermaid
// flowchart diagram with warnings.
func GenerateDiagram(scopeDir string, opts Options) (Result, error) {
	files, err := workflow.ListStateFiles(scopeDir, opts.WindowsMode)
	if err != nil {
		return Result{}, fmt.Errorf("listing state files: %w", err)
	}

	var warnings []string

	// Build node set from discovered files.
	nodes := make(map[string]*nodeInfo)
	for _, f := range files {
		id := parsing.ExtractStateName(f)
		ext := strings.ToLower(filepath.Ext(f))
		nodes[id] = &nodeInfo{
			id:       id,
			filename: f,
			isScript: ext == ".sh" || ext == ".bat" || ext == ".ps1",
		}
	}

	// Determine entry point.
	entryID := findEntryPoint(scopeDir, nodes)

	// Extract transitions from each file and build edges.
	var edges []edge
	var callSites []callSite
	// Track which nodes can emit <result>.
	resultEmitters := make(map[string]bool)

	for _, f := range files {
		id := parsing.ExtractStateName(f)
		transitions, parseWarnings := extractTransitions(scopeDir, f)
		warnings = append(warnings, parseWarnings...)

		for _, t := range transitions {
			newEdges, newCalls, newWarnings := transitionToEdges(id, t, nodes)
			edges = append(edges, newEdges...)
			callSites = append(callSites, newCalls...)
			warnings = append(warnings, newWarnings...)

			if t.Tag == "result" {
				resultEmitters[id] = true
			}
		}
	}

	// Build adjacency map for goto/reset edges (for result tracing).
	gotoResetAdj := buildGotoResetAdj(edges)

	// Assign call-depth levels to nodes reachable from the entry point.
	nodeLevel, maxLevel, levelWarnings := assignLevels(entryID, gotoResetAdj, callSites)
	warnings = append(warnings, levelWarnings...)

	// Trace result returns using level-based stack simulation.
	insideCall, returnEdges, traceWarnings := traceResults(
		nodeLevel, maxLevel, gotoResetAdj, callSites, resultEmitters, nodes)
	warnings = append(warnings, traceWarnings...)
	edges = append(edges, returnEdges...)

	// True terminations: result emitters NOT inside any call.
	terminalNodes := make(map[string]bool)
	for state := range resultEmitters {
		if !insideCall[state] {
			terminalNodes[state] = true
		}
	}

	// Populate file contents map keyed by sanitized node ID.
	fileContents := make(map[string]NodeContent)
	for _, f := range files {
		content, err := workflow.ReadFileContent(scopeDir, f)
		if err != nil {
			continue
		}
		id := sanitizeID(parsing.ExtractStateName(f))
		isMarkdown := strings.ToLower(filepath.Ext(f)) == ".md"
		fileContents[id] = NodeContent{Content: content, IsMarkdown: isMarkdown}
	}

	mermaid := renderMermaid(nodes, edges, entryID, terminalNodes)
	return Result{Mermaid: mermaid, Warnings: warnings, FileContents: fileContents}, nil
}

// findEntryPoint determines the entry node ID. Uses specifier.ResolveEntryPoint
// for the canonical lookup, falling back to scanning nodes.
func findEntryPoint(scopeDir string, nodes map[string]*nodeInfo) string {
	entry, err := specifier.ResolveEntryPoint(scopeDir)
	if err == nil {
		id := parsing.ExtractStateName(entry)
		if _, ok := nodes[id]; ok {
			return id
		}
		// The resolved entry file was filtered out (e.g. a .sh file when running
		// in Windows mode). Fall through to the node-set fallback.
	}
	// Fallback: look for 1_START or START in node set.
	if _, ok := nodes["1_START"]; ok {
		return "1_START"
	}
	if _, ok := nodes["START"]; ok {
		return "START"
	}
	return ""
}

// extractTransitions returns transitions for a single file, preferring
// frontmatter allowed_transitions when available.
func extractTransitions(scopeDir, filename string) ([]parsing.Transition, []string) {
	transitions, pol, _, _, err := workflow.ExtractFileData(scopeDir, filename)
	if err != nil {
		return nil, []string{fmt.Sprintf("cannot read %s: %v", filename, err)}
	}
	// Regenerate warnings for frontmatter entries that lack a required target.
	var warnings []string
	if pol != nil {
		for _, entry := range pol.AllowedTransitions {
			tag := entry["tag"]
			if tag == "" || tag == "result" {
				continue
			}
			if entry["target"] == "" {
				warnings = append(warnings, fmt.Sprintf(
					"%s: frontmatter entry with tag=%q has no target; omitting from diagram",
					filename, tag))
			}
		}
	}
	return transitions, warnings
}

// normalizeTarget strips extensions from a target name to produce a node ID.
func normalizeTarget(target string) string {
	return parsing.ExtractStateName(target)
}

// ensureNode adds a node to the map if it doesn't exist. If the node is new
// and has no file backing, it's marked as missing. Returns true if the node
// was newly created as missing.
func ensureNode(nodes map[string]*nodeInfo, id string) bool {
	if _, ok := nodes[id]; !ok {
		nodes[id] = &nodeInfo{
			id:        id,
			isMissing: true,
		}
		return true
	}
	return false
}

// ensureExternalNode adds a cross-workflow node.
func ensureExternalNode(nodes map[string]*nodeInfo, label string) string {
	// Clean up the label for use as a Mermaid ID.
	id := "xwf_" + sanitizeID(label)
	if _, ok := nodes[id]; !ok {
		nodes[id] = &nodeInfo{
			id:         id,
			filename:   label,
			isExternal: true,
		}
	}
	return id
}

// transitionToEdges converts a single transition into edges, call sites, and warnings.
func transitionToEdges(fromID string, t parsing.Transition, nodes map[string]*nodeInfo) ([]edge, []callSite, []string) {
	var edges []edge
	var calls []callSite
	var warnings []string

	labelSuffix := ""
	if _, hasInput := t.Attributes["input"]; hasInput {
		labelSuffix = " [input]"
	}

	missingWarn := func(id string) {
		if ensureNode(nodes, id) {
			warnings = append(warnings, fmt.Sprintf(
				"state %q is referenced but does not exist in the workflow scope", id))
		}
	}

	switch {
	case t.Tag == "goto":
		toID := normalizeTarget(t.Target)
		missingWarn(toID)
		edges = append(edges, edge{
			from: fromID, to: toID, tag: "goto",
			label: "goto" + labelSuffix, style: "solid",
		})

	case t.Tag == "reset":
		toID := normalizeTarget(t.Target)
		missingWarn(toID)
		edges = append(edges, edge{
			from: fromID, to: toID, tag: "reset",
			label: "reset" + labelSuffix, style: "dotted",
		})

	case t.Tag == "call" || t.Tag == "function":
		toID := normalizeTarget(t.Target)
		missingWarn(toID)
		edges = append(edges, edge{
			from: fromID, to: toID, tag: t.Tag,
			label: t.Tag + labelSuffix, style: "solid",
		})
		// Record call site for result-return tracing. The return path is
		// shown on the traced return edges (from result emitter to return
		// state), not as a separate caller→returnState edge.
		if ret, ok := t.Attributes["return"]; ok && ret != "" {
			retID := normalizeTarget(ret)
			missingWarn(retID)
			calls = append(calls, callSite{
				caller:      fromID,
				callee:      toID,
				returnState: retID, // store normalized
			})
		}

	case t.Tag == "fork":
		toID := normalizeTarget(t.Target)
		missingWarn(toID)
		edges = append(edges, edge{
			from: fromID, to: toID, tag: "fork",
			label: "fork" + labelSuffix, style: "dashed",
		})
		if next, ok := t.Attributes["next"]; ok && next != "" {
			nextID := normalizeTarget(next)
			missingWarn(nextID)
			edges = append(edges, edge{
				from: fromID, to: nextID, tag: "fork",
				label: "fork next", style: "solid",
			})
		}

	case t.Tag == "result":
		// Result edges are handled later (terminal or return tracing).
		// We just record the emitter; actual edges are added in GenerateDiagram.

	case t.Tag == "call-workflow" || t.Tag == "function-workflow":
		extID := ensureExternalNode(nodes, t.Target)
		edges = append(edges, edge{
			from: fromID, to: extID, tag: t.Tag,
			label: t.Tag + labelSuffix, style: "solid",
		})
		if ret, ok := t.Attributes["return"]; ok && ret != "" {
			retID := normalizeTarget(ret)
			missingWarn(retID)
			edges = append(edges, edge{
				from: fromID, to: retID, tag: t.Tag,
				label: t.Tag + " return", style: "dashed",
			})
			// Cross-workflow calls: we can't trace inside the sub-workflow,
			// so no callSite is recorded for result tracing.
		}

	case t.Tag == "fork-workflow":
		extID := ensureExternalNode(nodes, t.Target)
		edges = append(edges, edge{
			from: fromID, to: extID, tag: "fork-workflow",
			label: "fork-workflow" + labelSuffix, style: "dashed",
		})
		if next, ok := t.Attributes["next"]; ok && next != "" {
			nextID := normalizeTarget(next)
			missingWarn(nextID)
			edges = append(edges, edge{
				from: fromID, to: nextID, tag: "fork-workflow",
				label: "fork-workflow next", style: "solid",
			})
		}

	case t.Tag == "reset-workflow":
		extID := ensureExternalNode(nodes, t.Target)
		edges = append(edges, edge{
			from: fromID, to: extID, tag: "reset-workflow",
			label: "reset-workflow" + labelSuffix, style: "dotted",
		})
	}

	return edges, calls, warnings
}

// assignLevels assigns a call-depth level to each node reachable from the entry
// point. Level 0 is the top-level scope; each call/function transition increases
// the depth by 1. Return states stay at the caller's level. Warns if a node is
// reachable at multiple different levels.
func assignLevels(entryID string, adj map[string][]string, sites []callSite) (map[string]int, int, []string) {
	nodeLevel := make(map[string]int)
	var warnings []string
	maxLevel := 0

	if entryID == "" {
		return nodeLevel, maxLevel, warnings
	}

	// Index call sites by caller for fast lookup.
	callsBySource := make(map[string][]callSite)
	for _, cs := range sites {
		callsBySource[cs.caller] = append(callsBySource[cs.caller], cs)
	}

	// seeds[level] holds node IDs to BFS from at that level.
	// The slice grows dynamically as deeper levels are discovered.
	seeds := [][]string{{entryID}}

	for level := 0; level < len(seeds); level++ {
		// Process seeds at this level. The slice may grow during iteration
		// as return states at the same level are discovered.
		for i := 0; i < len(seeds[level]); i++ {
			seed := seeds[level][i]
			if existing, ok := nodeLevel[seed]; ok {
				if existing != level {
					warnings = append(warnings, fmt.Sprintf(
						"state %q is reachable at call depth %d and %d",
						seed, existing, level))
				}
				continue
			}

			// BFS from seed through goto/reset edges.
			reachable := workflow.BFSReachable(seed, adj)
			for node := range reachable {
				if existing, ok := nodeLevel[node]; ok {
					if existing != level {
						warnings = append(warnings, fmt.Sprintf(
							"state %q is reachable at call depth %d and %d",
							node, existing, level))
					}
					continue
				}
				nodeLevel[node] = level
				if level > maxLevel {
					maxLevel = level
				}

				// Discover call/function transitions from this node.
				for _, cs := range callsBySource[node] {
					// Callee enters the next level.
					nextLevel := level + 1
					for len(seeds) <= nextLevel {
						seeds = append(seeds, nil)
					}
					seeds[nextLevel] = append(seeds[nextLevel], cs.callee)
					// Return state stays at the caller's level.
					seeds[level] = append(seeds[level], cs.returnState)
				}
			}
		}
	}

	return nodeLevel, maxLevel, warnings
}

// traceResults performs bottom-up result tracing using call-depth levels.
//
// For each level N (deepest first), it finds all result-emitting states
// reachable from call targets at that level via goto/reset edges, and draws
// return edges back to the caller's return state. After processing level N,
// it adds synthetic goto edges at level N-1 (caller → returnState) so that
// the parent level's BFS can follow the call-and-return path.
//
// Call sites whose caller has no assigned level (disconnected subgraphs) are
// handled with a flat BFS fallback.
func traceResults(
	nodeLevel map[string]int,
	maxLevel int,
	adj map[string][]string,
	sites []callSite,
	resultEmitters map[string]bool,
	nodes map[string]*nodeInfo,
) (map[string]bool, []edge, []string) {
	insideCall := make(map[string]bool)
	var warnings []string

	// returnInfo pairs a return state with the caller that initiated the call.
	type returnInfo struct {
		returnState string
		caller      string
	}
	resultReturns := make(map[string][]returnInfo) // result emitter → return targets

	recordReturn := func(state string, cs callSite) {
		insideCall[state] = true
		resultReturns[state] = append(resultReturns[state], returnInfo{
			returnState: cs.returnState,
			caller:      cs.caller,
		})
	}

	// Group call sites by callee level. Sites whose caller has no level are
	// collected separately for flat fallback.
	callSitesByCalleeLevel := make(map[int][]callSite)
	var unleveledSites []callSite
	for _, cs := range sites {
		if _, ok := nodeLevel[cs.caller]; !ok {
			unleveledSites = append(unleveledSites, cs)
			continue
		}
		calleeLevel, ok := nodeLevel[cs.callee]
		if !ok {
			continue // callee unreachable; skip
		}
		callSitesByCalleeLevel[calleeLevel] = append(callSitesByCalleeLevel[calleeLevel], cs)
	}

	// Process levels bottom-up.
	for level := maxLevel; level >= 1; level-- {
		for _, cs := range callSitesByCalleeLevel[level] {
			reachable := workflow.BFSReachable(cs.callee, adj)
			for state := range reachable {
				if nodeLevel[state] != level {
					continue
				}
				if resultEmitters[state] {
					recordReturn(state, cs)
				}
			}
		}
		// Add synthetic edges: caller → returnState at the caller's level.
		// This lets the parent level's BFS follow the call-and-return path.
		for _, cs := range callSitesByCalleeLevel[level] {
			adj[cs.caller] = append(adj[cs.caller], cs.returnState)
		}
	}

	// Flat fallback for disconnected call sites.
	for _, cs := range unleveledSites {
		reachable := workflow.BFSReachable(cs.callee, adj)
		for state := range reachable {
			if resultEmitters[state] {
				recordReturn(state, cs)
			}
		}
	}

	// Deduplicate and build return edges. Iterate in sorted order for
	// deterministic output.
	var returnEdges []edge
	var emitterIDs []string
	for id := range resultReturns {
		emitterIDs = append(emitterIDs, id)
	}
	sort.Strings(emitterIDs)
	for _, state := range emitterIDs {
		returns := resultReturns[state]
		// Deduplicate by (returnState, caller) pair.
		type pair struct{ ret, caller string }
		seen := make(map[pair]bool)
		var unique []returnInfo
		for _, ri := range returns {
			p := pair{ri.returnState, ri.caller}
			if !seen[p] {
				seen[p] = true
				unique = append(unique, ri)
			}
		}

		// Warn if multiple distinct return states.
		retStates := make(map[string]bool)
		for _, ri := range unique {
			retStates[ri.returnState] = true
		}
		if len(retStates) > 1 {
			warnings = append(warnings, fmt.Sprintf(
				"state %q emits <result> and is reachable from multiple callers; drawing return edges to: %s",
				state, strings.Join(sortedKeys(retStates), ", ")))
		}

		// Group callers by return state for the label.
		callersByRet := make(map[string][]string)
		for _, ri := range unique {
			callersByRet[ri.returnState] = append(callersByRet[ri.returnState], ri.caller)
		}
		retIDs := sortedKeys(retStates)
		for _, retID := range retIDs {
			callers := callersByRet[retID]
			sort.Strings(callers)
			ensureNode(nodes, retID)
			label := fmt.Sprintf("return (%s)", strings.Join(callers, ", "))
			returnEdges = append(returnEdges, edge{
				from:  state,
				to:    retID,
				tag:   "result",
				label: label,
				style: "dashed",
			})
		}
	}

	return insideCall, returnEdges, warnings
}

// buildGotoResetAdj builds an adjacency list from edges with goto or reset tags only.
func buildGotoResetAdj(edges []edge) map[string][]string {
	adj := make(map[string][]string)
	for _, e := range edges {
		if e.tag == "goto" || e.tag == "reset" {
			adj[e.from] = append(adj[e.from], e.to)
		}
	}
	return adj
}

// sanitizeID makes a string safe for use as a Mermaid node ID.
func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// renderMermaid generates the Mermaid flowchart text.
func renderMermaid(
	nodes map[string]*nodeInfo,
	edges []edge,
	entryID string,
	terminalNodes map[string]bool,
) string {
	var b strings.Builder
	b.WriteString("flowchart TD\n")

	// Start node.
	if entryID != "" {
		b.WriteString("    __start__((\" \"))\n")
		b.WriteString(fmt.Sprintf("    __start__ --> %s\n", sanitizeID(entryID)))
	}

	// Sort node IDs for deterministic output.
	var nodeIDs []string
	for id := range nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)

	// Emit node definitions.
	b.WriteString("\n")
	for _, id := range nodeIDs {
		n := nodes[id]
		safeID := sanitizeID(id)
		label := id
		if n.isExternal {
			label = n.filename // show the workflow path
		}

		switch {
		case n.isExternal:
			b.WriteString(fmt.Sprintf("    %s[[\"%s\"]]\n", safeID, escapeLabel(label)))
		case n.isScript:
			b.WriteString(fmt.Sprintf("    %s{{\"%s\"}}\n", safeID, escapeLabel(label)))
		default:
			b.WriteString(fmt.Sprintf("    %s[\"%s\"]\n", safeID, escapeLabel(label)))
		}

		if n.isMissing {
			b.WriteString(fmt.Sprintf("    style %s stroke-dasharray: 5 5\n", safeID))
		}
	}

	// Terminal nodes.
	termIDs := sortedKeys(terminalNodes)
	for i, state := range termIDs {
		termNodeID := fmt.Sprintf("__end_%d__", i+1)
		b.WriteString(fmt.Sprintf("    %s((\" \"))\n", termNodeID))

		safeFrom := sanitizeID(state)
		b.WriteString(fmt.Sprintf("    %s -->|result| %s\n", safeFrom, termNodeID))
	}

	// Emit edges.
	b.WriteString("\n")
	seen := make(map[string]bool)
	for _, e := range edges {
		safeFrom := sanitizeID(e.from)
		safeTo := sanitizeID(e.to)
		edgeKey := fmt.Sprintf("%s|%s|%s|%s", safeFrom, safeTo, e.label, e.style)
		if seen[edgeKey] {
			continue
		}
		seen[edgeKey] = true

		escapedLabel := escapeLabel(e.label)
		// Mermaid flowcharts have only two edge styles: solid (-->) and
		// non-solid (-.->). We use -.-> for both "dashed" and "dotted"
		// because Mermaid has no separate dotted line. The distinction is
		// preserved in the data model for clarity and potential future use.
		switch e.style {
		case "dashed", "dotted":
			b.WriteString(fmt.Sprintf("    %s -.->|%s| %s\n", safeFrom, escapedLabel, safeTo))
		default: // solid
			b.WriteString(fmt.Sprintf("    %s -->|%s| %s\n", safeFrom, escapedLabel, safeTo))
		}
	}

	// Disconnected nodes (files with no edges) are already emitted in the
	// node definitions above. Mermaid renders them as floating boxes.

	return b.String()
}

// escapeLabel escapes characters that are special in Mermaid labels.
// Pipes would break -->|label| syntax; quotes would break "label" syntax;
// brackets and parentheses are interpreted as node shape tokens by the parser.
func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, "\"", "#quot;")
	s = strings.ReplaceAll(s, "|", "#124;")
	s = strings.ReplaceAll(s, "[", "#91;")
	s = strings.ReplaceAll(s, "]", "#93;")
	s = strings.ReplaceAll(s, "(", "#40;")
	s = strings.ReplaceAll(s, ")", "#41;")
	return s
}

// sortedKeys returns the keys of a map in sorted order.
func sortedKeys(m map[string]bool) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
