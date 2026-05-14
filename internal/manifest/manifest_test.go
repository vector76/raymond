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
input:
  mode: required
  label: Query
  description: A search query to run
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
	assert.Equal(t, InputSpec{Mode: "required", Label: "Query", Description: "A search query to run"}, m.Input)
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
	// Absent input block defaults to optional mode with empty label/description.
	assert.Equal(t, InputSpec{Mode: "optional"}, m.Input)
	assert.Equal(t, 0.0, m.DefaultBudget)
	assert.Equal(t, "", m.WorkingDirectory)
	assert.Nil(t, m.Environment)
	assert.Equal(t, "auto", m.RequiresHumanInput)
}

func TestParseManifest_InputModeDefaultsToOptional(t *testing.T) {
	// input block present but mode omitted → defaults to optional.
	yaml := `
id: labeled
input:
  label: Topic
`
	path := writeTempManifest(t, yaml)
	m, err := ParseManifest(path)
	require.NoError(t, err)
	assert.Equal(t, InputSpec{Mode: "optional", Label: "Topic"}, m.Input)
}

func TestParseManifest_ValidInputModes(t *testing.T) {
	for _, mode := range []string{"required", "optional", "none"} {
		t.Run(mode, func(t *testing.T) {
			yaml := "id: test\ninput:\n  mode: " + mode + "\n"
			path := writeTempManifest(t, yaml)
			m, err := ParseManifest(path)
			require.NoError(t, err)
			assert.Equal(t, mode, m.Input.Mode)
		})
	}
}

func TestParseManifest_InvalidInputMode(t *testing.T) {
	yaml := `
id: bad-input-mode
input:
  mode: maybe
`
	path := writeTempManifest(t, yaml)
	_, err := ParseManifest(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "input.mode")
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

// TestParseManifestData_YAMLScope_ReturnsErrNotManifest guards the contract of
// ParseManifestData against the embedded-manifest work: adding
// ExtractEmbeddedManifest must not widen ParseManifestData to start accepting
// YAML scope files.
func TestParseManifestData_YAMLScope_ReturnsErrNotManifest(t *testing.T) {
	yaml := `
id: embedded-id
name: Would-Be Manifest
states:
  START:
    prompt: Hello
`
	m, err := ParseManifestData([]byte(yaml))
	require.Error(t, err)
	assert.Nil(t, m)
	assert.ErrorIs(t, err, ErrNotManifest)
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

// --- BackendSpec parsing ---

func TestParseManifestData_BackendAbsent(t *testing.T) {
	data := []byte("id: no-backend\n")
	m, err := ParseManifestData(data)
	require.NoError(t, err)
	assert.Equal(t, "", m.Backend.Name)
}

func TestParseManifestData_BackendBareString(t *testing.T) {
	data := []byte("id: pi-workflow\nbackend: pi\n")
	m, err := ParseManifestData(data)
	require.NoError(t, err)
	assert.Equal(t, "pi", m.Backend.Name)
}

func TestParseManifestData_BackendStructuredNameOnly(t *testing.T) {
	data := []byte("id: pi-workflow\nbackend:\n  name: pi\n")
	m, err := ParseManifestData(data)
	require.NoError(t, err)
	assert.Equal(t, "pi", m.Backend.Name)
}

func TestParseManifestData_BackendStructuredWithOptions(t *testing.T) {
	data := []byte(`id: pi-full
backend:
  name: pi
  options:
    provider: anthropic
    thinking: medium
    tools: [read, edit, write]
    no_builtin_tools: false
    no_tools: false
    no_extensions: true
    no_skills: false
    extensions:
      - my-ext
    skills:
      - ./skills/code-review
    session_dir: /tmp/sessions
`)
	m, err := ParseManifestData(data)
	require.NoError(t, err)
	assert.Equal(t, "pi", m.Backend.Name)
	assert.Equal(t, "anthropic", m.Backend.Options.Provider)
	assert.Equal(t, "medium", m.Backend.Options.Thinking)
	assert.Equal(t, []string{"read", "edit", "write"}, m.Backend.Options.Tools)
	assert.False(t, m.Backend.Options.NoBuiltinTools)
	assert.False(t, m.Backend.Options.NoTools)
	assert.True(t, m.Backend.Options.NoExtensions)
	assert.False(t, m.Backend.Options.NoSkills)
	assert.Equal(t, []string{"my-ext"}, m.Backend.Options.Extensions)
	assert.Equal(t, []string{"./skills/code-review"}, m.Backend.Options.Skills)
	assert.Equal(t, "/tmp/sessions", m.Backend.Options.SessionDir)
}

func TestParseManifestData_BackendUnknownName(t *testing.T) {
	data := []byte("id: bad-backend\nbackend: codex\n")
	_, err := ParseManifestData(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backend.name")
	assert.Contains(t, err.Error(), "codex")
}
