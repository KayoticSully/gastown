package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"
)

// writeExitZeroScript writes an executable shell script that ignores its args
// and exits 0, returning its path. Used to make subprocess calls (e.g. the
// `gt sling` dog dispatch) succeed deterministically without a real binary.
func writeExitZeroScript(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("writing %s script: %v", name, err)
	}
	return p
}

// TestReapWispsClosesPluginBeadsWhenDogDispatched is the gt-b2s regression test.
//
// The daemon reaper cycle dispatches a Dog to do the heavy wisp reaping. Before
// gt-b2s, the plugin-bead fast-track closers (ClosePluginReceipts +
// ClosePluginDispatches) ran ONLY in the inline fallback that fires when Dog
// dispatch FAILS — so in the normal dog-dispatch-SUCCESS path the daemon never
// closed plugin run receipts ("Plugin run:"/dog RESULT chore wisps) or plugin
// dispatch mails ("Plugin: <name>"). They accumulated open until the 7-day
// AutoClose, regrowing hq bloat after every drain.
//
// This test makes Dog dispatch SUCCEED (fake gt exits 0) and asserts the daemon
// still closes plugin beads for every configured database.
//
// RED before the fix: closePluginBeads is never invoked in the primary path, so
// closedDBs is empty. GREEN after reapWisps calls closePluginBeads unconditionally.
func TestReapWispsClosesPluginBeadsWhenDogDispatched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd/gt are POSIX shell scripts")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(filepath.Join(stateDir, "beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	bdPath := writeFakeBd(t, tmp)
	t.Setenv("FAKE_BD_STATE", stateDir)
	// Fake gt makes `gt sling` (dispatchReaperDog) succeed → exercises the
	// PRIMARY dog-dispatch path, not the inline fallback.
	gtPath := writeExitZeroScript(t, tmp, "gt")

	var mu sync.Mutex
	var closedDBs []string
	d := &Daemon{
		config: &Config{TownRoot: tmp},
		logger: log.New(io.Discard, "", 0),
		bdPath: bdPath,
		gtPath: gtPath,
		patrolConfig: &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{
				WispReaper: &WispReaperConfig{
					Enabled:   true,
					Databases: []string{"hq", "gt"},
				},
			},
		},
		closePluginBeadsInDB: func(_ int, dbName string, _ time.Duration, _ bool) (int, int, error) {
			mu.Lock()
			defer mu.Unlock()
			closedDBs = append(closedDBs, dbName)
			return 0, 0, nil
		},
	}

	d.reapWisps()

	mu.Lock()
	got := append([]string(nil), closedDBs...)
	mu.Unlock()
	sort.Strings(got)

	want := []string{"gt", "hq"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after a dog-dispatched reaper cycle, plugin beads were closed for %v; want %v "+
			"(the daemon must fast-track close plugin beads itself — the dog-driven reaper formula never does)", got, want)
	}
}

// TestClosePluginBeadsAggregatesAndSkipsErrors verifies the per-database loop:
// counts are summed and a database whose close errors is skipped without
// aborting the remaining databases.
func TestClosePluginBeadsAggregatesAndSkipsErrors(t *testing.T) {
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(io.Discard, "", 0),
		closePluginBeadsInDB: func(_ int, dbName string, _ time.Duration, _ bool) (int, int, error) {
			switch dbName {
			case "hq":
				return 45, 5, nil
			case "gt":
				return 0, 0, errBoom
			case "beads":
				return 1, 2, nil
			}
			return 0, 0, nil
		},
	}

	receipts, dispatches := d.closePluginBeads([]string{"hq", "gt", "beads"}, false)
	if receipts != 46 {
		t.Errorf("receiptsClosed = %d; want 46 (45 hq + 1 beads, gt errored)", receipts)
	}
	if dispatches != 7 {
		t.Errorf("dispatchesClosed = %d; want 7 (5 hq + 2 beads, gt errored)", dispatches)
	}
}

var errBoom = errBoomType("boom")

type errBoomType string

func (e errBoomType) Error() string { return string(e) }
