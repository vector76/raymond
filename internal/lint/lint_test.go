package lint_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vector76/raymond/internal/lint"
)

func fixtureDir(name string) string {
	return filepath.Join("..", "..", "workflows", "test_cases", "lint", name)
}

func TestNoEntryPoint(t *testing.T) {
	diags, err := lint.Lint(fixtureDir("missing_entry"), lint.Options{})
	require.NoError(t, err)

	for _, d := range diags {
		if d.Check == "no-entry-point" && d.Severity == lint.Error {
			return
		}
	}
	assert.Fail(t, "expected diagnostic with Check==\"no-entry-point\" and Severity==Error, got", diags)
}
