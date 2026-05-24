package doltserver

import (
	"os"
	"strings"
	"testing"
)

// TestWispsMigrateUsesSplitDependencyColumns guards the wisp_dependencies
// schema-defining and data-copying code against the obsolete polymorphic
// depends_on_id column. bd 1.0.4 owns the dependency tables fleet-wide and
// split depends_on_id into depends_on_issue_id / depends_on_wisp_id /
// depends_on_external (gt-c7j). gt only CREATEs wisp_dependencies on a fresh
// database (existing DBs are skipped), so its DDL must match bd's current
// columns to avoid reintroducing schema skew, and the dependency-copy INSERT
// must carry all three split target columns.
func TestWispsMigrateUsesSplitDependencyColumns(t *testing.T) {
	data, err := os.ReadFile("wisps_migrate.go")
	if err != nil {
		t.Fatalf("read wisps_migrate.go: %v", err)
	}
	src := string(data)

	if strings.Contains(src, "depends_on_id") {
		t.Error("wisps_migrate.go references obsolete column depends_on_id; " +
			"align the wisp_dependencies CREATE TABLE + dependency copy to the split schema (gt-c7j)")
	}
	for _, col := range []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"} {
		if !strings.Contains(src, col) {
			t.Errorf("wisps_migrate.go should reference split column %q", col)
		}
	}
}
