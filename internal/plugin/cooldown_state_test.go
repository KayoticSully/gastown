package plugin

import (
	"os"
	"testing"
	"time"
)

func TestRecordAndReadCooldownState(t *testing.T) {
	town := t.TempDir()

	if _, ok := lastCooldownState(town, "dolt-backup"); ok {
		t.Fatal("expected no durable state before any run")
	}

	now := time.Now()
	if err := recordCooldownState(town, "dolt-backup", now); err != nil {
		t.Fatalf("recordCooldownState: %v", err)
	}

	got, ok := lastCooldownState(town, "dolt-backup")
	if !ok {
		t.Fatal("expected durable state after recording")
	}
	// RFC3339 truncates to seconds; compare at that resolution.
	if diff := got.Sub(now); diff > time.Second || diff < -time.Second {
		t.Errorf("round-trip drift: got %v, recorded %v (diff %v)", got, now, diff)
	}
}

func TestRecordCooldownStateOverwrites(t *testing.T) {
	town := t.TempDir()
	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now()

	if err := recordCooldownState(town, "p", older); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if err := recordCooldownState(town, "p", newer); err != nil {
		t.Fatalf("second record: %v", err)
	}

	got, ok := lastCooldownState(town, "p")
	if !ok {
		t.Fatal("expected durable state")
	}
	if diff := got.Sub(newer); diff > time.Second || diff < -time.Second {
		t.Errorf("expected latest timestamp, got %v want ~%v", got, newer)
	}
}

func TestSanitizePluginName(t *testing.T) {
	cases := map[string]string{
		"dolt-backup":  "dolt-backup",
		"a/b":          "a_b",
		"a\\b":         "a_b",
		"weird:name":   "weird_name",
		"../../escape": "____escape", // each ".." and "/" -> "_"
		"":             "_",
	}
	for in, want := range cases {
		if got := sanitizePluginName(in); got != want {
			t.Errorf("sanitizePluginName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizedNameStaysInDir(t *testing.T) {
	town := t.TempDir()
	// A malicious plugin name must not let the state file escape the dir.
	if err := recordCooldownState(town, "../../etc/passwd", time.Now()); err != nil {
		t.Fatalf("recordCooldownState: %v", err)
	}
	dir := cooldownStateDir(town)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading state dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 state file in dir, got %d", len(entries))
	}
}

// TestCountRunsSinceUsesDurableState verifies the cooldown gate is satisfied by
// durable state alone — i.e. without consulting the (GC-eligible) receipt
// beads. This is the core of the hq-1o1 fix: after `bd mol wisp gc` deletes the
// receipts, the gate must still report the recent run. The durable-hit path
// never shells out to bd, so this runs without a beads installation.
func TestCountRunsSinceUsesDurableState(t *testing.T) {
	town := t.TempDir()
	r := NewRecorder(town)

	// Ran 5 minutes ago, 15m cooldown -> still within window -> gate closed.
	if err := recordCooldownState(town, "dolt-backup", time.Now().Add(-5*time.Minute)); err != nil {
		t.Fatalf("recordCooldownState: %v", err)
	}
	count, err := r.CountRunsSince("dolt-backup", "15m")
	if err != nil {
		t.Fatalf("CountRunsSince: %v", err)
	}
	if count == 0 {
		t.Error("expected gate closed (count>0) from durable state within window, got 0")
	}
}

func TestCountRunsSinceRejectsBadDuration(t *testing.T) {
	town := t.TempDir()
	r := NewRecorder(town)
	if err := recordCooldownState(town, "p", time.Now()); err != nil {
		t.Fatalf("recordCooldownState: %v", err)
	}
	if _, err := r.CountRunsSince("p", "not-a-duration"); err == nil {
		t.Error("expected error for invalid duration, got nil")
	}
}
