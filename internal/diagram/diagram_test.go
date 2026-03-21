package diagram

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/parsing"
)

// writeFile is a test helper that writes content to a file in dir.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

// writeZip creates a zip file at path with the given files.
func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
}

func TestSimpleGotoChain(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `Do something.
When done: <goto>NEXT.md</goto>`)
	writeFile(t, dir, "NEXT.md", `Continue.
<goto>DONE.md</goto>`)
	writeFile(t, dir, "DONE.md", `Finish.
<result>complete</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)
	assert.Empty(t, result.Warnings)

	m := result.Mermaid
	assert.Contains(t, m, "flowchart TD")
	assert.Contains(t, m, `__start__((" "))`)
	assert.Contains(t, m, "__start__ --> 1_START")
	assert.Contains(t, m, `1_START["1_START"]`)
	assert.Contains(t, m, `NEXT["NEXT"]`)
	assert.Contains(t, m, `DONE["DONE"]`)
	assert.Contains(t, m, "1_START -->|goto| NEXT")
	assert.Contains(t, m, "NEXT -->|goto| DONE")
	// DONE emits result and is not inside a call → terminal node.
	assert.Contains(t, m, "-->|result|")
	assert.Contains(t, m, `__end_1__((" "))`)
}

func TestScriptStateShape(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.sh", `#!/bin/bash
echo "<goto>NEXT.md</goto>"`)
	writeFile(t, dir, "NEXT.md", `<result>done</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	// Script state gets hexagon shape.
	assert.Contains(t, m, `1_START{{"1_START"}}`)
	// Markdown state gets rectangle shape.
	assert.Contains(t, m, `NEXT["NEXT"]`)
}

func TestFrontmatterPreference(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `---
allowed_transitions:
  - { tag: goto, target: REVIEW.md }
  - { tag: goto, target: DONE.md }
---
Do stuff. <goto>WRONG.md</goto>`)
	writeFile(t, dir, "REVIEW.md", `<result>ok</result>`)
	writeFile(t, dir, "DONE.md", `<result>ok</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	// Should use frontmatter transitions, not body text.
	assert.Contains(t, m, "1_START -->|goto| REVIEW")
	assert.Contains(t, m, "1_START -->|goto| DONE")
	assert.NotContains(t, m, "WRONG")
}

func TestResetDottedEdge(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<reset>PHASE2.md</reset>`)
	writeFile(t, dir, "PHASE2.md", `<result>done</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	assert.Contains(t, m, "1_START -.->|reset| PHASE2")
}

func TestCallWithResultTracing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<call return="SUMMARIZE.md">RESEARCH.md</call>`)
	writeFile(t, dir, "RESEARCH.md", `Do research.
<goto>ANALYSIS.md</goto>`)
	writeFile(t, dir, "ANALYSIS.md", `<result>findings</result>`)
	writeFile(t, dir, "SUMMARIZE.md", `<result>summary</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	// Solid edge from caller to callee.
	assert.Contains(t, m, "1_START -->|call| RESEARCH")
	// No separate caller→returnState edge (suppressed).
	assert.NotContains(t, m, "call return")
	// Dashed return edge from ANALYSIS (result emitter) back to SUMMARIZE,
	// with caller name in label.
	assert.Contains(t, m, "ANALYSIS -.->|return #40;1_START#41;| SUMMARIZE")
	// ANALYSIS is inside a call → no terminal node.
	assert.NotContains(t, m, "ANALYSIS -->|result|")
	// SUMMARIZE emits result at top level → terminal node.
	assert.Contains(t, m, "SUMMARIZE -->|result|")
}

func TestCallResultEmitterIsCallee(t *testing.T) {
	// When the callee itself emits result (no goto chain).
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<call return="AFTER.md">CHILD.md</call>`)
	writeFile(t, dir, "CHILD.md", `<result>done</result>`)
	writeFile(t, dir, "AFTER.md", `<result>final</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	// CHILD emits result inside call → return edge to AFTER with caller name.
	assert.Contains(t, m, "CHILD -.->|return #40;1_START#41;| AFTER")
	// CHILD is inside call → no terminal node for CHILD.
	assert.NotContains(t, m, "CHILD -->|result|")
	// AFTER emits result at top level → terminal node.
	assert.Contains(t, m, "AFTER -->|result|")
}

func TestForkEdges(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<fork next="CONTINUE.md" item="task1">WORKER.md</fork>`)
	writeFile(t, dir, "WORKER.md", `<result>done</result>`)
	writeFile(t, dir, "CONTINUE.md", `<result>done</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	// Dashed to worker.
	assert.Contains(t, m, "1_START -.->|fork| WORKER")
	// Solid to next.
	assert.Contains(t, m, "1_START -->|fork next| CONTINUE")
}

func TestCrossWorkflowSubroutineShape(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `Spawn workers:
<fork-workflow next="MONITOR.md">../other_wf/</fork-workflow>`)
	writeFile(t, dir, "MONITOR.md", `<result>done</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	// Cross-workflow node gets subroutine shape.
	assert.Contains(t, m, `[["../other_wf/"]]`)
}

