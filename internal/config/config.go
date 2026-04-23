// Package config handles loading and validation of per-project configuration
// files from .raymond/config.toml. Configuration files are discovered by
// searching upward from the current working directory until a .git directory
// is found (the project boundary).
//
// Config file location: <project-root>/.raymond/config.toml
// TOML section: [raymond]
//
// Known config keys:
//
//	budget                      float64  positive
//	dangerously_skip_permissions bool
//	effort                      string   "low"|"medium"|"high"
//	model                       string   "opus"|"sonnet"|"haiku"
//	name                        string   any value
//	timeout                     float64  non-negative
//	no_debug                    bool
//	no_wait                     bool
//	verbose                     bool
//
// Unknown keys are silently ignored for forward compatibility.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// ConfigError is returned when configuration file operations fail.
type ConfigError struct {
	msg string
}

func (e *ConfigError) Error() string { return e.msg }

// CLIArgs holds values parsed from command-line flags.
// Pointer fields are nil when the flag was not provided on the command line.
type CLIArgs struct {
	Budget                     *float64 // nil if not specified
	DangerouslySkipPermissions bool
	Effort                     string   // "" if not specified
	Model                      string   // "" if not specified
	Name                       string   // "" if not specified
	Timeout                    *float64 // nil if not specified
	NoDebug                    bool
	NoWait                     bool
	Verbose                    bool
	TaskFolderPattern          string // "" if not specified; config-file-only
}

// FindProjectRoot returns the directory containing .git, searching upward from
// cwd. Returns the absolute form of cwd if no .git directory is found.
func FindProjectRoot(cwd string) string {
	current, err := filepath.Abs(cwd)
	if err != nil {
		return cwd
	}

	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			// Reached filesystem root.
			break
		}
		current = parent
	}

	abs, err := filepath.Abs(cwd)
	if err != nil {
		return cwd
	}
	return abs
}

