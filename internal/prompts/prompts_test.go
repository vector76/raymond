package prompts

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// skipWindows/skipUnix mirror the pattern used in other packages.
func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only test")
	}
}

func skipUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}
}

// makeZip creates a temp zip file with the given name→content map.
func makeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "scope.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// --------------------------------------------------------------------------
// LoadPrompt
// --------------------------------------------------------------------------

func TestLoadPrompt_ReturnsFileContents(t *testing.T) {
	dir := t.TempDir()
	content := "# Start\n\nThis is the start prompt."
	if err := os.WriteFile(filepath.Join(dir, "START.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	body, pol, err := LoadPrompt(dir, "START.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != content {
		t.Errorf("body = %q, want %q", body, content)
	}
	if pol != nil {
		t.Errorf("policy = %v, want nil (no frontmatter)", pol)
	}
}

func TestLoadPrompt_ParsesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	content := "---\nallowed_transitions:\n  - tag: goto\n    target: NEXT\n---\n# The prompt body.\n"
	if err := os.WriteFile(filepath.Join(dir, "STATE.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	body, pol, err := LoadPrompt(dir, "STATE.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pol == nil {
		t.Fatal("expected non-nil policy from frontmatter")
	}
	if len(pol.AllowedTransitions) != 1 {
		t.Errorf("expected 1 allowed transition, got %d", len(pol.AllowedTransitions))
	}
	// Body should not contain the frontmatter.
	if strings.Contains(body, "allowed_transitions") {
		t.Error("body should not contain frontmatter YAML")
	}
	if !strings.Contains(body, "# The prompt body.") {
		t.Error("body should contain the markdown content")
	}
}

func TestLoadPrompt_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, _, err := LoadPrompt(dir, "NONEXISTENT.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadPrompt_PathSeparatorForwardSlash(t *testing.T) {
	dir := t.TempDir()
	_, _, err := LoadPrompt(dir, "subdir/file.md")
	if err == nil {
		t.Fatal("expected error for path separator in filename")
	}
	if !strings.Contains(err.Error(), "path separator") {
		t.Errorf("error should mention 'path separator', got: %v", err)
	}
}

func TestLoadPrompt_PathSeparatorDotDot(t *testing.T) {
	dir := t.TempDir()
	_, _, err := LoadPrompt(dir, "../SECRET.md")
	if err == nil {
		t.Fatal("expected error for path traversal attempt")
	}
}

func TestLoadPrompt_PathSeparatorBackslash(t *testing.T) {
	dir := t.TempDir()
	_, _, err := LoadPrompt(dir, `C:\file.md`)
	if err == nil {
		t.Fatal("expected error for backslash in filename")
	}
}

func TestLoadPrompt_FromZip(t *testing.T) {
	zp := makeZip(t, map[string]string{"START.md": "# Zip prompt."})
	body, pol, err := LoadPrompt(zp, "START.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "# Zip prompt." {
		t.Errorf("body = %q, want %q", body, "# Zip prompt.")
	}
	if pol != nil {
		t.Errorf("policy = %v, want nil", pol)
	}
}

func TestLoadPrompt_FromZipMissingFile(t *testing.T) {
	zp := makeZip(t, map[string]string{"START.md": "content"})
	_, _, err := LoadPrompt(zp, "MISSING.md")
	if err == nil {
		t.Fatal("expected error for missing file in zip")
	}
}

// --------------------------------------------------------------------------
// RenderPrompt
// --------------------------------------------------------------------------

func TestRenderPrompt_ReplacesPlaceholder(t *testing.T) {
	result := RenderPrompt("Hello {{name}}, welcome to {{place}}!", map[string]any{
		"name":  "Alice",
		"place": "Raymond",
	})
	if result != "Hello Alice, welcome to Raymond!" {
		t.Errorf("got %q", result)
	}
}

func TestRenderPrompt_MultiplePlaceholders(t *testing.T) {
	result := RenderPrompt("{{greeting}} {{name}}, your task is {{task}}.", map[string]any{
		"greeting": "Hi",
		"name":     "Bob",
		"task":     "testing",
	})
	if result != "Hi Bob, your task is testing." {
		t.Errorf("got %q", result)
	}
}

func TestRenderPrompt_MissingKeyLeavesPlaceholder(t *testing.T) {
	result := RenderPrompt("Hello {{name}}, status: {{status}}", map[string]any{
		"name": "Charlie",
	})
	if result != "Hello Charlie, status: {{status}}" {
		t.Errorf("got %q", result)
	}
}

func TestRenderPrompt_ResultPlaceholder(t *testing.T) {
	result := RenderPrompt("Previous result: {{result}}\n\nContinue.", map[string]any{
		"result": "Task completed successfully",
	})
	if !strings.Contains(result, "Task completed successfully") {
		t.Error("should contain result value")
	}
	if strings.Contains(result, "{{result}}") {
		t.Error("placeholder should be replaced")
	}
}

func TestRenderPrompt_NoPlaceholders(t *testing.T) {
	template := "This is a plain template with no variables."
	result := RenderPrompt(template, map[string]any{"key": "value"})
	if result != template {
		t.Errorf("got %q, want unchanged", result)
	}
}

func TestRenderPrompt_EmptyVariables(t *testing.T) {
	template := "Hello {{name}}, from {{place}}"
	result := RenderPrompt(template, map[string]any{})
	if result != template {
		t.Errorf("got %q, want unchanged template", result)
	}
}

func TestRenderPrompt_SamePlaceholderMultipleTimes(t *testing.T) {
	result := RenderPrompt("{{name}} says hello. {{name}} is happy.", map[string]any{
		"name": "David",
	})
	if result != "David says hello. David is happy." {
		t.Errorf("got %q", result)
	}
}

func TestRenderPrompt_NestedBraces(t *testing.T) {
	result := RenderPrompt(`Value: {{value}}, JSON: {{json}}`, map[string]any{
		"value": "test",
		"json":  `{"key": "value"}`,
	})
	if !strings.Contains(result, "test") {
		t.Error("should contain value")
	}
	if !strings.Contains(result, `{"key": "value"}`) {
		t.Error("should contain JSON value")
	}
}

func TestRenderPrompt_NonStringValue(t *testing.T) {
	result := RenderPrompt("Count: {{n}}", map[string]any{"n": 42})
	if !strings.Contains(result, "42") {
		t.Errorf("non-string value should be stringified, got %q", result)
	}
}

// --------------------------------------------------------------------------
// ResolveState — cross-platform
// --------------------------------------------------------------------------

func TestResolveState_FindsMdWhenExists(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "NEXT.md"), []byte("# Next"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveState(dir, "NEXT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "NEXT.md" {
		t.Errorf("got %q, want %q", got, "NEXT.md")
	}
}

func TestResolveState_ExplicitMdExtension(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "NEXT.md"), []byte("# Next"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveState(dir, "NEXT.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "NEXT.md" {
		t.Errorf("got %q, want %q", got, "NEXT.md")
	}
}

func TestResolveState_RaisesWhenNoFileExists(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveState(dir, "NEXT")
	if err == nil {
		t.Fatal("expected error when no file exists")
	}
}

func TestResolveState_ExplicitMdRaisesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	// Create .sh and .bat but NOT .md
	os.WriteFile(filepath.Join(dir, "NEXT.sh"), []byte("echo"), 0o644)
	os.WriteFile(filepath.Join(dir, "NEXT.bat"), []byte("echo"), 0o644)
	_, err := ResolveState(dir, "NEXT.md")
	if err == nil {
		t.Fatal("expected error when NEXT.md doesn't exist (no fallback for explicit)")
	}
}

func TestResolveState_RespectsScope_Dir(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	os.WriteFile(filepath.Join(dirA, "STATE.md"), []byte("# A"), 0o644)
	os.WriteFile(filepath.Join(dirB, "OTHER.md"), []byte("# B"), 0o644)

	got, err := ResolveState(dirA, "STATE")
	if err != nil {
		t.Fatalf("unexpected error in dirA: %v", err)
	}
	if got != "STATE.md" {
		t.Errorf("got %q, want STATE.md", got)
	}

	_, err = ResolveState(dirB, "STATE")
	if err == nil {
		t.Fatal("expected error in dirB (STATE.md not there)")
	}
}

func TestResolveState_BothShAndBatExists(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "NEXT.sh"), []byte("echo"), 0o644)
	os.WriteFile(filepath.Join(dir, "NEXT.bat"), []byte("echo"), 0o644)

	got, err := ResolveState(dir, "NEXT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runtime.GOOS == "windows" {
		if got != "NEXT.bat" {
			t.Errorf("on Windows: got %q, want NEXT.bat", got)
		}
	} else {
		if got != "NEXT.sh" {
			t.Errorf("on Unix: got %q, want NEXT.sh", got)
		}
	}
}

