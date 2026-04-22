package parsing_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vector76/raymond/internal/parsing"
)

// ----------------------------------------------------------------------------
// ParseTransitions
// ----------------------------------------------------------------------------

func TestParseGoto(t *testing.T) {
	output := "Some text here\n<goto>FILE.md</goto>"
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, "goto", transitions[0].Tag)
	assert.Equal(t, "FILE.md", transitions[0].Target)
	assert.Empty(t, transitions[0].Attributes)
	assert.Equal(t, "", transitions[0].Payload)
}

func TestParseReset(t *testing.T) {
	output := "Some text\n<reset>FILE.md</reset>"
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, "reset", transitions[0].Tag)
	assert.Equal(t, "FILE.md", transitions[0].Target)
	assert.Empty(t, transitions[0].Attributes)
	assert.Equal(t, "", transitions[0].Payload)
}

func TestParseResultWithPayload(t *testing.T) {
	output := "Work complete\n<result>Task finished successfully</result>"
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, "result", transitions[0].Tag)
	assert.Equal(t, "", transitions[0].Target)
	assert.Empty(t, transitions[0].Attributes)
	assert.Equal(t, "Task finished successfully", transitions[0].Payload)
}

func TestParseFunctionWithAttributes(t *testing.T) {
	output := `<function return="X.md">Y.md</function>`
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, "function", transitions[0].Tag)
	assert.Equal(t, "Y.md", transitions[0].Target)
	assert.Equal(t, map[string]string{"return": "X.md"}, transitions[0].Attributes)
	assert.Equal(t, "", transitions[0].Payload)
}

func TestParseCallWithAttributes(t *testing.T) {
	output := `<call return="X.md">Y.md</call>`
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, "call", transitions[0].Tag)
	assert.Equal(t, "Y.md", transitions[0].Target)
	assert.Equal(t, map[string]string{"return": "X.md"}, transitions[0].Attributes)
	assert.Equal(t, "", transitions[0].Payload)
}

func TestParseForkWithAttributes(t *testing.T) {
	output := `<fork next="X.md" item="foo">Y.md</fork>`
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, "fork", transitions[0].Tag)
	assert.Equal(t, "Y.md", transitions[0].Target)
	assert.Equal(t, map[string]string{"next": "X.md", "item": "foo"}, transitions[0].Attributes)
	assert.Equal(t, "", transitions[0].Payload)
}

func TestTagAnywhereInText(t *testing.T) {
	output := "Beginning\n<goto>MIDDLE.md</goto>\nEnd of text"
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, "goto", transitions[0].Tag)
	assert.Equal(t, "MIDDLE.md", transitions[0].Target)
}

func TestZeroTagsReturnsEmpty(t *testing.T) {
	output := "Just some text with no tags"
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	assert.Empty(t, transitions)
}

func TestMultipleTagsReturnsAll(t *testing.T) {
	output := "<goto>A.md</goto>\n<goto>B.md</goto>"
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	require.Len(t, transitions, 2)
	assert.Equal(t, "goto", transitions[0].Tag)
	assert.Equal(t, "A.md", transitions[0].Target)
	assert.Equal(t, "goto", transitions[1].Tag)
	assert.Equal(t, "B.md", transitions[1].Target)
}

func TestPathSafetyRejectsRelativePath(t *testing.T) {
	output := "<goto>../FILE.md</goto>"
	_, err := parsing.ParseTransitions(output)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains path separator")
}

func TestPathSafetyRejectsSubdirectory(t *testing.T) {
	output := "<goto>foo/bar.md</goto>"
	_, err := parsing.ParseTransitions(output)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains path separator")
}

func TestPathSafetyRejectsWindowsPath(t *testing.T) {
	output := `<goto>C:\FILE.md</goto>`
	_, err := parsing.ParseTransitions(output)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains path separator")
}

func TestPathSafetyRejectsForwardSlashInTarget(t *testing.T) {
	output := "<goto>path/to/file.md</goto>"
	_, err := parsing.ParseTransitions(output)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains path separator")
}

func TestPathSafetyAcceptsValidFilename(t *testing.T) {
	output := "<goto>FILE.md</goto>"
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	assert.Equal(t, "FILE.md", transitions[0].Target)
}

func TestPathSafetyAcceptsFilenameWithDots(t *testing.T) {
	output := "<goto>my.file.name.md</goto>"
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	assert.Equal(t, "my.file.name.md", transitions[0].Target)
}

func TestAttributesWithSingleQuotes(t *testing.T) {
	output := "<function return='X.md'>Y.md</function>"
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, map[string]string{"return": "X.md"}, transitions[0].Attributes)
}

func TestMultipleAttributes(t *testing.T) {
	output := `<fork next="X.md" item="foo" priority="high">Y.md</fork>`
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, map[string]string{"next": "X.md", "item": "foo", "priority": "high"}, transitions[0].Attributes)
}

