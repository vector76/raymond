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
