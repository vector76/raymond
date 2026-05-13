package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vector76/raymond/internal/cli"
)

// withClosedStdin replaces os.Stdin with the read end of a pipe whose write
// end is immediately closed. Reads return EOF, so the MCP server's scanner
// loop exits on its first iteration and triggers the mcpDone branch in
// serve.go's select — letting the test exercise the full RunE path without
// sending a signal.
func withClosedStdin(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, w.Close())
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = r.Close()
	})
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

// runServe invokes `ray serve ...` against a freshly-built test CLI with a
// cancellable context, then cancels the context after Execute returns so any
// orchestrator goroutines spawned by --launch wind down promptly rather than
// running real LLM work past the assertion phase.
func runServe(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	c := cli.NewTestCLI(&out, &errOut)
	cmd := c.NewRootCmd()
	cmd.SetArgs(args)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd.SetContext(ctx)

	err = cmd.Execute()
	cancel()
	return out.String(), errOut.String(), err
}

func TestServeStartupLaunches_HappyPath(t *testing.T) {
	withClosedStdin(t)
	chdirIsolated(t)

	root := t.TempDir()
	writeServeWorkflow(t, root, "a")
	writeServeWorkflow(t, root, "b")

	stdout, stderr, err := runServe(t,
		"serve",
		"--root", root,
		"--mcp",
		"--no-http",
		"--launch", "a",
		"--launch", "b",
	)
	require.NoError(t, err)

	require.Contains(t, stderr, "Launched run ")
	require.Contains(t, stderr, "for workflow a")
	require.Contains(t, stderr, "for workflow b")

	// Under --mcp, stdout is reserved for JSON-RPC; launch lines must
	// not leak there.
	require.NotContains(t, stdout, "Launched run")
}

// TestServeRoutesLaunchesToServePool verifies that `ray serve --launch <id>`
// writes the launched run's state file into .raymond/serve-state/ (the serve
// pool), not .raymond/state/ (the CLI pool). This is the end-to-end shape of
// the Phase-2 routing change; the manager-level tests in
// internal/daemon/runmanager_test.go cover the same property at the RunManager
// API, but only this test exercises the path through serve.go itself.
func TestServeRoutesLaunchesToServePool(t *testing.T) {
	withClosedStdin(t)
	chdirIsolated(t)

	root := t.TempDir()
	writeServeWorkflow(t, root, "a")

	_, stderr, err := runServe(t,
		"serve",
		"--root", root,
		"--mcp",
		"--no-http",
		"--launch", "a",
	)
	require.NoError(t, err)
	require.Contains(t, stderr, "Launched run ")

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

func TestServeStartupLaunches_UnknownID(t *testing.T) {
	withClosedStdin(t)
	chdirIsolated(t)

	root := t.TempDir()
	// Intentionally no workflows — registry will be empty.

	_, stderr, err := runServe(t,
		"serve",
		"--root", root,
		"--mcp",
		"--no-http",
		"--launch", "nope",
	)
	require.NoError(t, err)

	require.Contains(t, stderr, "Failed to launch nope:")
}
