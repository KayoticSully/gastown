#!/usr/bin/env bash
# stuck-agent-dog/run.sh — Context-aware stuck/crashed agent detection.
#
# SCOPE: Only polecats and deacon. NEVER touches crew, mayor, witness, or refinery.
# The daemon detects; this plugin inspects context before acting.

set -euo pipefail

TOWN_ROOT="${GT_TOWN_ROOT:-$(gt town root 2>/dev/null)}"
RIGS_JSON_PATH="${TOWN_ROOT}/mayor/rigs.json"

log() { echo "[stuck-agent-dog] $*"; }

heartbeat_epoch() {
  local file="$1"
  local ts=""

  ts=$(jq -r '(.timestamp // empty) | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601? // empty' "$file" 2>/dev/null || true)
  if [ -n "$ts" ]; then
    echo "$ts"
    return 0
  fi

  # Fallback for malformed legacy files: use mtime rather than failing open.
  stat -f %m "$file" 2>/dev/null || stat -c %Y "$file" 2>/dev/null
}

has_in_progress_work() {
  local locations=("$TOWN_ROOT")
  local rig=""
  local prefix=""
  local loc=""
  local output=""
  local count=""

  while IFS='|' read -r rig prefix; do
    [ -z "$rig" ] && continue
    [ -d "$TOWN_ROOT/$rig" ] && locations+=("$TOWN_ROOT/$rig")
  done <<< "$RIG_PREFIX_MAP"

  for loc in "${locations[@]}"; do
    output=$(cd "$loc" && bd list --status=in_progress --json --limit=1 2>/dev/null) || return 0
    count=$(printf '%s' "$output" | jq 'length' 2>/dev/null || echo 1)
    if [ "${count:-1}" -gt 0 ]; then
      return 0
    fi
  done

  return 1
}

# --- Enumerate agents ---------------------------------------------------------

log "=== Checking agent health ==="

if [ ! -f "$RIGS_JSON_PATH" ]; then
  log "SKIP: rigs.json not found"
  exit 0
fi

# Build rig_name|prefix mapping
RIG_PREFIX_MAP=$(jq -r '.rigs | to_entries[] | "\(.key)|\(.value.beads.prefix // .key)"' "$RIGS_JSON_PATH" 2>/dev/null)
if [ -z "$RIG_PREFIX_MAP" ]; then
  log "SKIP: no rigs in rigs.json"
  exit 0
fi

# --- Check polecat health ----------------------------------------------------

CRASHED=()
STUCK=()
HEALTHY=0

