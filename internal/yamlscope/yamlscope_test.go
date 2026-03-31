package yamlscope

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/policy"
)

// writeTempYAML writes content to a temp .yaml file and returns its path.
func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "test-*.yaml")
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

// writeTempYML writes content to a temp .yml file and returns its path.
func writeTempYML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "test-*.yml")
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

// --- IsYamlScope ---

func TestIsYamlScope_YamlExtension(t *testing.T) {
	assert.True(t, IsYamlScope("workflow.yaml"))
}

func TestIsYamlScope_YmlExtension(t *testing.T) {
	assert.True(t, IsYamlScope("workflow.yml"))
}

func TestIsYamlScope_UppercaseYAML(t *testing.T) {
	assert.True(t, IsYamlScope("workflow.YAML"))
}

func TestIsYamlScope_UppercaseYML(t *testing.T) {
	assert.True(t, IsYamlScope("workflow.YML"))
}

func TestIsYamlScope_MixedCase(t *testing.T) {
	assert.True(t, IsYamlScope("workflow.Yaml"))
	assert.True(t, IsYamlScope("workflow.yMl"))
}

func TestIsYamlScope_Directory(t *testing.T) {
	assert.False(t, IsYamlScope("/path/to/dir"))
}

func TestIsYamlScope_ZipFile(t *testing.T) {
	assert.False(t, IsYamlScope("workflow.zip"))
}

func TestIsYamlScope_NoExtension(t *testing.T) {
	assert.False(t, IsYamlScope("workflow"))
}

func TestIsYamlScope_OtherExtension(t *testing.T) {
	assert.False(t, IsYamlScope("workflow.json"))
}

// --- Parse: valid workflows ---

func TestParse_ValidMarkdownState(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello, how can I help?"
`)
	wf, err := Parse(p)
	require.NoError(t, err)
	require.NotNil(t, wf)
	assert.Len(t, wf.States, 1)
	assert.Equal(t, "Hello, how can I help?", wf.States["greet"].Prompt)
	assert.Equal(t, []string{"greet"}, wf.StateOrder)
}

func TestParse_ValidScriptState(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    sh: "make build"
`)
	wf, err := Parse(p)
	require.NoError(t, err)
	assert.Len(t, wf.States, 1)
	assert.Equal(t, "make build", wf.States["build"].Sh)
}

func TestParse_MixedWorkflow(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello!"
    model: sonnet
  build:
    sh: "make build"
    bat: "nmake build"
`)
	wf, err := Parse(p)
	require.NoError(t, err)
	assert.Len(t, wf.States, 2)
	assert.Equal(t, []string{"greet", "build"}, wf.StateOrder)
	assert.Equal(t, "Hello!", wf.States["greet"].Prompt)
	assert.Equal(t, "sonnet", wf.States["greet"].Model)
	assert.Equal(t, "make build", wf.States["build"].Sh)
	assert.Equal(t, "nmake build", wf.States["build"].Bat)
}

func TestParse_MarkdownWithAllPolicyFields(t *testing.T) {
	p := writeTempYAML(t, `
states:
  ask:
    prompt: "What do you need?"
    allowed_transitions:
      - tag: goto
        target: done
    model: opus
    effort: high
`)
	wf, err := Parse(p)
	require.NoError(t, err)
	st := wf.States["ask"]
	assert.Equal(t, "What do you need?", st.Prompt)
	assert.Len(t, st.AllowedTransitions, 1)
	assert.Equal(t, "goto", st.AllowedTransitions[0]["tag"])
	assert.Equal(t, "done", st.AllowedTransitions[0]["target"])
	assert.Equal(t, "opus", st.Model)
	assert.Equal(t, "high", st.Effort)
}

func TestParse_ScriptMultiplePlatforms(t *testing.T) {
	p := writeTempYAML(t, `
states:
  deploy:
    sh: "deploy.sh"
    ps1: "deploy.ps1"
    bat: "deploy.bat"
`)
	wf, err := Parse(p)
	require.NoError(t, err)
	st := wf.States["deploy"]
	assert.Equal(t, "deploy.sh", st.Sh)
	assert.Equal(t, "deploy.ps1", st.Ps1)
	assert.Equal(t, "deploy.bat", st.Bat)
}

// --- Parse: validation errors ---

func TestParse_NoStatesKey(t *testing.T) {
	p := writeTempYAML(t, `
version: 1
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "missing 'states' key")
}

