package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vector76/raymond/internal/config"
)

// ----------------------------------------------------------------------------
// FindProjectRoot
// ----------------------------------------------------------------------------

func TestFindProjectRootFindsGitDir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))

	sub := filepath.Join(root, "subdir", "nested")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	result := config.FindProjectRoot(sub)
	// Compare after resolving symlinks (TempDir may use /var -> /private/var on macOS)
	want, _ := filepath.EvalSymlinks(root)
	got, _ := filepath.EvalSymlinks(result)
	assert.Equal(t, want, got)
}

func TestFindProjectRootReturnsCwdIfNoGit(t *testing.T) {
	sub := filepath.Join(t.TempDir(), "subdir", "nested")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	result := config.FindProjectRoot(sub)
	want, _ := filepath.EvalSymlinks(sub)
	got, _ := filepath.EvalSymlinks(result)
	assert.Equal(t, want, got)
}

func TestFindProjectRootStopsAtFilesystemRoot(t *testing.T) {
	deep := t.TempDir()
	for i := 0; i < 5; i++ {
		deep = filepath.Join(deep, "level")
		require.NoError(t, os.Mkdir(deep, 0o755))
	}

	result := config.FindProjectRoot(deep)
	assert.NotEmpty(t, result)
	_, err := os.Stat(result)
	assert.NoError(t, err)
}

// ----------------------------------------------------------------------------
// FindRaymondDir
// ----------------------------------------------------------------------------

func TestFindRaymondDirFindsExisting(t *testing.T) {
	root := t.TempDir()
	rdir := filepath.Join(root, ".raymond")
	require.NoError(t, os.Mkdir(rdir, 0o755))

	sub := filepath.Join(root, "subdir")
	require.NoError(t, os.Mkdir(sub, 0o755))

	got, err := config.FindRaymondDir(sub, false)
	require.NoError(t, err)
	want, _ := filepath.EvalSymlinks(rdir)
	g, _ := filepath.EvalSymlinks(got)
	assert.Equal(t, want, g)
}

func TestFindRaymondDirSearchesUpwardUntilGit(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))

	sub := filepath.Join(root, "subdir")
	require.NoError(t, os.Mkdir(sub, 0o755))

	rdir := filepath.Join(sub, ".raymond")
	require.NoError(t, os.Mkdir(rdir, 0o755))

	nested := filepath.Join(sub, "nested")
	require.NoError(t, os.Mkdir(nested, 0o755))

	got, err := config.FindRaymondDir(nested, false)
	require.NoError(t, err)
	want, _ := filepath.EvalSymlinks(rdir)
	g, _ := filepath.EvalSymlinks(got)
	assert.Equal(t, want, g)
}

func TestFindRaymondDirCreatesAtProjectRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))

	sub := filepath.Join(root, "subdir", "nested")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	got, err := config.FindRaymondDir(sub, true)
	require.NoError(t, err)

	want, _ := filepath.EvalSymlinks(filepath.Join(root, ".raymond"))
	g, _ := filepath.EvalSymlinks(got)
	assert.Equal(t, want, g)

	info, err := os.Stat(got)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestFindRaymondDirCreatesAtCwdIfNoGit(t *testing.T) {
	sub := filepath.Join(t.TempDir(), "subdir", "nested")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	got, err := config.FindRaymondDir(sub, true)
	require.NoError(t, err)

	want, _ := filepath.EvalSymlinks(filepath.Join(sub, ".raymond"))
	g, _ := filepath.EvalSymlinks(got)
	assert.Equal(t, want, g)

	info, err := os.Stat(got)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestFindRaymondDirReturnsEmptyIfNotFoundAndNotCreating(t *testing.T) {
	sub := filepath.Join(t.TempDir(), "subdir")
	require.NoError(t, os.Mkdir(sub, 0o755))

	got, err := config.FindRaymondDir(sub, false)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFindRaymondDirIgnoresFileNamedRaymond(t *testing.T) {
	root := t.TempDir()
	// .raymond is a file, not a directory
	require.NoError(t, os.WriteFile(filepath.Join(root, ".raymond"), []byte("not a dir"), 0o644))

	got, err := config.FindRaymondDir(root, false)
	require.NoError(t, err)
	// Should not return the file; returns "" or a parent's .raymond
	if got != "" {
		info, err := os.Stat(got)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	}
}

// ----------------------------------------------------------------------------
// FindConfigFile
// ----------------------------------------------------------------------------

func TestFindConfigFileFindsFile(t *testing.T) {
	root := t.TempDir()
	rdir := filepath.Join(root, ".raymond")
	require.NoError(t, os.Mkdir(rdir, 0o755))
	cf := filepath.Join(rdir, "config.toml")
	require.NoError(t, os.WriteFile(cf, []byte("[raymond]\nbudget = 50.0\n"), 0o644))

	got := config.FindConfigFile(root)
	want, _ := filepath.EvalSymlinks(cf)
	g, _ := filepath.EvalSymlinks(got)
	assert.Equal(t, want, g)
}

func TestFindConfigFileReturnsEmptyIfNoRaymondDir(t *testing.T) {
	root := t.TempDir()
	got := config.FindConfigFile(root)
	assert.Empty(t, got)
}

func TestFindConfigFileReturnsEmptyIfNoConfigTOML(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".raymond"), 0o755))
	got := config.FindConfigFile(root)
	assert.Empty(t, got)
}

