package parsing_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vector76/raymond/internal/parsing"
)

// ----------------------------------------------------------------------------
// ParseFileAffordance — direct tests
// ----------------------------------------------------------------------------

func TestFileAffordance_TextOnly_NoAttributes(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, parsing.ModeTextOnly, fa.Mode)
	assert.Empty(t, fa.Slots)
	assert.Empty(t, fa.DisplayFiles)
	assert.Equal(t, parsing.BucketSpec{}, fa.Bucket)
}

func TestFileAffordance_TextOnly_OnlyUnrelatedAttrs(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"next":         "NEXT.md",
		"timeout":      "5m",
		"timeout_next": "TO.md",
	})
	require.NoError(t, err)
	assert.Equal(t, parsing.ModeTextOnly, fa.Mode)
}

// --- Slot mode ---

func TestFileAffordance_SlotMode_PlainNames(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots": "resume.pdf,cover.pdf",
	})
	require.NoError(t, err)
	assert.Equal(t, parsing.ModeSlot, fa.Mode)
	require.Len(t, fa.Slots, 2)
	assert.Equal(t, "resume.pdf", fa.Slots[0].Name)
	assert.Empty(t, fa.Slots[0].MIME)
	assert.Equal(t, "cover.pdf", fa.Slots[1].Name)
}

func TestFileAffordance_SlotMode_PerSlotMIME(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots": "resume.pdf:application/pdf,cover.pdf:application/pdf|text/plain",
	})
	require.NoError(t, err)
	assert.Equal(t, parsing.ModeSlot, fa.Mode)
	require.Len(t, fa.Slots, 2)
	assert.Equal(t, "resume.pdf", fa.Slots[0].Name)
	assert.Equal(t, []string{"application/pdf"}, fa.Slots[0].MIME)
	assert.Equal(t, "cover.pdf", fa.Slots[1].Name)
	assert.Equal(t, []string{"application/pdf", "text/plain"}, fa.Slots[1].MIME)
}

func TestFileAffordance_SlotMode_MIME_TrimmedAndLowercased(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots": "resume.pdf: Application/PDF | TEXT/Plain ",
	})
	require.NoError(t, err)
	require.Len(t, fa.Slots, 1)
	assert.Equal(t, []string{"application/pdf", "text/plain"}, fa.Slots[0].MIME)
}

func TestFileAffordance_SlotMode_TrimmedNames(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots": "  resume.pdf , cover.pdf  ",
	})
	require.NoError(t, err)
	require.Len(t, fa.Slots, 2)
	assert.Equal(t, "resume.pdf", fa.Slots[0].Name)
	assert.Equal(t, "cover.pdf", fa.Slots[1].Name)
}

func TestFileAffordance_SlotMode_RejectEmptyList(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots": "",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload_slots")
}

func TestFileAffordance_SlotMode_RejectEmptySlotName(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots": "resume.pdf,,cover.pdf",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty slot name")
}

func TestFileAffordance_SlotMode_RejectDuplicateSlotNames(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots": "resume.pdf,resume.pdf",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate slot name")
}

func TestFileAffordance_SlotMode_RejectPathSeparator(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots": "sub/resume.pdf",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path separator")

	_, err = parsing.ParseFileAffordance(map[string]string{
		"upload_slots": "sub\\resume.pdf",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path separator")
}

func TestFileAffordance_SlotMode_RejectLeadingDot(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots": ".hidden.pdf",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leading dot")
}

func TestFileAffordance_SlotMode_RejectControlChar(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots": "resume\x01.pdf",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "control")
}

func TestFileAffordance_SlotMode_RejectNullByte(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots": "resume\x00.pdf",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "null")
}

func TestFileAffordance_SlotMode_RejectEmptyMIMEEntry(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots": "resume.pdf:application/pdf||text/plain",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty MIME")
}

// --- Bucket mode ---

func TestFileAffordance_BucketMode_Minimal(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"upload_bucket": "true",
	})
	require.NoError(t, err)
	assert.Equal(t, parsing.ModeBucket, fa.Mode)
	assert.Equal(t, parsing.BucketSpec{}, fa.Bucket)
}