func TestParse_EmptyStates(t *testing.T) {
	p := writeTempYAML(t, `
states:
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
}

func TestParse_EmptyStatesMap(t *testing.T) {
	p := writeTempYAML(t, `
states: {}
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "must not be empty")
}

func TestParse_DualTypeState(t *testing.T) {
	p := writeTempYAML(t, `
states:
  broken:
    prompt: "Hello"
    sh: "echo hi"
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "both 'prompt' and script keys")
}

func TestParse_NeitherTypeState(t *testing.T) {
	p := writeTempYAML(t, `
states:
  empty:
    model: sonnet
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "neither 'prompt' nor script keys")
}

func TestParse_ScriptWithAllowedTransitions(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    sh: "make"
    allowed_transitions:
      - tag: goto
        target: next
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "must not have 'allowed_transitions'")
}

func TestParse_ScriptWithModel(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    sh: "make"
    model: sonnet
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "must not have 'model'")
}

func TestParse_ScriptWithEffort(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    sh: "make"
    effort: high
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "must not have 'effort'")
}

func TestParse_EmptyStateName(t *testing.T) {
	// Use a YAML key that is an empty string.
	p := writeTempYAML(t, `
states:
  "":
    prompt: "Hello"
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "empty state name")
}

func TestParse_StateNameWithSlash(t *testing.T) {
	p := writeTempYAML(t, `
states:
  "path/traversal":
    prompt: "Hello"
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "path separators")
}

func TestParse_StateNameWithBackslash(t *testing.T) {
	p := writeTempYAML(t, `
states:
  "path\\traversal":
    prompt: "Hello"
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "path separators")
}

func TestParse_DuplicateStateName(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello!"
  greet:
    prompt: "Hi again!"
`)
	_, err := Parse(p)
	require.Error(t, err)
	// yaml.v3 catches duplicate mapping keys at the unmarshal level,
	// returning a YamlParseError before our own duplicate check runs.
	assert.Contains(t, err.Error(), "already defined")
}

func TestParse_MalformedYAML(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    - this is not valid
  [broken
`)
	_, err := Parse(p)
	require.Error(t, err)
	var pe *YamlParseError
	assert.ErrorAs(t, err, &pe)
}

func TestParse_StatesAtRootLevel(t *testing.T) {
	p := writeTempYAML(t, `
greet:
  prompt: "Hello!"
build:
  sh: "make build"
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "states appear to be defined at root level")
}

func TestParse_EmptyFile(t *testing.T) {
	p := writeTempYAML(t, ``)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "missing 'states' key")
}

func TestParse_FileNotFound(t *testing.T) {
	_, err := Parse("/nonexistent/path/workflow.yaml")
	require.Error(t, err)
	var pe *YamlParseError
	assert.ErrorAs(t, err, &pe)
}

// --- ListFiles ---

func TestListFiles_MarkdownState(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello!"
`)
	files, err := ListFiles(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"greet.md"}, files)
}

func TestListFiles_ScriptState(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    sh: "make build"
`)
	files, err := ListFiles(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"build.sh"}, files)
}

func TestListFiles_ScriptMultiplePlatforms(t *testing.T) {
	p := writeTempYAML(t, `
states:
  deploy:
    sh: "./deploy.sh"
    ps1: "deploy.ps1"
    bat: "deploy.bat"
`)
	files, err := ListFiles(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"deploy.sh", "deploy.ps1", "deploy.bat"}, files)
}

func TestListFiles_MixedWorkflow(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello!"
  build:
    sh: "make build"
    bat: "nmake build"
  review:
    prompt: "Review the results."
`)
	files, err := ListFiles(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"greet.md", "build.sh", "build.bat", "review.md"}, files)
}

func TestListFiles_ShAndPs1(t *testing.T) {
	p := writeTempYAML(t, `
states:
  run:
    sh: "run.sh"
    ps1: "run.ps1"
`)
	files, err := ListFiles(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"run.sh", "run.ps1"}, files)
}

// --- ReadText: markdown states ---

func TestReadText_MarkdownNoPolicyFields(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello, how can I help?"
`)
	content, err := ReadText(p, "greet.md")
	require.NoError(t, err)
	assert.Equal(t, "Hello, how can I help?", content)
}

