package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeBdScript is a minimal stand-in for the real `bd` binary. It models the
// only behaviors pourDogMolecule + dogMol.close() exercise:
//
//   - `mol wisp <formula> [--root-only] [--var k=v]...`
//     Creates a root wisp. WITHOUT --root-only it ALSO materializes child step
//     wisps (mirroring real bd, which materializes a child issue per formula
//     step). WITH --root-only it creates only the root.
//   - `show <root> --children --json`  → {"<root>":[{id,title,status},...]}
//   - `close <id> [--reason ...]`      → marks the bead closed.
//
// State lives under $FAKE_BD_STATE/beads/<id> as "status|title|parent" so the
// test can inspect exactly which wisps were created and their statuses.
const fakeBdScript = `#!/usr/bin/env bash
set -u
DB="${FAKE_BD_STATE}/beads"
mkdir -p "$DB"
sub="${1:-}"; shift || true
case "$sub" in
  mol)
    shift || true            # consume "wisp"
    formula="${1:-}"; shift || true
    rootonly=0
    for a in "$@"; do [ "$a" = "--root-only" ] && rootonly=1; done
    root="tw-wisp-root"
    printf 'open|%s|\n' "$formula" > "$DB/$root"
    if [ "$rootonly" -eq 0 ]; then
      idx=0
      for title in "Probe Dolt server connectivity" "Inspect resource conditions" "Report findings and return to kennel"; do
        printf 'open|%s|%s\n' "$title" "$root" > "$DB/tw-wisp-step$idx"
        idx=$((idx+1))
      done
    fi
    printf '\xe2\x9c\x93 Spawned wisp: %s \xe2\x80\x94 %s\n' "$root" "$formula"
    ;;
  show)
    id="${1:-}"; shift || true
    printf '{"%s":[' "$id"
    first=1
    if ls "$DB"/* >/dev/null 2>&1; then
      for f in "$DB"/*; do
        bid="$(basename "$f")"
        line="$(cat "$f")"
        st="${line%%|*}"; rest="${line#*|}"
        title="${rest%%|*}"; parent="${rest#*|}"
        if [ "$parent" = "$id" ]; then
          [ "$first" -eq 0 ] && printf ','
          printf '{"id":"%s","title":"%s","status":"%s"}' "$bid" "$title" "$st"
          first=0
        fi
      done
    fi
    printf ']}\n'
    ;;
  close)
    id="${1:-}"; shift || true
    f="$DB/$id"
    if [ -e "$f" ]; then
      line="$(cat "$f")"; rest="${line#*|}"
      title="${rest%%|*}"; parent="${rest#*|}"
      printf 'closed|%s|%s\n' "$title" "$parent" > "$f"
    fi
    printf 'closed %s\n' "$id"
    ;;
  *)
    printf 'fake bd: unknown subcommand: %s\n' "$sub" >&2
    exit 1
    ;;
esac
`

// writeFakeBd writes the fake bd script into dir and returns its path.
func writeFakeBd(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "bd")
	if err := os.WriteFile(p, []byte(fakeBdScript), 0o755); err != nil {
		t.Fatalf("writing fake bd: %v", err)
	}
	return p
}

// readBead returns (status, title, parent) for a wisp in the fake DB, or
// ("", "", "") if it does not exist.
func readBead(t *testing.T, stateDir, id string) (status, title, parent string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDir, "beads", id))
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", ""
		}
		t.Fatalf("reading bead %s: %v", id, err)
	}
	parts := strings.SplitN(strings.TrimRight(string(raw), "\n"), "|", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2]
}

// childWisps returns the IDs of all wisps whose parent is rootID, optionally
// filtered to only open ones.
func childWisps(t *testing.T, stateDir, rootID string, onlyOpen bool) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateDir, "beads"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading state dir: %v", err)
	}
	var ids []string
	for _, e := range entries {
		status, _, parent := readBead(t, stateDir, e.Name())
		if parent != rootID {
			continue
		}
		if onlyOpen && status != "open" {
			continue
		}
		ids = append(ids, e.Name())
	}
	return ids
}

// TestPourDogMoleculeIsRootOnly verifies the core contract that prevents the
// dog/patrol orphan-wisp accumulation (gt-ldw): pourDogMolecule must create the
// molecule root-only, so child step wisps are NEVER materialized. A completed
// dog molecule must therefore leave zero open child step-wisps.
//
// Before the fix, pourDogMolecule ran `bd mol wisp <formula>` without
// --root-only, so bd materialized one child step-wisp per formula step. When
// the molecule root was closed, those children were orphaned as status=open,
// accumulating in the hq DB. This test fails (3 children materialized) until
// pourDogMolecule passes --root-only.
func TestPourDogMoleculeIsRootOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd is a POSIX shell script")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(filepath.Join(stateDir, "beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	bdPath := writeFakeBd(t, tmp)
	t.Setenv("FAKE_BD_STATE", stateDir)

	d := &Daemon{
		config: &Config{TownRoot: tmp},
		logger: log.New(io.Discard, "", 0),
		bdPath: bdPath,
	}

	mol := d.pourDogMolecule("mol-dog-doctor", map[string]string{"port": "3307"})
	if mol.rootID == "" {
		t.Fatalf("pourDogMolecule returned empty rootID")
	}

	// Contract: root-only means no child step wisps are ever created.
	if got := childWisps(t, stateDir, mol.rootID, false); len(got) != 0 {
		t.Fatalf("pourDogMolecule materialized %d child step-wisp(s) %v; want 0 (must use --root-only)", len(got), got)
	}

	// Run the molecule to completion.
	mol.close()

	// The bead's explicit requirement: a completed dog molecule leaves
	// 0 OPEN child step-wisps.
	if open := childWisps(t, stateDir, mol.rootID, true); len(open) != 0 {
		t.Fatalf("after completion, %d open child step-wisp(s) remain %v; want 0", len(open), open)
	}

	// The root itself is closed on completion.
	if status, _, _ := readBead(t, stateDir, mol.rootID); status != "closed" {
		t.Fatalf("root wisp status = %q after close; want %q", status, "closed")
	}
}
