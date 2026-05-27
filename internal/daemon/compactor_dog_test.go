package daemon

import (
	"testing"
	"time"
)

// TestCompactorDogInterval_Default verifies the compactor cadence default.
// At ~78 commits/hr (observed on hq), a 24h cadence let history bloat reach
// ~3.7k commits between compactions, driving Dolt RSS growth. A 6h cadence
// bounds bloat to ~2.5k commits while keeping the 2000-commit threshold that
// guards against the escalation feedback loop (gt-enz).
func TestCompactorDogInterval_Default(t *testing.T) {
	if got := compactorDogInterval(nil); got != defaultCompactorDogInterval {
		t.Errorf("compactorDogInterval(nil) = %v, want %v", got, defaultCompactorDogInterval)
	}
	if defaultCompactorDogInterval != 6*time.Hour {
		t.Errorf("defaultCompactorDogInterval = %v, want 6h", defaultCompactorDogInterval)
	}
}

// TestCompactorDogInterval_Configured verifies an explicit interval overrides
// the default.
func TestCompactorDogInterval_Configured(t *testing.T) {
	cfg := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CompactorDog: &CompactorDogConfig{IntervalStr: "2h"},
		},
	}
	if got := compactorDogInterval(cfg); got != 2*time.Hour {
		t.Errorf("compactorDogInterval = %v, want 2h", got)
	}
}

// TestDefaultLifecycle_CompactorInterval verifies the lifecycle defaults wire
// the 6h cadence into the generated daemon config.
func TestDefaultLifecycle_CompactorInterval(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	if got := cfg.Patrols.CompactorDog.IntervalStr; got != "6h" {
		t.Errorf("expected compactor_dog interval 6h, got %s", got)
	}
}
