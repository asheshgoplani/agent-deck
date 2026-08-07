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
ID_FILE="$D/.conductor-id"
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

log "started; interval=${INTERVAL}s max_misses=$MAX_MISSES run_dir=$D"
misses=0
while :; do
  sleep "$INTERVAL"

  if [ -e "$STOP_FILE" ]; then
    log "stop file present — exiting cleanly."
    exit 0
  fi

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
        # submit. That is the stalled-composer signature, not an absent session.
        misses=$((misses + 1))
        log "NOT DELIVERED rc=$rc (miss $misses/$MAX_MISSES) $(printf '%s' "$out" | tr -d '\n' | cut -c1-200)"
        ;;
    esac
  fi

  if [ "$misses" -ge "$MAX_MISSES" ]; then
    log "FATAL $misses consecutive undeliverable beats — conductor is gone or wedged. Exiting."
    log "      Inspect: agent-deck session show \"$CID\" ; tail \"$D/heartbeat.log\""
    exit 1
  fi
done
