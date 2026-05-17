package piwrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flagVal returns the value following flag in cmd, or "" if not found.
func flagVal(cmd []string, flag string) string {
	for i, v := range cmd {
		if v == flag && i+1 < len(cmd) {
			return cmd[i+1]
		}
	}
	return ""
}

// flagVals returns all values immediately following flag in cmd (for repeatable flags).
func flagVals(cmd []string, flag string) []string {
	var vals []string
	for i, v := range cmd {
		if v == flag && i+1 < len(cmd) {
			vals = append(vals, cmd[i+1])
		}
	}
	return vals
}

func hasFlag(cmd []string, flag string) bool {
	for _, v := range cmd {
		if v == flag {
			return true
		}
	}
	return false
}

// --- BuildPiCommand ---

func TestBuildPiCommand_Basics(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{
		Prompt: "hello",
	})
	assert.Equal(t, "pi", cmd[0])
	assert.Equal(t, "--mode", cmd[1])
	assert.Equal(t, "json", cmd[2])
	assert.Equal(t, "hello", cmd[len(cmd)-1])
}

func TestBuildPiCommand_NewSession(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{Prompt: "p"})
	assert.False(t, hasFlag(cmd, "--session"))
	assert.False(t, hasFlag(cmd, "--fork"))
}

func TestBuildPiCommand_SessionResume(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{
		Prompt:    "p",
		SessionID: "abc-123",
		Fork:      false,
	})
	assert.Equal(t, "abc-123", flagVal(cmd, "--session"))
	assert.False(t, hasFlag(cmd, "--fork"))
}

func TestBuildPiCommand_ForkSession(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{
		Prompt:    "p",
		SessionID: "caller-session-id",
		Fork:      true,
	})
	assert.Equal(t, "caller-session-id", flagVal(cmd, "--fork"))
	assert.False(t, hasFlag(cmd, "--session"))
}

func TestBuildPiCommand_ForkNoSessionID(t *testing.T) {
	// Fork=true with empty SessionID should produce neither --fork nor --session.
	cmd := BuildPiCommand(CommandSpec{Prompt: "p", Fork: true, SessionID: ""})
	assert.False(t, hasFlag(cmd, "--fork"))
	assert.False(t, hasFlag(cmd, "--session"))
}

func TestBuildPiCommand_ModelAndProvider(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{
		Prompt:   "p",
		Model:    "anthropic/claude-sonnet-4-6",
		Provider: "anthropic",
	})
	assert.Equal(t, "anthropic/claude-sonnet-4-6", flagVal(cmd, "--model"))
	assert.Equal(t, "anthropic", flagVal(cmd, "--provider"))
}

func TestBuildPiCommand_ModelOmittedWhenEmpty(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{Prompt: "p", Model: ""})
	assert.False(t, hasFlag(cmd, "--model"))
}

func TestBuildPiCommand_ProviderOmittedWhenEmpty(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{Prompt: "p", Provider: ""})
	assert.False(t, hasFlag(cmd, "--provider"))
}

func TestBuildPiCommand_ThinkingFromPerStateEffort(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{
		Prompt:          "p",
		Effort:          "high",
		ThinkingDefault: "low",
	})
	assert.Equal(t, "high", flagVal(cmd, "--thinking"))
}

func TestBuildPiCommand_ThinkingFromDefaultWhenNoEffort(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{
		Prompt:          "p",
		Effort:          "",
		ThinkingDefault: "medium",
	})
	assert.Equal(t, "medium", flagVal(cmd, "--thinking"))
}

func TestBuildPiCommand_ThinkingOmittedWhenBothEmpty(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{Prompt: "p"})
	assert.False(t, hasFlag(cmd, "--thinking"))
}

func TestBuildPiCommand_ThinkingVerbatimUnknownValue(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{Prompt: "p", Effort: "xhigh"})
	assert.Equal(t, "xhigh", flagVal(cmd, "--thinking"))
}

func TestBuildPiCommand_NoTools(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{Prompt: "p", NoTools: true})
	assert.True(t, hasFlag(cmd, "--no-tools"))
	assert.False(t, hasFlag(cmd, "--no-builtin-tools"))
	assert.False(t, hasFlag(cmd, "--tools"))
}

func TestBuildPiCommand_NoBuiltinTools(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{Prompt: "p", NoBuiltinTools: true})
	assert.True(t, hasFlag(cmd, "--no-builtin-tools"))
	assert.False(t, hasFlag(cmd, "--no-tools"))
	assert.False(t, hasFlag(cmd, "--tools"))
}

