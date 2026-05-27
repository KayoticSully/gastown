package daemon

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const mib = 1024 * 1024

func TestMemoryCeilingBytes(t *testing.T) {
	tests := []struct {
		name      string
		config    *DoltServerConfig
		wantBytes int64
	}{
		{
			name:      "nil config disabled",
			config:    nil,
			wantBytes: 0,
		},
		{
			name:      "unset uses default ceiling",
			config:    &DoltServerConfig{MemoryCeilingMB: 0},
			wantBytes: int64(defaultDoltMemoryCeilingMB) * mib,
		},
		{
			name:      "negative is explicitly disabled",
			config:    &DoltServerConfig{MemoryCeilingMB: -1},
			wantBytes: 0,
		},
		{
			name:      "positive uses configured value",
			config:    &DoltServerConfig{MemoryCeilingMB: 512},
			wantBytes: 512 * mib,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &DoltServerManager{
				config: tt.config,
				logger: func(format string, v ...interface{}) {},
			}
			if got := m.memoryCeilingBytes(); got != tt.wantBytes {
				t.Errorf("memoryCeilingBytes() = %d, want %d", got, tt.wantBytes)
			}
		})
	}
}

func TestDefaultConfig_MemoryCeiling(t *testing.T) {
	cfg := DefaultDoltServerConfig("/tmp/test")
	if cfg.MemoryCeilingMB != defaultDoltMemoryCeilingMB {
		t.Errorf("expected MemoryCeilingMB %d, got %d", defaultDoltMemoryCeilingMB, cfg.MemoryCeilingMB)
	}
}

// TestEnsureRunning_MemoryCeilingTriggersRestart verifies that a healthy,
// writable server whose RSS exceeds the configured ceiling is proactively
// stopped and restarted (graceful recycle before the OS OOM-kills it).
func TestEnsureRunning_MemoryCeilingTriggersRestart(t *testing.T) {
	var stopCount, startCount atomic.Int32
	var memoryAlerted atomic.Bool
	var running atomic.Bool
	running.Store(true)

	m := newTestManager(t)
	m.config.MemoryCeilingMB = 1000
	m.runningFn = func() (int, bool) {
		if running.Load() {
			return 1234, true
		}
		return 0, false
	}
	m.healthCheckFn = func() error { return nil }
	m.writeProbeCheckFn = func() error { return nil }
	m.rssCheckFn = func(pid int) int64 { return 1100 * mib } // over ceiling
	m.stopFn = func() {
		stopCount.Add(1)
		running.Store(false)
	}
	m.startFn = func() error {
		startCount.Add(1)
		running.Store(true)
		return nil
	}
	m.memoryAlertFn = func(rss, ceiling int64) { memoryAlerted.Store(true) }
	m.sleepFn = func(d time.Duration) {}

	if err := m.EnsureRunning(); err != nil {
		t.Fatalf("EnsureRunning returned error: %v", err)
	}

	if got := stopCount.Load(); got != 1 {
		t.Errorf("expected 1 stop, got %d", got)
	}
	if got := startCount.Load(); got != 1 {
		t.Errorf("expected 1 start (restart), got %d", got)
	}
	if !memoryAlerted.Load() {
		t.Error("expected memory ceiling alert to be sent")
	}
}

// TestEnsureRunning_MemoryCeilingWritesUnhealthySignal verifies that a
// memory-ceiling restart records the DOLT_UNHEALTHY signal with the
// "memory_ceiling" reason so witnesses can see why the recycle happened.
func TestEnsureRunning_MemoryCeilingWritesUnhealthySignal(t *testing.T) {
	var running atomic.Bool
	running.Store(true)

	m := newTestManager(t)
	m.config.MemoryCeilingMB = 1000
	m.runningFn = func() (int, bool) {
		if running.Load() {
			return 1234, true
		}
		return 0, false
	}
	m.healthCheckFn = func() error { return nil }
	m.writeProbeCheckFn = func() error { return nil }
	m.rssCheckFn = func(pid int) int64 { return 2000 * mib }
	m.stopFn = func() { running.Store(false) }
	m.startFn = func() error {
		running.Store(true)
		return nil
	}
	m.memoryAlertFn = func(rss, ceiling int64) {}
	m.sleepFn = func(d time.Duration) {}

	_ = m.EnsureRunning()

	data, err := os.ReadFile(m.unhealthySignalFile())
	if err != nil {
		t.Fatalf("expected DOLT_UNHEALTHY signal file: %v", err)
	}
	if !strings.Contains(string(data), "memory_ceiling") {
		t.Errorf("expected 'memory_ceiling' reason in signal file, got: %s", string(data))
	}
}

// TestEnsureRunning_BelowMemoryCeilingNoRestart verifies that a healthy server
// under the memory ceiling is left running (no proactive restart).
func TestEnsureRunning_BelowMemoryCeilingNoRestart(t *testing.T) {
	var startCount atomic.Int32

	m := newTestManager(t)
	m.config.MemoryCeilingMB = 1000
	m.runningFn = func() (int, bool) { return 1234, true }
	m.healthCheckFn = func() error { return nil }
	m.writeProbeCheckFn = func() error { return nil }
	m.rssCheckFn = func(pid int) int64 { return 500 * mib } // under ceiling
	m.startFn = func() error {
		startCount.Add(1)
		return nil
	}
	m.sleepFn = func(d time.Duration) {}

	if err := m.EnsureRunning(); err != nil {
		t.Fatalf("EnsureRunning returned error: %v", err)
	}
	if got := startCount.Load(); got != 0 {
		t.Errorf("expected no restart under ceiling, got %d starts", got)
	}
}

// TestEnsureRunning_MemoryCeilingDisabledNoRestart verifies that a negative
// ceiling disables the memory watchdog entirely, even at extreme RSS.
func TestEnsureRunning_MemoryCeilingDisabledNoRestart(t *testing.T) {
	var startCount atomic.Int32

	m := newTestManager(t)
	m.config.MemoryCeilingMB = -1 // disabled
	m.runningFn = func() (int, bool) { return 1234, true }
	m.healthCheckFn = func() error { return nil }
	m.writeProbeCheckFn = func() error { return nil }
	m.rssCheckFn = func(pid int) int64 { return 9999 * mib }
	m.startFn = func() error {
		startCount.Add(1)
		return nil
	}
	m.sleepFn = func(d time.Duration) {}

	if err := m.EnsureRunning(); err != nil {
		t.Fatalf("EnsureRunning returned error: %v", err)
	}
	if got := startCount.Load(); got != 0 {
		t.Errorf("expected no restart when disabled, got %d starts", got)
	}
}