// ----------------------------------------------------------------------------
// ValidateConfig
// ----------------------------------------------------------------------------

func TestValidateConfigBudgetWrongType(t *testing.T) {
	_, err := config.ValidateConfig(map[string]any{"budget": "50.0"}, "config.toml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "budget")
	assert.Contains(t, err.Error(), "expected number")
}

func TestValidateConfigBudgetNotPositive(t *testing.T) {
	_, err := config.ValidateConfig(map[string]any{"budget": float64(-10)}, "config.toml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "budget")
	assert.Contains(t, err.Error(), "must be positive")
}

func TestValidateConfigBudgetZeroNotAllowed(t *testing.T) {
	_, err := config.ValidateConfig(map[string]any{"budget": float64(0)}, "config.toml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "budget")
}

func TestValidateConfigBudgetIntegerAccepted(t *testing.T) {
	// TOML integer 50 comes as int64
	result, err := config.ValidateConfig(map[string]any{"budget": int64(50)}, "config.toml")
	require.NoError(t, err)
	assert.Equal(t, float64(50), result["budget"])
}

func TestValidateConfigTimeoutWrongType(t *testing.T) {
	_, err := config.ValidateConfig(map[string]any{"timeout": "600"}, "config.toml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
	assert.Contains(t, err.Error(), "expected number")
}

func TestValidateConfigTimeoutNegativeNotAllowed(t *testing.T) {
	_, err := config.ValidateConfig(map[string]any{"timeout": float64(-1)}, "config.toml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
	assert.Contains(t, err.Error(), "must be non-negative")
}

func TestValidateConfigTimeoutZeroAllowed(t *testing.T) {
	result, err := config.ValidateConfig(map[string]any{"timeout": float64(0)}, "config.toml")
	require.NoError(t, err)
	assert.Equal(t, float64(0), result["timeout"])
}

func TestValidateConfigBooleanFlagsWrongType(t *testing.T) {
	for _, flag := range []string{"dangerously_skip_permissions", "no_debug", "no_wait", "verbose"} {
		_, err := config.ValidateConfig(map[string]any{flag: "true"}, "config.toml")
		require.Error(t, err, "flag %q should fail", flag)
		assert.Contains(t, err.Error(), flag)
		assert.Contains(t, err.Error(), "expected boolean")
	}
}

func TestValidateConfigModelWrongType(t *testing.T) {
	_, err := config.ValidateConfig(map[string]any{"model": 123}, "config.toml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model")
	assert.Contains(t, err.Error(), "expected string")
}

func TestValidateConfigModelInvalidChoice(t *testing.T) {
	_, err := config.ValidateConfig(map[string]any{"model": "gpt4"}, "config.toml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model")
	assert.Contains(t, err.Error(), "must be one of")
}

func TestValidateConfigModelValidChoices(t *testing.T) {
	for _, m := range []string{"opus", "sonnet", "haiku"} {
		result, err := config.ValidateConfig(map[string]any{"model": m}, "config.toml")
		require.NoError(t, err, "model %q should be valid", m)
		assert.Equal(t, m, result["model"])
	}
}

func TestValidateConfigEffortWrongType(t *testing.T) {
	_, err := config.ValidateConfig(map[string]any{"effort": 2}, "config.toml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "effort")
	assert.Contains(t, err.Error(), "expected string")
}

func TestValidateConfigEffortInvalidChoice(t *testing.T) {
	_, err := config.ValidateConfig(map[string]any{"effort": "extreme"}, "config.toml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "effort")
	assert.Contains(t, err.Error(), "must be one of")
}

func TestValidateConfigEffortValidChoices(t *testing.T) {
	for _, e := range []string{"low", "medium", "high"} {
		result, err := config.ValidateConfig(map[string]any{"effort": e}, "config.toml")
		require.NoError(t, err, "effort %q should be valid", e)
		assert.Equal(t, e, result["effort"])
	}
}

func TestValidateConfigNameAccepted(t *testing.T) {
	result, err := config.ValidateConfig(map[string]any{"name": "foo"}, "config.toml")
	require.NoError(t, err)
	assert.Equal(t, "foo", result["name"])
}

func TestValidateConfigNameWrongType(t *testing.T) {
	_, err := config.ValidateConfig(map[string]any{"name": 123}, "config.toml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
	assert.Contains(t, err.Error(), "expected string")
}

func TestValidateConfigUnknownKeysFiltered(t *testing.T) {
	in := map[string]any{
		"budget":      float64(50),
		"unknown_key": "value",
		"another":     123,
	}
	result, err := config.ValidateConfig(in, "config.toml")
	require.NoError(t, err)
	assert.Equal(t, float64(50), result["budget"])
	assert.NotContains(t, result, "unknown_key")
	assert.NotContains(t, result, "another")
}

func TestValidateConfigAllValidValues(t *testing.T) {
	in := map[string]any{
		"budget":                      float64(50),
		"timeout":                     float64(300),
		"dangerously_skip_permissions": true,
		"no_debug":                    false,
		"no_wait":                     true,
		"verbose":                     true,
		"model":                       "opus",
		"effort":                      "high",
	}
	result, err := config.ValidateConfig(in, "config.toml")
	require.NoError(t, err)
	assert.Equal(t, float64(50), result["budget"])
	assert.Equal(t, float64(300), result["timeout"])
	assert.Equal(t, true, result["dangerously_skip_permissions"])
	assert.Equal(t, false, result["no_debug"])
	assert.Equal(t, true, result["no_wait"])
	assert.Equal(t, true, result["verbose"])
	assert.Equal(t, "opus", result["model"])
	assert.Equal(t, "high", result["effort"])
}

// ----------------------------------------------------------------------------
// LoadConfig
// ----------------------------------------------------------------------------

func TestLoadConfigLoadsValidConfig(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".raymond"), 0o755))
	content := strings.Join([]string{
		"[raymond]",
		"budget = 50.0",
		`dangerously_skip_permissions = true`,
		`model = "sonnet"`,
		"timeout = 300.0",
		"no_debug = true",
		"verbose = true",
	}, "\n")
	require.NoError(t, os.WriteFile(
		filepath.Join(root, ".raymond", "config.toml"),
		[]byte(content), 0o644,
	))

	cfg, err := config.LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, float64(50), cfg["budget"])
	assert.Equal(t, true, cfg["dangerously_skip_permissions"])
	assert.Equal(t, "sonnet", cfg["model"])
	assert.Equal(t, float64(300), cfg["timeout"])
	assert.Equal(t, true, cfg["no_debug"])
	assert.Equal(t, true, cfg["verbose"])
}

func TestLoadConfigReturnsEmptyMapIfNoConfig(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.LoadConfig(root)
	require.NoError(t, err)
	assert.Empty(t, cfg)
}

func TestLoadConfigReturnsEmptyMapIfMissingRaymondSection(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".raymond"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, ".raymond", "config.toml"),
		[]byte("[other]\nkey = \"value\"\n"), 0o644,
	))

	cfg, err := config.LoadConfig(root)
	require.NoError(t, err)
	assert.Empty(t, cfg)
}

func TestLoadConfigErrorOnInvalidTOML(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".raymond"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, ".raymond", "config.toml"),
		[]byte("[raymond]\nbudget = not valid toml\n"), 0o644,
	))

	_, err := config.LoadConfig(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Failed to parse")
	assert.Contains(t, err.Error(), "config.toml")
}