func TestBuildPiCommand_ExplicitToolsList(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{Prompt: "p", Tools: []string{"read", "edit", "bash"}})
	assert.Equal(t, "read,edit,bash", flagVal(cmd, "--tools"))
}

func TestBuildPiCommand_DangerouslySkipPermissionsNoTools_NoFlag(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{
		Prompt:                     "p",
		DangerouslySkipPermissions: true,
	})
	assert.False(t, hasFlag(cmd, "--tools"))
	assert.False(t, hasFlag(cmd, "--no-tools"))
	assert.False(t, hasFlag(cmd, "--no-builtin-tools"))
}

func TestBuildPiCommand_DefaultToolsList(t *testing.T) {
	// No explicit tools, no dangerous flag → conservative default, exact set.
	cmd := BuildPiCommand(CommandSpec{Prompt: "p"})
	assert.Equal(t, "read,edit,write,grep,find,ls", flagVal(cmd, "--tools"))
}

func TestBuildPiCommand_DangerousWithExplicitTools(t *testing.T) {
	// When explicit tools are set, they take precedence even with dangerous flag.
	cmd := BuildPiCommand(CommandSpec{
		Prompt:                     "p",
		Tools:                      []string{"bash"},
		DangerouslySkipPermissions: true,
	})
	assert.Equal(t, "bash", flagVal(cmd, "--tools"))
}

func TestBuildPiCommand_Extensions(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{
		Prompt:     "p",
		Extensions: []string{"my-ext", "https://github.com/org/ext"},
	})
	assert.Equal(t, []string{"my-ext", "https://github.com/org/ext"}, flagVals(cmd, "--extension"))
	assert.False(t, hasFlag(cmd, "--no-extensions"))
}

func TestBuildPiCommand_Skills(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{
		Prompt: "p",
		Skills: []string{"./skills/code-review", "./skills/test"},
	})
	assert.Equal(t, []string{"./skills/code-review", "./skills/test"}, flagVals(cmd, "--skill"))
	assert.False(t, hasFlag(cmd, "--no-skills"))
}

func TestBuildPiCommand_NoExtensions(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{Prompt: "p", NoExtensions: true})
	assert.True(t, hasFlag(cmd, "--no-extensions"))
}

func TestBuildPiCommand_NoSkills(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{Prompt: "p", NoSkills: true})
	assert.True(t, hasFlag(cmd, "--no-skills"))
}

func TestBuildPiCommand_SessionDir(t *testing.T) {
	cmd := BuildPiCommand(CommandSpec{Prompt: "p", SessionDir: "/tmp/sessions"})
	assert.Equal(t, "/tmp/sessions", flagVal(cmd, "--session-dir"))
}

func TestBuildPiCommand_SessionDirBeforeSessionFlags(t *testing.T) {
	// Spec: --session-dir must be placed before session flags.
	cmd := BuildPiCommand(CommandSpec{
		Prompt:     "p",
		SessionDir: "/sessions",
		SessionID:  "sess-abc",
	})
	dirIdx, sessIdx := -1, -1
	for i, v := range cmd {
		switch v {
		case "--session-dir":
			dirIdx = i
		case "--session":
			sessIdx = i
		}
	}
	require.True(t, dirIdx >= 0, "--session-dir not found")
	require.True(t, sessIdx >= 0, "--session not found")
	assert.Less(t, dirIdx, sessIdx, "--session-dir must precede --session")
}

func TestBuildPiCommand_NoBuiltinToolsOverridesExplicitTools(t *testing.T) {
	// NoBuiltinTools takes priority over an explicit tools list.
	cmd := BuildPiCommand(CommandSpec{
		Prompt:         "p",
		NoBuiltinTools: true,
		Tools:          []string{"bash"},
	})
	assert.True(t, hasFlag(cmd, "--no-builtin-tools"))
	assert.False(t, hasFlag(cmd, "--tools"))
}

func TestBuildPiCommand_PromptIsLastArg(t *testing.T) {
	prompt := "do something with 'quotes' and \"double quotes\""
	cmd := BuildPiCommand(CommandSpec{
		Prompt: prompt,
		Model:  "some-model",
	})
	assert.Equal(t, prompt, cmd[len(cmd)-1])
}

func TestBuildPiCommand_NoToolsOverridesNoBuiltinTools(t *testing.T) {
	// no_tools takes priority over no_builtin_tools.
	cmd := BuildPiCommand(CommandSpec{
		Prompt:         "p",
		NoTools:        true,
		NoBuiltinTools: true,
	})
	assert.True(t, hasFlag(cmd, "--no-tools"))
	assert.False(t, hasFlag(cmd, "--no-builtin-tools"))
}

