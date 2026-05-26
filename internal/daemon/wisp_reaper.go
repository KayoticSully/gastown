package daemon

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/reaper"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	// defaultWispReaperInterval is the patrol interval. Set to 1h since reaping
	// is cleanup work, not latency-sensitive. Was 30m before Dog-driven refactor.
	defaultWispReaperInterval = 1 * time.Hour
	// Wisps older than this are reaped (closed). Configurable via formula var max_age.
	defaultWispMaxAge = 24 * time.Hour
	// Closed wisps older than this are permanently deleted. Formula var: purge_age.
	defaultWispDeleteAge = 7 * 24 * time.Hour
	// Alert threshold: if open wisp count exceeds this, the Dog should escalate.
	// Shared with `gt reaper run` warning. See reaper.DefaultAlertThreshold.
	wispAlertThreshold = reaper.DefaultAlertThreshold
	// Closed mail older than this is permanently deleted. Formula var: mail_delete_age.
	defaultMailDeleteAge = 7 * 24 * time.Hour
	// Issues stale longer than this are auto-closed. Formula var: stale_issue_age.
	defaultStaleIssueAge = 7 * 24 * time.Hour
	// Plugin run receipts and dispatch mails are closed on a fast track (1h)
	// rather than waiting for the 7-day AutoClose. They are transient
	// daemon/dog bookkeeping beads that exist only for cooldown-gate/audit
	// queries (which include closed beads); leaving them open accumulates hq
	// bloat. See gt-b2s.
	pluginBeadFastTrackAge = 1 * time.Hour
)

// WispReaperConfig holds configuration for the wisp_reaper patrol.
type WispReaperConfig struct {
	Enabled      bool     `json:"enabled"`
	DryRun       bool     `json:"dry_run,omitempty"`
	IntervalStr  string   `json:"interval,omitempty"`
	MaxAgeStr    string   `json:"max_age,omitempty"`
	DeleteAgeStr string   `json:"delete_age,omitempty"`
	Databases    []string `json:"databases,omitempty"`
}

// wispReaperInterval returns the configured interval, or the default (1h).
func wispReaperInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.WispReaper != nil {
		if config.Patrols.WispReaper.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.WispReaper.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultWispReaperInterval
}

