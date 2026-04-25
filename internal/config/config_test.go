package config_test

import (
	"fmt"
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

func TestValidateConfigTaskFolderPatternValidString(t *testing.T) {
	result, err := config.ValidateConfig(map[string]any{"task_folder_pattern": ".tasks/{{workflow_id}}/{{agent_id}}"}, "config.toml")
	require.NoError(t, err)
	assert.Equal(t, ".tasks/{{workflow_id}}/{{agent_id}}", result["task_folder_pattern"])
}

func TestValidateConfigTaskFolderPatternEmptyStringAccepted(t *testing.T) {
	result, err := config.ValidateConfig(map[string]any{"task_folder_pattern": ""}, "config.toml")
	require.NoError(t, err)
	assert.Equal(t, "", result["task_folder_pattern"])
}

func TestValidateConfigTaskFolderPatternNonStringReturnsError(t *testing.T) {
	_, err := config.ValidateConfig(map[string]any{"task_folder_pattern": 42}, "config.toml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task_folder_pattern")
	assert.Contains(t, err.Error(), "expected string")
	assert.Contains(t, err.Error(), "int")
}

func TestValidateConfigTaskFolderPatternAbsentNotInResult(t *testing.T) {
	result, err := config.ValidateConfig(map[string]any{"budget": float64(10)}, "config.toml")
	require.NoError(t, err)
	assert.NotContains(t, result, "task_folder_pattern")
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

func TestMergeConfigFillsTaskFolderPatternFromConfig(t *testing.T) {
	args := config.CLIArgs{} // TaskFolderPattern is ""
	fileConfig := map[string]any{"task_folder_pattern": ".custom/{{workflow_id}}/{{agent_id}}"}

	result := config.MergeConfig(fileConfig, args)
	assert.Equal(t, ".custom/{{workflow_id}}/{{agent_id}}", result.TaskFolderPattern)
}

func TestMergeConfigTaskFolderPatternNotOverriddenWhenAlreadySet(t *testing.T) {
	args := config.CLIArgs{TaskFolderPattern: "existing-pattern"}
	fileConfig := map[string]any{"task_folder_pattern": "config-pattern"}

	result := config.MergeConfig(fileConfig, args)
	assert.Equal(t, "existing-pattern", result.TaskFolderPattern)
}

func TestMergeConfigTaskFolderPatternEmptyStringInConfigNotApplied(t *testing.T) {
	args := config.CLIArgs{}
	fileConfig := map[string]any{"task_folder_pattern": ""}

	result := config.MergeConfig(fileConfig, args)
	assert.Equal(t, "", result.TaskFolderPattern)
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
	assert.Contains(t, s, "[raymond.serve]")
	assert.Contains(t, s, `# root = "workflows"`)
	assert.Contains(t, s, "# port = 8080")
	assert.Contains(t, s, "# mcp = false")
	assert.Contains(t, s, "# no_http = false")
	assert.Contains(t, s, `# workdir = ""`)
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

// ----------------------------------------------------------------------------
// LoadServeConfig and ServeConfig validation
// ----------------------------------------------------------------------------

// writeServeConfig writes a config.toml at <root>/.raymond/config.toml.
func writeServeConfig(t *testing.T, root, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".raymond"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, ".raymond", "config.toml"),
		[]byte(content), 0o644,
	))
}

func TestLoadServeConfigReturnsZeroIfNoConfigFile(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.LoadServeConfig(root)
	require.NoError(t, err)
	assert.Equal(t, "", cfg.Root)
	assert.Nil(t, cfg.Port)
	assert.False(t, cfg.MCP)
	assert.False(t, cfg.NoHTTP)
	assert.Equal(t, "", cfg.Workdir)
}

func TestLoadServeConfigReturnsZeroIfNoServeSection(t *testing.T) {
	root := t.TempDir()
	writeServeConfig(t, root, "[raymond]\nbudget = 50.0\n")
	cfg, err := config.LoadServeConfig(root)
	require.NoError(t, err)
	assert.Equal(t, "", cfg.Root)
	assert.Nil(t, cfg.Port)
}