func TestBuildPiCommand_PiExeOverride(t *testing.T) {
	restore := SetPiExeForTest("/custom/pi")
	defer restore()
	cmd := BuildPiCommand(CommandSpec{Prompt: "p"})
	assert.Equal(t, "/custom/pi", cmd[0])
}

// --- ReadSessionCost ---

func TestReadSessionCost_MissingFile(t *testing.T) {
	cost, err := ReadSessionCost("no-such-session", "/nonexistent/dir", "/cwd")
	require.NoError(t, err)
	assert.Equal(t, 0.0, cost.CostUSD)
	assert.Equal(t, int64(0), cost.InputTokens)
}

func TestReadSessionCost_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	sessionID := "test-session"
	writeSessionFile(t, dir, sessionID, "")
	cost, err := ReadSessionCost(sessionID, dir, "")
	require.NoError(t, err)
	assert.Equal(t, 0.0, cost.CostUSD)
	assert.Equal(t, int64(0), cost.InputTokens)
}

// piTurnEnd builds a turn_end JSONL line matching pi v0.74's actual shape:
// nested message.usage.cost.total plus message.usage.{input,cacheRead,cacheWrite}.
func piTurnEnd(input, cacheRead, cacheWrite int, total float64) string {
	return fmt.Sprintf(
		`{"type":"turn_end","message":{"role":"assistant","usage":{"input":%d,"output":50,"cacheRead":%d,"cacheWrite":%d,"totalTokens":%d,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":%g}}}}`,
		input, cacheRead, cacheWrite, input+cacheRead+cacheWrite+50, total,
	)
}

func TestReadSessionCost_SingleTurnEnd(t *testing.T) {
	dir := t.TempDir()
	sessionID := "sess-1"
	writeSessionFile(t, dir, sessionID, piTurnEnd(100, 0, 0, 0.001234))
	cost, err := ReadSessionCost(sessionID, dir, "")
	require.NoError(t, err)
	assert.InDelta(t, 0.001234, cost.CostUSD, 1e-9)
	assert.Equal(t, int64(100), cost.InputTokens)
}

func TestReadSessionCost_MultipleTurns_Summed(t *testing.T) {
	// Across turns the per-turn turn_end records sum (e.g. resume loops).
	dir := t.TempDir()
	sessionID := "sess-2"
	lines := strings.Join([]string{
		piTurnEnd(50, 0, 0, 0.001),
		piTurnEnd(75, 0, 0, 0.002),
		`{"type":"other_event"}`,
	}, "\n")
	writeSessionFile(t, dir, sessionID, lines)
	cost, err := ReadSessionCost(sessionID, dir, "")
	require.NoError(t, err)
	assert.InDelta(t, 0.003, cost.CostUSD, 1e-9)
	assert.Equal(t, int64(125), cost.InputTokens)
}

func TestReadSessionCost_CacheTokensSummedIntoInputSide(t *testing.T) {
	// Within a single turn_end record, cacheRead and cacheWrite roll into
	// the "input-side" token count (mirrors Claude's input_tokens semantics).
	dir := t.TempDir()
	sessionID := "sess-3"
	writeSessionFile(t, dir, sessionID, piTurnEnd(10, 20, 30, 0.005))
	cost, err := ReadSessionCost(sessionID, dir, "")
	require.NoError(t, err)
	assert.InDelta(t, 0.005, cost.CostUSD, 1e-9)
	assert.Equal(t, int64(60), cost.InputTokens)
}

func TestReadSessionCost_StreamingSnapshotsIgnored(t *testing.T) {
	// Within a turn, pi's live stream emits the same usage on every
	// message_start / message_update / message_end snapshot. Only finalized
	// per-turn records (turn_end in live form, or {"type":"message"} in
	// on-disk form) should count — a turn with many in-flight snapshots
	// plus one turn_end must report the turn_end value once, not summed
	// across snapshots.
	dir := t.TempDir()
	sessionID := "sess-stream"
	lines := strings.Join([]string{
		// 3 in-flight snapshots showing the same (or smaller) running cost.
		`{"type":"message_start","message":{"usage":{"input":100,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.005}}}}`,
		`{"type":"message_update","message":{"usage":{"input":100,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.005}}}}`,
		`{"type":"message_end","message":{"usage":{"input":100,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.005}}}}`,
		// The authoritative per-turn record:
		piTurnEnd(100, 0, 0, 0.005),
	}, "\n")
	writeSessionFile(t, dir, sessionID, lines)
	cost, err := ReadSessionCost(sessionID, dir, "")
	require.NoError(t, err)
	assert.InDelta(t, 0.005, cost.CostUSD, 1e-9)
	assert.Equal(t, int64(100), cost.InputTokens)
}