func TestResolveState_PathSeparatorForwardSlash(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveState(dir, "../SECRET")
	if err == nil {
		t.Fatal("expected error for path separator")
	}
	if !strings.Contains(err.Error(), "path separator") {
		t.Errorf("error should mention 'path separator', got: %v", err)
	}
}

func TestResolveState_PathSeparatorSubdir(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveState(dir, "subdir/STATE")
	if err == nil {
		t.Fatal("expected error for path separator")
	}
}

func TestResolveState_PathSeparatorBackslash(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveState(dir, `C:\STATE`)
	if err == nil {
		t.Fatal("expected error for backslash")
	}
}

// --------------------------------------------------------------------------
// ResolveState — Unix-only
// --------------------------------------------------------------------------

func TestResolveState_FindsShWhenMdMissing(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "NEXT.sh"), []byte("echo"), 0o644)
	got, err := ResolveState(dir, "NEXT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "NEXT.sh" {
		t.Errorf("got %q, want NEXT.sh", got)
	}
}

func TestResolveState_ExplicitShExtension(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "NEXT.sh"), []byte("echo"), 0o644)
	got, err := ResolveState(dir, "NEXT.sh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "NEXT.sh" {
		t.Errorf("got %q, want NEXT.sh", got)
	}
}

func TestResolveState_RaisesWhenOnlyBatExists(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "NEXT.bat"), []byte("echo"), 0o644)
	_, err := ResolveState(dir, "NEXT")
	if err == nil {
		t.Fatal("expected error (only .bat, no .md or .sh)")
	}
}