// wispReaperMaxAge returns the configured max age, or the default (24h).
func wispReaperMaxAge(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.WispReaper != nil {
		if config.Patrols.WispReaper.MaxAgeStr != "" {
			if d, err := time.ParseDuration(config.Patrols.WispReaper.MaxAgeStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultWispMaxAge
}

// wispDeleteAge returns the configured delete age, or the default (7 days).
func wispDeleteAge(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.WispReaper != nil {
		if config.Patrols.WispReaper.DeleteAgeStr != "" {
			if d, err := time.ParseDuration(config.Patrols.WispReaper.DeleteAgeStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultWispDeleteAge
}

// reapWisps is the thin orchestrator for the wisp_reaper patrol.
// It pours a mol-dog-reaper molecule, then dispatches a Dog to execute it.
// The Dog reads the formula steps and calls `gt reaper` CLI helpers.
// Falls back to inline execution if Dog dispatch fails.
func (d *Daemon) reapWisps() {
	if !d.isPatrolActive("wisp_reaper") {
		return
	}

	config := d.patrolConfig.Patrols.WispReaper
	maxAge := wispReaperMaxAge(d.patrolConfig)
	deleteAge := wispDeleteAge(d.patrolConfig)

	vars := map[string]string{
		"max_age":         maxAge.String(),
		"purge_age":       deleteAge.String(),
		"stale_issue_age": defaultStaleIssueAge.String(),
		"mail_delete_age": defaultMailDeleteAge.String(),
		"alert_threshold": fmt.Sprintf("%d", wispAlertThreshold),
		"dolt_port":       fmt.Sprintf("%d", d.doltServerPort()),
	}

	if config.DryRun {
		vars["dry_run"] = "true"
	}
	if len(config.Databases) > 0 {
		vars["databases"] = strings.Join(config.Databases, ",")
	}

	// Pour the molecule for observability tracking.
	mol := d.pourDogMolecule(constants.MolDogReaper, vars)
	defer mol.close()

	if config.DryRun {
		d.logger.Printf("wisp_reaper: DRY RUN — reporting only, no changes will be made")
	}

	// Resolve the database list once — needed both by the inline fallback and
	// by the unconditional plugin-bead close below.
	databases := config.Databases
	if len(databases) == 0 {
		databases = reaper.DiscoverDatabases("127.0.0.1", d.doltServerPort())
	}

	// Heavy wisp reaping: prefer Dog dispatch (formula-driven), fall back to
	// inline execution if the Dog can't be dispatched.
	if err := d.dispatchReaperDog(vars); err != nil {
		d.logger.Printf("wisp_reaper: Dog dispatch failed (%v), running inline fallback", err)
		d.reapWispsInline(config, maxAge, deleteAge, databases, mol)
	} else {
		d.logger.Printf("wisp_reaper: dispatched to Dog for formula-driven execution")
	}

	// Always fast-track close plugin run receipts + dispatch mails (gt-b2s).
	// The Dog-driven mol-dog-reaper formula never closes these, and reaper.Reap
	// can't (they live in the issues table, not wisps), so the daemon must own
	// this every cycle — independent of whether reaping ran via Dog or inline.
	d.closePluginBeads(databases, config.DryRun)
}

// dispatchReaperDog dispatches the mol-dog-reaper formula to a Dog via gt sling.
func (d *Daemon) dispatchReaperDog(vars map[string]string) error {
	args := []string{"sling", constants.MolDogReaper, "deacon/dogs"}
	for k, v := range vars {
		args = append(args, "--var", fmt.Sprintf("%s=%s", k, v))
	}

	cmd := exec.Command(d.gtPath, args...) //nolint:gosec // G204: d.gtPath resolved at daemon init via LookPath
	cmd.Dir = d.config.TownRoot
	// Inherit os.Environ() (cmd.Env left nil) — gt sling performs WRITES
	// (creates wisps, dispatches dogs) so it must NOT carry
	// BD_DOLT_AUTO_COMMIT=off from bdReadOnlyEnv(). PATH augmentation at
	// daemon startup (PATCH-007) ensures the inherited env still finds
	// gt/bd via os.Environ()'s PATH.
	util.SetDetachedProcessGroup(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gt sling: %w", err)
	}
	return nil
}

// reapWispsInline is the fallback that runs the reaper cycle inline when
// Dog dispatch is unavailable. Delegates to the reaper package for SQL execution.
// Plugin run receipts + dispatch mails are NOT closed here — reapWisps closes
// them unconditionally after the dispatch decision (gt-b2s).
func (d *Daemon) reapWispsInline(config *WispReaperConfig, maxAge, deleteAge time.Duration, databases []string, mol *dogMol) {
	if len(databases) == 0 {
		d.logger.Printf("wisp_reaper: no databases to reap")
		mol.failStep("scan", "no databases found")
		return
	}
	d.logger.Printf("wisp_reaper: scanning %d databases (inline fallback)", len(databases))
	mol.closeStep("scan")

	port := d.doltServerPort()
	dryRun := config.DryRun
	var totalReaped, totalOpen, totalPurged, totalMailPurged, totalAutoClosed int

	// Step 2: Reap
	reapErrors := 0
	for _, dbName := range databases {
		if err := reaper.ValidateDBName(dbName); err != nil {
			continue
		}
		db, err := reaper.OpenDB("127.0.0.1", port, dbName, 10*time.Second, 10*time.Second)
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: connect error: %v", dbName, err)
			reapErrors++
			continue
		}
		if ok, _ := reaper.HasReaperSchema(db); !ok {
			d.logger.Printf("wisp_reaper: %s: skipped (no reaper schema)", dbName)
			db.Close()
			continue
		}
		result, err := reaper.Reap(db, dbName, maxAge, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: reap error: %v", dbName, err)
			reapErrors++
			continue
		}
		totalReaped += result.Reaped
		totalOpen += result.OpenRemain
		if result.Reaped > 0 {
			d.logger.Printf("wisp_reaper: %s: reaped %d stale wisps, %d open remain", dbName, result.Reaped, result.OpenRemain)
		}
	}
	if reapErrors > 0 {
		mol.failStep("reap", fmt.Sprintf("%d databases had reap errors", reapErrors))
	} else {
		mol.closeStep("reap")
	}

	// Step 3: Purge
	purgeErrors := 0
	for _, dbName := range databases {
		if err := reaper.ValidateDBName(dbName); err != nil {
			continue
		}
		db, err := reaper.OpenDB("127.0.0.1", port, dbName, 30*time.Second, 30*time.Second)
		if err != nil {
			purgeErrors++
			continue
		}
		if ok, _ := reaper.HasReaperSchema(db); !ok {
			db.Close()
			continue
		}
		result, err := reaper.Purge(db, dbName, deleteAge, defaultMailDeleteAge, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: purge error: %v", dbName, err)
			purgeErrors++
			continue
		}
		totalPurged += result.WispsPurged
		totalMailPurged += result.MailPurged
		for _, a := range result.Anomalies {
			d.logger.Printf("wisp_reaper: %s: ANOMALY: %s", dbName, a.Message)
		}
	}
	if purgeErrors > 0 {
		mol.failStep("purge", fmt.Sprintf("%d databases had purge errors", purgeErrors))
	} else {
		mol.closeStep("purge")
	}

	// Plugin run receipts + dispatch mails are closed by reapWisps (gt-b2s),
	// not here, so they get fast-track-closed on both the Dog and inline paths.

	// Step 4: Auto-close
	autoCloseErrors := 0
	for _, dbName := range databases {
		if err := reaper.ValidateDBName(dbName); err != nil {
			continue
		}
		db, err := reaper.OpenDB("127.0.0.1", port, dbName, 10*time.Second, 10*time.Second)
		if err != nil {
			autoCloseErrors++
			continue
		}
		// Auto-close operates on the issues table, not wisps, but if the database
		// has no beads schema at all we should skip it too.
		if ok, _ := reaper.HasReaperSchema(db); !ok {
			db.Close()
			continue
		}
		result, err := reaper.AutoClose(db, dbName, defaultStaleIssueAge, dryRun)
		db.Close()
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: auto-close error: %v", dbName, err)
			autoCloseErrors++
			continue
		}
		totalAutoClosed += result.Closed
	}
	if autoCloseErrors > 0 {
		mol.failStep("auto-close", fmt.Sprintf("%d databases had auto-close errors", autoCloseErrors))
	} else {
		mol.closeStep("auto-close")
	}

	// Step 5: Report
	if totalOpen > wispAlertThreshold {
		d.logger.Printf("wisp_reaper: WARNING: %d open wisps exceed threshold %d — investigate wisp lifecycle",
			totalOpen, wispAlertThreshold)
	}
	d.logger.Printf("wisp_reaper: inline cycle complete — reaped=%d purged=%d mail_purged=%d auto_closed=%d open=%d databases=%d dryRun=%v",
		totalReaped, totalPurged, totalMailPurged, totalAutoClosed, totalOpen, len(databases), dryRun)
	mol.closeStep("report")
}

// closePluginBeads fast-track closes plugin run receipts (type:plugin-run, e.g.
// dog RESULT chore wisps like "compactor-dog: ...") and plugin dispatch mails
// (from:daemon, title "Plugin:...") across the given databases.
//
// This MUST run every reaper cycle regardless of whether the heavy wisp reaping
// was delegated to a Dog or run inline: the Dog-driven mol-dog-reaper formula
// (scan/reap/purge/auto-close) never closes these beads, and reaper.Reap can't
// (it operates on the wisps table while these live in the issues table). Before
// gt-b2s the closers ran only in reapWispsInline (the dog-dispatch-failure
// fallback), so in normal operation they never ran and the beads accumulated
// until the 7-day AutoClose — the recurring source of hq bloat. Dog sessions
// die unreliably, so the daemon owns this close itself.
func (d *Daemon) closePluginBeads(databases []string, dryRun bool) (receiptsClosed, dispatchesClosed int) {
	closeFn := d.closePluginBeadsInDB
	if closeFn == nil {
		closeFn = d.defaultClosePluginBeadsInDB
	}
	port := d.doltServerPort()
	for _, dbName := range databases {
		receipts, dispatches, err := closeFn(port, dbName, pluginBeadFastTrackAge, dryRun)
		if err != nil {
			d.logger.Printf("wisp_reaper: %s: plugin bead close error: %v", dbName, err)
			continue
		}
		receiptsClosed += receipts
		dispatchesClosed += dispatches
		if receipts > 0 || dispatches > 0 {
			d.logger.Printf("wisp_reaper: %s: closed %d plugin receipts, %d plugin dispatches", dbName, receipts, dispatches)
		}
	}
	return receiptsClosed, dispatchesClosed
}

// defaultClosePluginBeadsInDB is the live implementation backing closePluginBeads:
// it opens dbName and delegates to reaper.ClosePluginReceipts + ClosePluginDispatches.
func (d *Daemon) defaultClosePluginBeadsInDB(port int, dbName string, age time.Duration, dryRun bool) (int, int, error) {
	if err := reaper.ValidateDBName(dbName); err != nil {
		return 0, 0, err
	}
	db, err := reaper.OpenDB("127.0.0.1", port, dbName, 10*time.Second, 10*time.Second)
	if err != nil {
		return 0, 0, err
	}
	defer db.Close()
	if ok, _ := reaper.HasReaperSchema(db); !ok {
		return 0, 0, nil
	}
	receipts, err := reaper.ClosePluginReceipts(db, dbName, age, dryRun)
	if err != nil {
		return 0, 0, err
	}
	dispatches, err := reaper.ClosePluginDispatches(db, dbName, age, dryRun)
	if err != nil {
		return receipts.Closed, 0, err
	}
	return receipts.Closed, dispatches.Closed, nil
}

// doltServerPort returns the configured Dolt server port.
func (d *Daemon) doltServerPort() int {
	if d.doltServer != nil {
		return d.doltServer.config.Port
	}
	return 3307
}