// FindRaymondDir searches upward from cwd for a .raymond directory, stopping
// when a .git directory is encountered (project boundary) or the filesystem
// root is reached.
//
// If createIfMissing is true and no .raymond directory exists, one is created
// at the project root (or at cwd if no .git is found).
//
// Returns ("", nil) when not found and createIfMissing is false.
// Returns ("", error) when directory creation fails.
func FindRaymondDir(cwd string, createIfMissing bool) (string, error) {
	current, err := filepath.Abs(cwd)
	if err != nil {
		return "", &ConfigError{msg: fmt.Sprintf("failed to resolve path %q: %v", cwd, err)}
	}

	var projectRoot string

	for {
		raymondDir := filepath.Join(current, ".raymond")
		if info, err := os.Stat(raymondDir); err == nil && info.IsDir() {
			return raymondDir, nil
		}

		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			projectRoot = current
			break
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	if !createIfMissing {
		return "", nil
	}

	targetDir := projectRoot
	if targetDir == "" {
		targetDir, err = filepath.Abs(cwd)
		if err != nil {
			return "", &ConfigError{msg: fmt.Sprintf("failed to resolve path %q: %v", cwd, err)}
		}
	}

	raymondDir := filepath.Join(targetDir, ".raymond")
	if err := os.MkdirAll(raymondDir, 0o755); err != nil {
		return "", &ConfigError{msg: fmt.Sprintf("failed to create .raymond directory: %v", err)}
	}
	return raymondDir, nil
}

// FindConfigFile returns the path to .raymond/config.toml found by searching
// upward from cwd. Returns "" if not found.
func FindConfigFile(cwd string) string {
	raymondDir, err := FindRaymondDir(cwd, false)
	if err != nil || raymondDir == "" {
		return ""
	}

	configFile := filepath.Join(raymondDir, "config.toml")
	if info, err := os.Stat(configFile); err == nil && !info.IsDir() {
		return configFile
	}
	return ""
}

// knownKeys is the set of recognized configuration keys in the [raymond] section.
var knownKeys = map[string]bool{
	"budget":                      true,
	"dangerously_skip_permissions": true,
	"effort":                      true,
	"model":                       true,
	"name":                        true,
	"timeout":                     true,
	"no_debug":                    true,
	"no_wait":                     true,
	"verbose":                     true,
	"task_folder_pattern":         true,
}

// ValidateConfig validates the configuration values in config, filters out
// unknown keys, and normalizes numeric types to float64. configFile is used
// only in error messages.
//
// Returns a new map containing only the valid, known keys.
func ValidateConfig(config map[string]any, configFile string) (map[string]any, error) {
	// Filter to known keys only (forward compatibility).
	validated := make(map[string]any, len(config))
	for k, v := range config {
		if knownKeys[k] {
			validated[k] = v
		}
	}

	// budget: must be a number and positive.
	if v, ok := validated["budget"]; ok {
		f, err := toFloat64(v)
		if err != nil {
			return nil, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'budget' in %s: expected number, got %T", configFile, v,
			)}
		}
		if f <= 0 {
			return nil, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'budget' in %s: must be positive, got %v", configFile, f,
			)}
		}
		validated["budget"] = f
	}

	// timeout: must be a number and non-negative.
	if v, ok := validated["timeout"]; ok {
		f, err := toFloat64(v)
		if err != nil {
			return nil, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'timeout' in %s: expected number, got %T", configFile, v,
			)}
		}
		if f < 0 {
			return nil, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'timeout' in %s: must be non-negative, got %v", configFile, f,
			)}
		}
		validated["timeout"] = f
	}

	// Boolean flags.
	for _, flag := range []string{"dangerously_skip_permissions", "no_debug", "no_wait", "verbose"} {
		if v, ok := validated[flag]; ok {
			if _, isBool := v.(bool); !isBool {
				return nil, &ConfigError{msg: fmt.Sprintf(
					"Invalid value for %q in %s: expected boolean, got %T", flag, configFile, v,
				)}
			}
		}
	}

	// model: must be a string and one of the allowed values.
	if v, ok := validated["model"]; ok {
		s, isStr := v.(string)
		if !isStr {
			return nil, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'model' in %s: expected string, got %T", configFile, v,
			)}
		}
		if s != "opus" && s != "sonnet" && s != "haiku" {
			return nil, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'model' in %s: must be one of 'opus', 'sonnet', 'haiku' (lowercase), got %q",
				configFile, s,
			)}
		}
	}

	// name: must be a string (any value is valid).
	if v, ok := validated["name"]; ok {
		if _, isStr := v.(string); !isStr {
			return nil, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'name' in %s: expected string, got %T", configFile, v,
			)}
		}
	}

	// task_folder_pattern: must be a string (empty string is accepted).
	if v, ok := validated["task_folder_pattern"]; ok {
		if _, isStr := v.(string); !isStr {
			return nil, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'task_folder_pattern' in %s: expected string, got %T", configFile, v,
			)}
		}
	}

	// effort: must be a string and one of the allowed values.
	if v, ok := validated["effort"]; ok {
		s, isStr := v.(string)
		if !isStr {
			return nil, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'effort' in %s: expected string, got %T", configFile, v,
			)}
		}
		if s != "low" && s != "medium" && s != "high" {
			return nil, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'effort' in %s: must be one of 'low', 'medium', 'high' (lowercase), got %q",
				configFile, s,
			)}
		}
	}

	return validated, nil
}

// toFloat64 converts a numeric value (float64, int64, or int) to float64.
// TOML integers arrive as int64 when decoded into map[string]any.
func toFloat64(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int64:
		return float64(n), nil
	case int:
		return float64(n), nil
	default:
		return 0, fmt.Errorf("not a number: %T", v)
	}
}