func TestLoadServeConfigParsesAllFields(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "wf"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "wd"), 0o755))
	writeServeConfig(t, root, strings.Join([]string{
		"[raymond.serve]",
		`root = "wf"`,
		"port = 9000",
		"mcp = true",
		"no_http = true",
		`workdir = "wd"`,
		"max_file_size = 1024",
		"max_total_size = 4096",
		"max_file_count = 8",
	}, "\n"))

	cfg, err := config.LoadServeConfig(root)
	require.NoError(t, err)
	wantRoot, _ := filepath.EvalSymlinks(filepath.Join(root, "wf"))
	gotRoot, _ := filepath.EvalSymlinks(cfg.Root)
	assert.Equal(t, wantRoot, gotRoot)
	require.NotNil(t, cfg.Port)
	assert.Equal(t, 9000, *cfg.Port)
	assert.True(t, cfg.MCP)
	assert.True(t, cfg.NoHTTP)
	wantWD, _ := filepath.EvalSymlinks(filepath.Join(root, "wd"))
	gotWD, _ := filepath.EvalSymlinks(cfg.Workdir)
	assert.Equal(t, wantWD, gotWD)
	assert.Equal(t, int64(1024), cfg.MaxFileSize)
	assert.Equal(t, int64(4096), cfg.MaxTotalSize)
	assert.Equal(t, 8, cfg.MaxFileCount)
}

func TestLoadServeConfigUploadCapsRejectsNonPositive(t *testing.T) {
	for _, key := range []string{"max_file_size", "max_total_size", "max_file_count"} {
		for _, v := range []int{0, -1} {
			root := t.TempDir()
			writeServeConfig(t, root, fmt.Sprintf("[raymond.serve]\n%s = %d\n", key, v))
			_, err := config.LoadServeConfig(root)
			require.Error(t, err, "%s=%d must be rejected", key, v)
			assert.Contains(t, err.Error(), key)
		}
	}
}

func TestLoadServeConfigUploadCapsRejectsWrongType(t *testing.T) {
	for _, key := range []string{"max_file_size", "max_total_size", "max_file_count"} {
		root := t.TempDir()
		writeServeConfig(t, root, fmt.Sprintf("[raymond.serve]\n%s = \"big\"\n", key))
		_, err := config.LoadServeConfig(root)
		require.Error(t, err, "%s must reject string", key)
		assert.Contains(t, err.Error(), key)
		assert.Contains(t, err.Error(), "integer")
	}
}

func TestLoadServeConfigResolvesRelativeRootAgainstConfigDir(t *testing.T) {
	// Config file is at <root>/.raymond/config.toml.
	// A relative root="workflows" should resolve to <root>/workflows,
	// NOT to <cwd>/workflows.
	root := t.TempDir()
	writeServeConfig(t, root, "[raymond.serve]\nroot = \"workflows\"\n")

	// Invoke from a subdirectory to ensure cwd is not used as the base.
	sub := filepath.Join(root, "subdir", "deep")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	cfg, err := config.LoadServeConfig(sub)
	require.NoError(t, err)
	want, _ := filepath.EvalSymlinks(root)
	got, _ := filepath.EvalSymlinks(filepath.Dir(cfg.Root))
	assert.Equal(t, want, got)
	assert.Equal(t, "workflows", filepath.Base(cfg.Root))
}

func TestLoadServeConfigKeepsAbsoluteRoot(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, "elsewhere")
	require.NoError(t, os.Mkdir(abs, 0o755))
	writeServeConfig(t, root, fmt.Sprintf("[raymond.serve]\nroot = %q\n", abs))

	cfg, err := config.LoadServeConfig(root)
	require.NoError(t, err)
	want, _ := filepath.EvalSymlinks(abs)
	got, _ := filepath.EvalSymlinks(cfg.Root)
	assert.Equal(t, want, got)
}

func TestLoadServeConfigRootMustBeString(t *testing.T) {
	root := t.TempDir()
	writeServeConfig(t, root, "[raymond.serve]\nroot = 42\n")
	_, err := config.LoadServeConfig(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "root")
	assert.Contains(t, err.Error(), "string")
}