func TestMissingStateWarning(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<goto>MISSING.md</goto>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	// Missing node gets dashed border style.
	assert.Contains(t, m, "style MISSING stroke-dasharray: 5 5")
	// The node should still appear.
	assert.Contains(t, m, `MISSING["MISSING"]`)
	// Should also emit a warning.
	hasWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "MISSING") && strings.Contains(w, "does not exist") {
			hasWarning = true
			break
		}
	}
	assert.True(t, hasWarning, "expected warning about missing state")
}

func TestUnreachableState(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<goto>NEXT.md</goto>`)
	writeFile(t, dir, "NEXT.md", `<result>done</result>`)
	writeFile(t, dir, "ORPHAN.md", `<result>orphan</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	// Orphan should still appear as a node (disconnected).
	assert.Contains(t, m, `ORPHAN["ORPHAN"]`)
}

func TestREADMEExcluded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<result>done</result>`)
	writeFile(t, dir, "README.md", `# Documentation`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	assert.NotContains(t, result.Mermaid, "README")
}

func TestNoTargetFrontmatterWarning(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `---
allowed_transitions:
  - { tag: goto }
  - { tag: result }
---
Do stuff.
<result>done</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	// Should warn about no-target entry.
	hasWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "no target") {
			hasWarning = true
			break
		}
	}
	assert.True(t, hasWarning, "expected warning about no-target frontmatter entry")
}

func TestInputAnnotation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<goto input="data">NEXT.md</goto>`)
	writeFile(t, dir, "NEXT.md", `<result>done</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	assert.Contains(t, result.Mermaid, "goto #91;input#93;")
}

func TestMultipleCallersResultWarning(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<call return="AFTER_A.md">SHARED.md</call>`)
	writeFile(t, dir, "OTHER.md", `<call return="AFTER_B.md">SHARED.md</call>`)
	writeFile(t, dir, "SHARED.md", `<result>data</result>`)
	writeFile(t, dir, "AFTER_A.md", `<result>done</result>`)
	writeFile(t, dir, "AFTER_B.md", `<result>done</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	// Should warn about multiple callers.
	hasWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "multiple callers") {
			hasWarning = true
			break
		}
	}
	assert.True(t, hasWarning, "expected warning about multiple callers for result state")

	// Should draw return edges to both return states with caller names.
	m := result.Mermaid
	assert.Contains(t, m, "SHARED -.->|return #40;1_START#41;| AFTER_A")
	assert.Contains(t, m, "SHARED -.->|return #40;OTHER#41;| AFTER_B")
}

func TestFunctionEdges(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<function return="NEXT.md">EVAL.md</function>`)
	writeFile(t, dir, "EVAL.md", `<result>YES</result>`)
	writeFile(t, dir, "NEXT.md", `<result>done</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	assert.Contains(t, m, "1_START -->|function| EVAL")
	assert.NotContains(t, m, "function return")
	assert.Contains(t, m, "EVAL -.->|return #40;1_START#41;| NEXT")
}

func TestSelfLoop(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `---
allowed_transitions:
  - { tag: goto, target: 1_START.md }
  - { tag: result }
---
Try again: <goto>1_START.md</goto>
Or finish: <result>done</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	assert.Contains(t, result.Mermaid, "1_START -->|goto| 1_START")
}