// LoadConfig loads configuration from .raymond/config.toml found by searching
// upward from cwd. Returns an empty map if no config file is found.
//
// If cwd is empty, the process working directory is used.
//
// Returns a ConfigError if the config file exists but cannot be read or parsed.
func LoadConfig(cwd string) (map[string]any, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, &ConfigError{msg: fmt.Sprintf("failed to get working directory: %v", err)}
		}
	}

	configFile := FindConfigFile(cwd)
	if configFile == "" {
		return map[string]any{}, nil
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, &ConfigError{msg: fmt.Sprintf("Failed to read %s: %v", configFile, err)}
	}

	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, &ConfigError{msg: fmt.Sprintf(
			"Failed to parse %s: Invalid TOML syntax - %v", configFile, err,
		)}
	}

	// Extract the [raymond] section.
	raymondSection, ok := raw["raymond"].(map[string]any)
	if !ok {
		if _, exists := raw["raymond"]; exists {
			return nil, &ConfigError{msg: fmt.Sprintf(
				"Failed to parse %s: [raymond] must be a TOML table, not a scalar value", configFile,
			)}
		}
		return map[string]any{}, nil
	}

	validated, err := ValidateConfig(raymondSection, configFile)
	if err != nil {
		return nil, err
	}
	return validated, nil
}

// MergeConfig merges fileConfig values into args, with CLI values taking
// precedence. Returns the merged CLIArgs.
//
// Rules:
//   - Pointer fields (Budget, Timeout): config fills in only when nil.
//   - String fields (Model, Effort): config fills in only when "".
//   - Boolean flags: config can only enable (set to true), not disable.
//     If the CLI flag is already true, the config value is ignored.
func MergeConfig(fileConfig map[string]any, args CLIArgs) CLIArgs {
	result := args

	// Budget: fill from config when not specified on CLI.
	if result.Budget == nil {
		if v, ok := fileConfig["budget"]; ok {
			if f, err := toFloat64(v); err == nil {
				result.Budget = &f
			}
		}
	}

	// Timeout: fill from config when not specified on CLI.
	if result.Timeout == nil {
		if v, ok := fileConfig["timeout"]; ok {
			if f, err := toFloat64(v); err == nil {
				result.Timeout = &f
			}
		}
	}

	// Model: fill from config when not specified on CLI.
	if result.Model == "" {
		if v, ok := fileConfig["model"].(string); ok {
			result.Model = v
		}
	}

	// Effort: fill from config when not specified on CLI.
	if result.Effort == "" {
		if v, ok := fileConfig["effort"].(string); ok {
			result.Effort = v
		}
	}

	// Name: CLI wins if non-empty, else config value, else ""; trim whitespace.
	name := result.Name
	if name == "" {
		if v, ok := fileConfig["name"].(string); ok {
			name = v
		}
	}
	result.Name = strings.TrimSpace(name)

	// Boolean flags: config can only enable, not disable.
	if !result.DangerouslySkipPermissions {
		if v, _ := fileConfig["dangerously_skip_permissions"].(bool); v {
			result.DangerouslySkipPermissions = true
		}
	}
	if !result.NoDebug {
		if v, _ := fileConfig["no_debug"].(bool); v {
			result.NoDebug = true
		}
	}
	if !result.NoWait {
		if v, _ := fileConfig["no_wait"].(bool); v {
			result.NoWait = true
		}
	}
	if !result.Verbose {
		if v, _ := fileConfig["verbose"].(bool); v {
			result.Verbose = true
		}
	}

	// TaskFolderPattern: fill from config when not specified.
	if result.TaskFolderPattern == "" {
		if v, ok := fileConfig["task_folder_pattern"].(string); ok && v != "" {
			result.TaskFolderPattern = v
		}
	}

	return result
}

// DefaultServePort is the HTTP port used by `ray serve` when no port is
// supplied via CLI flag or config file.
const DefaultServePort = 8080

// ServeFileConfig holds values from the [raymond.serve] section of config.toml.
// Path fields (Root, Workdir) are resolved to absolute paths at load time,
// relative to the directory containing .raymond/. A pointer or empty/false
// value indicates the option was not set in the config file.
type ServeFileConfig struct {
	Root    string
	Port    *int
	MCP     bool
	NoHTTP  bool
	Workdir string
}

// ServeCLIArgs holds values parsed from `ray serve` command-line flags.
// Roots holds raw --root paths (multiple allowed). Port is nil when --port
// was not explicitly set. Workdir is "" when --workdir was not specified.
type ServeCLIArgs struct {
	Roots   []string
	Port    *int
	MCP     bool
	NoHTTP  bool
	Workdir string
}