func TestFileAffordance_BucketMode_AllConstraints(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"upload_bucket":         "true",
		"upload_max_count":      "5",
		"upload_max_size":       "10485760",
		"upload_max_total_size": "52428800",
		"upload_mime":           "image/png, image/jpeg",
	})
	require.NoError(t, err)
	assert.Equal(t, parsing.ModeBucket, fa.Mode)
	assert.Equal(t, 5, fa.Bucket.MaxCount)
	assert.Equal(t, int64(10485760), fa.Bucket.MaxSizePerFile)
	assert.Equal(t, int64(52428800), fa.Bucket.MaxTotalSize)
	assert.Equal(t, []string{"image/png", "image/jpeg"}, fa.Bucket.MIME)
}

func TestFileAffordance_BucketMode_MIME_Lowercased(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"upload_bucket": "true",
		"upload_mime":   "Image/PNG,image/JPEG",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"image/png", "image/jpeg"}, fa.Bucket.MIME)
}

func TestFileAffordance_BucketMode_RejectInvalidBoolean(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_bucket": "yes",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload_bucket")
}

func TestFileAffordance_BucketMode_FalseIsTextOnly(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"upload_bucket": "false",
	})
	require.NoError(t, err)
	assert.Equal(t, parsing.ModeTextOnly, fa.Mode)
}

func TestFileAffordance_BucketMode_RejectZeroMaxCount(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_bucket":    "true",
		"upload_max_count": "0",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload_max_count")
}

func TestFileAffordance_BucketMode_RejectNegativeMaxSize(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_bucket":   "true",
		"upload_max_size": "-1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload_max_size")
}

func TestFileAffordance_BucketMode_RejectNonInteger(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_bucket":    "true",
		"upload_max_count": "abc",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload_max_count")
}

func TestFileAffordance_BucketMode_RejectEmptyMIMEEntry(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_bucket": "true",
		"upload_mime":   "image/png,,image/jpeg",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty MIME")
}

func TestFileAffordance_BucketConstraintsWithoutBucketAttribute(t *testing.T) {
	// Bucket sub-attrs without upload_bucket="true" should be rejected.
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_max_count": "5",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload_bucket")
}

// --- Mode conflicts ---

func TestFileAffordance_RejectSlotAndBucket(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots":  "resume.pdf",
		"upload_bucket": "true",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload_slots")
	assert.Contains(t, err.Error(), "upload_bucket")
}

// --- Display files ---

func TestFileAffordance_DisplayOnly(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"display_files": "report.pdf,chart.png",
	})
	require.NoError(t, err)
	assert.Equal(t, parsing.ModeDisplayOnly, fa.Mode)
	require.Len(t, fa.DisplayFiles, 2)
	assert.Equal(t, "report.pdf", fa.DisplayFiles[0].SourcePath)
	assert.Equal(t, "", fa.DisplayFiles[0].DisplayName)
	assert.Equal(t, "chart.png", fa.DisplayFiles[1].SourcePath)
}

func TestFileAffordance_DisplayWithLabels(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"display_files": "out/report.pdf:Final Report,chart.png:Q1 Chart",
	})
	require.NoError(t, err)
	require.Len(t, fa.DisplayFiles, 2)
	assert.Equal(t, "out/report.pdf", fa.DisplayFiles[0].SourcePath)
	assert.Equal(t, "Final Report", fa.DisplayFiles[0].DisplayName)
	assert.Equal(t, "chart.png", fa.DisplayFiles[1].SourcePath)
	assert.Equal(t, "Q1 Chart", fa.DisplayFiles[1].DisplayName)
}

func TestFileAffordance_DisplayAlongsideSlot(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"upload_slots":  "corrected.csv",
		"display_files": "original.csv:Original",
	})
	require.NoError(t, err)
	assert.Equal(t, parsing.ModeSlot, fa.Mode)
	require.Len(t, fa.Slots, 1)
	require.Len(t, fa.DisplayFiles, 1)
	assert.Equal(t, "original.csv", fa.DisplayFiles[0].SourcePath)
	assert.Equal(t, "Original", fa.DisplayFiles[0].DisplayName)
}

func TestFileAffordance_DisplayAlongsideBucket(t *testing.T) {
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"upload_bucket": "true",
		"display_files": "spec.pdf",
	})
	require.NoError(t, err)
	assert.Equal(t, parsing.ModeBucket, fa.Mode)
	require.Len(t, fa.DisplayFiles, 1)
}

