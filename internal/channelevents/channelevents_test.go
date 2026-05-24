package channelevents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmitToTown(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	path, err := EmitToTown(townRoot, "dashboard", "refinery", "MERGE_READY", []string{
		"source=witness",
		"rig=dashboard",
	})
	if err != nil {
		t.Fatalf("EmitToTown failed: %v", err)
	}

	if !strings.HasSuffix(path, ".event") {
		t.Errorf("expected .event suffix, got %q", path)
	}

	// Event must be written under the rig-scoped directory (gt-gyc).
	wantDir := filepath.Join(townRoot, "events", "dashboard", "refinery")
	if got := filepath.Dir(path); got != wantDir {
		t.Errorf("event dir = %q, want %q", got, wantDir)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading event file: %v", err)
	}

	var event map[string]interface{}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("unmarshaling event: %v", err)
	}

	if event["type"] != "MERGE_READY" {
		t.Errorf("type = %v, want MERGE_READY", event["type"])
	}
	if event["channel"] != "refinery" {
		t.Errorf("channel = %v, want refinery", event["channel"])
	}
	if event["rig"] != "dashboard" {
		t.Errorf("rig = %v, want dashboard", event["rig"])
	}

	payload, ok := event["payload"].(map[string]interface{})
	if !ok {
		t.Fatal("payload is not a map")
	}
	if payload["source"] != "witness" {
		t.Errorf("payload.source = %v, want witness", payload["source"])
	}
	if payload["rig"] != "dashboard" {
		t.Errorf("payload.rig = %v, want dashboard", payload["rig"])
	}
}

func TestEmitToTown_InvalidChannel(t *testing.T) {
	t.Parallel()
	_, err := EmitToTown(t.TempDir(), "", "../escape", "TEST", nil)
	if err == nil {
		t.Error("expected error for invalid channel name")
	}
}

func TestEmitToTown_InvalidRig(t *testing.T) {
	t.Parallel()
	// A rig name with a path-traversal component must be rejected so it can
	// never escape the events directory.
	_, err := EmitToTown(t.TempDir(), "../escape", "refinery", "TEST", nil)
	if err == nil {
		t.Error("expected error for invalid rig name")
	}
}

func TestEmitToTown_UniqueFilenames(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	seen := make(map[string]bool)

	for i := 0; i < 10; i++ {
		path, err := EmitToTown(townRoot, "gastown", "test", "EVENT", nil)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if seen[path] {
			t.Errorf("duplicate filename: %s", path)
		}
		seen[path] = true
	}
}

func TestValidChannelName(t *testing.T) {
	t.Parallel()
	valid := []string{"refinery", "witness", "my-channel", "test_chan", "abc123"}
	for _, name := range valid {
		if !ValidChannelName.MatchString(name) {
			t.Errorf("%q should be valid", name)
		}
	}

	invalid := []string{"../escape", "has space", "has/slash", "", "has.dot"}
	for _, name := range invalid {
		if ValidChannelName.MatchString(name) {
			t.Errorf("%q should be invalid", name)
		}
	}
}

func TestEmitToTown_CreatesDirectory(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	channelDir := filepath.Join(townRoot, "events", "myrig", "newchannel")

	if _, err := os.Stat(channelDir); !os.IsNotExist(err) {
		t.Fatal("channel dir should not exist yet")
	}

	_, err := EmitToTown(townRoot, "myrig", "newchannel", "TEST", nil)
	if err != nil {
		t.Fatalf("EmitToTown failed: %v", err)
	}

	if _, err := os.Stat(channelDir); err != nil {
		t.Errorf("channel dir should exist after emit: %v", err)
	}
}

func TestEventDir(t *testing.T) {
	t.Parallel()
	townRoot := "/town"

	// Rig-scoped path keeps each rig's channel isolated.
	if got, want := EventDir(townRoot, "gastown", "refinery"), filepath.Join("/town", "events", "gastown", "refinery"); got != want {
		t.Errorf("EventDir(gastown) = %q, want %q", got, want)
	}

	// Different rigs resolve to different directories — this is what prevents
	// cross-rig event theft (gt-gyc).
	if EventDir(townRoot, "gastown", "refinery") == EventDir(townRoot, "beads", "refinery") {
		t.Error("different rigs must resolve to different event dirs")
	}

	// Empty rig falls back to the legacy unscoped path.
	if got, want := EventDir(townRoot, "", "refinery"), filepath.Join("/town", "events", "refinery"); got != want {
		t.Errorf("EventDir(empty rig) = %q, want %q (legacy fallback)", got, want)
	}
}

// TestEmitToTown_RigIsolation is the acceptance test for gt-gyc: an event
// emitted for one rig must not be visible in another rig's channel directory,
// so a beads-rig await-event cannot consume or delete gastown's events.
func TestEmitToTown_RigIsolation(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	if _, err := EmitToTown(townRoot, "gastown", "refinery", "MQ_SUBMIT", nil); err != nil {
		t.Fatalf("emit for gastown failed: %v", err)
	}

	// gastown's refinery dir holds exactly one event.
	gastownEntries, err := os.ReadDir(EventDir(townRoot, "gastown", "refinery"))
	if err != nil {
		t.Fatalf("reading gastown event dir: %v", err)
	}
	if len(gastownEntries) != 1 {
		t.Errorf("gastown refinery dir has %d events, want 1", len(gastownEntries))
	}

	// The beads rig's refinery dir must NOT see gastown's event. The directory
	// typically does not exist yet, which is correct isolation.
	beadsEntries, err := os.ReadDir(EventDir(townRoot, "beads", "refinery"))
	if err == nil && len(beadsEntries) != 0 {
		t.Errorf("beads refinery dir saw %d events; must be isolated from gastown", len(beadsEntries))
	}
}