// MergedServeConfig is the resolved configuration the serve command uses.
// Roots are absolute paths, deduplicated.
type MergedServeConfig struct {
	Roots   []string
	Port    int
	MCP     bool
	NoHTTP  bool
	Workdir string
}

// serveKnownKeys is the recognized set of keys in the [raymond.serve] section.
var serveKnownKeys = map[string]bool{
	"root":    true,
	"port":    true,
	"mcp":     true,
	"no_http": true,
	"workdir": true,
}

// validateServeSection validates and normalizes the raw [raymond.serve] map.
// Unknown keys are silently dropped for forward compatibility. Path fields
// are returned unresolved; LoadServeConfig handles resolution.
func validateServeSection(raw map[string]any, configFile string) (ServeFileConfig, error) {
	var out ServeFileConfig

	for k := range raw {
		if !serveKnownKeys[k] {
			delete(raw, k)
		}
	}

	if v, ok := raw["root"]; ok {
		s, isStr := v.(string)
		if !isStr {
			return out, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'root' in %s: expected string (single path), got %T",
				configFile, v,
			)}
		}
		out.Root = s
	}

	if v, ok := raw["port"]; ok {
		var p int
		switch n := v.(type) {
		case int64:
			p = int(n)
		case int:
			p = n
		default:
			return out, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'port' in %s: expected integer, got %T", configFile, v,
			)}
		}
		if p < 1 || p > 65535 {
			return out, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'port' in %s: must be in range 1-65535, got %d",
				configFile, p,
			)}
		}
		out.Port = &p
	}

	for _, flag := range []string{"mcp", "no_http"} {
		if v, ok := raw[flag]; ok {
			b, isBool := v.(bool)
			if !isBool {
				return out, &ConfigError{msg: fmt.Sprintf(
					"Invalid value for %q in %s: expected boolean, got %T",
					flag, configFile, v,
				)}
			}
			switch flag {
			case "mcp":
				out.MCP = b
			case "no_http":
				out.NoHTTP = b
			}
		}
	}

	if v, ok := raw["workdir"]; ok {
		s, isStr := v.(string)
		if !isStr {
			return out, &ConfigError{msg: fmt.Sprintf(
				"Invalid value for 'workdir' in %s: expected string, got %T", configFile, v,
			)}
		}
		out.Workdir = s
	}

	return out, nil
}

// LoadServeConfig loads the [raymond.serve] subsection from
// .raymond/config.toml found by searching upward from cwd. Relative paths in
// `root` and `workdir` are resolved against the directory containing
// .raymond/ (so behavior is independent of the user's cwd when invoking
// `ray serve`). Returns a zero ServeFileConfig if no config file is found.
func LoadServeConfig(cwd string) (ServeFileConfig, error) {
	var zero ServeFileConfig

	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return zero, &ConfigError{msg: fmt.Sprintf("failed to get working directory: %v", err)}
		}
	}

	configFile := FindConfigFile(cwd)
	if configFile == "" {
		return zero, nil
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		return zero, &ConfigError{msg: fmt.Sprintf("Failed to read %s: %v", configFile, err)}
	}

	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return zero, &ConfigError{msg: fmt.Sprintf(
			"Failed to parse %s: Invalid TOML syntax - %v", configFile, err,
		)}
	}

	raymondSection, ok := raw["raymond"].(map[string]any)
	if !ok {
		return zero, nil
	}

	serveRaw, ok := raymondSection["serve"].(map[string]any)
	if !ok {
		if _, exists := raymondSection["serve"]; exists {
			return zero, &ConfigError{msg: fmt.Sprintf(
				"Failed to parse %s: [raymond.serve] must be a TOML table, not a scalar value",
				configFile,
			)}
		}
		return zero, nil
	}

	cfg, err := validateServeSection(serveRaw, configFile)
	if err != nil {
		return zero, err
	}

	// Resolve path fields relative to the directory containing .raymond/
	// (i.e., the parent of the .raymond directory).
	baseDir := filepath.Dir(filepath.Dir(configFile))
	if cfg.Root != "" && !filepath.IsAbs(cfg.Root) {
		cfg.Root = filepath.Join(baseDir, cfg.Root)
	}
	if cfg.Workdir != "" && !filepath.IsAbs(cfg.Workdir) {
		cfg.Workdir = filepath.Join(baseDir, cfg.Workdir)
	}

	return cfg, nil
}

