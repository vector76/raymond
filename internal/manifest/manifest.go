// Package manifest handles parsing and validation of workflow manifest files
// (workflow.yaml). A manifest describes workflow metadata — distinct from YAML
// scope files which define states inline.
package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// ErrNotManifest is returned when a file looks like a YAML scope (has a
// "states" key) rather than a workflow manifest.
var ErrNotManifest = errors.New("file is a YAML scope, not a workflow manifest")

// manifestFileName is the expected name for manifest files.
const manifestFileName = "workflow.yaml"

// Manifest holds the parsed contents of a workflow manifest file.
type Manifest struct {
	ID                 string            `yaml:"id"`
	Name               string            `yaml:"name"`
	Description        string            `yaml:"description"`
	InputSchema        map[string]string `yaml:"input_schema"`
	DefaultBudget      float64           `yaml:"default_budget"`
	WorkingDirectory   string            `yaml:"working_directory"`
	Environment        map[string]string `yaml:"environment"`
	RequiresHumanInput string            `yaml:"requires_human_input"`
}

// rawManifest is used for initial unmarshalling so we can detect YAML scope
// files (which have a "states" key).
type rawManifest struct {
	States       interface{} `yaml:"states"`
	InitialState interface{} `yaml:"initial_state"`
	Manifest     `yaml:",inline"`
}

// validHumanInput lists the accepted values for RequiresHumanInput.
var validHumanInput = map[string]bool{
	"auto":  true,
	"true":  true,
	"false": true,
}

// interpolateRe matches ${VAR} patterns for environment variable interpolation.
var interpolateRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// ParseManifest reads a workflow.yaml file at path, validates it, and returns
// the parsed Manifest. It returns ErrNotManifest if the file looks like a YAML
// scope (contains "states").
func ParseManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseManifestData(data)
}

// ParseManifestData parses and validates a workflow manifest from raw YAML
// bytes. It returns ErrNotManifest if the data looks like a YAML scope
// (contains "states"). This is useful when manifest content is read from
// non-filesystem sources such as zip archives.
func ParseManifestData(data []byte) (*Manifest, error) {
	var raw rawManifest
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	// Detect YAML scope files: they have a "states" key.
	if raw.States != nil || raw.InitialState != nil {
		return nil, ErrNotManifest
	}

	m := &raw.Manifest

	// Apply defaults.
	if m.RequiresHumanInput == "" {
		m.RequiresHumanInput = "auto"
	}

	// Validate required fields.
	if m.ID == "" {
		return nil, fmt.Errorf("manifest validation: id is required")
	}

	// Validate requires_human_input.
	if !validHumanInput[m.RequiresHumanInput] {
		return nil, fmt.Errorf("manifest validation: requires_human_input must be one of auto, true, false; got %q", m.RequiresHumanInput)
	}

	return m, nil
}

// FindManifest checks whether a workflow.yaml file exists in dir. If found it
// returns the full path and true; otherwise it returns ("", false).
func FindManifest(dir string) (string, bool) {
	path := filepath.Join(dir, manifestFileName)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

// InterpolateEnv processes ${VAR} patterns in manifest environment values,
// resolving them against the host environment (os.Getenv). Missing variables
// resolve to the empty string. Returns a new map; the input is not modified.
// Returns nil when given a nil map.
func InterpolateEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	result := make(map[string]string, len(env))
	for k, v := range env {
		result[k] = interpolateRe.ReplaceAllStringFunc(v, func(match string) string {
			// Extract variable name from ${VAR}.
			name := match[2 : len(match)-1]
			return os.Getenv(name)
		})
	}
	return result
}