func TestReadText_MarkdownWithAllPolicyFields(t *testing.T) {
	p := writeTempYAML(t, `
states:
  ask:
    prompt: "What do you need?"
    allowed_transitions:
      - tag: goto
        target: done
    model: opus
    effort: high
`)
	content, err := ReadText(p, "ask.md")
	require.NoError(t, err)
	assert.Contains(t, content, "---\n")
	assert.Contains(t, content, "What do you need?")

	// Verify round-trip through policy.ParseFrontmatter.
	pol, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, pol)
	assert.Equal(t, "What do you need?", body)
	assert.Len(t, pol.AllowedTransitions, 1)
	assert.Equal(t, "goto", pol.AllowedTransitions[0]["tag"])
	assert.Equal(t, "done", pol.AllowedTransitions[0]["target"])
	assert.Equal(t, "opus", pol.Model)
	assert.Equal(t, "high", pol.Effort)
}

func TestReadText_MarkdownWithModelOnly(t *testing.T) {
	p := writeTempYAML(t, `
states:
  think:
    prompt: "Think carefully."
    model: opus
`)
	content, err := ReadText(p, "think.md")
	require.NoError(t, err)

	pol, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, pol)
	assert.Equal(t, "Think carefully.", body)
	assert.Equal(t, "opus", pol.Model)
	assert.Empty(t, pol.Effort)
}

func TestReadText_MarkdownWithEffortOnly(t *testing.T) {
	p := writeTempYAML(t, `
states:
  work:
    prompt: "Work hard."
    effort: high
`)
	content, err := ReadText(p, "work.md")
	require.NoError(t, err)

	pol, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, pol)
	assert.Equal(t, "Work hard.", body)
	assert.Equal(t, "high", pol.Effort)
}

func TestReadText_MarkdownMultipleTransitions(t *testing.T) {
	p := writeTempYAML(t, `
states:
  decide:
    prompt: "Choose a path."
    allowed_transitions:
      - tag: goto
        target: left
      - tag: goto
        target: right
      - tag: result
        payload: done
`)
	content, err := ReadText(p, "decide.md")
	require.NoError(t, err)

	pol, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, pol)
	assert.Equal(t, "Choose a path.", body)
	assert.Len(t, pol.AllowedTransitions, 3)
}

// --- ReadText: script states ---

func TestReadText_ShScript(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    sh: |
      #!/bin/bash
      make build
`)
	content, err := ReadText(p, "build.sh")
	require.NoError(t, err)
	assert.Contains(t, content, "#!/bin/bash")
	assert.Contains(t, content, "make build")
}

func TestReadText_Ps1Script(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    ps1: "Write-Host 'building'"
`)
	content, err := ReadText(p, "build.ps1")
	require.NoError(t, err)
	assert.Equal(t, "Write-Host 'building'", content)
}

func TestReadText_BatScript(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    bat: "echo building"
`)
	content, err := ReadText(p, "build.bat")
	require.NoError(t, err)
	assert.Equal(t, "echo building", content)
}

// --- ReadText: errors ---

func TestReadText_MissingFile(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello!"
`)
	_, err := ReadText(p, "nonexistent.md")
	require.Error(t, err)
	var nf *YamlFileNotFoundError
	assert.ErrorAs(t, err, &nf)
}

func TestReadText_WrongExtension(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello!"
`)
	// greet is a markdown state, so greet.sh should not exist.
	_, err := ReadText(p, "greet.sh")
	require.Error(t, err)
	var nf *YamlFileNotFoundError
	assert.ErrorAs(t, err, &nf)
}

func TestReadText_PathTraversal(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello!"
`)
	_, err := ReadText(p, "../etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path separators")
}

// --- FileExists ---

func TestFileExists_ExistingMarkdown(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello!"
`)
	exists, err := FileExists(p, "greet.md")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestFileExists_ExistingScript(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    sh: "make build"
`)
	exists, err := FileExists(p, "build.sh")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestFileExists_NonExisting(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello!"
`)
	exists, err := FileExists(p, "nonexistent.md")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestFileExists_WrongExtension(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello!"
`)
	exists, err := FileExists(p, "greet.sh")
	require.NoError(t, err)
	assert.False(t, exists)
}

// --- ExtractScript ---