func TestLoadConfigIgnoresUnknownKeys(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".raymond"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, ".raymond", "config.toml"),
		[]byte("[raymond]\nbudget = 50.0\nunknown_key = \"value\"\n"), 0o644,
	))

	cfg, err := config.LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, float64(50), cfg["budget"])
	assert.NotContains(t, cfg, "unknown_key")
}

func TestLoadConfigErrorOnValidationFailure(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".raymond"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, ".raymond", "config.toml"),
		[]byte(`[raymond]`+"\n"+`model = "gpt5"`+"\n"), 0o644,
	))

	_, err := config.LoadConfig(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model")
}

func TestLoadConfigErrorOnScalarRaymondSection(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".raymond"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, ".raymond", "config.toml"),
		[]byte("raymond = \"oops\"\n"), 0o644,
	))

	_, err := config.LoadConfig(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[raymond]")
	assert.Contains(t, err.Error(), "table")
}

// ----------------------------------------------------------------------------
// MergeConfig
// ----------------------------------------------------------------------------

func TestMergeConfigCLIBudgetOverridesConfig(t *testing.T) {
	budget := float64(100)
	args := config.CLIArgs{Budget: &budget}
	fileConfig := map[string]any{"budget": float64(50)}

	result := config.MergeConfig(fileConfig, args)
	require.NotNil(t, result.Budget)
	assert.Equal(t, float64(100), *result.Budget)
}

