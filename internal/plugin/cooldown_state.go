package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Durable plugin cooldown state.
//
// Plugin cooldown gates were originally evaluated by querying the ephemeral
// plugin-run receipt beads (wisps) created by RecordRun. Those receipts are
// created closed, so the patrol formulas' `bd mol wisp gc --closed --force`
// step deleted them every cycle — wiping the run history and resetting every
// cooldown gate to "never ran". Plugins with cooldowns (dolt-backup 15m,
// dolt-log-rotate 6h, ...) then re-fired on consecutive patrol cycles. See
// hq-1o1.
//
// The fix stores the cooldown-relevant timestamp outside GC-eligible wisps, in
// a plain runtime file under <townRoot>/.runtime/. This survives `bd mol wisp
// gc`, generates no Dolt commits, and keeps one file per plugin so it does not
// accumulate. The ephemeral receipt bead is still recorded for audit/history.

// cooldownStateDir returns the directory holding durable plugin cooldown
// timestamps. It lives under <townRoot>/.runtime/ alongside other local
// runtime state (heartbeats, pids), which is never touched by wisp GC.
func cooldownStateDir(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "plugin-cooldowns")
}

// cooldownStateFile returns the path to a single plugin's durable last-run file.
func cooldownStateFile(townRoot, pluginName string) string {
	return filepath.Join(cooldownStateDir(townRoot), sanitizePluginName(pluginName)+".txt")
}

// sanitizePluginName makes a plugin name safe to use as a filename so a plugin
// named with path separators cannot escape the cooldown-state directory.
func sanitizePluginName(name string) string {
	repl := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "..", "_")
	cleaned := repl.Replace(name)
	if cleaned == "" {
		return "_"
	}
	return cleaned
}

// recordCooldownState writes the durable last-run timestamp for a plugin.
// The write is atomic (temp file + rename) so a concurrent reader never sees a
// truncated timestamp.
func recordCooldownState(townRoot, pluginName string, when time.Time) error {
	dir := cooldownStateDir(townRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := cooldownStateFile(townRoot, pluginName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(when.UTC().Format(time.RFC3339)), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// lastCooldownState reads the durable last-run timestamp for a plugin. It
// returns ok=false when no durable record exists or it cannot be parsed, so
// callers fall back to the bead-based query.
func lastCooldownState(townRoot, pluginName string) (time.Time, bool) {
	data, err := os.ReadFile(cooldownStateFile(townRoot, pluginName))
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
