package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/cli"
)

// freePort grabs an OS-assigned free TCP port for the daemon to bind. There
// is an inherent race between releasing the port and the daemon binding it,
// but it is small enough in practice that these tests have proven stable.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// chdirIsolated chdirs into a fresh temp dir whose cleanup tolerates the
// orchestrator's async writes. Successful launches spawn `go runOrchestrator`
// goroutines that may still be touching `.raymond/{state,debug,tasks}` after
// RunE returns; t.TempDir's strict RemoveAll fails with "directory not empty"
// when those writes race the cleanup. We poll RemoveAll briefly to absorb
// that race instead.
//
// Chdir is required because `ray serve` resolves the serve-pool state
// directory (.raymond/serve-state/) via config.FindRaymondDir +
// wfstate.ResolvePoolDir, which walk up from os.Getwd() — independent of
// the --workdir flag.
func chdirIsolated(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ray-serve-test-")
	require.NoError(t, err)
	t.Cleanup(func() {
		deadline := time.Now().Add(3 * time.Second)
		for {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	t.Chdir(dir)
}

// writeServeWorkflow creates <root>/<id>/ with the minimal files
// (workflow.yaml manifest + START.md entry point) required for
// daemon.NewRegistry to discover the workflow and for LaunchRun to
// resolve an entry point.
func writeServeWorkflow(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, id)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "workflow.yaml"),
		[]byte("id: "+id+"\nname: "+id+"\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "START.md"),
		[]byte("# Start\nDo it.\n"),
		0o644,
	))
}

// runServe invokes `ray serve ...` against a freshly-built test CLI on an
// ephemeral port, then triggers a graceful shutdown via POST /shutdown once
// the HTTP server is up. By the time HTTP is accepting requests,
// launchStartupRuns has already finished synchronously, so launch log lines
// are guaranteed to be in the captured output before we shut down.
func runServe(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	port := freePort(t)
	args = append(args, "--port", strconv.Itoa(port))

	var out, errOut bytes.Buffer
	c := cli.NewTestCLI(&out, &errOut)
	cmd := c.NewRootCmd()
	cmd.SetArgs(args)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd.SetContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		err = cmd.Execute()
	}()

	// Poll POST /shutdown until the daemon accepts it (signals HTTP is up
	// and the shutdown coordinator is installed). Once accepted, the
	// coordinator drains active runs and serve.RunE returns.
	shutdownURL := fmt.Sprintf("http://127.0.0.1:%d/shutdown", port)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, postErr := http.Post(shutdownURL, "application/json", nil)
		if postErr == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit within 5s of POST /shutdown")
	}
	cancel()
	return out.String(), errOut.String(), err
}

func TestServeStartupLaunches_HappyPath(t *testing.T) {
	chdirIsolated(t)

	root := t.TempDir()
	writeServeWorkflow(t, root, "a")
	writeServeWorkflow(t, root, "b")

	stdout, _, err := runServe(t,
		"serve",
		"--root", root,
		"--launch", "a",
		"--launch", "b",
	)
	require.NoError(t, err)

	require.Contains(t, stdout, "Launched run ")
	require.Contains(t, stdout, "for workflow a")
	require.Contains(t, stdout, "for workflow b")
}

// TestServeRoutesLaunchesToServePool verifies that `ray serve --launch <id>`
// writes the launched run's state file into .raymond/serve-state/ (the serve
// pool), not .raymond/state/ (the CLI pool). This is the end-to-end shape of
// the Phase-2 routing change; the manager-level tests in
// internal/daemon/runmanager_test.go cover the same property at the RunManager
// API, but only this test exercises the path through serve.go itself.
func TestServeRoutesLaunchesToServePool(t *testing.T) {
	chdirIsolated(t)

	root := t.TempDir()
	writeServeWorkflow(t, root, "a")

	stdout, _, err := runServe(t,
		"serve",
		"--root", root,
		"--launch", "a",
	)
	require.NoError(t, err)
	require.Contains(t, stdout, "Launched run ")

	cwd, err := os.Getwd()
	require.NoError(t, err)
	serveStateDir := filepath.Join(cwd, ".raymond", "serve-state")
	cliStateDir := filepath.Join(cwd, ".raymond", "state")

	// Serve pool should contain exactly the one launched run's state file.
	serveEntries, err := os.ReadDir(serveStateDir)
	require.NoError(t, err, "serve-state dir should exist after a launch")
	var serveJSON []string
	for _, e := range serveEntries {
		if filepath.Ext(e.Name()) == ".json" {
			serveJSON = append(serveJSON, e.Name())
		}
	}
	require.Len(t, serveJSON, 1, "exactly one state file should land in the serve pool")

	// CLI pool either does not exist or holds no state files — serve must
	// not have written into it.
	if cliEntries, err := os.ReadDir(cliStateDir); err == nil {
		for _, e := range cliEntries {
			require.NotEqual(t, ".json", filepath.Ext(e.Name()),
				"CLI pool must remain empty of state files after `ray serve`")
		}
	}
}