func TestZipScope(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "workflow.zip")
	writeZip(t, zipPath, map[string]string{
		"1_START.md": "<goto>NEXT.md</goto>",
		"NEXT.md":    "<result>done</result>",
	})

	result, err := GenerateDiagram(zipPath, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	assert.Contains(t, m, "flowchart TD")
	assert.Contains(t, m, "1_START -->|goto| NEXT")
	assert.Contains(t, m, "-->|result|")
}

func TestResetWorkflowEdge(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<reset-workflow>../next_phase/</reset-workflow>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	assert.Contains(t, m, "-.->|reset-workflow|")
	assert.Contains(t, m, `[["../next_phase/"]]`)
}

func TestCallWorkflowEdge(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<call-workflow return="DONE.md">../sub_wf/</call-workflow>`)
	writeFile(t, dir, "DONE.md", `<result>complete</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	assert.Contains(t, m, "-->|call-workflow|")
	assert.Contains(t, m, "-.->|call-workflow return|")
	assert.Contains(t, m, `[["../sub_wf/"]]`)
}

func TestResultTracingThroughResetEdges(t *testing.T) {
	// Result tracing should follow reset edges too.
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<call return="AFTER.md">LOOP.md</call>`)
	writeFile(t, dir, "LOOP.md", `---
allowed_transitions:
  - { tag: reset, target: LOOP.md }
  - { tag: goto, target: FINAL.md }
---
Loop or finish.`)
	writeFile(t, dir, "FINAL.md", `<result>done</result>`)
	writeFile(t, dir, "AFTER.md", `<result>complete</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	// FINAL is reachable from LOOP via goto, and emits result.
	// Should have return edge from FINAL to AFTER with caller name.
	assert.Contains(t, m, "FINAL -.->|return #40;1_START#41;| AFTER")
}

func TestEmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	// Should produce a minimal diagram with no nodes.
	assert.Contains(t, result.Mermaid, "flowchart TD")
}

func TestHybridScriptAndMarkdown(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.sh", `#!/bin/bash
echo "<goto>PROCESS.md</goto>"`)
	writeFile(t, dir, "PROCESS.md", `Process data.
<goto>FINISH.sh</goto>`)
	writeFile(t, dir, "FINISH.sh", `#!/bin/bash
echo "<result>done</result>"`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	// Script nodes get hexagon shape.
	assert.Contains(t, m, `1_START{{"1_START"}}`)
	assert.Contains(t, m, `FINISH{{"FINISH"}}`)
	// Markdown node gets rectangle.
	assert.Contains(t, m, `PROCESS["PROCESS"]`)
}

func TestDedupEdges(t *testing.T) {
	dir := t.TempDir()
	// Body text with the same transition appearing twice.
	writeFile(t, dir, "1_START.md", `If A: <goto>NEXT.md</goto>
If B: <goto>NEXT.md</goto>`)
	writeFile(t, dir, "NEXT.md", `<result>done</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	// Edge should appear only once.
	count := strings.Count(m, "1_START -->|goto| NEXT")
	assert.Equal(t, 1, count, "duplicate edge should be deduplicated")
}

func TestFilterStateFiles(t *testing.T) {
	names := []string{
		"START.md", "POLL.sh", "BUILD.bat", "SCRIPT.ps1",
		"notes.txt", "image.png", "README.md", "readme.md",
	}

	t.Run("default (Unix) mode", func(t *testing.T) {
		filtered := filterStateFiles(names, Options{})
		assert.Equal(t, []string{"POLL.sh", "START.md"}, filtered)
	})

	t.Run("Windows mode", func(t *testing.T) {
		filtered := filterStateFiles(names, Options{WindowsMode: true})
		assert.Equal(t, []string{"BUILD.bat", "SCRIPT.ps1", "START.md"}, filtered)
	})
}

func TestSanitizeID(t *testing.T) {
	assert.Equal(t, "hello_world", sanitizeID("hello-world"))
	assert.Equal(t, "foo_bar_baz", sanitizeID("foo/bar.baz"))
	assert.Equal(t, "UPPER123", sanitizeID("UPPER123"))
}