func TestExtractScript_HappyPath(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    sh: |
      #!/bin/bash
      make build
`)
	tmpPath, err := ExtractScript(p, "build.sh")
	require.NoError(t, err)
	defer os.Remove(tmpPath)

	assert.Equal(t, ".sh", filepath.Ext(tmpPath))

	content, err := os.ReadFile(tmpPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "#!/bin/bash")
	assert.Contains(t, string(content), "make build")
}

func TestExtractScript_Ps1(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    ps1: "Write-Host 'done'"
`)
	tmpPath, err := ExtractScript(p, "build.ps1")
	require.NoError(t, err)
	defer os.Remove(tmpPath)

	assert.Equal(t, ".ps1", filepath.Ext(tmpPath))

	content, err := os.ReadFile(tmpPath)
	require.NoError(t, err)
	assert.Equal(t, "Write-Host 'done'", string(content))
}

func TestExtractScript_Bat(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    bat: "echo done"
`)
	tmpPath, err := ExtractScript(p, "build.bat")
	require.NoError(t, err)
	defer os.Remove(tmpPath)

	assert.Equal(t, ".bat", filepath.Ext(tmpPath))

	content, err := os.ReadFile(tmpPath)
	require.NoError(t, err)
	assert.Equal(t, "echo done", string(content))
}

func TestExtractScript_MissingFile(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    sh: "make build"
`)
	_, err := ExtractScript(p, "nonexistent.sh")
	require.Error(t, err)
	var nf *YamlFileNotFoundError
	assert.ErrorAs(t, err, &nf)
}

func TestExtractScript_MarkdownState(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello!"
`)
	_, err := ExtractScript(p, "greet.md")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot extract script from markdown state")
}

// --- Edge cases ---

func TestParse_MultilinePromptWithSpecialChars(t *testing.T) {
	p := writeTempYAML(t, `
states:
  complex:
    prompt: |
      Hello! Here's a prompt with:
      - YAML special chars: { } [ ] : > |
      - Quotes: "double" and 'single'
      - Unicode: émojis 🎉
      - Newlines preserved
`)
	wf, err := Parse(p)
	require.NoError(t, err)
	assert.Contains(t, wf.States["complex"].Prompt, "YAML special chars")
	assert.Contains(t, wf.States["complex"].Prompt, `"double"`)
}

func TestReadText_MultilinePromptRoundTrip(t *testing.T) {
	p := writeTempYAML(t, `
states:
  complex:
    prompt: |
      Line 1
      Line 2
      Line 3
    model: sonnet
`)
	content, err := ReadText(p, "complex.md")
	require.NoError(t, err)

	pol, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, pol)
	assert.Equal(t, "sonnet", pol.Model)
	assert.Contains(t, body, "Line 1")
	assert.Contains(t, body, "Line 2")
	assert.Contains(t, body, "Line 3")
}

func TestParse_YmlExtension(t *testing.T) {
	p := writeTempYML(t, `
states:
  greet:
    prompt: "Hello from yml!"
`)
	wf, err := Parse(p)
	require.NoError(t, err)
	assert.Equal(t, "Hello from yml!", wf.States["greet"].Prompt)
}

func TestReadText_TransitionsWithPayload(t *testing.T) {
	p := writeTempYAML(t, `
states:
  judge:
    prompt: "Evaluate."
    allowed_transitions:
      - tag: result
        payload: success
      - tag: result
        payload: failure
`)
	content, err := ReadText(p, "judge.md")
	require.NoError(t, err)

	pol, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, pol)
	assert.Equal(t, "Evaluate.", body)
	assert.Len(t, pol.AllowedTransitions, 2)
}

func TestListFiles_OrderPreserved(t *testing.T) {
	p := writeTempYAML(t, `
states:
  zeta:
    prompt: "Zeta"
  alpha:
    prompt: "Alpha"
  middle:
    sh: "run.sh"
`)
	files, err := ListFiles(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"zeta.md", "alpha.md", "middle.sh"}, files)
}

func TestValidateFilename_Clean(t *testing.T) {
	assert.NoError(t, validateFilename("greet.md"))
	assert.NoError(t, validateFilename("build.sh"))
}

func TestValidateFilename_PathSeparators(t *testing.T) {
	err := validateFilename("../etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path separators")

	err = validateFilename(`dir\file`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path separators")
}

func TestParse_StatesNullValue(t *testing.T) {
	p := writeTempYAML(t, `
states: null
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
}

