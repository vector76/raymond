package daemon

import (
	"path/filepath"
	"sync"
)

// shutdownSentinelFilename is the basename of the on-disk marker that indicates
// the daemon is shutting down. The file itself is created/removed by a later
// bead; this primitive only resolves and exposes its path so other components
// can agree on a single location.
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
// creating and removing the on-disk sentinel is the responsibility of a later
// bead.
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
// to true and closes the Await channel; subsequent calls are no-ops. Safe to
// call concurrently.
func (s *ShutdownSignal) Request() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requested {
		return
	}
	s.requested = true
	close(s.awaitCh)
}

// SentinelPath returns the resolved path to the shutdown sentinel file. The
// path is fixed at construction.
func (s *ShutdownSignal) SentinelPath() string {
	return s.sentinelPath
}