func TestNormalizeTarget(t *testing.T) {
	assert.Equal(t, "NEXT", normalizeTarget("NEXT.md"))
	assert.Equal(t, "POLL", normalizeTarget("POLL.sh"))
	assert.Equal(t, "POLL", normalizeTarget("POLL.bat"))
	assert.Equal(t, "POLL", normalizeTarget("POLL"))
}

func TestBfsReachable(t *testing.T) {
	adj := map[string][]string{
		"A": {"B", "C"},
		"B": {"D"},
		"C": {"D"},
		"D": {},
	}
	reachable := bfsReachable("A", adj)
	assert.True(t, reachable["A"])
	assert.True(t, reachable["B"])
	assert.True(t, reachable["C"])
	assert.True(t, reachable["D"])
	assert.False(t, reachable["E"])
}

func TestEntryPointFilteredOutFallsBackToNone(t *testing.T) {
	// When the resolved entry file is excluded by the current mode's filter
	// (e.g. a .sh file when --win is active), findEntryPoint must not emit a
	// dangling __start__ edge pointing to a node that is absent from the diagram.
	dir := t.TempDir()
	writeFile(t, dir, "1_START.sh", `echo "<result>done</result>"`)
	writeFile(t, dir, "WIN_STEP.bat", `echo ^<result^>done^</result^>`)

	// Windows mode: .sh is filtered out, so 1_START.sh is not in nodes.
	// ResolveEntryPoint (platform-based) succeeds and returns 1_START.sh on
	// Unix, but that node is absent from the Windows-mode diagram.
	result, err := GenerateDiagram(dir, Options{WindowsMode: true})
	require.NoError(t, err)

	// The diagram must not contain a __start__ edge to a non-existent node.
	assert.NotContains(t, result.Mermaid, "__start__")
}

