package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	wfstate "github.com/vector76/raymond/internal/state"
)

// abandonedDirName is the directory under the serve pool that holds
// timestamped subdirectories of non-terminal state files swept by
// `ray serve --clean`. Kept inert by design: state.ListWorkflowsIn reads
// the pool non-recursively (it skips directory entries), so an archive
// under abandoned/<ts>/ is never picked up by auto-resume or diagnostic
// listings.
const abandonedDirName = "abandoned"

// AbandonedTimestampFormat is the layout used to name the chronological
// subdirectory created by ArchiveNonTerminalServeState. It is UTC with
// nanosecond precision so two back-to-back `--clean` invocations cannot
// collide on the resulting filename — see test (b) of the bead.
const AbandonedTimestampFormat = "2006-01-02T15-04-05.000000000Z"

// ArchiveNonTerminalServeState moves every non-terminal state file in
// serveStateDir into a fresh subdirectory under
// serve-state/abandoned/<ts>/, where <ts> is now() formatted with
// AbandonedTimestampFormat.
//
// Non-terminal is determined by the same classifier the recovery path
// uses (isTerminalRecoveredState: zero remaining agents == terminal),
// so `--clean` and auto-resume agree on the set of files to act on.
// Malformed/unreadable state files are treated as non-terminal: better
// to move a corrupted file out of the way than to leave it where a
// later recovery would log it as unreadable on every restart.
//
// Files are renamed individually — not the whole serve-state directory
// — so any future sibling under serve-state/ (e.g. if the pending
// registry ever moves there) is left in place. Terminal state files
// are also left in place; only non-terminal *.json files at the top of
// the pool are swept.
//
// now provides the wall-clock used to name the archive subdirectory.
// Production callers pass time.Now; tests inject a deterministic
// function to assert nanosecond precision without relying on the
// real clock advancing. A nil now is treated as time.Now.
//
// Returns the path of the abandoned subdirectory (rooted in
// serveStateDir; absolute iff serveStateDir is) and the list of
// archived workflow ids in os.ReadDir's filename-sorted order. The
// subdirectory is created only when at least one file is moved, so a
// no-op `--clean` leaves no empty timestamp directory behind. A
// missing serve-state directory is not an error: there is nothing to
// archive and the function returns the would-be archive path with an
// empty id list.
//
// On a partial failure (one rename succeeds, the next fails) the
// already-moved files stay in abandoned/<ts>/ and the function returns
// the error along with the prefix of archived ids that did make it. A
// half-cleaned pool is the operator-visible outcome; the partial work
// is intentional because there is no safe way to roll a half-moved set
// back without risking double-clobbering.
func ArchiveNonTerminalServeState(serveStateDir string, now func() time.Time) (string, []string, error) {
	if now == nil {
		now = time.Now
	}
	abandonDir := filepath.Join(serveStateDir, abandonedDirName, now().UTC().Format(AbandonedTimestampFormat))

	entries, err := os.ReadDir(serveStateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return abandonDir, nil, nil
		}
		return "", nil, fmt.Errorf("read serve-state dir %s: %w", serveStateDir, err)
	}

	var toMove []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		ws, readErr := wfstate.ReadStateIn(id, wfstate.PoolServe, serveStateDir)
		if readErr != nil || !isTerminalRecoveredState(ws) {
			toMove = append(toMove, e.Name())
		}
	}

	if len(toMove) == 0 {
		return abandonDir, nil, nil
	}

	if err := os.MkdirAll(abandonDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create abandoned dir %s: %w", abandonDir, err)
	}

	archived := make([]string, 0, len(toMove))
	for _, name := range toMove {
		src := filepath.Join(serveStateDir, name)
		dst := filepath.Join(abandonDir, name)
		if err := os.Rename(src, dst); err != nil {
			return abandonDir, archived, fmt.Errorf("archive %s -> %s: %w", src, dst, err)
		}
		archived = append(archived, strings.TrimSuffix(name, ".json"))
	}
	return abandonDir, archived, nil
}
