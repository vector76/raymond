package parsing_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vector76/raymond/internal/parsing"
)

// ----------------------------------------------------------------------------
// ExtractPrintTags
// ----------------------------------------------------------------------------

func TestExtractPrintTagsSingleComplete(t *testing.T) {
	payloads, remainder := parsing.ExtractPrintTags("<print>hello world</print>")
	require.Len(t, payloads, 1)
	assert.Equal(t, "hello world", payloads[0])
	assert.Equal(t, "", remainder)
}

func TestExtractPrintTagsMultipleComplete(t *testing.T) {
	payloads, remainder := parsing.ExtractPrintTags("<print>first</print><print>second</print>")
	require.Len(t, payloads, 2)
	assert.Equal(t, "first", payloads[0])
	assert.Equal(t, "second", payloads[1])
	assert.Equal(t, "", remainder)
}

func TestExtractPrintTagsEmptyContent(t *testing.T) {
	payloads, remainder := parsing.ExtractPrintTags("<print></print>")
	require.Len(t, payloads, 1)
	assert.Equal(t, "", payloads[0])
	assert.Equal(t, "", remainder)
}

func TestExtractPrintTagsContentWithTrailingNewline(t *testing.T) {
	payloads, remainder := parsing.ExtractPrintTags("<print>hello\n</print>")
	require.Len(t, payloads, 1)
	assert.Equal(t, "hello\n", payloads[0])
	assert.Equal(t, "", remainder)
}

func TestExtractPrintTagsContentWithoutTrailingNewline(t *testing.T) {
	payloads, remainder := parsing.ExtractPrintTags("<print>hello</print>")
	require.Len(t, payloads, 1)
	assert.Equal(t, "hello", payloads[0])
	assert.Equal(t, "", remainder)
}

func TestExtractPrintTagsMultilineContent(t *testing.T) {
	input := "<print>line one\nline two\nline three</print>"
	payloads, remainder := parsing.ExtractPrintTags(input)
	require.Len(t, payloads, 1)
	assert.Equal(t, "line one\nline two\nline three", payloads[0])
	assert.Equal(t, "", remainder)
}

func TestExtractPrintTagsIgnoresTransitionTags(t *testing.T) {
	input := "text <goto>NEXT.md</goto> more <print>output</print> end"
	payloads, remainder := parsing.ExtractPrintTags(input)
	require.Len(t, payloads, 1)
	assert.Equal(t, "output", payloads[0])
	assert.Equal(t, "", remainder)
}

func TestExtractPrintTagsInterleavedWithPlainText(t *testing.T) {
	input := "intro <print>first</print> middle <print>second</print> tail"
	payloads, remainder := parsing.ExtractPrintTags(input)
	require.Len(t, payloads, 2)
	assert.Equal(t, "first", payloads[0])
	assert.Equal(t, "second", payloads[1])
	assert.Equal(t, "", remainder)
}

func TestExtractPrintTagsNoTags(t *testing.T) {
	payloads, remainder := parsing.ExtractPrintTags("plain text with no tags")
	assert.Empty(t, payloads)
	assert.Equal(t, "", remainder)
}

func TestExtractPrintTagsEmptyInput(t *testing.T) {
	payloads, remainder := parsing.ExtractPrintTags("")
	assert.Empty(t, payloads)
	assert.Equal(t, "", remainder)
}

func TestExtractPrintTagsIncompleteTagAtEnd(t *testing.T) {
	payloads, remainder := parsing.ExtractPrintTags("before <print>incomplete")
	assert.Empty(t, payloads)
	assert.Equal(t, "<print>incomplete", remainder)
}

func TestExtractPrintTagsCompleteBeforeIncomplete(t *testing.T) {
	input := "<print>done</print> text <print>incomplete"
	payloads, remainder := parsing.ExtractPrintTags(input)
	require.Len(t, payloads, 1)
	assert.Equal(t, "done", payloads[0])
	assert.Equal(t, "<print>incomplete", remainder)
}

func TestExtractPrintTagsPartialOpenerAtEnd(t *testing.T) {
	payloads, remainder := parsing.ExtractPrintTags("text <pri")
	assert.Empty(t, payloads)
	assert.Equal(t, "<pri", remainder)
}

func TestExtractPrintTagsPartialOpenerSingleChar(t *testing.T) {
	payloads, remainder := parsing.ExtractPrintTags("text <")
	assert.Empty(t, payloads)
	assert.Equal(t, "<", remainder)
}

func TestExtractPrintTagsPartialOpenerFullNameMissingBracket(t *testing.T) {
	// "<print" without ">" is still a partial opener.
	payloads, remainder := parsing.ExtractPrintTags("text <print")
	assert.Empty(t, payloads)
	assert.Equal(t, "<print", remainder)
}

func TestExtractPrintTagsIncrementalSplitInsideContent(t *testing.T) {
	// Chunk 1 ends mid-content after the open tag.
	payloads1, remainder1 := parsing.ExtractPrintTags("some text <print>hel")
	assert.Empty(t, payloads1)
	assert.Equal(t, "<print>hel", remainder1)

	// Chunk 2 completes the tag.
	payloads2, remainder2 := parsing.ExtractPrintTags(remainder1 + "lo</print> done")
	require.Len(t, payloads2, 1)
	assert.Equal(t, "hello", payloads2[0])
	assert.Equal(t, "", remainder2)
}

func TestExtractPrintTagsIncrementalSplitMidOpeningTag(t *testing.T) {
	// Chunk 1 ends mid-opening-tag (e.g. "<pri").
	payloads1, remainder1 := parsing.ExtractPrintTags("text <pri")
	assert.Empty(t, payloads1)
	assert.Equal(t, "<pri", remainder1)

	// Chunk 2 finishes the opening tag and provides the full content.
	payloads2, remainder2 := parsing.ExtractPrintTags(remainder1 + "nt>content</print>")
	require.Len(t, payloads2, 1)
	assert.Equal(t, "content", payloads2[0])
	assert.Equal(t, "", remainder2)
}

func TestExtractPrintTagsIncrementalMultipleChunks(t *testing.T) {
	// Simulate three successive chunks, each leaving a remainder.
	chunk1 := "start <print>val"
	payloads1, rem1 := parsing.ExtractPrintTags(chunk1)
	assert.Empty(t, payloads1)

	chunk2 := rem1 + "ue</print> inter"
	payloads2, rem2 := parsing.ExtractPrintTags(chunk2)
	require.Len(t, payloads2, 1)
	assert.Equal(t, "value", payloads2[0])

	chunk3 := rem2 + "lude <print>next</print>"
	payloads3, rem3 := parsing.ExtractPrintTags(chunk3)
	require.Len(t, payloads3, 1)
	assert.Equal(t, "next", payloads3[0])
	assert.Equal(t, "", rem3)
}
