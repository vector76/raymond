package manifest

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ExtractEmbeddedManifest parses a YAML scope file and returns its embedded
// manifest metadata. Returns (nil, nil) when the file is not a YAML scope
// (no "states" key) — callers should treat that as "not a workflow." Returns
// a non-nil error when the file declares the deprecated `id` field, has an
// invalid requires_human_input value, or is malformed YAML. Returns
// (*Manifest, nil) for any well-formed YAML scope; defaults are applied for
// omitted fields. The ID field on the returned Manifest is left empty — the
// registry derives the workflow id from the on-disk filename.
func ExtractEmbeddedManifest(data []byte) (*Manifest, error) {
	// Unmarshal into a raw map first so we can detect a present-but-deprecated
	// `id` key (struct unmarshaling would conflate "absent" with "present-empty").
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing embedded manifest: %w", err)
	}

	// Not a YAML workflow at all — leave standalone-manifest concerns to the
	// directory indexer.
	if _, hasStates := raw["states"]; !hasStates {
		return nil, nil
	}

	// Reject the deprecated `id` field. Workflow ids are derived from the
	// on-disk filename by the registry.
	if _, hasID := raw["id"]; hasID {
		return nil, fmt.Errorf("embedded manifest validation: the `id` field is no longer supported; the workflow id is derived from the filename. Remove the `id:` line.")
	}

	// Unmarshal the manifest fields from the same data. The Manifest struct has
	// no "states" field, so the states block is ignored automatically. Unknown
	// top-level keys are also ignored by yaml.v3 by default.
	m := &Manifest{}
	if err := yaml.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("parsing embedded manifest: %w", err)
	}

	// Default requires_human_input to "auto" when absent, matching standalone
	// manifest behavior.
	if m.RequiresHumanInput == "" {
		m.RequiresHumanInput = "auto"
	}
	if m.Input.Mode == "" {
		m.Input.Mode = InputModeOptional
	}

	if !validHumanInput[m.RequiresHumanInput] {
		return nil, fmt.Errorf("embedded manifest validation: requires_human_input must be one of auto, true, false; got %q", m.RequiresHumanInput)
	}

	if !validInputMode[m.Input.Mode] {
		return nil, fmt.Errorf("embedded manifest validation: input.mode must be one of required, optional, none; got %q", m.Input.Mode)
	}

	return m, nil
}