// piOnDiskAssistantMessage builds the persisted on-disk record pi writes for
// each finalized assistant message: {"type":"message","message":{...}}.
// This is structurally different from the live-stream "turn_end" event.
func piOnDiskAssistantMessage(input, cacheRead, cacheWrite int, total float64) string {
	return fmt.Sprintf(
		`{"type":"message","message":{"role":"assistant","usage":{"input":%d,"output":40,"cacheRead":%d,"cacheWrite":%d,"totalTokens":%d,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":%g}}}}`,
		input, cacheRead, cacheWrite, input+cacheRead+cacheWrite+40, total,
	)
}

func TestReadSessionCost_OnDiskMessageShape(t *testing.T) {
	// Pi's on-disk session file persists assistant turns as `message` records,
	// not `turn_end` (turn_end is live-stream only). Two assistant messages
	// (e.g. a goto-loop) sum across the session.
	dir := t.TempDir()
	sessionID := "ondisk-1"
	lines := strings.Join([]string{
		`{"type":"session","id":"ondisk-1"}`,
		`{"type":"model_change","modelId":"claude-opus-4-7"}`,
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
		piOnDiskAssistantMessage(5, 3200, 0, 0.002),
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"next"}]}}`,
		piOnDiskAssistantMessage(10, 0, 0, 0.003),
	}, "\n")
	writeSessionFile(t, dir, sessionID, lines)

	cost, err := ReadSessionCost(sessionID, dir, "")
	require.NoError(t, err)
	assert.InDelta(t, 0.005, cost.CostUSD, 1e-9)
	assert.Equal(t, int64(3215), cost.InputTokens) // (5+3200+0) + (10+0+0)
}

func TestReadSessionCost_TimestampPrefixedFile(t *testing.T) {
	// pi names session files "<ISO-timestamp>_<sessionID>.jsonl". Verify
	// raymond locates the file by suffix.
	dir := t.TempDir()
	sessionID := "019e3384-f19e-772a-822f-ce68383c21d6"
	prefixedName := "2026-05-17T01-20-11-166Z_" + sessionID
	writeSessionFile(t, dir, prefixedName, piTurnEnd(50, 0, 0, 0.007))

	cost, err := ReadSessionCost(sessionID, dir, "")
	require.NoError(t, err)
	assert.InDelta(t, 0.007, cost.CostUSD, 1e-9)
	assert.Equal(t, int64(50), cost.InputTokens)
}

func TestReadSessionCost_WithSessionDir(t *testing.T) {
	dir := t.TempDir()
	sessionID := "explicit-dir-session"
	writeSessionFile(t, dir, sessionID, piTurnEnd(10, 0, 0, 0.01))
	cost, err := ReadSessionCost(sessionID, dir, "/some/cwd")
	require.NoError(t, err)
	assert.InDelta(t, 0.01, cost.CostUSD, 1e-9)
}

func TestSessionFilePath_HomeDirCwdMangling(t *testing.T) {
	// Pi stores sessions under ~/.pi/agent/sessions/<mangled-cwd>/<id>.jsonl
	// where mangled-cwd strips leading/trailing slashes, replaces remaining
	// slashes with `-`, and wraps in `--`. Verified empirically against
	// pi v0.74 (see plan record).
	cases := []struct {
		cwd  string
		want string
	}{
		{"/home/devuser/work/raymond", "--home-devuser-work-raymond--"},
		{"/tmp/pi-test-cwd-x", "--tmp-pi-test-cwd-x--"},
		{"/a/b", "--a-b--"},
	}
	for _, tc := range cases {
		got := piCwdDirName(tc.cwd)
		assert.Equal(t, tc.want, got, "cwd=%q", tc.cwd)
	}
}

func TestReadSessionCost_HomeDirPath_FileNotFound(t *testing.T) {
	// sessionDir="" exercises the home-dir path (~/.pi/agent/sessions/<cwd>/<id>.jsonl).
	// The session file won't exist; expect zero-cost with no error.
	cost, err := ReadSessionCost("nonexistent-session-piwrap-test-xyz", "", "/some/cwd")
	require.NoError(t, err)
	assert.Equal(t, 0.0, cost.CostUSD)
	assert.Equal(t, int64(0), cost.InputTokens)
}

// writeSessionFile creates a session JSONL file in dir/<sessionID>.jsonl.
func writeSessionFile(t *testing.T, dir, sessionID, content string) {
	t.Helper()
	path := filepath.Join(dir, sessionID+".jsonl")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
