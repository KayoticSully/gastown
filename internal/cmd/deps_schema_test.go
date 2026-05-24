package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// readCmdSource returns the contents of a source file in this package for
// source-pattern assertions.
func readCmdSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// TestNoObsoleteDependsOnIdColumnInCmd guards against regression to the obsolete
// dependencies/wisp_dependencies column depends_on_id. As of 2026-05-24 (gt-jpr,
// gt-c7j) that polymorphic column was split fleet-wide into depends_on_issue_id +
// depends_on_wisp_id + depends_on_external; the old column is gone, so any
// reference errors against the live schema. Note: the split column names do NOT
// contain the substring "depends_on_id", so this check only catches the obsolete
// column (including in comments).
func TestNoObsoleteDependsOnIdColumnInCmd(t *testing.T) {
	for _, name := range []string{"convoy.go", "compact.go", "convoy_stage.go", "sling_convoy.go"} {
		if strings.Contains(readCmdSource(t, name), "depends_on_id") {
			t.Errorf("%s references obsolete column depends_on_id; "+
				"port to depends_on_issue_id / depends_on_wisp_id / depends_on_external (gt-c7j)", name)
		}
	}
}

// TestCleanOrphanedWispDepsUsesSplitColumns verifies the orphaned-wisp-dep
// cleanup query resolves a dependency's target against both wisps (via
// depends_on_wisp_id) and issues (via depends_on_issue_id) and preserves
// external refs (depends_on_external), rather than the obsolete single
// depends_on_id column. A row must NOT be deleted when its target is a valid
// issue, so the cleanup checks the issues table too. See gt-c7j.
func TestCleanOrphanedWispDepsUsesSplitColumns(t *testing.T) {
	src := readCmdSource(t, "compact.go")
	for _, want := range []string{"depends_on_wisp_id", "depends_on_issue_id", "depends_on_external"} {
		if !strings.Contains(src, want) {
			t.Errorf("compact.go orphan cleanup should reference %q for the split-schema target", want)
		}
	}
	// The cleanup must consult the issues table, not only wisps, so valid
	// issue-targeted parent-child deps are preserved.
	if !strings.Contains(src, "FROM issues WHERE id = wisp_dependencies.depends_on_issue_id") {
		t.Error("compact.go orphan cleanup should check issues table for depends_on_issue_id targets")
	}
}

// TestBdDepListRawIDsQueryUsesSplitColumns verifies the SQL emitted by
// bdDepListRawIDs targets the split dependency-target columns rather than the
// obsolete depends_on_id. "down" must coalesce all three target columns (the
// polymorphic target could be a same-db issue, a wisp, or a cross-db external
// ref); "up" must match the queried ID against any of the three.
func TestBdDepListRawIDsQueryUsesSplitColumns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	binDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "query.txt")

	// Stub bd to capture the SQL argument and return an empty result set.
	bdScript := "#!/bin/sh\nprintf '%s\\n' \"$2\" > " + capture + "\necho '[]'\n"
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	cases := []struct {
		direction  string
		mustHave   []string
		mustNotHave string
	}{
		{
			direction: "down",
			mustHave:  []string{"COALESCE(", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external", "issue_id = 'gt-abc123'"},
		},
		{
			direction: "up",
			mustHave:  []string{"depends_on_issue_id = 'gt-abc123'", "depends_on_wisp_id = 'gt-abc123'", "depends_on_external = 'gt-abc123'"},
		},
	}
	for _, tc := range cases {
		if _, err := bdDepListRawIDs(binDir, "gt-abc123", tc.direction, "tracks"); err != nil {
			t.Fatalf("bdDepListRawIDs(%s): %v", tc.direction, err)
		}
		got, err := os.ReadFile(capture)
		if err != nil {
			t.Fatalf("read captured query (%s): %v", tc.direction, err)
		}
		query := string(got)
		if strings.Contains(query, "depends_on_id") {
			t.Errorf("%s query references obsolete depends_on_id: %s", tc.direction, query)
		}
		for _, want := range tc.mustHave {
			if !strings.Contains(query, want) {
				t.Errorf("%s query should contain %q, got: %s", tc.direction, want, query)
			}
		}
	}
}