func TestResolveState_RaisesWhenMdAndShBothExist(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "NEXT.md"), []byte("# Next"), 0o644)
	os.WriteFile(filepath.Join(dir, "NEXT.sh"), []byte("echo"), 0o644)
	_, err := ResolveState(dir, "NEXT")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Errorf("error should mention 'ambiguous', got: %v", err)
	}
}

func TestResolveState_ExplicitBatRaisesWrongPlatform(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "NEXT.bat"), []byte("echo"), 0o644)
	_, err := ResolveState(dir, "NEXT.bat")
	if err == nil {
		t.Fatal("expected platform error for .bat on Unix")
	}
	errLow := strings.ToLower(err.Error())
	if !strings.Contains(errLow, "platform") && !strings.Contains(errLow, "windows") && !strings.Contains(errLow, "unix") {
		t.Errorf("error should mention platform/windows/unix, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// ResolveState — Windows-only
// --------------------------------------------------------------------------

func TestResolveState_FindsBatWhenMdMissing(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "NEXT.bat"), []byte("echo"), 0o644)
	got, err := ResolveState(dir, "NEXT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "NEXT.bat" {
		t.Errorf("got %q, want NEXT.bat", got)
	}
}

func TestResolveState_ExplicitBatExtension(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "NEXT.bat"), []byte("echo"), 0o644)
	got, err := ResolveState(dir, "NEXT.bat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "NEXT.bat" {
		t.Errorf("got %q, want NEXT.bat", got)
	}
}

func TestResolveState_RaisesWhenOnlyShExists(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "NEXT.sh"), []byte("echo"), 0o644)
	_, err := ResolveState(dir, "NEXT")
	if err == nil {
		t.Fatal("expected error (only .sh, no .md or .bat)")
	}
}

func TestResolveState_RaisesWhenMdAndBatBothExist(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "NEXT.md"), []byte("# Next"), 0o644)
	os.WriteFile(filepath.Join(dir, "NEXT.bat"), []byte("echo"), 0o644)
	_, err := ResolveState(dir, "NEXT")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Errorf("error should mention 'ambiguous', got: %v", err)
	}
}