// TestServeClean_MixedSeed is test (a) of bead bd-z68c. It seeds the
// serve pool with a mix of terminal and non-terminal state files plus
// one CLI-pool file, runs `ray serve --clean`, and asserts the
// rearrangement on disk:
//
//   - non-terminal serve-state files live under serve-state/abandoned/<ts>/
//     with their original bytes (read both ends and byte-compare)
//   - terminal serve-state files remain in serve-state/ at the top level
//   - the CLI pool (.raymond/state/) is left strictly untouched
//
// "The daemon's active set is empty" is asserted indirectly: after
// --clean, no non-terminal *.json remains at the top of serve-state/,
// so recoverRuns has nothing non-terminal to relaunch. The same property
// is asserted at the daemon API in
// internal/daemon/cleanpool_test.go (TestArchiveNonTerminalServeState_RecoveryDoesNotDescend).
func TestServeClean_MixedSeed(t *testing.T) {
	chdirIsolated(t)

	cwd, err := os.Getwd()
	require.NoError(t, err)
	raymondDir := filepath.Join(cwd, ".raymond")
	serveStateDir := filepath.Join(raymondDir, "serve-state")
	cliStateDir := filepath.Join(raymondDir, "state")
	require.NoError(t, os.MkdirAll(serveStateDir, 0o755))
	require.NoError(t, os.MkdirAll(cliStateDir, 0o755))

	// Two non-terminal state files: one agent each, asking. Recovery
	// would auto-resume both absent --clean.
	nonTerminal := func(id string) []byte {
		ws := map[string]any{
			"workflow_id": id,
			"scope_dir":   "/some/scope",
			"agents": []map[string]any{
				{
					"id":            "main",
					"current_state": "WAIT.md",
					"session_id":    nil,
					"stack":         []any{},
					"status":        "asking",
				},
			},
		}
		b, err := json.Marshal(ws)
		require.NoError(t, err)
		return b
	}
	// One terminal state file: zero agents. The recovery path treats
	// this as a history-only entry; --clean must leave it in place.
	terminal := func(id string) []byte {
		ws := map[string]any{
			"workflow_id": id,
			"scope_dir":   "/some/scope",
			"agents":      []any{},
		}
		b, err := json.Marshal(ws)
		require.NoError(t, err)
		return b
	}

	ntA := nonTerminal("nt-a")
	ntB := nonTerminal("nt-b")
	tDone := terminal("t-done")

	require.NoError(t, os.WriteFile(filepath.Join(serveStateDir, "nt-a.json"), ntA, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(serveStateDir, "nt-b.json"), ntB, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(serveStateDir, "t-done.json"), tDone, 0o644))

	// CLI-pool sentinel: any bytes will do; the test only cares that
	// --clean does NOT touch this file.
	cliBytes := []byte(`{"workflow_id":"cli-run","scope_dir":"/x","agents":[]}`)
	require.NoError(t, os.WriteFile(filepath.Join(cliStateDir, "cli-run.json"), cliBytes, 0o644))

	root := t.TempDir()
	writeServeWorkflow(t, root, "a")

	stdout, _, err := runServe(t,
		"serve",
		"--root", root,
		"--clean",
	)
	require.NoError(t, err)
	require.Contains(t, stdout, "--clean: archived 2 non-terminal serve-state file(s)")

	// CLI pool is untouched (file present + byte-identical).
	gotCLI, err := os.ReadFile(filepath.Join(cliStateDir, "cli-run.json"))
	require.NoError(t, err, "CLI pool file must remain present after `ray serve --clean`")
	require.Equal(t, cliBytes, gotCLI, "CLI pool file must be byte-identical after `ray serve --clean`")

	// Terminal serve-state file still at the top of the pool, untouched.
	gotTerm, err := os.ReadFile(filepath.Join(serveStateDir, "t-done.json"))
	require.NoError(t, err, "terminal serve-state file must remain at top level after --clean")
	require.Equal(t, tDone, gotTerm, "terminal state file must be byte-identical")

	// Non-terminal serve-state files are gone from the top of the pool.
	_, err = os.Stat(filepath.Join(serveStateDir, "nt-a.json"))
	require.True(t, os.IsNotExist(err), "non-terminal file must be moved out of serve-state/ top level")
	_, err = os.Stat(filepath.Join(serveStateDir, "nt-b.json"))
	require.True(t, os.IsNotExist(err), "non-terminal file must be moved out of serve-state/ top level")

	// And they live under serve-state/abandoned/<ts>/ with original bytes.
	abandonedRoot := filepath.Join(serveStateDir, "abandoned")
	entries, err := os.ReadDir(abandonedRoot)
	require.NoError(t, err, "abandoned root should exist after --clean archived at least one file")
	require.Len(t, entries, 1, "exactly one timestamped subdirectory should be created per --clean invocation")
	tsDir := filepath.Join(abandonedRoot, entries[0].Name())
	require.True(t, entries[0].IsDir(), "abandoned/<ts> should be a directory")

	gotA, err := os.ReadFile(filepath.Join(tsDir, "nt-a.json"))
	require.NoError(t, err)
	require.Equal(t, ntA, gotA, "abandoned file must preserve its original bytes (nt-a)")
	gotB, err := os.ReadFile(filepath.Join(tsDir, "nt-b.json"))
	require.NoError(t, err)
	require.Equal(t, ntB, gotB, "abandoned file must preserve its original bytes (nt-b)")
}

func TestServeStartupLaunches_UnknownID(t *testing.T) {
	chdirIsolated(t)

	root := t.TempDir()
	// Intentionally no workflows — registry will be empty.

	stdout, _, err := runServe(t,
		"serve",
		"--root", root,
		"--launch", "nope",
	)
	require.NoError(t, err)

	require.Contains(t, stdout, "Failed to launch nope:")
}