func TestReadText_MarkdownWithTransitionsAttributeOrder(t *testing.T) {
	// Verify that allowed_transitions with extra attributes round-trip correctly.
	p := writeTempYAML(t, `
states:
  caller:
    prompt: "Call a function."
    allowed_transitions:
      - tag: call
        target: worker
        return: caller
`)
	content, err := ReadText(p, "caller.md")
	require.NoError(t, err)

	pol, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, pol)
	assert.Equal(t, "Call a function.", body)
	assert.Len(t, pol.AllowedTransitions, 1)
	assert.Equal(t, "call", pol.AllowedTransitions[0]["tag"])
	assert.Equal(t, "worker", pol.AllowedTransitions[0]["target"])
	assert.Equal(t, "caller", pol.AllowedTransitions[0]["return"])
}

func TestParse_StatesAtRootWithScripts(t *testing.T) {
	p := writeTempYAML(t, `
build:
  sh: "make"
test:
  sh: "make test"
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "states appear to be defined at root level")
}

func TestReadText_ScriptStateWrongPlatform(t *testing.T) {
	// State only has sh, requesting ps1 should fail.
	p := writeTempYAML(t, `
states:
  build:
    sh: "make build"
`)
	_, err := ReadText(p, "build.ps1")
	require.Error(t, err)
	var nf *YamlFileNotFoundError
	assert.ErrorAs(t, err, &nf)
}

func TestFileExists_PathTraversal(t *testing.T) {
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello!"
`)
	_, err := FileExists(p, "../evil.md")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path separators")
}

func TestExtractScript_PathTraversal(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    sh: "make"
`)
	_, err := ExtractScript(p, "../evil.sh")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path separators")
}

func TestReadText_MarkdownPromptNoTrailingNewline(t *testing.T) {
	// Ensure prompts without trailing newlines work.
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "No trailing newline"
    model: sonnet
`)
	content, err := ReadText(p, "greet.md")
	require.NoError(t, err)

	pol, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, pol)
	assert.Equal(t, "No trailing newline", body)
}

func TestReadText_MarkdownEmptyTransitions(t *testing.T) {
	// allowed_transitions present but empty should not produce frontmatter for it.
	p := writeTempYAML(t, `
states:
  greet:
    prompt: "Hello!"
    allowed_transitions: []
    model: opus
`)
	content, err := ReadText(p, "greet.md")
	require.NoError(t, err)

	pol, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, pol)
	assert.Equal(t, "Hello!", body)
	assert.Equal(t, "opus", pol.Model)
}

func TestListFiles_MultipleScriptPlatformsOrdering(t *testing.T) {
	// Verify the order is sh, ps1, bat (matching scriptExtensions order).
	p := writeTempYAML(t, `
states:
  run:
    bat: "run.bat"
    ps1: "run.ps1"
    sh: "run.sh"
`)
	files, err := ListFiles(p)
	require.NoError(t, err)
	// Order should follow code order: sh, ps1, bat regardless of YAML key order.
	assert.Equal(t, []string{"run.sh", "run.ps1", "run.bat"}, files)
}

func TestParse_StatesKeyAsString(t *testing.T) {
	// states: "not a mapping" should error.
	p := writeTempYAML(t, `
states: "not a mapping"
`)
	_, err := Parse(p)
	require.Error(t, err)
}

func TestReadText_MarkdownWithOnlyTransitions(t *testing.T) {
	p := writeTempYAML(t, `
states:
  step:
    prompt: "Do the thing."
    allowed_transitions:
      - tag: goto
        target: next
`)
	content, err := ReadText(p, "step.md")
	require.NoError(t, err)

	// Must have frontmatter delimiters.
	assert.True(t, strings.HasPrefix(content, "---\n"))

	pol, body, err := policy.ParseFrontmatter(content)
	require.NoError(t, err)
	require.NotNil(t, pol)
	assert.Equal(t, "Do the thing.", body)
	assert.Len(t, pol.AllowedTransitions, 1)
	assert.Empty(t, pol.Model)
	assert.Empty(t, pol.Effort)
}

// --- Parse: timeout field ---

func TestParse_TimeoutIntegerValue(t *testing.T) {
	p := writeTempYAML(t, `
states:
  work:
    prompt: "Do something"
    timeout: 30
`)
	wf, err := Parse(p)
	require.NoError(t, err)
	st := wf.States["work"]
	require.NotNil(t, st.Timeout)
	assert.Equal(t, 30.0, *st.Timeout)
}