func TestResolveState_ExplicitShRaisesWrongPlatform(t *testing.T) {
	skipUnix(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "NEXT.sh"), []byte("echo"), 0o644)
	_, err := ResolveState(dir, "NEXT.sh")
	if err == nil {
		t.Fatal("expected platform error for .sh on Windows")
	}
	errLow := strings.ToLower(err.Error())
	if !strings.Contains(errLow, "platform") && !strings.Contains(errLow, "windows") && !strings.Contains(errLow, "unix") {
		t.Errorf("error should mention platform/windows/unix, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// ResolveState — zip scope
// --------------------------------------------------------------------------

func TestResolveState_ZipFindsMd(t *testing.T) {
	zp := makeZip(t, map[string]string{"NEXT.md": "# Next"})
	got, err := ResolveState(zp, "NEXT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "NEXT.md" {
		t.Errorf("got %q, want NEXT.md", got)
	}
}

func TestResolveState_ZipExplicitMd(t *testing.T) {
	zp := makeZip(t, map[string]string{"NEXT.md": "# Next"})
	got, err := ResolveState(zp, "NEXT.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "NEXT.md" {
		t.Errorf("got %q, want NEXT.md", got)
	}
}

func TestResolveState_ZipMissingFile(t *testing.T) {
	zp := makeZip(t, map[string]string{"OTHER.md": "x"})
	_, err := ResolveState(zp, "NEXT")
	if err == nil {
		t.Fatal("expected error for missing state in zip")
	}
}

func TestResolveState_ZipFindsPlatformScript(t *testing.T) {
	var files map[string]string
	var want string
	if runtime.GOOS == "windows" {
		files = map[string]string{"NEXT.bat": "echo"}
		want = "NEXT.bat"
	} else {
		files = map[string]string{"NEXT.sh": "echo"}
		want = "NEXT.sh"
	}
	zp := makeZip(t, files)
	got, err := ResolveState(zp, "NEXT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveState_ZipAmbiguous(t *testing.T) {
	var files map[string]string
	if runtime.GOOS == "windows" {
		files = map[string]string{"NEXT.md": "x", "NEXT.bat": "y"}
	} else {
		files = map[string]string{"NEXT.md": "x", "NEXT.sh": "y"}
	}
	zp := makeZip(t, files)
	_, err := ResolveState(zp, "NEXT")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Errorf("error should mention 'ambiguous', got: %v", err)
	}
}

// --------------------------------------------------------------------------
// GetStateType — cross-platform
// --------------------------------------------------------------------------

func TestGetStateType_Markdown(t *testing.T) {
	got, err := GetStateType("NEXT.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "markdown" {
		t.Errorf("got %q, want markdown", got)
	}
}

func TestGetStateType_MarkdownUppercase(t *testing.T) {
	got, err := GetStateType("START.MD")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "markdown" {
		t.Errorf("got %q, want markdown", got)
	}
}

func TestGetStateType_UnsupportedExtension(t *testing.T) {
	_, err := GetStateType("NEXT.py")
	if err == nil {
		t.Fatal("expected error for unsupported extension")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unsupported") {
		t.Errorf("error should mention 'unsupported', got: %v", err)
	}
}

func TestGetStateType_NoExtension(t *testing.T) {
	_, err := GetStateType("NEXT")
	if err == nil {
		t.Fatal("expected error when no extension")
	}
}

func TestGetStateType_OtherUnsupported(t *testing.T) {
	for _, name := range []string{"script.js", "config.yaml"} {
		_, err := GetStateType(name)
		if err == nil {
			t.Errorf("expected error for %q", name)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "unsupported") {
			t.Errorf("error for %q should mention 'unsupported', got: %v", name, err)
		}
	}
}

// --------------------------------------------------------------------------
// GetStateType — Unix-only
// --------------------------------------------------------------------------

func TestGetStateType_ShOnUnix(t *testing.T) {
	skipWindows(t)
	got, err := GetStateType("NEXT.sh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "script" {
		t.Errorf("got %q, want script", got)
	}
}

func TestGetStateType_ShUppercaseOnUnix(t *testing.T) {
	skipWindows(t)
	got, err := GetStateType("SCRIPT.SH")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "script" {
		t.Errorf("got %q, want script", got)
	}
}

func TestGetStateType_BatRaisesOnUnix(t *testing.T) {
	skipWindows(t)
	_, err := GetStateType("NEXT.bat")
	if err == nil {
		t.Fatal("expected error for .bat on Unix")
	}
	errLow := strings.ToLower(err.Error())
	if !strings.Contains(errLow, "platform") && !strings.Contains(errLow, "windows") && !strings.Contains(errLow, "unix") {
		t.Errorf("error should mention platform/windows/unix, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// GetStateType — Windows-only
// --------------------------------------------------------------------------

func TestGetStateType_BatOnWindows(t *testing.T) {
	skipUnix(t)
	got, err := GetStateType("NEXT.bat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "script" {
		t.Errorf("got %q, want script", got)
	}
}

func TestGetStateType_BatUppercaseOnWindows(t *testing.T) {
	skipUnix(t)
	got, err := GetStateType("SCRIPT.BAT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "script" {
		t.Errorf("got %q, want script", got)
	}
}

func TestGetStateType_ShRaisesOnWindows(t *testing.T) {
	skipUnix(t)
	_, err := GetStateType("NEXT.sh")
	if err == nil {
		t.Fatal("expected error for .sh on Windows")
	}
	errLow := strings.ToLower(err.Error())
	if !strings.Contains(errLow, "platform") && !strings.Contains(errLow, "windows") && !strings.Contains(errLow, "unix") {
		t.Errorf("error should mention platform/windows/unix, got: %v", err)
	}
}
