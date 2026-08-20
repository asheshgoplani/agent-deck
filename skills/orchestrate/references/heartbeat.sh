#!/usr/bin/env bash
# Wall-clock watchdog for the orchestrate conductor.
#
# Start it detached, exactly once, at run setup:
#   nohup bash "$RUN_DIR/heartbeat.sh" >> "$RUN_DIR/heartbeat.log" 2>&1 &
#
# Why it exists: the conductor's supervision loop only advances when the
# conductor takes a turn, and nothing in Claude Code takes a turn on its own. A
# conductor that ends a turn with every child running sits idle until a child's
# Stop-hook notification arrives — and those arrive late or not at all, so the
# practical floor on "how long can a finished run sit unnoticed" was however
# long until a human looked. This is that floor, moved to INTERVAL.
#
# It nudges rather than sends: `session nudge` delivers only when the session
# can actually receive a message and verifies submission, so a beat that lands
# mid-turn is skipped instead of interrupting one.
set -uo pipefail   # deliberately NOT -e: one failed nudge must not kill the loop

D="$(cd "$(dirname "$0")" && pwd)"          # = $RUN_DIR
INTERVAL="${HEARTBEAT_INTERVAL:-900}"       # seconds; 15 minutes
DETECT="${WATCHDOG_DETECT_INTERVAL:-90}"    # seconds; conductor substate poll
ID_FILE="$D/.conductor-id"
WD_FILE="$D/.watchdog-id"
STOP_FILE="$D/.heartbeat-stop"
# Consecutive undeliverable beats tolerated before the watchdog gives up. A
# conductor can be legitimately unreachable for one or two beats (restart,
# rotation in progress); a run of them means it is gone or wedged.
MAX_MISSES="${HEARTBEAT_MAX_MISSES:-4}"

MSG="Heartbeat. Run: bash \"$D/poll.sh\" — then act on whatever it reports. If it reports no change and no child is waiting on you, say so in one line and stop; do not re-read the manifest or sweep child output just because this beat fired."

log() { printf '%s heartbeat: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"; }

if [ ! -s "$ID_FILE" ]; then
  log "FATAL $ID_FILE is missing or empty — run setup must write the conductor's id there."
  exit 2
fi

# Detector: while the conductor is between beats, poll its substate every
# DETECT seconds; `waiting` or `stalled` means it cannot help itself (nudge
# refuses those targets), so wake the watchdog child to judge the situation.
# No .watchdog-id file ⇒ no watchdog in this run ⇒ the detector is inert.
detect_tick() {
  CID="$(cat "$ID_FILE" 2>/dev/null || true)"
  WID="$(cat "$WD_FILE" 2>/dev/null || true)"
  [ -n "$CID" ] && [ -n "$WID" ] || return 0

  out="$(agent-deck session show "$CID" --json 2>/dev/null || true)"
  substate="$(printf '%s' "$out" | sed -n 's/.*"substate"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  # A watchdog that exhausted its misses is dead; a *different* id in the
  # file (respawn) starts fresh.
  if [ "$WID" = "$wd_dead_id" ]; then
    return 0
  fi
  [ "$WID" = "$wd_miss_id" ] || { wd_misses=0; wd_miss_id="$WID"; }

  out="$(agent-deck session show "$CID" --json 2>/dev/null || true)"
  substate="$(printf '%s' "$out" | sed -n 's/.*"substate"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  case "$substate" in
    waiting|stalled)
      # Debounce: one wake per INTERVAL for a persistent state. A state
      # *change* re-arms immediately; the watchdog's own "same state on next
      # wake → escalate" rule owns the follow-up within the window. Failed
      # wakes are not debounced — they retry every tick and count misses.
      if [ "$substate" != "$wd_last_substate" ] || [ "$clock" -ge "$wd_next_ok" ]; then
        if wake_watchdog "conductor substate=$substate"; then
          wd_misses=0
          wd_last_substate="$substate"
          wd_next_ok=$((clock + INTERVAL))
        else
          wd_misses=$((wd_misses + 1))
          log "watchdog nudge failed (wid=$WID, miss $wd_misses/$MAX_MISSES)"
          if [ "$wd_misses" -ge "$MAX_MISSES" ]; then
            wd_dead_id="$WID"
            log "watchdog is gone or wedged after $wd_misses misses — notifying the user; conductor beats continue."
            command -v terminal-notifier >/dev/null 2>&1 && \
              terminal-notifier -title "agent-deck orchestrate" \
                -message "Watchdog $WID is unreachable and the conductor is $substate — run $D needs a look." || true
          fi
        fi
      fi
      ;;
    *) wd_last_substate="" ;;
  esac
}

wake_watchdog() {
  reason="$1"
  agent-deck session nudge "$WID" "Watchdog check: $reason. Inspect the conductor ($CID) per your policy and act." --json >/dev/null 2>&1
  rc=$?
  [ "$rc" -eq 0 ] && log "watchdog nudged (wid=$WID): $reason"
  return "$rc"
}

log "started; interval=${INTERVAL}s detect=${DETECT}s max_misses=$MAX_MISSES run_dir=$D"
misses=0
elapsed=0
clock=0
wd_last_substate=""
wd_next_ok=0
wd_misses=0
wd_miss_id=""
wd_dead_id=""
while :; do
  sleep "$DETECT"
  elapsed=$((elapsed + DETECT))
  clock=$((clock + DETECT))

  if [ -e "$STOP_FILE" ]; then
    log "stop file present — exiting cleanly."
    exit 0
  fi

  detect_tick

  # The conductor beat itself stays on the slow clock.
  [ "$elapsed" -ge "$INTERVAL" ] || continue
  elapsed=0

  # Re-read the id every beat rather than caching it: rotate-conductor.sh
  # rewrites this file when the conductor rotates, and a watchdog pinned to the
  # dead predecessor is worse than no watchdog — it reports healthy beats
  # (exit 2, "not found") against a session nobody is running.
  CID="$(cat "$ID_FILE" 2>/dev/null || true)"
  if [ -z "$CID" ]; then
    log "conductor id file is empty; treating as a miss."
    misses=$((misses + 1))
  else
    out="$(agent-deck session nudge "$CID" "$MSG" --json 2>&1)"
    rc=$?
    case "$rc" in
      0)
        # 0 covers both "delivered" and "skipped, session was busy". Busy is the
        # healthy case during active supervision, so neither resets to an error.
        misses=0
        log "ok rc=0 $(printf '%s' "$out" | tr -d '\n' | cut -c1-200)"
        ;;
      2)
        misses=$((misses + 1))
        log "session not found (rc=2, miss $misses/$MAX_MISSES) id=$CID"
        ;;
      *)
        # rc=1 is the one that matters: reachable but the message did not
        # submit. That is the stalled-composer signature, not an absent session
        # — exactly what the watchdog exists to judge, so wake it too.
        misses=$((misses + 1))
        log "NOT DELIVERED rc=$rc (miss $misses/$MAX_MISSES) $(printf '%s' "$out" | tr -d '\n' | cut -c1-200)"
        WID="$(cat "$WD_FILE" 2>/dev/null || true)"
        if [ -n "$WID" ] && [ "$WID" != "$wd_dead_id" ]; then
          wake_watchdog "conductor nudge not submitted (rc=$rc) — stalled composer" || true
        fi
        ;;
    esac
  fi

  if [ "$misses" -ge "$MAX_MISSES" ]; then
    log "FATAL $misses consecutive undeliverable beats — conductor is gone or wedged. Exiting."
    log "      Inspect: agent-deck session show \"$CID\" ; tail \"$D/heartbeat.log\""
    exit 1
  fi
done