func TestFileAffordance_Display_RejectAbsolutePath(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"display_files": "/etc/passwd:Important",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

func TestFileAffordance_Display_RejectDotDotSegment(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"display_files": "../secret.txt",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "..")

	_, err = parsing.ParseFileAffordance(map[string]string{
		"display_files": "out/../secret.txt",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "..")
}

func TestFileAffordance_Display_AllowsForwardSlashInSource(t *testing.T) {
	// Display source paths are relative paths within the task folder; slashes are OK.
	fa, err := parsing.ParseFileAffordance(map[string]string{
		"display_files": "out/report.pdf",
	})
	require.NoError(t, err)
	require.Len(t, fa.DisplayFiles, 1)
	assert.Equal(t, "out/report.pdf", fa.DisplayFiles[0].SourcePath)
}

func TestFileAffordance_Display_RejectEmptySource(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"display_files": "",
	})
	require.Error(t, err)

	_, err = parsing.ParseFileAffordance(map[string]string{
		"display_files": "a.pdf,,b.pdf",
	})
	require.Error(t, err)
}

func TestFileAffordance_Display_RejectControlCharInLabel(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"display_files": "report.pdf:bad\x01label",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "control")
}

func TestFileAffordance_Display_RejectNullByteInLabel(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"display_files": "report.pdf:bad\x00label",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "null")
}

func TestFileAffordance_Display_RejectPathSeparatorInLabel(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"display_files": "report.pdf:bad/label",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path separator")
}

func TestFileAffordance_Display_RejectLeadingDotInLabel(t *testing.T) {
	_, err := parsing.ParseFileAffordance(map[string]string{
		"display_files": "report.pdf:.hidden",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "leading dot")
}

// ----------------------------------------------------------------------------
// ParseTransitions integration — descriptor attached to <await>
// ----------------------------------------------------------------------------

func TestParseAwaitAttachesFileAffordance_TextOnly(t *testing.T) {
	out := `<await next="NEXT.md">prompt</await>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, parsing.ModeTextOnly, transitions[0].FileAffordance.Mode)
}

func TestParseAwaitAttachesFileAffordance_SlotMode(t *testing.T) {
	out := `<await next="NEXT.md" upload_slots="resume.pdf,cover.pdf">Upload your docs</await>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	fa := transitions[0].FileAffordance
	assert.Equal(t, parsing.ModeSlot, fa.Mode)
	require.Len(t, fa.Slots, 2)
	assert.Equal(t, "resume.pdf", fa.Slots[0].Name)
	assert.Equal(t, "cover.pdf", fa.Slots[1].Name)
}

func TestParseAwaitAttachesFileAffordance_BucketMode(t *testing.T) {
	out := `<await next="NEXT.md" upload_bucket="true" upload_max_count="3" upload_mime="image/png">Send images</await>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	fa := transitions[0].FileAffordance
	assert.Equal(t, parsing.ModeBucket, fa.Mode)
	assert.Equal(t, 3, fa.Bucket.MaxCount)
	assert.Equal(t, []string{"image/png"}, fa.Bucket.MIME)
}

func TestParseAwaitAttachesFileAffordance_DisplayOnly(t *testing.T) {
	out := `<await next="NEXT.md" display_files="out/report.pdf:Final Report">Review</await>`
	transitions, err := parsing.ParseTransitions(out)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	fa := transitions[0].FileAffordance
	assert.Equal(t, parsing.ModeDisplayOnly, fa.Mode)
	require.Len(t, fa.DisplayFiles, 1)
	assert.Equal(t, "out/report.pdf", fa.DisplayFiles[0].SourcePath)
	assert.Equal(t, "Final Report", fa.DisplayFiles[0].DisplayName)
}

func TestParseAwaitInvalidFileAffordanceReturnsError(t *testing.T) {
	out := `<await next="NEXT.md" upload_slots="resume.pdf,resume.pdf">prompt</await>`
	_, err := parsing.ParseTransitions(out)
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "duplicate") ||
			strings.Contains(err.Error(), "upload_slots"),
		"error %q should mention the offending attribute", err.Error())
}

func TestParseAwaitFileAffordanceErrorIncludesMode(t *testing.T) {
	// A parse-time obvious-bad display source path surfaces an error.
	out := `<await next="NEXT.md" display_files="/etc/passwd">x</await>`
	_, err := parsing.ParseTransitions(out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}
