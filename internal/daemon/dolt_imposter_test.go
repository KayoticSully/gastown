package daemon

import (
	"sync/atomic"
	"testing"
)

// TestStartLocked_EvictsImposterBeforeStart reproduces gt-ac8: when the real
// Dolt server has died and bd's embedded fallback has squatted the port with a
// rogue server, the daemon's restart path must kill the imposter BEFORE it
// launches a new server. Otherwise the new server cannot bind the port and the
// daemon crash-loops with no auto-recovery.
func TestStartLocked_EvictsImposterBeforeStart(t *testing.T) {
	m := newTestManager(t)

	var killed atomic.Bool
	var startedAfterKill atomic.Bool

	// An imposter holds the port until it is killed.
	m.checkPortConflictFn = func() (int, string) {
		if killed.Load() {
			return 0, ""
		}
		return 99999, "/some/rig/.beads/dolt"
	}
	m.killImpostersFn = func() error {
		killed.Store(true)
		return nil
	}
	m.startFn = func() error {
		if !killed.Load() {
			t.Error("startFn called before imposter was killed — new server would fail to bind the squatted port")
		}
		startedAfterKill.Store(true)
		return nil
	}

	if err := m.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}

	if !killed.Load() {
		t.Error("expected imposter to be killed before start")
	}
	if !startedAfterKill.Load() {
		t.Error("expected server to start after the imposter was evicted")
	}
}

// TestStartLocked_NoEvictionWhenPortFree verifies the eviction is a no-op when
// no imposter is squatting the port (KillImposters must not be invoked).
func TestStartLocked_NoEvictionWhenPortFree(t *testing.T) {
	m := newTestManager(t)

	var killCalled atomic.Bool
	m.checkPortConflictFn = func() (int, string) { return 0, "" }
	m.killImpostersFn = func() error {
		killCalled.Store(true)
		return nil
	}

	if err := m.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}

	if killCalled.Load() {
		t.Error("KillImposters should not be called when the port is free")
	}
}

// TestEnsureRunning_DeadServerWithImposterRecovers reproduces the full gt-ac8
// outage path: the server is not running (died) and an imposter squats the
// port. EnsureRunning must evict the imposter and start a fresh server rather
// than crash-looping.
func TestEnsureRunning_DeadServerWithImposterRecovers(t *testing.T) {
	m := newTestManager(t)
	// Server is not running (runningFn already returns 0,false in newTestManager).

	var killed atomic.Bool
	var started atomic.Bool

	m.checkPortConflictFn = func() (int, string) {
		if killed.Load() {
			return 0, ""
		}
		return 88888, "/some/rig/.beads/dolt"
	}
	m.killImpostersFn = func() error {
		killed.Store(true)
		return nil
	}
	m.startFn = func() error {
		if !killed.Load() {
			t.Error("startFn called before imposter eviction")
		}
		started.Store(true)
		return nil
	}

	if err := m.EnsureRunning(); err != nil {
		t.Fatalf("EnsureRunning() returned error: %v", err)
	}

	if !killed.Load() {
		t.Error("expected imposter to be evicted during recovery")
	}
	if !started.Load() {
		t.Error("expected a fresh server to start after eviction")
	}
}
