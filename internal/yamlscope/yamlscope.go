// Package yamlscope provides access to workflow scopes stored as YAML files.
//
// A "yaml scope" is a .yaml or .yml file used in place of a directory to define
// all workflow states in a single file. The functions in this package mirror the
// directory- and zip-based equivalents used elsewhere (os.ReadFile, zipscope.ReadText)
// but operate on the YAML file's virtual file system.
//
// Detection: IsYamlScope returns true when the scope path ends in ".yaml" or
// ".yml" (case-insensitive). All other paths are treated as plain directories
// or zip archives.
//
// Virtual files: Each state defined in the YAML file maps to one or more virtual
// filenames. Markdown states (with a "prompt" key) produce "{name}.md". Script
// states (with "sh", "ps1", or "bat" keys) produce one virtual file per platform
// key present (e.g., "{name}.sh", "{name}.ps1").
package yamlscope

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// YamlParseError is returned when the YAML file contains syntax errors or
// cannot be read.
type YamlParseError struct {
	msg string
}

func (e *YamlParseError) Error() string { return e.msg }

// YamlValidationError is returned when the YAML file is syntactically valid but
// violates schema constraints (missing states key, empty states, dual-type
// states, etc.).
type YamlValidationError struct {
	msg string
}

func (e *YamlValidationError) Error() string { return e.msg }

// YamlFileNotFoundError is returned when a requested virtual filename does not
// correspond to any state in the YAML workflow.
type YamlFileNotFoundError struct {
	YamlPath string
	Filename string
}

func (e *YamlFileNotFoundError) Error() string {
	return fmt.Sprintf("file not found in yaml scope: %s (in %s)", e.Filename, e.YamlPath)
}

// scriptExtensions lists the platform keys recognized as script types, in the
// order they appear in virtual file listings.
var scriptExtensions = []string{"sh", "ps1", "bat"}

// yamlState represents a single state definition in the YAML file.
type yamlState struct {
	Prompt             string              `yaml:"prompt"`
	Sh                 string              `yaml:"sh"`
	Ps1                string              `yaml:"ps1"`
	Bat                string              `yaml:"bat"`
	AllowedTransitions []map[string]string `yaml:"allowed_transitions"`
	Model              string              `yaml:"model"`
	Effort             string              `yaml:"effort"`
}

// YamlWorkflow is the validated, parsed representation of a YAML workflow file.
type YamlWorkflow struct {
	// States maps state name to its definition, preserving the original
	// order via StateOrder.
	States     map[string]*yamlState
	StateOrder []string
}