func TestShellEscapedQuotes(t *testing.T) {
	dir := t.TempDir()
	// Bash: \" escapes inside double-quoted echo
	writeFile(t, dir, "1_START.sh", `#!/bin/bash
echo "<function return=\"DONE.sh\" input=\"task1\">WORK.md</function>"`)
	writeFile(t, dir, "WORK.md", `<result>finished</result>`)
	writeFile(t, dir, "DONE.sh", `#!/bin/bash
echo "<result>all done</result>"`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	// Should parse the return attribute despite bash escaping.
	// Label includes [input] because input attribute is present (brackets escaped).
	assert.Contains(t, m, "1_START -->|function #91;input#93;| WORK")
	assert.NotContains(t, m, "function return")
	// Should trace result from WORK back to DONE with caller name.
	assert.Contains(t, m, "WORK -.->|return #40;1_START#41;| DONE")
}

func TestPowerShellEscapedQuotes(t *testing.T) {
	dir := t.TempDir()
	// PowerShell: `" escapes inside double-quoted strings
	writeFile(t, dir, "1_START.ps1", "Write-Output \"<function return=`\"DONE.md`\" input=`\"task1`\">WORK.md</function>\"")
	writeFile(t, dir, "WORK.md", `<result>finished</result>`)
	writeFile(t, dir, "DONE.md", `<result>all done</result>`)

	result, err := GenerateDiagram(dir, Options{WindowsMode: true})
	require.NoError(t, err)

	m := result.Mermaid
	assert.Contains(t, m, "1_START -->|function #91;input#93;| WORK")
	assert.NotContains(t, m, "function return")
	assert.Contains(t, m, "WORK -.->|return #40;1_START#41;| DONE")
}

func TestNestedCallResultTracing(t *testing.T) {
	// Simulate a workflow like bm_work where results must trace through
	// multiple call depths:
	//   Level 0: ENTRY → function → L1_START (return=L0_AFTER)
	//   Level 1: L1_START → function → L2_START (return=L1_AFTER)
	//   Level 2: L2_START → goto → L2_WORK → goto → L2_DONE → result
	//   Expected: L2_DONE returns to L1_AFTER, L1_AFTER returns to L0_AFTER
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<function return="L0_AFTER.md">L1_START.md</function>`)
	writeFile(t, dir, "L0_AFTER.md", `<result>workflow done</result>`)
	writeFile(t, dir, "L1_START.md", `<function return="L1_AFTER.md">L2_START.md</function>`)
	writeFile(t, dir, "L1_AFTER.md", `<result>level 1 done</result>`)
	writeFile(t, dir, "L2_START.md", `<goto>L2_WORK.md</goto>`)
	writeFile(t, dir, "L2_WORK.md", `<goto>L2_DONE.md</goto>`)
	writeFile(t, dir, "L2_DONE.md", `<result>level 2 done</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid

	// Level 2: L2_DONE emits result → returns to L1_AFTER, caller is L1_START.
	assert.Contains(t, m, "L2_DONE -.->|return #40;L1_START#41;| L1_AFTER")
	// L2_DONE is inside a call → no terminal node.
	assert.NotContains(t, m, "L2_DONE -->|result|")

	// Level 1: L1_AFTER emits result → returns to L0_AFTER, caller is 1_START.
	assert.Contains(t, m, "L1_AFTER -.->|return #40;1_START#41;| L0_AFTER")
	// L1_AFTER is inside a call → no terminal node.
	assert.NotContains(t, m, "L1_AFTER -->|result|")

	// Level 0: L0_AFTER emits result at top level → terminal node.
	assert.Contains(t, m, "L0_AFTER -->|result|")
}

func TestNestedCallWithGotoChainBeforeReturn(t *testing.T) {
	// The return state at level 1 does goto before emitting result.
	//   ENTRY → function → CALLEE (return=RETURN_STATE)
	//   CALLEE → call → INNER (return=AFTER_INNER)
	//   INNER → result
	//   AFTER_INNER → goto → FINAL → result
	// FINAL's result should return to RETURN_STATE.
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<function return="RETURN_STATE.md">CALLEE.md</function>`)
	writeFile(t, dir, "RETURN_STATE.md", `<result>done</result>`)
	writeFile(t, dir, "CALLEE.md", `<call return="AFTER_INNER.md">INNER.md</call>`)
	writeFile(t, dir, "INNER.md", `<result>inner done</result>`)
	writeFile(t, dir, "AFTER_INNER.md", `<goto>FINAL.md</goto>`)
	writeFile(t, dir, "FINAL.md", `<result>final</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid

	// INNER (level 2) returns to AFTER_INNER, caller is CALLEE.
	assert.Contains(t, m, "INNER -.->|return #40;CALLEE#41;| AFTER_INNER")
	// FINAL (level 1) returns to RETURN_STATE, caller is 1_START.
	assert.Contains(t, m, "FINAL -.->|return #40;1_START#41;| RETURN_STATE")
	// FINAL is inside a call → no terminal.
	assert.NotContains(t, m, "FINAL -->|result|")
	// RETURN_STATE is at level 0 → terminal.
	assert.Contains(t, m, "RETURN_STATE -->|result|")
}

func TestLevelAssignment(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<function return="AFTER.md">CHILD.md</function>`)
	writeFile(t, dir, "AFTER.md", `<result>done</result>`)
	writeFile(t, dir, "CHILD.md", `<goto>WORK.md</goto>`)
	writeFile(t, dir, "WORK.md", `<result>finished</result>`)

	// Test assignLevels directly.
	files, err := listStateFiles(dir, Options{})
	require.NoError(t, err)

	nodes := make(map[string]*nodeInfo)
	for _, f := range files {
		id := parsing.ExtractStateName(f)
		nodes[id] = &nodeInfo{id: id}
	}

	var edges []edge
	var cs []callSite
	for _, f := range files {
		id := parsing.ExtractStateName(f)
		transitions, _ := extractTransitions(dir, f)
		for _, tr := range transitions {
			newEdges, newCalls, _ := transitionToEdges(id, tr, nodes)
			edges = append(edges, newEdges...)
			cs = append(cs, newCalls...)
		}
	}

	adj := buildGotoResetAdj(edges)
	nodeLevel, maxLevel, warns := assignLevels("1_START", adj, cs)
	assert.Empty(t, warns)
	assert.Equal(t, 1, maxLevel)

	assert.Equal(t, 0, nodeLevel["1_START"])
	assert.Equal(t, 0, nodeLevel["AFTER"])
	assert.Equal(t, 1, nodeLevel["CHILD"])
	assert.Equal(t, 1, nodeLevel["WORK"])
}

func TestBadFrontmatterFallsBackToBody(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `---
invalid: yaml: [
---
Do stuff. <goto>NEXT.md</goto>`)
	writeFile(t, dir, "NEXT.md", `<result>done</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	// Should fall back to body parsing and find the goto.
	assert.Contains(t, result.Mermaid, "1_START -->|goto| NEXT")
}

func TestForkWorkflowWithInput(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "1_START.md", `<fork-workflow next="MONITOR.md" input="data">../worker/</fork-workflow>`)
	writeFile(t, dir, "MONITOR.md", `<result>done</result>`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	assert.Contains(t, m, "fork-workflow #91;input#93;")
}

func TestScriptTypeFilterDefaultMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", `<goto>WORK.sh</goto>`)
	writeFile(t, dir, "WORK.sh", `echo "<result>done</result>"`)
	writeFile(t, dir, "ALT.bat", `echo <result>done</result>`)
	writeFile(t, dir, "ALTPS.ps1", `Write-Output "<result>done</result>"`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	assert.Contains(t, m, "WORK")
	assert.Contains(t, m, "START")
	assert.NotContains(t, m, "ALT")
	assert.NotContains(t, m, "ALTPS")
}

func TestScriptTypeFilterWindowsMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "START.md", `<goto>WORK.bat</goto>`)
	writeFile(t, dir, "WORK.bat", `echo <goto>NEXT.ps1</goto>`)
	writeFile(t, dir, "NEXT.ps1", `Write-Output "<result>done</result>"`)
	writeFile(t, dir, "UNIX.sh", `echo "<result>done</result>"`)

	result, err := GenerateDiagram(dir, Options{WindowsMode: true})
	require.NoError(t, err)

	m := result.Mermaid
	assert.Contains(t, m, "WORK")
	assert.Contains(t, m, "NEXT")
	assert.Contains(t, m, "START")
	assert.NotContains(t, m, "UNIX")
}