func TestLoadServeConfigRejectsRootArray(t *testing.T) {
	// Per design, only a single root is allowed in TOML.
	root := t.TempDir()
	writeServeConfig(t, root, "[raymond.serve]\nroot = [\"a\", \"b\"]\n")
	_, err := config.LoadServeConfig(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "root")
}

func TestLoadServeConfigPortMustBeInteger(t *testing.T) {
	root := t.TempDir()
	writeServeConfig(t, root, "[raymond.serve]\nport = \"9000\"\n")
	_, err := config.LoadServeConfig(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "port")
	assert.Contains(t, err.Error(), "integer")
}

func TestLoadServeConfigPortRangeRejected(t *testing.T) {
	root := t.TempDir()
	for _, p := range []int{0, -1, 65536, 100000} {
		writeServeConfig(t, root, fmt.Sprintf("[raymond.serve]\nport = %d\n", p))
		_, err := config.LoadServeConfig(root)
		require.Error(t, err, "port %d should be rejected", p)
		assert.Contains(t, err.Error(), "port")
	}
}

func TestLoadServeConfigBoolFlagsWrongType(t *testing.T) {
	for _, flag := range []string{"mcp", "no_http"} {
		root := t.TempDir()
		writeServeConfig(t, root, fmt.Sprintf("[raymond.serve]\n%s = \"true\"\n", flag))
		_, err := config.LoadServeConfig(root)
		require.Error(t, err, "flag %q should fail", flag)
		assert.Contains(t, err.Error(), flag)
		assert.Contains(t, err.Error(), "boolean")
	}
}

func TestLoadServeConfigUnknownKeysIgnored(t *testing.T) {
	root := t.TempDir()
	writeServeConfig(t, root, "[raymond.serve]\nport = 9000\nfuture_option = \"x\"\n")
	cfg, err := config.LoadServeConfig(root)
	require.NoError(t, err)
	require.NotNil(t, cfg.Port)
	assert.Equal(t, 9000, *cfg.Port)
}

func TestLoadServeConfigErrorIfServeSectionScalar(t *testing.T) {
	root := t.TempDir()
	writeServeConfig(t, root, "[raymond]\nserve = \"oops\"\n")
	_, err := config.LoadServeConfig(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[raymond.serve]")
}

// ----------------------------------------------------------------------------
// MergeServeConfig
// ----------------------------------------------------------------------------

func TestMergeServeConfigCLIRootsAppendedToFileRoot(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a")
	b := filepath.Join(tmp, "b")
	c := filepath.Join(tmp, "c")
	for _, d := range []string{a, b, c} {
		require.NoError(t, os.Mkdir(d, 0o755))
	}
	file := config.ServeFileConfig{Root: a}
	args := config.ServeCLIArgs{Roots: []string{b, c}}

	merged := config.MergeServeConfig(file, args)
	require.Len(t, merged.Roots, 3)
	assert.Equal(t, a, merged.Roots[0])
	assert.Equal(t, b, merged.Roots[1])
	assert.Equal(t, c, merged.Roots[2])
}

func TestMergeServeConfigDedupesRootsByAbsolutePath(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "shared")
	require.NoError(t, os.Mkdir(a, 0o755))
	file := config.ServeFileConfig{Root: a}
	// CLI passes the same path again; should be deduped.
	args := config.ServeCLIArgs{Roots: []string{a}}

	merged := config.MergeServeConfig(file, args)
	require.Len(t, merged.Roots, 1)
	assert.Equal(t, a, merged.Roots[0])
}

func TestMergeServeConfigCLIOnlyWhenNoFileRoot(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a")
	require.NoError(t, os.Mkdir(a, 0o755))
	file := config.ServeFileConfig{}
	args := config.ServeCLIArgs{Roots: []string{a}}

	merged := config.MergeServeConfig(file, args)
	require.Len(t, merged.Roots, 1)
}

func TestMergeServeConfigFileRootOnlyWhenNoCLI(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a")
	require.NoError(t, os.Mkdir(a, 0o755))
	file := config.ServeFileConfig{Root: a}
	args := config.ServeCLIArgs{}

	merged := config.MergeServeConfig(file, args)
	require.Equal(t, []string{a}, merged.Roots)
}

