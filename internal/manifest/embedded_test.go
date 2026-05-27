package manifest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ExtractEmbeddedManifest ---

func TestExtractEmbeddedManifest_FullManifestAlongsideStates(t *testing.T) {
	yaml := `
name: My Workflow
description: A test workflow
input:
  mode: required
  label: Query
  description: A search query
default_budget: 2.5
working_directory: /tmp/work
environment:
  FOO: bar
requires_human_input: "true"

states:
  START:
    prompt: Hello
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.NoError(t, err)
	require.NotNil(t, m)
	// ID is not parsed from YAML; the registry derives it from the on-disk
	// filename.
	assert.Equal(t, "", m.ID)
	assert.Equal(t, "My Workflow", m.Name)
	assert.Equal(t, "A test workflow", m.Description)
	assert.Equal(t, InputSpec{Mode: "required", Label: "Query", Description: "A search query"}, m.Input)
	assert.Equal(t, 2.5, m.DefaultBudget)
	assert.Equal(t, "/tmp/work", m.WorkingDirectory)
	assert.Equal(t, map[string]string{"FOO": "bar"}, m.Environment)
	assert.Equal(t, "true", m.RequiresHumanInput)
}

func TestExtractEmbeddedManifest_InvalidInputMode_ReturnsError(t *testing.T) {
	yaml := `
input:
  mode: maybe
states:
  START:
    prompt: Hello
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.Error(t, err)
	assert.Nil(t, m)
	assert.Contains(t, err.Error(), "input.mode")
}

func TestExtractEmbeddedManifest_InputModeDefaultsToOptional(t *testing.T) {
	yaml := `
states:
  START:
    prompt: Hello
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, "optional", m.Input.Mode)
}

// TestExtractEmbeddedManifest_StatesOnly_ReturnsManifest verifies that every
// well-formed yaml-scope file is registrable. Previously the manifest was
// returned only when an explicit `id:` field was present; now the registry
// derives the id from the filename so any states-bearing file qualifies.
func TestExtractEmbeddedManifest_StatesOnly_ReturnsManifest(t *testing.T) {
	yaml := `
states:
  START:
    prompt: Hello
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, "", m.ID)
	assert.Equal(t, "", m.Name)
	assert.Equal(t, "optional", m.Input.Mode)
	assert.Equal(t, "auto", m.RequiresHumanInput)
}

func TestExtractEmbeddedManifest_NoStatesKey_ReturnsNilNil(t *testing.T) {
	yaml := `
name: Standalone Manifest
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.NoError(t, err)
	assert.Nil(t, m)
}

// TestExtractEmbeddedManifest_RejectsExplicitID covers every shape of the
// deprecated `id:` field. Each must hard-reject, regardless of value.
func TestExtractEmbeddedManifest_RejectsExplicitID(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"non-empty", "id: my-workflow\nstates:\n  START:\n    prompt: Hello\n"},
		{"empty-string", `id: ""` + "\nstates:\n  START:\n    prompt: Hello\n"},
		{"null", "id:\nstates:\n  START:\n    prompt: Hello\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := ExtractEmbeddedManifest([]byte(tc.yaml))
			require.Error(t, err)
			assert.Nil(t, m)
			assert.Contains(t, err.Error(), "`id` field is no longer supported")
		})
	}
}

func TestExtractEmbeddedManifest_InvalidRequiresHumanInput_ReturnsError(t *testing.T) {
	yaml := `
requires_human_input: maybe
states:
  START:
    prompt: Hello
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.Error(t, err)
	assert.Nil(t, m)
	assert.Contains(t, err.Error(), "requires_human_input")
}

func TestExtractEmbeddedManifest_DefaultsRequiresHumanInputToAuto(t *testing.T) {
	yaml := `
states:
  START:
    prompt: Hello
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, "auto", m.RequiresHumanInput)
}

func TestExtractEmbeddedManifest_ValidRequiresHumanInputValues(t *testing.T) {
	for _, val := range []string{"auto", "true", "false"} {
		t.Run(val, func(t *testing.T) {
			yaml := "requires_human_input: " + val + "\nstates:\n  START:\n    prompt: Hello\n"
			m, err := ExtractEmbeddedManifest([]byte(yaml))
			require.NoError(t, err)
			require.NotNil(t, m)
			assert.Equal(t, val, m.RequiresHumanInput)
		})
	}
}

func TestExtractEmbeddedManifest_MalformedYAML_ReturnsError(t *testing.T) {
	yaml := `
states:
  START:
    prompt: "unterminated
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.Error(t, err)
	assert.Nil(t, m)
}

func TestExtractEmbeddedManifest_UnknownTopLevelKeysIgnored(t *testing.T) {
	yaml := `
name: Has Extras
some_future_field: ignored
author: anonymous
states:
  START:
    prompt: Hello
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, "Has Extras", m.Name)
}

func TestExtractEmbeddedManifest_MinimalValid(t *testing.T) {
	yaml := `
states:
  START:
    prompt: Hello
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, "", m.ID)
	assert.Equal(t, "", m.Name)
	assert.Equal(t, "", m.Description)
	assert.Equal(t, InputSpec{Mode: "optional"}, m.Input)
	assert.Equal(t, 0.0, m.DefaultBudget)
	assert.Equal(t, "", m.WorkingDirectory)
	assert.Nil(t, m.Environment)
	assert.Equal(t, "auto", m.RequiresHumanInput)
}

func TestExtractEmbeddedManifest_EmptyData_ReturnsNilNil(t *testing.T) {
	m, err := ExtractEmbeddedManifest(nil)
	require.NoError(t, err)
	assert.Nil(t, m)
}
