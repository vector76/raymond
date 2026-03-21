package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vector76/raymond/internal/lint"
)

func TestFormatLintText_Empty(t *testing.T) {
	got := formatLintText(nil, lint.Info)
	assert.Equal(t, "No issues found.\n", got)

	got = formatLintText([]lint.Diagnostic{}, lint.Info)
	assert.Equal(t, "No issues found.\n", got)
}

func TestFormatLintText_LevelFiltering(t *testing.T) {
	diags := []lint.Diagnostic{
		{Severity: lint.Error, Message: "bad thing", Check: "c1"},
		{Severity: lint.Warning, Message: "maybe bad", Check: "c2"},
		{Severity: lint.Info, Message: "fyi", Check: "c3"},
	}

	// Only errors (level=Error hides warnings and info from output)
	got := formatLintText(diags, lint.Error)
	assert.Contains(t, got, "error: bad thing\n")
	assert.NotContains(t, got, "warning:")
	assert.NotContains(t, got, "info:")
	// Summary still counts all
	assert.Contains(t, got, "1 error")
	assert.Contains(t, got, "1 warning")
	assert.Contains(t, got, "1 info")

	// Errors and warnings (level=Warning)
	got = formatLintText(diags, lint.Warning)
	assert.Contains(t, got, "error: bad thing\n")
	assert.Contains(t, got, "warning: maybe bad\n")
	assert.NotContains(t, got, "info:")

	// All (level=Info)
	got = formatLintText(diags, lint.Info)
	assert.Contains(t, got, "error: bad thing\n")
	assert.Contains(t, got, "warning: maybe bad\n")
	assert.Contains(t, got, "info: fyi\n")
}

func TestFormatLintText_SummaryOmitsZeroCounts(t *testing.T) {
	diags := []lint.Diagnostic{
		{Severity: lint.Error, Message: "e1"},
		{Severity: lint.Error, Message: "e2"},
		{Severity: lint.Info, Message: "i1"},
	}
	got := formatLintText(diags, lint.Info)
	// Warning count is zero, should be omitted
	assert.Contains(t, got, "2 errors")
	assert.NotContains(t, got, "warning")
	assert.Contains(t, got, "1 info")
	assert.True(t, got[len(got)-1] == '\n', "output should end with newline")
}

func TestFormatLintText_SingularPlural(t *testing.T) {
	diags := []lint.Diagnostic{
		{Severity: lint.Error, Message: "e1"},
		{Severity: lint.Warning, Message: "w1"},
		{Severity: lint.Warning, Message: "w2"},
	}
	got := formatLintText(diags, lint.Info)
	assert.Contains(t, got, "1 error")
	assert.NotContains(t, got, "1 errors")
	assert.Contains(t, got, "2 warnings")
}

func TestFormatLintJSON_Empty(t *testing.T) {
	result, err := formatLintJSON(nil, lint.Info)
	require.NoError(t, err)
	assert.Equal(t, "[]", result)

	result, err = formatLintJSON([]lint.Diagnostic{}, lint.Info)
	require.NoError(t, err)
	assert.Equal(t, "[]", result)
}

func TestFormatLintJSON_Structure(t *testing.T) {
	diags := []lint.Diagnostic{
		{Severity: lint.Error, File: "foo.md", Message: "bad", Check: "missing-target"},
		{Severity: lint.Warning, File: "bar.md", Message: "maybe", Check: "unreachable"},
	}

	result, err := formatLintJSON(diags, lint.Info)
	require.NoError(t, err)

	var got []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(result), &got))

	require.Len(t, got, 2)
	assert.Equal(t, "error", got[0]["severity"])
	assert.Equal(t, "foo.md", got[0]["file"])
	assert.Equal(t, "bad", got[0]["message"])
	assert.Equal(t, "missing-target", got[0]["check"])

	assert.Equal(t, "warning", got[1]["severity"])
	assert.Equal(t, "bar.md", got[1]["file"])
}

func TestFormatLintJSON_LevelFiltering(t *testing.T) {
	diags := []lint.Diagnostic{
		{Severity: lint.Error, File: "a.md", Message: "e", Check: "c1"},
		{Severity: lint.Warning, File: "b.md", Message: "w", Check: "c2"},
		{Severity: lint.Info, File: "c.md", Message: "i", Check: "c3"},
	}

	// level=Error: only errors included
	result, err := formatLintJSON(diags, lint.Error)
	require.NoError(t, err)
	var got []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(result), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "error", got[0]["severity"])

	// level=Warning: errors and warnings
	result, err = formatLintJSON(diags, lint.Warning)
	require.NoError(t, err)
	got = nil
	require.NoError(t, json.Unmarshal([]byte(result), &got))
	require.Len(t, got, 2)
}

func TestParseLintLevel(t *testing.T) {
	s, err := parseLintLevel("error")
	require.NoError(t, err)
	assert.Equal(t, lint.Error, s)

	s, err = parseLintLevel("warning")
	require.NoError(t, err)
	assert.Equal(t, lint.Warning, s)

	s, err = parseLintLevel("info")
	require.NoError(t, err)
	assert.Equal(t, lint.Info, s)

	_, err = parseLintLevel("debug")
	assert.Error(t, err)

	_, err = parseLintLevel("")
	assert.Error(t, err)
}
