package daemon

import (
	"os"
	"path/filepath"
	"sync"
)

// shutdownSentinelFilename is the basename of the on-disk marker that indicates
// the daemon is shutting down. WriteSentinel/RemoveStaleSentinel manage the
// file itself; Request() creates it as a side effect so callers see a single
// atomic transition.
const shutdownSentinelFilename = "shutdown.sentinel"

// ShutdownSignal is the in-process source-of-truth for "is the daemon shutting
// down". Executors, the coordinator, and HTTP handlers consult it to decide
// whether to keep accepting work. It is safe for concurrent use.
//
// Once Request has been called, IsRequested returns true forever and Await's
// channel is closed. The transition is one-way and idempotent.
type ShutdownSignal struct {
	mu        sync.RWMutex
	requested bool
	awaitCh   chan struct{}

	// sentinelPath is resolved at construction and never mutated, so it can be
	// read without holding the lock.
	sentinelPath string
}

// NewShutdownSignal constructs a ShutdownSignal whose sentinel path is
// shutdown.sentinel joined onto raymondDir. It does not touch the filesystem;
// the sentinel file is created by Request/WriteSentinel and cleared by
// RemoveStaleSentinel.
func NewShutdownSignal(raymondDir string) *ShutdownSignal {
	return &ShutdownSignal{
		awaitCh:      make(chan struct{}),
		sentinelPath: filepath.Join(raymondDir, shutdownSentinelFilename),
	}
}

// IsRequested reports whether Request has been called. Cheap and safe to call
// from hot paths.
func (s *ShutdownSignal) IsRequested() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.requested
}

// Await returns a channel that is closed when Request is first called. Multiple
// callers may select on the returned channel; all are released by the single
// close.
func (s *ShutdownSignal) Await() <-chan struct{} {
	return s.awaitCh
}

// Request marks the daemon as shutting down. The first call flips IsRequested
// to true, closes the Await channel, and writes the on-disk sentinel;
// subsequent calls are no-ops. Safe to call concurrently.
//
// Ordering: under the write lock we (1) flip the bool, (2) close awaitCh, and
// (3) call WriteSentinel. Holding the lock across the file write means no
// concurrent observer can see "IsRequested() == true" before the sentinel
// exists on disk — the in-memory and on-disk views flip together. The lock is
// held for at most one short file create; the trade-off is intentional.
//
// A WriteSentinel error here is best-effort: in-memory shutdown state has
// already flipped and callers selecting on Await() are about to wake up. The
// error is swallowed so Request remains a no-return primitive; callers that
// need to surface the failure should invoke WriteSentinel directly.
func (s *ShutdownSignal) Request() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requested {
		return
	}
	s.requested = true
	close(s.awaitCh)
	_ = s.writeSentinelLocked()
}

// WriteSentinel creates the sentinel file at SentinelPath. Idempotent at the
// file level: a pre-existing file is not an error. Content is empty; only the
// file's presence is meaningful.
func (s *ShutdownSignal) WriteSentinel() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeSentinelLocked()
}

// writeSentinelLocked performs the actual file creation. The caller must hold
// s.mu for writing so the file create is ordered with Request's in-memory
// transition (see Request for the ordering rationale).
func (s *ShutdownSignal) writeSentinelLocked() error {
	f, err := os.OpenFile(s.sentinelPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// RemoveStaleSentinel unlinks the sentinel file if present. Returns nil when
// the file is absent, so it is safe to call unconditionally at daemon startup
// (to clear a leftover from a prior crash) and at daemon exit.
func (s *ShutdownSignal) RemoveStaleSentinel() error {
	if err := os.Remove(s.sentinelPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SentinelPath returns the resolved path to the shutdown sentinel file. The
// path is fixed at construction.
func (s *ShutdownSignal) SentinelPath() string {
	return s.sentinelPath
}
