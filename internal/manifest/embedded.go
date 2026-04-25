package manifest

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ExtractEmbeddedManifest parses a YAML scope file and returns its embedded
// manifest metadata, if any. Returns (nil, nil) when the file is not a
// discoverable YAML workflow (either no "states" key, or states present but
// no "id" field). Returns a non-nil error when a manifest is clearly intended
// but malformed (id present-but-empty, invalid requires_human_input,
// malformed YAML). Returns (*Manifest, nil) when a valid embedded manifest
// is present.
func ExtractEmbeddedManifest(data []byte) (*Manifest, error) {
	// Unmarshal into a raw map first so we can distinguish "key absent" from
	// "key present but empty" — struct unmarshaling would conflate these.
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing embedded manifest: %w", err)
	}

	// Not a YAML workflow at all — leave standalone-manifest concerns to the
	// directory indexer.
	if _, hasStates := raw["states"]; !hasStates {
		return nil, nil
	}

	// Valid YAML workflow not opting into daemon discovery.
	if _, hasID := raw["id"]; !hasID {
		return nil, nil
	}

	// Unmarshal the manifest fields from the same data. The Manifest struct has
	// no "states" field, so the states block is ignored automatically. Unknown
	// top-level keys are also ignored by yaml.v3 by default.
	m := &Manifest{}
	if err := yaml.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("parsing embedded manifest: %w", err)
	}

	// id key present but empty string (or null) → validation error.
	if m.ID == "" {
		return nil, fmt.Errorf("embedded manifest validation: id must be non-empty when present")
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