func TestScriptTypeFilterOnlyBatPs1DefaultMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ONLY.bat", `echo <result>done</result>`)
	writeFile(t, dir, "ALSO.ps1", `Write-Output "<result>done</result>"`)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	m := result.Mermaid
	// No state files in default mode → minimal diagram with no state nodes.
	assert.Contains(t, m, "flowchart TD")
	assert.NotContains(t, m, "ONLY")
	assert.NotContains(t, m, "ALSO")
}

func TestGenerateDiagramFileContents(t *testing.T) {
	dir := t.TempDir()
	// START.md references MISSING.md which has no file, so MISSING will
	// appear in the diagram as a missing-reference node.
	mdContent := "# Hello\n<goto>MISSING.md</goto>\n<result>done</result>"
	shContent := `#!/bin/bash
echo "<result>done</result>"`
	writeFile(t, dir, "START.md", mdContent)
	writeFile(t, dir, "WORK.sh", shContent)

	result, err := GenerateDiagram(dir, Options{})
	require.NoError(t, err)

	// Only the two files that exist should be in FileContents.
	require.Len(t, result.FileContents, 2)

	startID := sanitizeID("START")
	workID := sanitizeID("WORK")

	startNode, ok := result.FileContents[startID]
	require.True(t, ok, "expected FileContents to have key %q", startID)
	assert.Equal(t, mdContent, startNode.Content)
	assert.True(t, startNode.IsMarkdown)

	workNode, ok := result.FileContents[workID]
	require.True(t, ok, "expected FileContents to have key %q", workID)
	assert.Equal(t, shContent, workNode.Content)
	assert.False(t, workNode.IsMarkdown)

	// MISSING is referenced in a transition but has no backing file —
	// it appears in the diagram as a missing node but not in FileContents.
	_, exists := result.FileContents[sanitizeID("MISSING")]
	assert.False(t, exists)
}
