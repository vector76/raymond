package ccwrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// skipWindows skips the test on Windows (bash not available).
func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test requires Unix shell")
	}
}

// overrideClaudeExe temporarily sets claudeExe for the duration of the test.
func overrideClaudeExe(t *testing.T, exe string) {
	t.Helper()
	orig := claudeExe
	claudeExe = exe
	t.Cleanup(func() { claudeExe = orig })
}

// writeFakeClaude creates a bash script in a temp dir, marks it executable,
// and returns its path. The script ignores all arguments (like a fake claude).
func writeFakeClaude(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fakeclaude.sh")
	content := "#!/bin/bash\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// --------------------------------------------------------------------------
// BuildClaudeCommand
// --------------------------------------------------------------------------

func TestBuildClaudeCommand_Default(t *testing.T) {
	got := BuildClaudeCommand("hello world", "", "", false, false)
	want := []string{
		"claude", "-p",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", "acceptEdits",
		"--disallowed-tools", "EnterPlanMode,ExitPlanMode,AskUserQuestion,NotebookEdit",
		"--", "hello world",
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d\ncmd: %v", len(got), len(want), got)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("[%d] got %q, want %q", i, got[i], v)
		}
	}
}

func TestBuildClaudeCommand_WithModel(t *testing.T) {
	got := BuildClaudeCommand("prompt", "haiku", "", false, false)
	if !containsSeq(got, "--model", "haiku") {
		t.Errorf("expected --model haiku in %v", got)
	}
}

func TestBuildClaudeCommand_WithSessionID(t *testing.T) {
	got := BuildClaudeCommand("prompt", "", "sess-abc", false, false)
	if !containsSeq(got, "--resume", "sess-abc") {
		t.Errorf("expected --resume sess-abc in %v", got)
	}
}

func TestBuildClaudeCommand_DangerouslySkipPermissions(t *testing.T) {
	got := BuildClaudeCommand("prompt", "", "", true, false)
	if !contains(got, "--dangerously-skip-permissions") {
		t.Errorf("expected --dangerously-skip-permissions in %v", got)
	}
	if containsSeq(got, "--permission-mode", "acceptEdits") {
		t.Errorf("should not have --permission-mode acceptEdits when skipping perms")
	}
}

func TestBuildClaudeCommand_PermissionModeAcceptEdits(t *testing.T) {
	got := BuildClaudeCommand("prompt", "", "", false, false)
	if !containsSeq(got, "--permission-mode", "acceptEdits") {
		t.Errorf("expected --permission-mode acceptEdits in %v", got)
	}
}

func TestBuildClaudeCommand_Fork(t *testing.T) {
	got := BuildClaudeCommand("prompt", "", "sess-xyz", false, true)
	// --fork-session must appear after --
	sepIdx := indexOf(got, "--")
	forkIdx := indexOf(got, "--fork-session")
	if forkIdx < 0 {
		t.Fatalf("expected --fork-session in %v", got)
	}
	if forkIdx <= sepIdx {
		t.Errorf("--fork-session (idx %d) should come after -- (idx %d)", forkIdx, sepIdx)
	}
}

func TestBuildClaudeCommand_NoFork(t *testing.T) {
	got := BuildClaudeCommand("prompt", "", "", false, false)
	if contains(got, "--fork-session") {
		t.Errorf("should not have --fork-session when fork=false: %v", got)
	}
}

func TestBuildClaudeCommand_AllOptions(t *testing.T) {
	got := BuildClaudeCommand("my prompt", "opus", "sid123", true, true)
	checks := []struct {
		name string
		fn   func() bool
	}{
		{"claude exe", func() bool { return got[0] == "claude" }},
		{"-p flag", func() bool { return contains(got, "-p") }},
		{"stream-json", func() bool { return containsSeq(got, "--output-format", "stream-json") }},
		{"--verbose", func() bool { return contains(got, "--verbose") }},
		{"--dangerously-skip-permissions", func() bool { return contains(got, "--dangerously-skip-permissions") }},
		{"--model opus", func() bool { return containsSeq(got, "--model", "opus") }},
		{"--resume sid123", func() bool { return containsSeq(got, "--resume", "sid123") }},
		{"--disallowed-tools", func() bool { return contains(got, "--disallowed-tools") }},
		{"prompt after --", func() bool { return afterSep(got, "my prompt") }},
		{"--fork-session after --", func() bool { return afterSep(got, "--fork-session") }},
	}
	for _, c := range checks {
		if !c.fn() {
			t.Errorf("missing %s in cmd: %v", c.name, got)
		}
	}
}