// MergeServeConfig combines a ServeFileConfig and ServeCLIArgs into the
// MergedServeConfig used by the serve command.
//
// Roots: the file root (if set) is placed first, then CLI roots are appended.
// Duplicates are removed by absolute-path comparison. Relative CLI paths
// resolve against the process working directory.
//
// Port: CLI value wins if non-nil; otherwise file value; otherwise DefaultServePort.
// MCP/NoHTTP: CLI true wins; otherwise file value.
// Workdir: CLI value wins if non-empty; otherwise file value.
func MergeServeConfig(file ServeFileConfig, args ServeCLIArgs) MergedServeConfig {
	var roots []string
	seen := make(map[string]bool)
	add := func(p string) {
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		if seen[abs] {
			return
		}
		seen[abs] = true
		roots = append(roots, p)
	}

	add(file.Root)
	for _, r := range args.Roots {
		add(r)
	}

	port := DefaultServePort
	switch {
	case args.Port != nil:
		port = *args.Port
	case file.Port != nil:
		port = *file.Port
	}

	merged := MergedServeConfig{
		Roots:  roots,
		Port:   port,
		MCP:    args.MCP || file.MCP,
		NoHTTP: args.NoHTTP || file.NoHTTP,
	}
	if args.Workdir != "" {
		merged.Workdir = args.Workdir
	} else {
		merged.Workdir = file.Workdir
	}
	return merged
}

// configTemplate is the content of a newly generated config file.
// All options are commented out with explanatory comments.
const configTemplate = `# Raymond configuration file
# Command-line arguments override values in this file
# Uncomment and modify values as needed

[raymond]
# Cost budget limit in USD (default: 10.0)
# budget = 10.0

# Skip permission prompts (WARNING: allows any action without prompting) (default: false)
# dangerously_skip_permissions = false

# Default model: "opus", "sonnet", or "haiku" (default: None)
# model = "sonnet"

# Default effort level: "low", "medium", or "high" (default: None)
# effort = "medium"

# Workflow name (default: None)
# name = ""

# Timeout per Claude Code invocation in seconds (default: 600, 0=none)
# timeout = 600.0

# Disable debug mode (default: false, meaning debug mode is enabled by default)
# no_debug = false

# Don't wait for usage limit reset; pause and exit immediately (default: false)
# no_wait = false

# Enable verbose logging (default: false)
# verbose = false

# Task folder location pattern; supports {{workflow_id}} and {{agent_id}} (default: .raymond/tasks/{{workflow_id}}/{{agent_id}})
# task_folder_pattern = ".raymond/tasks/{{workflow_id}}/{{agent_id}}"

[raymond.serve]
# Defaults for the 'ray serve' subcommand.
# CLI flags override these values, except --root which is APPENDED to the
# value here. Relative paths resolve against this config file's directory.

# Workflow root directory scanned for workflow.yaml manifests (default: none)
# root = "workflows"

# HTTP server port (default: 8080)
# port = 8080

# Enable MCP transport over stdio (default: false)
# mcp = false

# Disable HTTP server; requires mcp = true (default: false)
# no_http = false

# Default working directory for workflow runs (default: process cwd)
# workdir = ""
`