// validateFilename rejects filenames that contain path separators, which would
// allow directory traversal outside the yaml scope.
func validateFilename(filename string) error {
	if strings.ContainsAny(filename, `/\`) {
		return fmt.Errorf("invalid filename (must not contain path separators): %q", filename)
	}
	return nil
}

// IsYamlScope reports whether scopeDir refers to a YAML workflow file (i.e.
// ends in ".yaml" or ".yml", case-insensitive).
func IsYamlScope(scopeDir string) bool {
	ext := strings.ToLower(filepath.Ext(scopeDir))
	return ext == ".yaml" || ext == ".yml"
}

// Parse reads and validates the YAML workflow file at yamlPath, returning a
// YamlWorkflow. Returns *YamlParseError for syntax errors and
// *YamlValidationError for schema violations.
func Parse(yamlPath string) (*YamlWorkflow, error) {
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, &YamlParseError{msg: fmt.Sprintf("failed to read yaml file: %s: %v", yamlPath, err)}
	}

	// First, check if the file has a "states" key at top level.
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, &YamlParseError{msg: fmt.Sprintf("invalid YAML syntax in %s: %v", yamlPath, err)}
	}

	if raw == nil {
		return nil, &YamlValidationError{msg: fmt.Sprintf("missing 'states' key in %s: file is empty", yamlPath)}
	}

	if _, ok := raw["states"]; !ok {
		// Check if the keys look like state definitions (maps with prompt/sh/etc.)
		// to provide a helpful error message.
		looksLikeStates := false
		for _, v := range raw {
			if m, ok := v.(map[string]interface{}); ok {
				if _, hasPrompt := m["prompt"]; hasPrompt {
					looksLikeStates = true
					break
				}
				for _, ext := range scriptExtensions {
					if _, has := m[ext]; has {
						looksLikeStates = true
						break
					}
				}
			}
			if looksLikeStates {
				break
			}
		}
		if looksLikeStates {
			return nil, &YamlValidationError{
				msg: fmt.Sprintf("missing 'states' key in %s: states appear to be defined at root level — wrap them under a 'states:' key", yamlPath),
			}
		}
		return nil, &YamlValidationError{msg: fmt.Sprintf("missing 'states' key in %s", yamlPath)}
	}

	// Parse with ordered state names. We need to preserve order, so we parse
	// the states map manually using yaml.v3's Node API.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, &YamlParseError{msg: fmt.Sprintf("invalid YAML syntax in %s: %v", yamlPath, err)}
	}

	// doc is a Document node; its first Content is the mapping node.
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, &YamlParseError{msg: fmt.Sprintf("unexpected YAML structure in %s", yamlPath)}
	}
	rootMap := doc.Content[0]
	if rootMap.Kind != yaml.MappingNode {
		return nil, &YamlParseError{msg: fmt.Sprintf("unexpected YAML structure in %s: root is not a mapping", yamlPath)}
	}

	// Find the "states" key in the root mapping.
	var statesNode *yaml.Node
	for i := 0; i < len(rootMap.Content)-1; i += 2 {
		if rootMap.Content[i].Value == "states" {
			statesNode = rootMap.Content[i+1]
			break
		}
	}

	if statesNode == nil || statesNode.Kind != yaml.MappingNode {
		return nil, &YamlValidationError{msg: fmt.Sprintf("'states' must be a mapping in %s", yamlPath)}
	}

	if len(statesNode.Content) == 0 {
		return nil, &YamlValidationError{msg: fmt.Sprintf("'states' must not be empty in %s", yamlPath)}
	}

	// Extract state names in order and unmarshal each state.
	workflow := &YamlWorkflow{
		States: make(map[string]*yamlState),
	}

	for i := 0; i < len(statesNode.Content)-1; i += 2 {
		keyNode := statesNode.Content[i]
		valNode := statesNode.Content[i+1]

		name := keyNode.Value

		// Validate state name.
		if name == "" {
			return nil, &YamlValidationError{msg: fmt.Sprintf("empty state name in %s", yamlPath)}
		}
		if strings.ContainsAny(name, `/\`) {
			return nil, &YamlValidationError{
				msg: fmt.Sprintf("state name must not contain path separators: %q in %s", name, yamlPath),
			}
		}
		if _, dup := workflow.States[name]; dup {
			return nil, &YamlValidationError{
				msg: fmt.Sprintf("duplicate state name %q in %s", name, yamlPath),
			}
		}

		var st yamlState
		if err := valNode.Decode(&st); err != nil {
			return nil, &YamlParseError{msg: fmt.Sprintf("failed to parse state %q in %s: %v", name, yamlPath, err)}
		}

		// Validate state type: must be markdown (prompt) XOR script (sh/ps1/bat).
		hasPrompt := st.Prompt != ""
		hasScript := st.Sh != "" || st.Ps1 != "" || st.Bat != ""

		if hasPrompt && hasScript {
			return nil, &YamlValidationError{
				msg: fmt.Sprintf("state %q in %s has both 'prompt' and script keys — must be one or the other", name, yamlPath),
			}
		}
		if !hasPrompt && !hasScript {
			return nil, &YamlValidationError{
				msg: fmt.Sprintf("state %q in %s has neither 'prompt' nor script keys (sh/ps1/bat) — must have one", name, yamlPath),
			}
		}

		// Script states must not have policy fields.
		if hasScript {
			if len(st.AllowedTransitions) > 0 {
				return nil, &YamlValidationError{
					msg: fmt.Sprintf("script state %q in %s must not have 'allowed_transitions'", name, yamlPath),
				}
			}
			if st.Model != "" {
				return nil, &YamlValidationError{
					msg: fmt.Sprintf("script state %q in %s must not have 'model'", name, yamlPath),
				}
			}
			if st.Effort != "" {
				return nil, &YamlValidationError{
					msg: fmt.Sprintf("script state %q in %s must not have 'effort'", name, yamlPath),
				}
			}
		}

		workflow.States[name] = &st
		workflow.StateOrder = append(workflow.StateOrder, name)
	}

	return workflow, nil
}

// virtualFiles returns the virtual filenames for a given state.
func virtualFiles(name string, st *yamlState) []string {
	if st.Prompt != "" {
		return []string{name + ".md"}
	}
	var files []string
	if st.Sh != "" {
		files = append(files, name+".sh")
	}
	if st.Ps1 != "" {
		files = append(files, name+".ps1")
	}
	if st.Bat != "" {
		files = append(files, name+".bat")
	}
	return files
}

// ListFiles parses the YAML workflow and returns virtual filenames for all
// states. Markdown states produce "{name}.md", script states produce one file
// per platform key present.
func ListFiles(yamlPath string) ([]string, error) {
	wf, err := Parse(yamlPath)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, name := range wf.StateOrder {
		st := wf.States[name]
		files = append(files, virtualFiles(name, st)...)
	}
	return files, nil
}

// synthesizeFrontmatter builds a YAML frontmatter string from a state's policy
// fields. Only includes fields that are present. Returns empty string if no
// policy fields are set.
func synthesizeFrontmatter(st *yamlState) string {
	if len(st.AllowedTransitions) == 0 && st.Model == "" && st.Effort == "" {
		return ""
	}

	// Build the frontmatter YAML manually to match the exact format expected
	// by policy.ParseFrontmatter (which uses gopkg.in/yaml.v3).
	var parts []string

	if len(st.AllowedTransitions) > 0 {
		// Marshal allowed_transitions using yaml.v3 for correct formatting.
		data, _ := yaml.Marshal(map[string]interface{}{
			"allowed_transitions": st.AllowedTransitions,
		})
		parts = append(parts, strings.TrimSpace(string(data)))
	}
	if st.Model != "" {
		data, _ := yaml.Marshal(map[string]interface{}{
			"model": st.Model,
		})
		parts = append(parts, strings.TrimSpace(string(data)))
	}
	if st.Effort != "" {
		data, _ := yaml.Marshal(map[string]interface{}{
			"effort": st.Effort,
		})
		parts = append(parts, strings.TrimSpace(string(data)))
	}

	return strings.Join(parts, "\n")
}

// ReadText reads a virtual file from the YAML workflow. For markdown states,
// synthesizes a complete markdown document with YAML frontmatter. For script
// states, returns the raw script text. Returns *YamlFileNotFoundError if the
// virtual filename doesn't match any state.
func ReadText(yamlPath, filename string) (string, error) {
	if err := validateFilename(filename); err != nil {
		return "", err
	}

	wf, err := Parse(yamlPath)
	if err != nil {
		return "", err
	}

	ext := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, ext)

	st, ok := wf.States[stem]
	if !ok {
		return "", &YamlFileNotFoundError{YamlPath: yamlPath, Filename: filename}
	}

	switch ext {
	case ".md":
		if st.Prompt == "" {
			return "", &YamlFileNotFoundError{YamlPath: yamlPath, Filename: filename}
		}
		fm := synthesizeFrontmatter(st)
		if fm != "" {
			return "---\n" + fm + "\n---\n" + st.Prompt, nil
		}
		return st.Prompt, nil

	case ".sh":
		if st.Sh == "" {
			return "", &YamlFileNotFoundError{YamlPath: yamlPath, Filename: filename}
		}
		return st.Sh, nil

	case ".ps1":
		if st.Ps1 == "" {
			return "", &YamlFileNotFoundError{YamlPath: yamlPath, Filename: filename}
		}
		return st.Ps1, nil

	case ".bat":
		if st.Bat == "" {
			return "", &YamlFileNotFoundError{YamlPath: yamlPath, Filename: filename}
		}
		return st.Bat, nil

	default:
		return "", &YamlFileNotFoundError{YamlPath: yamlPath, Filename: filename}
	}
}

// FileExists reports whether filename exists as a virtual file in the YAML
// workflow. Returns (false, nil) when the file is not present — matching
// zipscope's graceful convention for not-found. Returns error only for parse
// failures.
func FileExists(yamlPath, filename string) (bool, error) {
	if err := validateFilename(filename); err != nil {
		return false, err
	}

	files, err := ListFiles(yamlPath)
	if err != nil {
		return false, err
	}
	for _, f := range files {
		if f == filename {
			return true, nil
		}
	}
	return false, nil
}

// ExtractScript extracts a script virtual file to a temporary file and returns
// its path. The caller is responsible for removing the temp file when done.
// Returns an error if the filename doesn't correspond to a script state.
func ExtractScript(yamlPath, filename string) (string, error) {
	if err := validateFilename(filename); err != nil {
		return "", err
	}

	ext := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, ext)

	wf, err := Parse(yamlPath)
	if err != nil {
		return "", err
	}

	st, ok := wf.States[stem]
	if !ok {
		return "", &YamlFileNotFoundError{YamlPath: yamlPath, Filename: filename}
	}

	var content string
	switch ext {
	case ".sh":
		content = st.Sh
	case ".ps1":
		content = st.Ps1
	case ".bat":
		content = st.Bat
	default:
		// Not a script extension — includes .md
	}

	if content == "" {
		if st.Prompt != "" && ext == ".md" {
			return "", fmt.Errorf("cannot extract script from markdown state %q", stem)
		}
		return "", &YamlFileNotFoundError{YamlPath: yamlPath, Filename: filename}
	}

	tmp, err := os.CreateTemp("", "raymond-script-*"+ext)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file for script: %w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("failed to write temp script file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("failed to close temp script file: %w", err)
	}
	return tmp.Name(), nil
}