func TestBuildClaudeCommand_DisallowedToolsJoined(t *testing.T) {
	got := BuildClaudeCommand("p", "", "", false, false)
	idx := indexOf(got, "--disallowed-tools")
	if idx < 0 || idx+1 >= len(got) {
		t.Fatalf("--disallowed-tools not found in %v", got)
	}
	val := got[idx+1]
	for _, tool := range DisallowedTools {
		if !strings.Contains(val, tool) {
			t.Errorf("missing %q in disallowed-tools value %q", tool, val)
		}
	}
	// Should be comma-separated, not space-separated
	if strings.Contains(val, " ") {
		t.Errorf("disallowed-tools should be comma-separated, got %q", val)
	}
}

func TestBuildClaudeCommand_PromptAfterSeparator(t *testing.T) {
	prompt := "do something\nwith newlines"
	got := BuildClaudeCommand(prompt, "", "", false, false)
	sepIdx := indexOf(got, "--")
	if sepIdx < 0 {
		t.Fatal("-- separator not found")
	}
	if sepIdx+1 >= len(got) || got[sepIdx+1] != prompt {
		t.Errorf("prompt not immediately after --: %v", got)
	}
}

// --------------------------------------------------------------------------
// BuildClaudeEnv
// --------------------------------------------------------------------------

func TestBuildClaudeEnv_StripsCLAUDECODE(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	env := BuildClaudeEnv()
	if _, ok := env["CLAUDECODE"]; ok {
		t.Error("CLAUDECODE should be stripped from env")
	}
}

func TestBuildClaudeEnv_PreservesOtherVars(t *testing.T) {
	t.Setenv("RAYMOND_TEST_VAR", "hello")
	env := BuildClaudeEnv()
	if v, ok := env["RAYMOND_TEST_VAR"]; !ok || v != "hello" {
		t.Errorf("RAYMOND_TEST_VAR should be preserved, got %q", v)
	}
}

func TestBuildClaudeEnv_NoCLAUDECODE_NoError(t *testing.T) {
	os.Unsetenv("CLAUDECODE")
	env := BuildClaudeEnv()
	if _, ok := env["CLAUDECODE"]; ok {
		t.Error("CLAUDECODE should not appear when not set")
	}
	// Should still have PATH
	if _, ok := env["PATH"]; !ok {
		t.Error("PATH should be present in env")
	}
}

// --------------------------------------------------------------------------
// ExtractSessionID
// --------------------------------------------------------------------------

func TestExtractSessionID_TopLevel(t *testing.T) {
	obj := map[string]any{"session_id": "abc123", "type": "result"}
	got := ExtractSessionID(obj)
	if got != "abc123" {
		t.Errorf("got %q, want %q", got, "abc123")
	}
}

func TestExtractSessionID_Nested(t *testing.T) {
	obj := map[string]any{
		"type": "result",
		"metadata": map[string]any{
			"session_id": "nested-sess",
			"other":      "val",
		},
	}
	got := ExtractSessionID(obj)
	if got != "nested-sess" {
		t.Errorf("got %q, want %q", got, "nested-sess")
	}
}