// configUnsafeDefaultsTemplate is structurally identical to configTemplate but
// with budget and dangerously_skip_permissions uncommented and set to unsafe defaults.
const configUnsafeDefaultsTemplate = `# Raymond configuration file
# Command-line arguments override values in this file
# Uncomment and modify values as needed

[raymond]
# Cost budget limit in USD (default: 10.0)
budget = 1000.0

# Skip permission prompts (WARNING: allows any action without prompting) (default: false)
dangerously_skip_permissions = true

# Default model: "opus", "sonnet", or "haiku" (default: None)
# model = "sonnet"

# Default effort level: "low", "medium", or "high" (default: None)
# effort = "medium"

# Workflow name (default: None)
# name = ""

# Timeout per Claude Code invocation in seconds (default: 600, 0=none)
# timeout = 600.0

# Disable debug mode (default: false, meaning debug mode is enabled by default)
# no_debug = false

# Don't wait for usage limit reset; pause and exit immediately (default: false)
# no_wait = false

# Enable verbose logging (default: false)
# verbose = false

# Task folder location pattern; supports {{workflow_id}} and {{agent_id}} (default: .raymond/tasks/{{workflow_id}}/{{agent_id}})
# task_folder_pattern = ".raymond/tasks/{{workflow_id}}/{{agent_id}}"

[raymond.serve]
# Defaults for the 'ray serve' subcommand.
# CLI flags override these values, except --root which is APPENDED to the
# value here. Relative paths resolve against this config file's directory.

# Workflow root directory scanned for workflow.yaml manifests (default: none)
# root = "workflows"

# HTTP server port (default: 8080)
# port = 8080

# Enable MCP transport over stdio (default: false)
# mcp = false

# Disable HTTP server; requires mcp = true (default: false)
# no_http = false

# Default working directory for workflow runs (default: process cwd)
# workdir = ""
`

// InitConfig creates .raymond/config.toml at the project root with all options
// commented out. Returns a ConfigError if the file already exists or cannot be
// created.
//
// If cwd is empty, the process working directory is used.
func InitConfig(cwd string) error {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return &ConfigError{msg: fmt.Sprintf("failed to get working directory: %v", err)}
		}
	}

	// Refuse if config already exists.
	existing := FindConfigFile(cwd)
	if existing != "" {
		return &ConfigError{msg: fmt.Sprintf(
			"configuration file already exists at %s\n"+
				"Refusing to generate a new config file. Delete or rename the existing file first.",
			existing,
		)}
	}

	// Locate (or create) the .raymond directory at the project root.
	projectRoot := FindProjectRoot(cwd)
	raymondDir, err := FindRaymondDir(projectRoot, true)
	if err != nil {
		return err
	}
	if raymondDir == "" {
		return &ConfigError{msg: "failed to create .raymond directory"}
	}

	configFile := filepath.Join(raymondDir, "config.toml")
	if err := os.WriteFile(configFile, []byte(configTemplate), 0o644); err != nil {
		return &ConfigError{msg: fmt.Sprintf("failed to write configuration file: %v", err)}
	}
	return nil
}

// InitUnsafeDefaults creates .raymond/config.toml at the project root with
// budget set to 1000.0 and dangerously_skip_permissions set to true.
// Returns a ConfigError if the file already exists or cannot be created.
//
// If cwd is empty, the process working directory is used.
func InitUnsafeDefaults(cwd string) error {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return &ConfigError{msg: fmt.Sprintf("failed to get working directory: %v", err)}
		}
	}

	// Refuse if config already exists.
	existing := FindConfigFile(cwd)
	if existing != "" {
		return &ConfigError{msg: fmt.Sprintf(
			"configuration file already exists at %s\n"+
				"Refusing to generate a new config file. Delete or rename the existing file first.",
			existing,
		)}
	}

	// Locate (or create) the .raymond directory at the project root.
	projectRoot := FindProjectRoot(cwd)
	raymondDir, err := FindRaymondDir(projectRoot, true)
	if err != nil {
		return err
	}
	if raymondDir == "" {
		return &ConfigError{msg: "failed to create .raymond directory"}
	}

	configFile := filepath.Join(raymondDir, "config.toml")
	if err := os.WriteFile(configFile, []byte(configUnsafeDefaultsTemplate), 0o644); err != nil {
		return &ConfigError{msg: fmt.Sprintf("failed to write configuration file: %v", err)}
	}
	return nil
}
