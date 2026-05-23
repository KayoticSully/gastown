#!/usr/bin/env bash
# dolt-backup/run.sh — Deterministic Dolt database backup.
#
# Syncs production databases to filesystem backups via `dolt backup sync`.
# Skips databases that haven't changed since last backup (hash check).
# Only escalates when actual backup operations fail — not on ping failures.
#
# Usage: ./run.sh [--databases db1,db2,...] [--dry-run]

set -euo pipefail

# --- Configuration -----------------------------------------------------------

DOLT_DATA_DIR="${DOLT_DATA_DIR:-$HOME/gt/.dolt-data}"
BACKUP_DIR="${DOLT_BACKUP_DIR:-$HOME/gt/.dolt-backup}"
BACKUP_TIMEOUT=60

# --- Argument parsing ---------------------------------------------------------

DRY_RUN=false
EXPLICIT_DBS=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --databases) EXPLICIT_DBS="$2"; shift 2 ;;
    --dry-run)   DRY_RUN=true; shift ;;
    --help|-h)
      echo "Usage: $0 [--databases db1,db2,...] [--dry-run]"
      exit 0
      ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# --- Helpers ------------------------------------------------------------------

log() {
  echo "[dolt-backup] $*"
}

# Resolve a database's HEAD commit hash from the LIVE sql-server.
#
# Why not `dolt log`: the CLI reads the on-disk data dir directly, which lags
# the running sql-server's HEAD. After the compactor rewrites history, the CLI
# HEAD can sit unchanged for ~1h while the server keeps committing — so the old
# change-detection saw CURRENT==LAST and silently SKIPPED an actively-changing
# DB while still reporting success (hq-q7zs). `dolt sql` auto-connects to the
# running server on the same data dir, so it reflects live HEAD.
get_server_head() {
  local db_dir="$1"
  ( cd "$db_dir" && dolt sql -q "SELECT hashof('HEAD')" --result-format csv 2>/dev/null ) \
    | tail -n +2 | head -1 | tr -d '"[:space:]'
}

# --- Step 1: Discover databases -----------------------------------------------

# Use explicit list if provided, otherwise auto-discover by scanning
# DOLT_DATA_DIR for directories that contain a .dolt subdirectory,
# excluding system and test databases.
if [[ -n "$EXPLICIT_DBS" ]]; then
  IFS=',' read -ra PROD_DBS <<< "$EXPLICIT_DBS"
else
  PROD_DBS=()
  while IFS= read -r line; do
    PROD_DBS+=("$line")
  done < <(
    for d in "$DOLT_DATA_DIR"/*/; do
      name="$(basename "$d")"
      [[ -d "$d/.dolt" ]] || continue
      [[ "$name" =~ ^(testdb_|beads_t|beads_pt|doctest_) ]] && continue
      echo "$name"
    done | sort
  )
  if [[ ${#PROD_DBS[@]} -eq 0 ]]; then
    log "ERROR: No databases found in $DOLT_DATA_DIR"
    exit 1
  fi
fi

log "Databases to backup (${#PROD_DBS[@]}): ${PROD_DBS[*]}"

# --- Step 2: Backup each database ---------------------------------------------

SYNCED=0
SKIPPED=0
FAILED=0
FAILED_DBS=""

for DB in "${PROD_DBS[@]}"; do
  DB_DIR="$DOLT_DATA_DIR/$DB"
  BACKUP_NAME="${DB}-backup"
  HASH_FILE="$BACKUP_DIR/${DB}/.last-backup-hash"

  # Check DB dir exists
  if [[ ! -d "$DB_DIR/.dolt" ]]; then
    log "  $DB: no .dolt directory, skipping"
    FAILED=$((FAILED + 1))
    FAILED_DBS="$FAILED_DBS $DB(no-dir)"
    continue
  fi

  # Get current HEAD hash from the live sql-server (NOT the lagging CLI files).
  CURRENT_HASH=$(get_server_head "$DB_DIR")
  if [[ -z "$CURRENT_HASH" || "$CURRENT_HASH" == "NULL" ]]; then
    log "  $DB: could not read server HEAD, will sync anyway"
    CURRENT_HASH="unknown"
  fi

  # Check last backed-up hash
  LAST_HASH=""
  if [[ -f "$HASH_FILE" ]]; then
    LAST_HASH=$(cat "$HASH_FILE")
  fi

  if [[ "$CURRENT_HASH" = "$LAST_HASH" ]] && [[ "$CURRENT_HASH" != "unknown" ]]; then
    log "  $DB: unchanged ($CURRENT_HASH), skipping"
    SKIPPED=$((SKIPPED + 1))
    continue
  fi

  if $DRY_RUN; then
    log "  $DB: DRY RUN would sync ($LAST_HASH -> $CURRENT_HASH)"
    SYNCED=$((SYNCED + 1))
    continue
  fi

  # Sync backup with timeout
  log "  $DB: syncing ($LAST_HASH -> $CURRENT_HASH)..."
  SYNC_START=$(date +%s)

  SYNC_OUTPUT=$(cd "$DB_DIR" && timeout "$BACKUP_TIMEOUT" dolt backup sync "$BACKUP_NAME" 2>&1) || true
  SYNC_RC=${PIPESTATUS[0]:-$?}
  SYNC_ELAPSED=$(( $(date +%s) - SYNC_START ))

  if [[ $SYNC_RC -eq 0 ]]; then
    # Record the hash we just backed up
    mkdir -p "$(dirname "$HASH_FILE")"
    echo "$CURRENT_HASH" > "$HASH_FILE"

    DB_SIZE=$(du -sh "$BACKUP_DIR/$DB" 2>/dev/null | cut -f1 || echo "?")
    SYNCED=$((SYNCED + 1))
    log "  $DB: synced in ${SYNC_ELAPSED}s ($DB_SIZE)"
  elif [[ $SYNC_RC -eq 124 ]]; then
    FAILED=$((FAILED + 1))
    FAILED_DBS="$FAILED_DBS $DB(timeout)"
    log "  $DB: TIMEOUT after ${BACKUP_TIMEOUT}s"
  else
    FAILED=$((FAILED + 1))
    FAILED_DBS="$FAILED_DBS $DB(exit-$SYNC_RC)"
    log "  $DB: FAILED (exit $SYNC_RC): $SYNC_OUTPUT"
  fi
done

# --- Step 3: Report results ---------------------------------------------------

SUMMARY="Backup: $SYNCED synced, $SKIPPED unchanged, $FAILED failed (of ${#PROD_DBS[@]} DBs)"
log "$SUMMARY"

# --- Step 4: Record result and escalate if needed -----------------------------

if [[ "$FAILED" -eq 0 ]]; then
  # Success — record quietly
  bd create --title "dolt-backup: $SUMMARY" -t chore --ephemeral \
    -l type:plugin-run,plugin:dolt-backup,result:success \
    -d "$SUMMARY" --silent 2>/dev/null || true
else
  # Failure — record and escalate
  FAIL_MSG="$SUMMARY. Failed:$FAILED_DBS"
  bd create --title "dolt-backup: FAILED - $FAIL_MSG" -t chore --ephemeral \
    -l type:plugin-run,plugin:dolt-backup,result:failure \
    -d "$FAIL_MSG" --silent 2>/dev/null || true

  gt escalate "dolt-backup FAILED: $FAIL_MSG" \
    --severity high \
    --reason "$FAIL_MSG" 2>/dev/null || true

  exit 1
fi