func TestMergeServeConfigPortCLIOverridesFile(t *testing.T) {
	cliPort := 7000
	filePort := 9000
	merged := config.MergeServeConfig(
		config.ServeFileConfig{Port: &filePort},
		config.ServeCLIArgs{Port: &cliPort},
	)
	assert.Equal(t, 7000, merged.Port)
}

func TestMergeServeConfigPortFromFileWhenCLIUnset(t *testing.T) {
	filePort := 9000
	merged := config.MergeServeConfig(
		config.ServeFileConfig{Port: &filePort},
		config.ServeCLIArgs{},
	)
	assert.Equal(t, 9000, merged.Port)
}

func TestMergeServeConfigPortDefaultsTo8080(t *testing.T) {
	merged := config.MergeServeConfig(
		config.ServeFileConfig{},
		config.ServeCLIArgs{},
	)
	assert.Equal(t, 8080, merged.Port)
}

func TestMergeServeConfigBoolsCLIWinsWhenTrue(t *testing.T) {
	merged := config.MergeServeConfig(
		config.ServeFileConfig{MCP: false, NoHTTP: false},
		config.ServeCLIArgs{MCP: true, NoHTTP: true},
	)
	assert.True(t, merged.MCP)
	assert.True(t, merged.NoHTTP)
}

func TestMergeServeConfigBoolsFromFileWhenCLIFalse(t *testing.T) {
	merged := config.MergeServeConfig(
		config.ServeFileConfig{MCP: true, NoHTTP: true},
		config.ServeCLIArgs{},
	)
	assert.True(t, merged.MCP)
	assert.True(t, merged.NoHTTP)
}

func TestMergeServeConfigWorkdirCLIWins(t *testing.T) {
	merged := config.MergeServeConfig(
		config.ServeFileConfig{Workdir: "/file/wd"},
		config.ServeCLIArgs{Workdir: "/cli/wd"},
	)
	assert.Equal(t, "/cli/wd", merged.Workdir)
}

func TestMergeServeConfigWorkdirFromFile(t *testing.T) {
	merged := config.MergeServeConfig(
		config.ServeFileConfig{Workdir: "/file/wd"},
		config.ServeCLIArgs{},
	)
	assert.Equal(t, "/file/wd", merged.Workdir)
}

func TestMergeServeConfigUploadCapsCLIWinsWhenPositive(t *testing.T) {
	merged := config.MergeServeConfig(
		config.ServeFileConfig{MaxFileSize: 100, MaxTotalSize: 200, MaxFileCount: 3},
		config.ServeCLIArgs{MaxFileSize: 1000, MaxTotalSize: 2000, MaxFileCount: 30},
	)
	assert.Equal(t, int64(1000), merged.MaxFileSize)
	assert.Equal(t, int64(2000), merged.MaxTotalSize)
	assert.Equal(t, 30, merged.MaxFileCount)
}

func TestMergeServeConfigUploadCapsFromFileWhenCLIZero(t *testing.T) {
	merged := config.MergeServeConfig(
		config.ServeFileConfig{MaxFileSize: 100, MaxTotalSize: 200, MaxFileCount: 3},
		config.ServeCLIArgs{},
	)
	assert.Equal(t, int64(100), merged.MaxFileSize)
	assert.Equal(t, int64(200), merged.MaxTotalSize)
	assert.Equal(t, 3, merged.MaxFileCount)
}

func TestMergeServeConfigUploadCapsZeroWhenNeitherSet(t *testing.T) {
	// Zero in the merged config signals "unset" so the daemon falls
	// through to its hardcoded defaults — the resolver, not the merge
	// step, owns the final fallback.
	merged := config.MergeServeConfig(
		config.ServeFileConfig{},
		config.ServeCLIArgs{},
	)
	assert.Equal(t, int64(0), merged.MaxFileSize)
	assert.Equal(t, int64(0), merged.MaxTotalSize)
	assert.Equal(t, 0, merged.MaxFileCount)
}