func TestMergeConfigFillsMissingBudgetFromConfig(t *testing.T) {
	args := config.CLIArgs{} // Budget is nil
	fileConfig := map[string]any{"budget": float64(50)}

	result := config.MergeConfig(fileConfig, args)
	require.NotNil(t, result.Budget)
	assert.Equal(t, float64(50), *result.Budget)
}

func TestMergeConfigCLIModelOverridesConfig(t *testing.T) {
	args := config.CLIArgs{Model: "haiku"}
	fileConfig := map[string]any{"model": "sonnet"}

	result := config.MergeConfig(fileConfig, args)
	assert.Equal(t, "haiku", result.Model)
}

func TestMergeConfigFillsMissingModelFromConfig(t *testing.T) {
	args := config.CLIArgs{} // Model is ""
	fileConfig := map[string]any{"model": "sonnet"}

	result := config.MergeConfig(fileConfig, args)
	assert.Equal(t, "sonnet", result.Model)
}

func TestMergeConfigCLIEffortOverridesConfig(t *testing.T) {
	args := config.CLIArgs{Effort: "low"}
	fileConfig := map[string]any{"effort": "high"}

	result := config.MergeConfig(fileConfig, args)
	assert.Equal(t, "low", result.Effort)
}

func TestMergeConfigFillsMissingEffortFromConfig(t *testing.T) {
	args := config.CLIArgs{}
	fileConfig := map[string]any{"effort": "high"}

	result := config.MergeConfig(fileConfig, args)
	assert.Equal(t, "high", result.Effort)
}

func TestMergeConfigCLITimeoutOverridesConfig(t *testing.T) {
	timeout := float64(900)
	args := config.CLIArgs{Timeout: &timeout}
	fileConfig := map[string]any{"timeout": float64(300)}

	result := config.MergeConfig(fileConfig, args)
	require.NotNil(t, result.Timeout)
	assert.Equal(t, float64(900), *result.Timeout)
}

func TestMergeConfigFillsMissingTimeoutFromConfig(t *testing.T) {
	args := config.CLIArgs{}
	fileConfig := map[string]any{"timeout": float64(300)}

	result := config.MergeConfig(fileConfig, args)
	require.NotNil(t, result.Timeout)
	assert.Equal(t, float64(300), *result.Timeout)
}

func TestMergeConfigBooleanFlagsTrueStaysTrueFromCLI(t *testing.T) {
	// CLI sets flags to true — config cannot override/disable
	args := config.CLIArgs{
		DangerouslySkipPermissions: true,
		NoDebug:                    true,
		NoWait:                     true,
		Verbose:                    true,
	}
	// Config also sets them (but that doesn't matter — CLI wins)
	fileConfig := map[string]any{
		"dangerously_skip_permissions": true,
		"no_debug":                     true,
		"no_wait":                      true,
		"verbose":                      true,
	}
	result := config.MergeConfig(fileConfig, args)
	assert.True(t, result.DangerouslySkipPermissions)
	assert.True(t, result.NoDebug)
	assert.True(t, result.NoWait)
	assert.True(t, result.Verbose)
}