func TestEmptyResultTag(t *testing.T) {
	output := "<result></result>"
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, "result", transitions[0].Tag)
	assert.Equal(t, "", transitions[0].Payload)
}

func TestResultWithMultilinePayload(t *testing.T) {
	output := "<result>Line 1\nLine 2\nLine 3</result>"
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, "Line 1\nLine 2\nLine 3", transitions[0].Payload)
}

func TestEmptyTargetRaisesForGoto(t *testing.T) {
	output := "<goto></goto>"
	_, err := parsing.ParseTransitions(output)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty target")
}

func TestEmptyTargetRaisesForReset(t *testing.T) {
	output := "<reset></reset>"
	_, err := parsing.ParseTransitions(output)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty target")
}

func TestEmptyTargetRaisesForFunction(t *testing.T) {
	output := `<function return="X.md"></function>`
	_, err := parsing.ParseTransitions(output)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty target")
}

func TestWhitespaceOnlyTargetRaises(t *testing.T) {
	output := "<goto>   </goto>"
	_, err := parsing.ParseTransitions(output)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty target")
}

func TestUnrecognizedTagsIgnored(t *testing.T) {
	// Tags like <div>, <span>, etc. should be silently ignored
	output := "<div>something</div><goto>FILE.md</goto>"
	transitions, err := parsing.ParseTransitions(output)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, "goto", transitions[0].Tag)
}

// ----------------------------------------------------------------------------
// ValidateSingleTransition
// ----------------------------------------------------------------------------

func TestValidateSingleTransitionPasses(t *testing.T) {
	transitions := []parsing.Transition{{Tag: "goto", Target: "FILE.md"}}
	err := parsing.ValidateSingleTransition(transitions)
	assert.NoError(t, err)
}

func TestValidateZeroTransitionsErrors(t *testing.T) {
	err := parsing.ValidateSingleTransition([]parsing.Transition{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected exactly one transition")
}

func TestValidateMultipleTransitionsErrors(t *testing.T) {
	transitions := []parsing.Transition{
		{Tag: "goto", Target: "A.md"},
		{Tag: "goto", Target: "B.md"},
	}
	err := parsing.ValidateSingleTransition(transitions)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected exactly one transition")
}

func TestParseForkWithEmptyAttribute(t *testing.T) {
	// An attribute with an empty quoted value is valid XML and should parse correctly.
	out := `<fork next="NEXT.md" item="">WORKER.md</fork>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	tr := transitions[0]
	assert.Equal(t, "fork", tr.Tag)
	assert.Equal(t, "WORKER.md", tr.Target)
	assert.Equal(t, "NEXT.md", tr.Attributes["next"])
	assert.Equal(t, "", tr.Attributes["item"])
}

// ----------------------------------------------------------------------------
// Cross-workflow tags: call-workflow, function-workflow, fork-workflow
// ----------------------------------------------------------------------------

func TestParseCallWorkflow(t *testing.T) {
	out := `<call-workflow return="RESUME.md" input="data.json">other-workflow/start.md</call-workflow>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	tr := transitions[0]
	assert.Equal(t, "call-workflow", tr.Tag)
	assert.Equal(t, "other-workflow/start.md", tr.Target)
	assert.Equal(t, "RESUME.md", tr.Attributes["return"])
	assert.Equal(t, "data.json", tr.Attributes["input"])
	assert.Empty(t, tr.Payload)
}

func TestParseFunctionWorkflow(t *testing.T) {
	out := `<function-workflow return="BACK.md" cwd="/tmp/mydir">project/init.md</function-workflow>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	tr := transitions[0]
	assert.Equal(t, "function-workflow", tr.Tag)
	assert.Equal(t, "project/init.md", tr.Target)
	assert.Equal(t, "BACK.md", tr.Attributes["return"])
	assert.Equal(t, "/tmp/mydir", tr.Attributes["cwd"])
	assert.Empty(t, tr.Payload)
}

func TestParseForkWorkflow(t *testing.T) {
	out := `<fork-workflow next="COLLECT.md" input="items.json">parallel/worker.md</fork-workflow>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	tr := transitions[0]
	assert.Equal(t, "fork-workflow", tr.Tag)
	assert.Equal(t, "parallel/worker.md", tr.Target)
	assert.Equal(t, "COLLECT.md", tr.Attributes["next"])
	assert.Equal(t, "items.json", tr.Attributes["input"])
	assert.Empty(t, tr.Payload)
}

func TestParseResetWorkflow(t *testing.T) {
	out := `<reset-workflow input="x" cd="/tmp">../other/</reset-workflow>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	tr := transitions[0]
	assert.Equal(t, "reset-workflow", tr.Tag)
	assert.Equal(t, "../other/", tr.Target)
	assert.Equal(t, "x", tr.Attributes["input"])
	assert.Equal(t, "/tmp", tr.Attributes["cd"])
	assert.Empty(t, tr.Payload)
}

func TestWorkflowTagAcceptsForwardSlash(t *testing.T) {
	// Path separators are valid in workflow specifiers.
	out := `<call-workflow return="R.md">some/nested/workflow.md</call-workflow>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, "some/nested/workflow.md", transitions[0].Target)
}

func TestWorkflowTagAcceptsBackslash(t *testing.T) {
	out := `<function-workflow return="R.md">some\nested\workflow.md</function-workflow>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, `some\nested\workflow.md`, transitions[0].Target)
}

func TestWorkflowTagAcceptsRelativePathSpecifier(t *testing.T) {
	out := `<fork-workflow next="N.md">../other/start.md</fork-workflow>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, "../other/start.md", transitions[0].Target)
}

