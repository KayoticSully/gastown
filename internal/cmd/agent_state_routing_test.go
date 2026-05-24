package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestAgentStateRoutesToTownDB verifies that `gt agent state` resolves agent
// beads against the TOWN database, even when invoked from a rig agent's CWD.
//
// Agent beads (labeled gt:agent, owner: mayor) live in the town/hq database.
// CWD-based BEADS_DIR resolution routes to the rig database, where the agent
// bead does not exist, so heartbeat/idle/backoff persistence silently fails.
//
// Regression test for gt-5n3.
func TestAgentStateRoutesToTownDB(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	townRoot, expectedWD := makeRoutingTownWorkspace(t)

	// Rig agent directory with its OWN .beads (the wrong DB). A witness/refinery
	// patrol runs from here; CWD resolution would route agent-bead ops here.
	rigAgentDir := filepath.Join(townRoot, "gastown", "witness")
	if err := os.MkdirAll(filepath.Join(rigAgentDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	chdirConvoyTest(t, rigAgentDir)

	// Simulate an inherited rig BEADS_DIR (what a patrol shell would carry).
	// The fix must override this and target the town DB.
	t.Setenv("BEADS_DIR", filepath.Join(expectedWD, "gastown", "witness", ".beads"))

	scriptBody := fmt.Sprintf(`
# version probe (if any) is exempt
if [ "$*" = "--allow-stale version" ]; then exit 0; fi

if [ "$BEADS_DIR" != "%s/.beads" ]; then
  echo "expected town BEADS_DIR %s/.beads, got $BEADS_DIR" >&2
  exit 1
fi

case "$*" in
  "show gt-gastown-witness --json")
    echo '[{"id":"gt-gastown-witness","labels":["gt:agent","idle:0"]}]'
    ;;
  *)
    echo "unexpected bd args: $*" >&2
    exit 1
    ;;
esac
`, expectedWD, expectedWD)
	writeRoutingBdStub(t, scriptBody)

	// Reset query-mode flags.
	oldSet, oldIncr, oldDel, oldJSON := agentStateSet, agentStateIncr, agentStateDel, agentStateJSON
	agentStateSet, agentStateIncr, agentStateDel, agentStateJSON = nil, "", nil, false
	t.Cleanup(func() {
		agentStateSet, agentStateIncr, agentStateDel, agentStateJSON = oldSet, oldIncr, oldDel, oldJSON
	})

	if _, err := captureConvoyStdoutErr(t, func() error {
		return runAgentState(nil, []string{"gt-gastown-witness"})
	}); err != nil {
		t.Fatalf("runAgentState routed to wrong DB or failed: %v", err)
	}
}