func TestMergeConfigBooleanFlagsSetFromConfigWhenCLIIsFalse(t *testing.T) {
	args := config.CLIArgs{} // all booleans default to false
	fileConfig := map[string]any{
		"dangerously_skip_permissions": true,
		"no_debug":                     true,
		"no_wait":                      true,
		"verbose":                      true,
	}
	result := config.MergeConfig(fileConfig, args)
	assert.True(t, result.DangerouslySkipPermissions)
	assert.True(t, result.NoDebug)
	assert.True(t, result.NoWait)
	assert.True(t, result.Verbose)
}

func TestMergeConfigBooleanFlagsNotDisabledByConfig(t *testing.T) {
	// Config sets flags to false, CLI is false — stays false
	args := config.CLIArgs{}
	fileConfig := map[string]any{
		"dangerously_skip_permissions": false,
		"no_debug":                     false,
		"no_wait":                      false,
		"verbose":                      false,
	}
	result := config.MergeConfig(fileConfig, args)
	assert.False(t, result.DangerouslySkipPermissions)
	assert.False(t, result.NoDebug)
	assert.False(t, result.NoWait)
	assert.False(t, result.Verbose)
}

func TestMergeConfigCLINameOverridesConfig(t *testing.T) {
	args := config.CLIArgs{Name: "cli-name"}
	fileConfig := map[string]any{"name": "config-name"}

	result := config.MergeConfig(fileConfig, args)
	assert.Equal(t, "cli-name", result.Name)
}

func TestMergeConfigWhitespaceOnlyCLINameMergesToEmpty(t *testing.T) {
	args := config.CLIArgs{Name: "   "}
	fileConfig := map[string]any{"name": "config-name"}

	result := config.MergeConfig(fileConfig, args)
	assert.Equal(t, "", result.Name)
}

func TestMergeConfigFillsMissingNameFromConfig(t *testing.T) {
	args := config.CLIArgs{} // Name is ""
	fileConfig := map[string]any{"name": "  my-project  "}

	result := config.MergeConfig(fileConfig, args)
	assert.Equal(t, "my-project", result.Name)
}

func TestMergeConfigEmptyFileConfig(t *testing.T) {
	timeout := float64(600)
	args := config.CLIArgs{
		Model:   "opus",
		Timeout: &timeout,
		Verbose: true,
	}
	result := config.MergeConfig(map[string]any{}, args)
	assert.Equal(t, "opus", result.Model)
	require.NotNil(t, result.Timeout)
	assert.Equal(t, float64(600), *result.Timeout)
	assert.True(t, result.Verbose)
}

// ----------------------------------------------------------------------------
// InitConfig
// ----------------------------------------------------------------------------

func TestInitConfigCreatesConfigFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))

	sub := filepath.Join(root, "subdir")
	require.NoError(t, os.Mkdir(sub, 0o755))

	err := config.InitConfig(sub)
	require.NoError(t, err)

	cf := filepath.Join(root, ".raymond", "config.toml")
	_, statErr := os.Stat(cf)
	require.NoError(t, statErr)

	content, readErr := os.ReadFile(cf)
	require.NoError(t, readErr)
	s := string(content)
	assert.Contains(t, s, "[raymond]")
	assert.Contains(t, s, "# budget = 10.0")
	assert.Contains(t, s, "# dangerously_skip_permissions = false")
	assert.Contains(t, s, `# model = "sonnet"`)
	assert.Contains(t, s, "# timeout = 600.0")
	assert.Contains(t, s, "# no_debug = false")
	assert.Contains(t, s, "# no_wait = false")
	assert.Contains(t, s, "# verbose = false")
}

func TestInitConfigCreatesRaymondDirIfMissing(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))

	err := config.InitConfig(root)
	require.NoError(t, err)

	rdir := filepath.Join(root, ".raymond")
	info, statErr := os.Stat(rdir)
	require.NoError(t, statErr)
	assert.True(t, info.IsDir())
}

func TestInitConfigCreatesAtProjectRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))

	nested := filepath.Join(root, "subdir", "nested")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	err := config.InitConfig(nested)
	require.NoError(t, err)

	cf := filepath.Join(root, ".raymond", "config.toml")
	_, statErr := os.Stat(cf)
	require.NoError(t, statErr)
}

