package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTempManifest writes content to workflow.yaml in a temp dir and returns
// the file path.
func writeTempManifest(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// --- ParseManifest ---

func TestParseManifest_AllFields(t *testing.T) {
	yaml := `
id: my-workflow
name: My Workflow
description: A test workflow
input_schema:
  query: string
  verbose: bool
default_budget: 42.5
working_directory: /tmp/work
environment:
  FOO: bar
  BAZ: qux
requires_human_input: "true"
`
	path := writeTempManifest(t, yaml)
	m, err := ParseManifest(path)
	require.NoError(t, err)

	assert.Equal(t, "my-workflow", m.ID)
	assert.Equal(t, "My Workflow", m.Name)
	assert.Equal(t, "A test workflow", m.Description)
	assert.Equal(t, map[string]string{"query": "string", "verbose": "bool"}, m.InputSchema)
	assert.Equal(t, 42.5, m.DefaultBudget)
	assert.Equal(t, "/tmp/work", m.WorkingDirectory)
	assert.Equal(t, map[string]string{"FOO": "bar", "BAZ": "qux"}, m.Environment)
	assert.Equal(t, "true", m.RequiresHumanInput)
}

func TestParseManifest_MinimalIDOnly(t *testing.T) {
	yaml := `id: minimal`
	path := writeTempManifest(t, yaml)
	m, err := ParseManifest(path)
	require.NoError(t, err)

	assert.Equal(t, "minimal", m.ID)
	assert.Equal(t, "", m.Name)
	assert.Equal(t, "", m.Description)
	assert.Nil(t, m.InputSchema)
	assert.Equal(t, 0.0, m.DefaultBudget)
	assert.Equal(t, "", m.WorkingDirectory)
	assert.Nil(t, m.Environment)
	assert.Equal(t, "auto", m.RequiresHumanInput)
}

func TestParseManifest_MissingID(t *testing.T) {
	yaml := `
name: No ID Workflow
description: Missing the required id field
`
	path := writeTempManifest(t, yaml)
	_, err := ParseManifest(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id")
}

func TestParseManifest_InvalidRequiresHumanInput(t *testing.T) {
	yaml := `
id: bad-human-input
requires_human_input: maybe
`
	path := writeTempManifest(t, yaml)
	_, err := ParseManifest(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires_human_input")
}

func TestParseManifest_ValidRequiresHumanInputValues(t *testing.T) {
	for _, val := range []string{"auto", "true", "false"} {
		t.Run(val, func(t *testing.T) {
			yaml := "id: test\nrequires_human_input: " + val + "\n"
			path := writeTempManifest(t, yaml)
			m, err := ParseManifest(path)
			require.NoError(t, err)
			assert.Equal(t, val, m.RequiresHumanInput)
		})
	}
}

func TestParseManifest_YAMLScopeFile(t *testing.T) {
	yaml := `
states:
  START:
    prompt: Hello
initial_state: START
`
	path := writeTempManifest(t, yaml)
	_, err := ParseManifest(path)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotManifest)
}

func TestParseManifest_YAMLScopeStatesOnly(t *testing.T) {
	yaml := `
states:
  START:
    prompt: Hello
`
	path := writeTempManifest(t, yaml)
	_, err := ParseManifest(path)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotManifest)
}

func TestParseManifest_NonexistentFile(t *testing.T) {
	_, err := ParseManifest("/no/such/file/workflow.yaml")
	require.Error(t, err)
}

// --- FindManifest ---

func TestFindManifest_Exists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	require.NoError(t, os.WriteFile(path, []byte("id: test\n"), 0o644))

	found, ok := FindManifest(dir)
	assert.True(t, ok)
	assert.Equal(t, path, found)
}

func TestFindManifest_NotExists(t *testing.T) {
	dir := t.TempDir()
	found, ok := FindManifest(dir)
	assert.False(t, ok)
	assert.Equal(t, "", found)
}

// --- InterpolateEnv ---

func TestInterpolateEnv_ResolvesHostVar(t *testing.T) {
	t.Setenv("MANIFEST_TEST_VAR", "hello")
	env := map[string]string{
		"GREETING": "${MANIFEST_TEST_VAR}",
	}
	result := InterpolateEnv(env)
	assert.Equal(t, "hello", result["GREETING"])
}

func TestInterpolateEnv_MissingVarResolvesToEmpty(t *testing.T) {
	env := map[string]string{
		"MISSING": "${THIS_VAR_DEFINITELY_DOES_NOT_EXIST_12345}",
	}
	result := InterpolateEnv(env)
	assert.Equal(t, "", result["MISSING"])
}

func TestInterpolateEnv_LiteralPassedThrough(t *testing.T) {
	env := map[string]string{
		"LITERAL": "no-interpolation-here",
	}
	result := InterpolateEnv(env)
	assert.Equal(t, "no-interpolation-here", result["LITERAL"])
}

func TestInterpolateEnv_MixedContent(t *testing.T) {
	t.Setenv("MANIFEST_TEST_HOST", "localhost")
	env := map[string]string{
		"URL": "http://${MANIFEST_TEST_HOST}:8080/api",
	}
	result := InterpolateEnv(env)
	assert.Equal(t, "http://localhost:8080/api", result["URL"])
}

func TestInterpolateEnv_NilMap(t *testing.T) {
	result := InterpolateEnv(nil)
	assert.Nil(t, result)
}

func TestInterpolateEnv_EmptyMap(t *testing.T) {
	result := InterpolateEnv(map[string]string{})
	assert.NotNil(t, result)
	assert.Empty(t, result)
}
