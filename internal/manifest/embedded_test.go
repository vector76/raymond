package manifest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ExtractEmbeddedManifest ---

func TestExtractEmbeddedManifest_FullManifestAlongsideStates(t *testing.T) {
	yaml := `
id: my-workflow
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
	assert.Equal(t, "my-workflow", m.ID)
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
id: bad-input-mode
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
id: default-mode
states:
  START:
    prompt: Hello
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, "optional", m.Input.Mode)
}

func TestExtractEmbeddedManifest_StatesWithoutID_ReturnsNilNil(t *testing.T) {
	yaml := `
states:
  START:
    prompt: Hello
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.NoError(t, err)
	assert.Nil(t, m)
}

func TestExtractEmbeddedManifest_NoStatesKey_ReturnsNilNil(t *testing.T) {
	yaml := `
id: standalone
name: Standalone Manifest
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.NoError(t, err)
	assert.Nil(t, m)
}

func TestExtractEmbeddedManifest_IDPresentButEmpty_ReturnsError(t *testing.T) {
	yaml := `
id: ""
states:
  START:
    prompt: Hello
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.Error(t, err)
	assert.Nil(t, m)
	assert.Contains(t, err.Error(), "id")
}

func TestExtractEmbeddedManifest_IDPresentButNull_ReturnsError(t *testing.T) {
	// `id:` with no value parses to nil, which should be distinguishable from
	// the absent-key case and treated as an empty-id validation error.
	yaml := `
id:
states:
  START:
    prompt: Hello
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.Error(t, err)
	assert.Nil(t, m)
	assert.Contains(t, err.Error(), "id")
}

func TestExtractEmbeddedManifest_InvalidRequiresHumanInput_ReturnsError(t *testing.T) {
	yaml := `
id: bad-human-input
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
id: defaulted
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
			yaml := "id: test\nrequires_human_input: " + val + "\nstates:\n  START:\n    prompt: Hello\n"
			m, err := ExtractEmbeddedManifest([]byte(yaml))
			require.NoError(t, err)
			require.NotNil(t, m)
			assert.Equal(t, val, m.RequiresHumanInput)
		})
	}
}

func TestExtractEmbeddedManifest_MalformedYAML_ReturnsError(t *testing.T) {
	yaml := `
id: broken
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
id: extras
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
	assert.Equal(t, "extras", m.ID)
	assert.Equal(t, "Has Extras", m.Name)
}

func TestExtractEmbeddedManifest_MinimalValid(t *testing.T) {
	yaml := `
id: minimal
states:
  START:
    prompt: Hello
`
	m, err := ExtractEmbeddedManifest([]byte(yaml))
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, "minimal", m.ID)
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

// TestExtractEmbeddedManifest_DistinguishesIDAbsentFromIDEmpty asserts that
// "id key absent" and "id key present but empty" are observably different
// return values. A future refactor that collapsed the two cases (e.g. by
// struct-only unmarshaling) would regress here.
func TestExtractEmbeddedManifest_DistinguishesIDAbsentFromIDEmpty(t *testing.T) {
	absentYAML := `
states:
  START:
    prompt: Hello
`
	emptyYAML := `
id: ""
states:
  START:
    prompt: Hello
`

	mAbsent, errAbsent := ExtractEmbeddedManifest([]byte(absentYAML))
	mEmpty, errEmpty := ExtractEmbeddedManifest([]byte(emptyYAML))

	// Absent: no error, nil manifest (opt-out signal).
	require.NoError(t, errAbsent)
	assert.Nil(t, mAbsent)

	// Empty: non-nil error, nil manifest (validation failure).
	require.Error(t, errEmpty)
	assert.Nil(t, mEmpty)

	// Observable distinction: the error outcomes differ.
	assert.NotEqual(t, errAbsent == nil, errEmpty == nil,
		"id-absent and id-empty must produce observably different error results")
}