func TestInitConfigRefusesIfConfigExists(t *testing.T) {
	root := t.TempDir()
	rdir := filepath.Join(root, ".raymond")
	require.NoError(t, os.Mkdir(rdir, 0o755))
	cf := filepath.Join(rdir, "config.toml")
	require.NoError(t, os.WriteFile(cf, []byte("[raymond]\nbudget = 50.0\n"), 0o644))

	err := config.InitConfig(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	// Original file should be unchanged
	content, _ := os.ReadFile(cf)
	assert.Contains(t, string(content), "budget = 50.0")
}

func TestInitConfigErrorMentionsExistingPath(t *testing.T) {
	root := t.TempDir()
	rdir := filepath.Join(root, ".raymond")
	require.NoError(t, os.Mkdir(rdir, 0o755))
	cf := filepath.Join(rdir, "config.toml")
	require.NoError(t, os.WriteFile(cf, []byte("[raymond]\n"), 0o644))

	err := config.InitConfig(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config.toml")
}

// ----------------------------------------------------------------------------
// InitUnsafeDefaults
// ----------------------------------------------------------------------------

func TestInitUnsafeDefaultsCreatesConfigFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))

	sub := filepath.Join(root, "subdir")
	require.NoError(t, os.Mkdir(sub, 0o755))

	err := config.InitUnsafeDefaults(sub)
	require.NoError(t, err)

	cf := filepath.Join(root, ".raymond", "config.toml")
	_, statErr := os.Stat(cf)
	require.NoError(t, statErr)

	content, readErr := os.ReadFile(cf)
	require.NoError(t, readErr)
	s := string(content)
	assert.Contains(t, s, "[raymond]")
	assert.Contains(t, s, "budget = 1000.0")
	assert.Contains(t, s, "dangerously_skip_permissions = true")
	assert.Contains(t, s, "# Skip permission prompts (WARNING:")
}

func TestInitUnsafeDefaultsOtherFieldsRemainCommented(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))

	err := config.InitUnsafeDefaults(root)
	require.NoError(t, err)

	cf := filepath.Join(root, ".raymond", "config.toml")
	content, readErr := os.ReadFile(cf)
	require.NoError(t, readErr)
	s := string(content)
	assert.Contains(t, s, `# model = "sonnet"`)
	assert.Contains(t, s, "# timeout = 600.0")
	assert.Contains(t, s, "# no_debug = false")
	assert.Contains(t, s, "# no_wait = false")
	assert.Contains(t, s, "# verbose = false")
}

func TestInitUnsafeDefaultsCreatesRaymondDirIfMissing(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))

	err := config.InitUnsafeDefaults(root)
	require.NoError(t, err)

	rdir := filepath.Join(root, ".raymond")
	info, statErr := os.Stat(rdir)
	require.NoError(t, statErr)
	assert.True(t, info.IsDir())
}

func TestInitUnsafeDefaultsCreatesAtProjectRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))

	nested := filepath.Join(root, "subdir", "nested")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	err := config.InitUnsafeDefaults(nested)
	require.NoError(t, err)

	cf := filepath.Join(root, ".raymond", "config.toml")
	_, statErr := os.Stat(cf)
	require.NoError(t, statErr)
}

func TestInitUnsafeDefaultsRefusesIfConfigExists(t *testing.T) {
	root := t.TempDir()
	rdir := filepath.Join(root, ".raymond")
	require.NoError(t, os.Mkdir(rdir, 0o755))
	cf := filepath.Join(rdir, "config.toml")
	require.NoError(t, os.WriteFile(cf, []byte("[raymond]\nbudget = 50.0\n"), 0o644))

	err := config.InitUnsafeDefaults(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	// Original file should be unchanged
	content, _ := os.ReadFile(cf)
	assert.Contains(t, string(content), "budget = 50.0")
}

func TestInitUnsafeDefaultsErrorMentionsExistingPath(t *testing.T) {
	root := t.TempDir()
	rdir := filepath.Join(root, ".raymond")
	require.NoError(t, os.Mkdir(rdir, 0o755))
	cf := filepath.Join(rdir, "config.toml")
	require.NoError(t, os.WriteFile(cf, []byte("[raymond]\n"), 0o644))

	err := config.InitUnsafeDefaults(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config.toml")
}
