+++
name = "dolt-backup"
description = "Smart Dolt database backup with change detection"
version = 3

[gate]
type = "cooldown"
duration = "15m"

[tracking]
labels = ["plugin:dolt-backup", "category:data-safety"]
digest = true

[execution]
timeout = "5m"
notify_on_failure = true
severity = "high"
+++

# Dolt Backup

Syncs production Dolt databases to filesystem backups via `dolt backup sync`.
Executed via `run.sh` — no AI interpretation.

## What it does

1. For each production DB (hq, beads, gt): compare HEAD hash against last backup
2. Skip unchanged databases
3. Run `dolt backup sync` for changed databases
4. Only escalate when actual backup operations fail (FAILED > 0)

HEAD is read from the **live sql-server** (`dolt sql`), not the on-disk CLI
(`dolt log`). The CLI view lags the server after the compactor rewrites history,
which previously caused an actively-changing DB to be silently skipped as
"unchanged" while still reporting success (hq-q7zs). If the server HEAD can't be
read, the DB is synced anyway rather than skipped.

## Usage

```bash
./run.sh                          # Normal execution
./run.sh --dry-run                # Report without syncing
./run.sh --databases hq,beads    # Specific databases only
```