func TestExtractSessionID_Missing(t *testing.T) {
	obj := map[string]any{"type": "data", "content": "hello"}
	got := ExtractSessionID(obj)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestExtractSessionID_NonString(t *testing.T) {
	obj := map[string]any{"session_id": 42}
	got := ExtractSessionID(obj)
	if got != "" {
		t.Errorf("non-string session_id should return empty, got %q", got)
	}
}

func TestExtractSessionID_EmptyString(t *testing.T) {
	obj := map[string]any{"session_id": ""}
	got := ExtractSessionID(obj)
	if got != "" {
		t.Errorf("empty session_id should return empty, got %q", got)
	}
}

func TestExtractSessionID_TopLevelPreferredOverNested(t *testing.T) {
	obj := map[string]any{
		"session_id": "top-level",
		"metadata": map[string]any{
			"session_id": "nested",
		},
	}
	got := ExtractSessionID(obj)
	if got != "top-level" {
		t.Errorf("top-level should take precedence, got %q", got)
	}
}

// --------------------------------------------------------------------------
// InvokeStream — requires Unix bash
// --------------------------------------------------------------------------

func TestInvokeStream_HappyPath(t *testing.T) {
	skipWindows(t)
	script := writeFakeClaude(t, `
echo '{"type":"start","session_id":"sess-happy"}'
echo '{"type":"end"}'
`)
	overrideClaudeExe(t, script)

	ctx := context.Background()
	ch := InvokeStream(ctx, "test prompt", "", "", 0, false, false, "")

	var items []StreamItem
	for item := range ch {
		items = append(items, item)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d: %v", len(items), items)
	}
	for _, it := range items {
		if it.Err != nil {
			t.Errorf("unexpected error: %v", it.Err)
		}
	}
	if items[0].Object["type"] != "start" {
		t.Errorf("first item type = %v, want start", items[0].Object["type"])
	}
	if items[1].Object["type"] != "end" {
		t.Errorf("second item type = %v, want end", items[1].Object["type"])
	}
}

func TestInvokeStream_EmptyLinesSkipped(t *testing.T) {
	skipWindows(t)
	script := writeFakeClaude(t, `
echo ''
echo '{"type":"data"}'
echo ''
echo '{"type":"data2"}'
`)
	overrideClaudeExe(t, script)

	ch := InvokeStream(context.Background(), "p", "", "", 0, false, false, "")
	var objects []map[string]any
	for item := range ch {
		if item.Err != nil {
			t.Errorf("unexpected error: %v", item.Err)
		}
		if item.Object != nil {
			objects = append(objects, item.Object)
		}
	}
	if len(objects) != 2 {
		t.Errorf("expected 2 objects, got %d", len(objects))
	}
}

func TestInvokeStream_NonJsonLinesSkipped(t *testing.T) {
	skipWindows(t)
	script := writeFakeClaude(t, `
echo 'not json at all'
echo '{"type":"valid"}'
echo 'also not json'
`)
	overrideClaudeExe(t, script)

	ch := InvokeStream(context.Background(), "p", "", "", 0, false, false, "")
	var objects []map[string]any
	for item := range ch {
		if item.Err != nil {
			t.Errorf("unexpected error: %v", item.Err)
		}
		if item.Object != nil {
			objects = append(objects, item.Object)
		}
	}
	if len(objects) != 1 {
		t.Errorf("expected 1 object (non-JSON skipped), got %d", len(objects))
	}
	if objects[0]["type"] != "valid" {
		t.Errorf("got type=%v, want valid", objects[0]["type"])
	}
}

func TestInvokeStream_ExitError(t *testing.T) {
	skipWindows(t)
	script := writeFakeClaude(t, `
echo '{"type":"data"}'
exit 42
`)
	overrideClaudeExe(t, script)

	ch := InvokeStream(context.Background(), "p", "", "", 0, false, false, "")
	var lastErr error
	var objCount int
	for item := range ch {
		if item.Err != nil {
			lastErr = item.Err
		} else {
			objCount++
		}
	}
	if objCount != 1 {
		t.Errorf("expected 1 object before error, got %d", objCount)
	}
	if lastErr == nil {
		t.Fatal("expected error for non-zero exit code")
	}
	if !strings.Contains(lastErr.Error(), "42") {
		t.Errorf("error should mention exit code 42, got: %v", lastErr)
	}
}

func TestInvokeStream_IdleTimeout(t *testing.T) {
	skipWindows(t)
	script := writeFakeClaude(t, `
echo '{"type":"start"}'
sleep 30
echo '{"type":"end"}'
`)
	overrideClaudeExe(t, script)

	ctx := context.Background()
	start := time.Now()
	ch := InvokeStream(ctx, "p", "", "", 0.15, false, false, "") // 150 ms idle timeout

	var lastErr error
	for item := range ch {
		if item.Err != nil {
			lastErr = item.Err
		}
	}
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("idle timeout test took too long: %v (expected < 5s)", elapsed)
	}

	var timeoutErr *ClaudeCodeTimeoutError
	if !errors.As(lastErr, &timeoutErr) {
		t.Fatalf("expected *ClaudeCodeTimeoutError, got %T: %v", lastErr, lastErr)
	}
	if !timeoutErr.Idle {
		t.Error("expected Idle=true for idle timeout")
	}
}

func TestInvokeStream_CtxCancel(t *testing.T) {
	skipWindows(t)
	script := writeFakeClaude(t, `
echo '{"type":"first"}'
sleep 30
`)
	overrideClaudeExe(t, script)

	ctx, cancel := context.WithCancel(context.Background())
	ch := InvokeStream(ctx, "p", "", "", 0, false, false, "")

	// Receive the first item, then cancel.
	item := <-ch
	if item.Err != nil {
		t.Fatalf("unexpected error on first item: %v", item.Err)
	}
	cancel()

	// Channel must close within a reasonable time.
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Error("channel did not close after ctx cancel within 5s")
	}
}