func TestParse_TimeoutFloatValue(t *testing.T) {
	p := writeTempYAML(t, `
states:
  work:
    prompt: "Do something"
    timeout: 30.5
`)
	wf, err := Parse(p)
	require.NoError(t, err)
	st := wf.States["work"]
	require.NotNil(t, st.Timeout)
	assert.Equal(t, 30.5, *st.Timeout)
}

func TestParse_TimeoutZeroIsExplicit(t *testing.T) {
	p := writeTempYAML(t, `
states:
  work:
    prompt: "Do something"
    timeout: 0
`)
	wf, err := Parse(p)
	require.NoError(t, err)
	st := wf.States["work"]
	require.NotNil(t, st.Timeout)
	assert.Equal(t, 0.0, *st.Timeout)
}

func TestParse_TimeoutOmittedIsNil(t *testing.T) {
	p := writeTempYAML(t, `
states:
  work:
    prompt: "Do something"
`)
	wf, err := Parse(p)
	require.NoError(t, err)
	st := wf.States["work"]
	assert.Nil(t, st.Timeout)
}

func TestParse_TimeoutNegativeReturnsValidationError(t *testing.T) {
	p := writeTempYAML(t, `
states:
  work:
    prompt: "Do something"
    timeout: -1
`)
	_, err := Parse(p)
	require.Error(t, err)
	var ve *YamlValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Contains(t, err.Error(), "work")
	assert.Contains(t, err.Error(), "timeout")
}

func TestParse_TimeoutValidOnMarkdownState(t *testing.T) {
	p := writeTempYAML(t, `
states:
  work:
    prompt: "Do something"
    timeout: 45.5
    model: sonnet
`)
	wf, err := Parse(p)
	require.NoError(t, err)
	st := wf.States["work"]
	require.NotNil(t, st.Timeout)
	assert.Equal(t, 45.5, *st.Timeout)
	assert.Equal(t, "sonnet", st.Model)
}

func TestParse_TimeoutValidOnScriptState(t *testing.T) {
	p := writeTempYAML(t, `
states:
  build:
    sh: "make build"
    timeout: 120
`)
	wf, err := Parse(p)
	require.NoError(t, err)
	st := wf.States["build"]
	require.NotNil(t, st.Timeout)
	assert.Equal(t, 120.0, *st.Timeout)
}

// --- GetStateTimeout accessor ---

func TestGetStateTimeout_StateWithTimeout(t *testing.T) {
	p := writeTempYAML(t, `
states:
  work:
    prompt: "Do something"
    timeout: 30
`)
	result, err := GetStateTimeout(p, "work")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 30.0, *result)
}

func TestGetStateTimeout_StateWithZeroTimeout(t *testing.T) {
	p := writeTempYAML(t, `
states:
  work:
    prompt: "Do something"
    timeout: 0
`)
	result, err := GetStateTimeout(p, "work")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0.0, *result)
}

func TestGetStateTimeout_StateWithoutTimeout(t *testing.T) {
	p := writeTempYAML(t, `
states:
  work:
    prompt: "Do something"
`)
	result, err := GetStateTimeout(p, "work")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestGetStateTimeout_StateNotPresent(t *testing.T) {
	p := writeTempYAML(t, `
states:
  work:
    prompt: "Do something"
    timeout: 30
`)
	result, err := GetStateTimeout(p, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestGetStateTimeout_ParseError(t *testing.T) {
	result, err := GetStateTimeout("/nonexistent/path.yaml", "work")
	assert.Error(t, err)
	assert.Nil(t, result)
}

func TestGetStateTimeout_MultipleStates(t *testing.T) {
	p := writeTempYAML(t, `
states:
  first:
    prompt: "First"
    timeout: 60
  second:
    prompt: "Second"
    timeout: 30
  third:
    prompt: "Third"
`)
	// Query first state.
	result1, err := GetStateTimeout(p, "first")
	require.NoError(t, err)
	require.NotNil(t, result1)
	assert.Equal(t, 60.0, *result1)

	// Query second state.
	result2, err := GetStateTimeout(p, "second")
	require.NoError(t, err)
	require.NotNil(t, result2)
	assert.Equal(t, 30.0, *result2)

	// Query third state (no timeout).
	result3, err := GetStateTimeout(p, "third")
	require.NoError(t, err)
	assert.Nil(t, result3)
}