while IFS='|' read -r RIG PREFIX; do
  [ -z "$RIG" ] && continue
  POLECAT_DIR="$TOWN_ROOT/$RIG/polecats"
  [ -d "$POLECAT_DIR" ] || continue

  for PCAT_PATH in "$POLECAT_DIR"/*/; do
    [ -d "$PCAT_PATH" ] || continue
    PCAT_NAME=$(basename "$PCAT_PATH")
    SESSION_NAME="${PREFIX}-${PCAT_NAME}"

    if ! tmux has-session -t "$SESSION_NAME" 2>/dev/null; then
      # Session dead — check hook
      HOOK_OUTPUT=$(gt hook show "$RIG/polecats/$PCAT_NAME" 2>/dev/null | head -1)
      HOOK_BEAD=$(echo "$HOOK_OUTPUT" | grep -v '(empty)' | awk '{print $2}' || true)

      if [ -n "$HOOK_BEAD" ]; then
        # Check agent_state
        AGENT_STATE=$(bd show "$HOOK_BEAD" --json 2>/dev/null \
          | python3 -c "import json,sys; d=json.load(sys.stdin); print(d[0].get('status',''))" 2>/dev/null || echo "")

        case "$AGENT_STATE" in
          closed) log "  SKIP $SESSION_NAME: bead closed (completed normally)"; continue ;;
        esac

        CRASHED+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD")
        log "  CRASHED: $SESSION_NAME (hook=$HOOK_BEAD)"
      fi
    else
      # Session alive — check process
      PANE_PID=$(tmux list-panes -t "$SESSION_NAME" -F '#{pane_pid}' 2>/dev/null | head -1)
      if [ -n "$PANE_PID" ]; then
        PROC_COMM=$(ps -o comm= -p "$PANE_PID" 2>/dev/null)
        if [ -z "$PROC_COMM" ]; then
          # Zombie: process dead, session alive
          HOOK_OUTPUT=$(gt hook show "$RIG/polecats/$PCAT_NAME" 2>/dev/null | head -1)
          HOOK_BEAD=$(echo "$HOOK_OUTPUT" | grep -v '(empty)' | awk '{print $2}' || true)
          if [ -n "$HOOK_BEAD" ]; then
            STUCK+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD|agent_dead")
            log "  ZOMBIE: $SESSION_NAME (pid=$PANE_PID dead, hook=$HOOK_BEAD)"
          fi
        else
          HEALTHY=$((HEALTHY + 1))
        fi
      else
        HEALTHY=$((HEALTHY + 1))
      fi
    fi
  done
done <<< "$RIG_PREFIX_MAP"

log ""
log "Polecat health: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, $HEALTHY healthy"

# --- Check deacon health -----------------------------------------------------

log ""
log "=== Deacon Health ==="

DEACON_SESSION="hq-deacon"
DEACON_ISSUE=""
DEACON_PROCESS_ALIVE=0

if ! tmux has-session -t "$DEACON_SESSION" 2>/dev/null; then
  log "  CRASHED: Deacon session is dead"
  DEACON_ISSUE="crashed"
else
  DEACON_PID=$(tmux list-panes -t "$DEACON_SESSION" -F '#{pane_pid}' 2>/dev/null | head -1)
  DEACON_COMM=$(ps -o comm= -p "$DEACON_PID" 2>/dev/null)
  if [ -z "$DEACON_COMM" ]; then
    log "  ZOMBIE: Deacon process dead (pid=$DEACON_PID), session alive"
    DEACON_ISSUE="zombie"
  else
    log "  Process alive: pid=$DEACON_PID comm=$DEACON_COMM"
    DEACON_PROCESS_ALIVE=1
  fi

  HEARTBEAT_FILE="$TOWN_ROOT/deacon/heartbeat.json"
  if [ -z "$DEACON_ISSUE" ] && [ -f "$HEARTBEAT_FILE" ]; then
    HEARTBEAT_TIME=$(heartbeat_epoch "$HEARTBEAT_FILE" || true)
    NOW=$(date +%s)
    HEARTBEAT_AGE=$(( NOW - ${HEARTBEAT_TIME:-0} ))

    if [ "$HEARTBEAT_AGE" -gt 1200 ]; then
      if [ "$DEACON_PROCESS_ALIVE" -eq 1 ] && ! has_in_progress_work; then
        log "  SKIP: Deacon heartbeat stale (${HEARTBEAT_AGE}s old) but process is alive and no in_progress work exists"
      else
        log "  STUCK: Deacon heartbeat stale (${HEARTBEAT_AGE}s old, >20m threshold)"
        DEACON_ISSUE="stuck_heartbeat_${HEARTBEAT_AGE}s"
      fi
    else
      log "  OK: Deacon heartbeat ${HEARTBEAT_AGE}s old"
    fi
  fi
fi

# --- Mass death check ---------------------------------------------------------

TOTAL_ISSUES=$(( ${#CRASHED[@]} + ${#STUCK[@]} ))
if [ "$TOTAL_ISSUES" -ge 3 ]; then
  log ""
  log "MASS DEATH: $TOTAL_ISSUES agents down — escalating instead of restarting"
  gt escalate "Mass agent death: $TOTAL_ISSUES agents down" \
    -s CRITICAL 2>/dev/null || true
fi

# --- Take action --------------------------------------------------------------

# Crashed polecats: notify witness to restart
# Note: `"${arr[@]:-}"` expands an empty array to a single empty string under
# `set -u`, which would fire a phantom `RESTART_POLECAT: /` notification. The
# `${arr[@]+"${arr[@]}"}` form expands to nothing when the array is empty.
for ENTRY in ${CRASHED[@]+"${CRASHED[@]}"}; do
  IFS='|' read -r SESSION RIG PCAT HOOK <<< "$ENTRY"
  log "Requesting restart for $RIG/polecats/$PCAT (hook=$HOOK)"
  gt mail send "$RIG/witness" -s "RESTART_POLECAT: $RIG/$PCAT" --stdin <<BODY
Polecat $PCAT crash confirmed by stuck-agent-dog plugin.
hook_bead: $HOOK
action: restart requested
BODY
done

# Zombie polecats: kill zombie session, then request restart
for ENTRY in ${STUCK[@]+"${STUCK[@]}"}; do
  IFS='|' read -r SESSION RIG PCAT HOOK REASON <<< "$ENTRY"
  log "Killing zombie session $SESSION and requesting restart"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  gt mail send "$RIG/witness" -s "RESTART_POLECAT: $RIG/$PCAT (zombie cleared)" --stdin <<BODY
Polecat $PCAT zombie session cleared by stuck-agent-dog plugin.
hook_bead: $HOOK
reason: $REASON
action: restart requested
BODY
done

# Deacon recovery: auto-restart (rate-limited) instead of escalating to Mayor.
#
# The Deacon is a data-safe singleton — it owns no git worktree and has no work
# to lose, so a stuck/frozen/crashed Deacon is ALWAYS safe to restart. The root
# cause is usually the Claude API / agent-harness layer (e.g. a 400 on
# thinking/redacted_thinking blocks) which gastown cannot fix, and the manual
# recovery is invariably the same trivial `gt deacon restart`. So make recovery
# automatic here instead of generating repeated HIGH-escalation + manual toil.
#
# RATE LIMIT: auto-restart up to N times per rolling window (default 3/hour). If
# the Deacon re-freezes more often than that, auto-restart is not holding (a
# genuine crash-loop / backend problem) — escalate HIGH to the Mayor instead.
DEACON_RESTART_MAX="${STUCK_AGENT_DOG_DEACON_RESTART_MAX:-3}"
DEACON_RESTART_WINDOW="${STUCK_AGENT_DOG_DEACON_RESTART_WINDOW:-3600}"
DEACON_RESTART_STATE="$TOWN_ROOT/.runtime/stuck-agent-dog/deacon-restarts.log"

if [ -n "$DEACON_ISSUE" ]; then
	NOW=$(date +%s)
	WINDOW_START=$(( NOW - DEACON_RESTART_WINDOW ))
	WINDOW_MIN=$(( DEACON_RESTART_WINDOW / 60 ))

	# Count + prune prior auto-restarts within the rolling window. The state
	# file is a plain newline-delimited list of epoch timestamps under
	# .runtime/ so it survives `bd mol wisp gc` and creates no Dolt commits
	# (same durable-state pattern as plugin cooldowns, hq-1o1).
	RECENT_RESTARTS=()
	if [ -f "$DEACON_RESTART_STATE" ]; then
		while IFS= read -r ts; do
			[ -z "$ts" ] && continue
			if [ "$ts" -ge "$WINDOW_START" ] 2>/dev/null; then
				RECENT_RESTARTS+=("$ts")
			fi
		done < "$DEACON_RESTART_STATE"
	fi
	RESTART_COUNT=${#RECENT_RESTARTS[@]}

	if [ "$RESTART_COUNT" -ge "$DEACON_RESTART_MAX" ]; then
		# Rate limit hit — auto-restart is not holding. Escalate HIGH.
		log "Deacon $DEACON_ISSUE: auto-restart rate limit hit (${RESTART_COUNT}/${DEACON_RESTART_MAX} in last ${WINDOW_MIN}m) — escalating HIGH"
		gt escalate "Deacon re-froze ${RESTART_COUNT}x in ${WINDOW_MIN}m — auto-restart not holding, needs investigation/backend swap" \
			-s HIGH \
			--source "plugin:stuck-agent-dog" \
			--fingerprint "stuck-agent-dog:deacon:restart-loop" 2>/dev/null || true
	else
		# Data-safe singleton: auto-restart instead of escalating to Mayor.
		ATTEMPT=$(( RESTART_COUNT + 1 ))
		log "Deacon $DEACON_ISSUE: auto-restarting (data-safe, attempt ${ATTEMPT}/${DEACON_RESTART_MAX} in last ${WINDOW_MIN}m)"
		if gt deacon restart 2>&1 | sed 's/^/[stuck-agent-dog]   /'; then
			RESTART_OK=1
		else
			RESTART_OK=0
		fi

		# Record this attempt (in-window history + now) for the next cycle's
		# rate-limit check. Write atomically via a temp file.
		mkdir -p "$(dirname "$DEACON_RESTART_STATE")" 2>/dev/null || true
		{
			for ts in ${RECENT_RESTARTS[@]+"${RECENT_RESTARTS[@]}"}; do echo "$ts"; done
			echo "$NOW"
		} > "$DEACON_RESTART_STATE.tmp" 2>/dev/null \
			&& mv "$DEACON_RESTART_STATE.tmp" "$DEACON_RESTART_STATE" 2>/dev/null || true

		# Audit bead so auto-restart frequency stays visible (hq-l2msx).
		bd create "stuck-agent-dog: auto-restarted Deacon ($DEACON_ISSUE)" -t chore --ephemeral \
			-l type:plugin-action,plugin:stuck-agent-dog,action:deacon-auto-restart \
			-d "Auto-restarted Deacon (issue=$DEACON_ISSUE, attempt ${ATTEMPT}/${DEACON_RESTART_MAX} in last ${WINDOW_MIN}m, restart_ok=${RESTART_OK}). Data-safe singleton recovery; no Mayor escalation. See hq-l2msx." \
			--silent 2>/dev/null || true

		if [ "$RESTART_OK" -ne 1 ]; then
			# The restart command itself failed — that IS a real problem.
			log "Deacon auto-restart command FAILED — escalating HIGH"
			gt escalate "Deacon auto-restart FAILED ($DEACON_ISSUE) — 'gt deacon restart' returned error" \
				-s HIGH \
				--source "plugin:stuck-agent-dog" \
				--fingerprint "stuck-agent-dog:deacon:restart-failed" 2>/dev/null || true
		fi
	fi
fi

# --- Report -------------------------------------------------------------------

SUMMARY="Agent health: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, $HEALTHY healthy"
[ -n "$DEACON_ISSUE" ] && SUMMARY="$SUMMARY, deacon=$DEACON_ISSUE"
log ""
log "=== $SUMMARY ==="

bd create "stuck-agent-dog: $SUMMARY" -t chore --ephemeral \
  -l type:plugin-run,plugin:stuck-agent-dog,result:success \
  -d "$SUMMARY" --silent 2>/dev/null || true