// Regression: existing tags must still reject path separators.

func TestGotoStillRejectsPathSeparator(t *testing.T) {
	out := "<goto>subdir/file.md</goto>"
	_, err := parsing.ParseTransitions(out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains path separator")
}

func TestResetStillRejectsPathSeparator(t *testing.T) {
	out := "<reset>subdir/file.md</reset>"
	_, err := parsing.ParseTransitions(out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains path separator")
}

func TestCallStillRejectsPathSeparator(t *testing.T) {
	out := `<call return="R.md">subdir/file.md</call>`
	_, err := parsing.ParseTransitions(out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains path separator")
}

func TestFunctionStillRejectsPathSeparator(t *testing.T) {
	out := `<function return="R.md">subdir/file.md</function>`
	_, err := parsing.ParseTransitions(out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains path separator")
}

func TestForkStillRejectsPathSeparator(t *testing.T) {
	out := `<fork next="N.md">subdir/file.md</fork>`
	_, err := parsing.ParseTransitions(out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains path separator")
}

// ----------------------------------------------------------------------------
// Await tag
// ----------------------------------------------------------------------------

func TestParseAwaitBasic(t *testing.T) {
	out := `<await next="TARGET.md">prompt text</await>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	tr := transitions[0]
	assert.Equal(t, "await", tr.Tag)
	assert.Equal(t, "TARGET.md", tr.Target)
	assert.Equal(t, "prompt text", tr.Payload)
	assert.Equal(t, "TARGET.md", tr.Attributes["next"])
}

func TestParseAwaitAllAttributes(t *testing.T) {
	out := `<await next="A.md" timeout="24h" timeout_next="B.md">Please review the document.</await>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	tr := transitions[0]
	assert.Equal(t, "await", tr.Tag)
	assert.Equal(t, "A.md", tr.Target)
	assert.Equal(t, "Please review the document.", tr.Payload)
	assert.Equal(t, "A.md", tr.Attributes["next"])
	assert.Equal(t, "24h", tr.Attributes["timeout"])
	assert.Equal(t, "B.md", tr.Attributes["timeout_next"])
}

func TestParseAwaitMissingNextAttribute(t *testing.T) {
	out := `<await>prompt text</await>`
	_, err := parsing.ParseTransitions(out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a non-empty \"next\" attribute")
}

func TestParseAwaitEmptyNextAttribute(t *testing.T) {
	out := `<await next="">prompt text</await>`
	_, err := parsing.ParseTransitions(out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a non-empty \"next\" attribute")
}

func TestParseAwaitMultilinePrompt(t *testing.T) {
	out := "<await next=\"REVIEW.md\">Please review:\n\n- **Item 1**: Check formatting\n- **Item 2**: Verify links\n\nThanks!</await>"
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	tr := transitions[0]
	assert.Equal(t, "await", tr.Tag)
	assert.Equal(t, "REVIEW.md", tr.Target)
	assert.Equal(t, "Please review:\n\n- **Item 1**: Check formatting\n- **Item 2**: Verify links\n\nThanks!", tr.Payload)
}

func TestParseAwaitAlongsideOtherTransition(t *testing.T) {
	out := "<goto>NEXT.md</goto>\n<await next=\"WAIT.md\">Hold on</await>"
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 2)
	assert.Equal(t, "goto", transitions[0].Tag)
	assert.Equal(t, "NEXT.md", transitions[0].Target)
	assert.Equal(t, "await", transitions[1].Tag)
	assert.Equal(t, "WAIT.md", transitions[1].Target)
	assert.Equal(t, "Hold on", transitions[1].Payload)
}

func TestParseAwaitRejectsPathSeparatorInNext(t *testing.T) {
	out := `<await next="subdir/file.md">prompt</await>`
	_, err := parsing.ParseTransitions(out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains path separator")
}

func TestParseAwaitRejectsBackslashInNext(t *testing.T) {
	out := `<await next="sub\file.md">prompt</await>`
	_, err := parsing.ParseTransitions(out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains path separator")
}

func TestParseAwaitEmptyContent(t *testing.T) {
	out := `<await next="TARGET.md"></await>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	tr := transitions[0]
	assert.Equal(t, "await", tr.Tag)
	assert.Equal(t, "TARGET.md", tr.Target)
	assert.Equal(t, "", tr.Payload)
}
