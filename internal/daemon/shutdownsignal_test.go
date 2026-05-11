package daemon

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShutdownSignal_AtRest(t *testing.T) {
	s := NewShutdownSignal(t.TempDir())

	assert.False(t, s.IsRequested(), "freshly constructed signal must not be requested")

	// Await channel must be open and not yet receivable.
	select {
	case <-s.Await():
		t.Fatal("Await channel closed before Request was called")
	default:
	}
}

func TestShutdownSignal_RequestFlipsAndUnblocks(t *testing.T) {
	s := NewShutdownSignal(t.TempDir())

	s.Request()

	assert.True(t, s.IsRequested(), "IsRequested must be true after Request")

	select {
	case <-s.Await():
		// expected: Await channel is closed.
	case <-time.After(time.Second):
		t.Fatal("Await channel did not unblock within timeout after Request")
	}
}

func TestShutdownSignal_RequestIdempotent_Sequential(t *testing.T) {
	s := NewShutdownSignal(t.TempDir())

	require.NotPanics(t, func() {
		s.Request()
		s.Request()
		s.Request()
	})
	assert.True(t, s.IsRequested())
}

func TestShutdownSignal_RequestIdempotent_Concurrent(t *testing.T) {
	s := NewShutdownSignal(t.TempDir())

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			s.Request()
		}()
	}
	require.NotPanics(t, func() {
		close(start)
		wg.Wait()
	})
	assert.True(t, s.IsRequested())

	// Await must still be closed exactly once — readable without blocking.
	select {
	case <-s.Await():
	default:
		t.Fatal("Await channel still open after concurrent Request calls")
	}
}

func TestShutdownSignal_AwaitReadableMultipleTimes(t *testing.T) {
	s := NewShutdownSignal(t.TempDir())
	s.Request()

	// A closed channel is readable arbitrarily many times without panic and
	// always yields the zero value immediately.
	for i := 0; i < 5; i++ {
		select {
		case v, ok := <-s.Await():
			assert.Equal(t, struct{}{}, v)
			assert.False(t, ok, "channel should be closed (ok == false)")
		case <-time.After(time.Second):
			t.Fatalf("Await read %d blocked unexpectedly", i)
		}
	}
}

func TestShutdownSignal_SentinelPath(t *testing.T) {
	dir := t.TempDir()
	s := NewShutdownSignal(dir)

	assert.Equal(t, filepath.Join(dir, "shutdown.sentinel"), s.SentinelPath())
}