func TestInvokeStream_Cwd(t *testing.T) {
	skipWindows(t)
	// Script prints the current working directory as JSON.
	script := writeFakeClaude(t, `echo "{\"cwd\":\"$(pwd)\",\"type\":\"data\"}"`)
	overrideClaudeExe(t, script)

	dir := t.TempDir()
	ch := InvokeStream(context.Background(), "p", "", "", 0, false, false, dir)
	var objects []map[string]any
	for item := range ch {
		if item.Err != nil {
			t.Errorf("unexpected error: %v", item.Err)
		}
		if item.Object != nil {
			objects = append(objects, item.Object)
		}
	}
	if len(objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objects))
	}
	// On macOS, TempDir may have a symlink prefix; accept any path ending.
	cwd, _ := objects[0]["cwd"].(string)
	if cwd == "" {
		t.Fatalf("cwd field missing or empty: %v", objects[0])
	}
	// The reported cwd should match dir (modulo symlink resolution).
	if !strings.HasSuffix(cwd, filepath.Base(dir)) {
		t.Errorf("cwd=%q does not contain expected dir base %q", cwd, filepath.Base(dir))
	}
}

// --------------------------------------------------------------------------
// Invoke — requires Unix bash
// --------------------------------------------------------------------------

func TestInvoke_HappyPath(t *testing.T) {
	skipWindows(t)
	script := writeFakeClaude(t, `
echo '{"type":"start","session_id":"sid-001"}'
echo '{"type":"result","content":"done"}'
`)
	overrideClaudeExe(t, script)

	objects, sid, err := Invoke(context.Background(), "p", "", "", 0, false, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(objects) != 2 {
		t.Errorf("expected 2 objects, got %d", len(objects))
	}
	if sid != "sid-001" {
		t.Errorf("session_id = %q, want %q", sid, "sid-001")
	}
}

func TestInvoke_ExtractNestedSessionID(t *testing.T) {
	skipWindows(t)
	script := writeFakeClaude(t, `
echo '{"type":"meta","metadata":{"session_id":"nested-sid"}}'
`)
	overrideClaudeExe(t, script)

	_, sid, err := Invoke(context.Background(), "p", "", "", 0, false, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sid != "nested-sid" {
		t.Errorf("session_id = %q, want %q", sid, "nested-sid")
	}
}

func TestInvoke_TotalTimeout(t *testing.T) {
	skipWindows(t)
	script := writeFakeClaude(t, `sleep 30`)
	overrideClaudeExe(t, script)

	start := time.Now()
	_, _, err := Invoke(context.Background(), "p", "", "", 0.2, false, false, "") // 200 ms timeout
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("total timeout test took too long: %v (expected < 5s)", elapsed)
	}

	var timeoutErr *ClaudeCodeTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected *ClaudeCodeTimeoutError, got %T: %v", err, err)
	}
	if timeoutErr.Idle {
		t.Error("expected Idle=false for total timeout")
	}
}

func TestInvoke_ExitError(t *testing.T) {
	skipWindows(t)
	script := writeFakeClaude(t, `exit 1`)
	overrideClaudeExe(t, script)

	_, _, err := Invoke(context.Background(), "p", "", "", 0, false, false, "")
	if err == nil {
		t.Fatal("expected error for exit code 1")
	}
	if !strings.Contains(err.Error(), "1") {
		t.Errorf("error should mention exit code: %v", err)
	}
}

func TestInvoke_NoTimeout(t *testing.T) {
	skipWindows(t)
	// totalTimeout=0 means no timeout; script exits immediately.
	script := writeFakeClaude(t, `echo '{"type":"ok"}'`)
	overrideClaudeExe(t, script)

	objects, _, err := Invoke(context.Background(), "p", "", "", 0, false, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(objects) != 1 {
		t.Errorf("expected 1 object, got %d", len(objects))
	}
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func containsSeq(slice []string, a, b string) bool {
	for i := 0; i < len(slice)-1; i++ {
		if slice[i] == a && slice[i+1] == b {
			return true
		}
	}
	return false
}

func indexOf(slice []string, s string) int {
	for i, v := range slice {
		if v == s {
			return i
		}
	}
	return -1
}

// afterSep returns true if s appears in slice after the "--" separator.
func afterSep(slice []string, s string) bool {
	sepIdx := indexOf(slice, "--")
	if sepIdx < 0 {
		return false
	}
	for _, v := range slice[sepIdx+1:] {
		if v == s {
			return true
		}
	}
	return false
}
