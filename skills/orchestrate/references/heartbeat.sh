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
# How long a decision prompt may sit before the user is bannered directly. The
# watchdog is woken immediately; this is the backstop for when the watchdog is
# absent, dead, or correctly decides the prompt is not its to answer.
CHOICE_ESCALATE="${WATCHDOG_CHOICE_ESCALATE:-300}"

MSG="Heartbeat. Run: bash \"$D/poll.sh\" — then act on whatever it reports. If it reports no change and no child is waiting on you, say so in one line and stop; do not re-read the manifest or sweep child output just because this beat fired."

log() { printf '%s heartbeat: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"; }

if [ ! -s "$ID_FILE" ]; then
  log "FATAL $ID_FILE is missing or empty — run setup must write the conductor's id there."
  exit 2
fi

# Detector: while the conductor is between beats, poll its state every DETECT
# seconds and wake the watchdog child when the conductor cannot help itself.
#
# Three states qualify, and the difference between them is load-bearing:
#
#   awaiting-choice  a permission dialog or an AskUserQuestion decision menu is
#                    on screen. The conductor is HEALTHY — it is waiting on a
#                    person. Never nudge it (that dismisses the question and
#                    pastes the options into the composer as text); wake the
#                    watchdog to approve a safe permission prompt, and banner
#                    the human directly for a decision only they can make.
#   stalled          the composer is gated. Wedged; the watchdog judges it.
#   nudge rc=1       reachable but the beat did not submit.
#
# This block used to match `waiting|stalled` against the SUBSTATE field. There
# is no `waiting` substate — `waiting` is a coarse STATUS value — so only
# `stalled` could ever fire, and a conductor sitting on a decision prompt was
# invisible to the detector. It cost an hour of a live run on 2026-08-20: the
# conductor asked its human twice, its own heartbeat destroyed the question
# both times, and the watchdog was never woken to notice.
#
# No .watchdog-id file ⇒ no watchdog in this run ⇒ wakes are skipped, but the
# direct user banner below still fires: escalation must not depend on an agent
# being alive.
detect_tick() {
  CID="$(cat "$ID_FILE" 2>/dev/null || true)"
  [ -n "$CID" ] || return 0
  WID="$(cat "$WD_FILE" 2>/dev/null || true)"

  # A watchdog that exhausted its misses is dead; a *different* id in the file
  # (respawn) starts fresh.
  [ "$WID" = "$wd_miss_id" ] || { wd_misses=0; wd_miss_id="$WID"; }

  out="$(agent-deck session show "$CID" --json 2>/dev/null || true)"
  substate="$(printf '%s' "$out" | sed -n 's/.*"substate"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"

  # A decision prompt is the human's to answer. Track how long it has been up
  # so the user is told directly, whether or not a watchdog is alive.
  if [ "$substate" = "awaiting-choice" ]; then
    [ "$choice_since" -gt 0 ] || choice_since="$clock"
    if [ $((clock - choice_since)) -ge "$CHOICE_ESCALATE" ] && [ "$clock" -ge "$choice_next_banner" ]; then
      log "conductor has been awaiting a human choice for $((clock - choice_since))s — notifying the user."
      command -v terminal-notifier >/dev/null 2>&1 && \
        terminal-notifier -title "agent-deck orchestrate" \
          -message "Conductor is waiting on YOUR answer ($((clock - choice_since))s). Run: agent-deck session attach $CID" || true
      choice_next_banner=$((clock + INTERVAL))
    fi
  else
    choice_since=0
    choice_next_banner=0
  fi

  [ -n "$WID" ] && [ "$WID" != "$wd_dead_id" ] || return 0

  case "$substate" in
    awaiting-choice|stalled)
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
choice_since=0
choice_next_banner=0
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
        # A refusal because the conductor is showing a prompt only a human can
        # answer is NOT a miss. The conductor is healthy and may legitimately
        # wait hours; counting it would exhaust MAX_MISSES and kill the
        # heartbeat of a live run — the loop must keep beating and keep
        # escalating to the person instead. `session nudge` refuses this
        # deliberately: sending would dismiss the prompt and paste its options
        # into the composer as text.
        if printf '%s' "$out" | grep -q 'SESSION_AWAITING_CHOICE'; then
          log "beat withheld — conductor is waiting on a human choice (not a miss; misses stay at $misses)"
          WID="$(cat "$WD_FILE" 2>/dev/null || true)"
          if [ -n "$WID" ] && [ "$WID" != "$wd_dead_id" ]; then
            wake_watchdog "conductor is showing a prompt only a human can answer" || true
          fi
        else
          # rc=1 is the one that matters: reachable but the message did not
          # submit. That is the stalled-composer signature, not an absent
          # session — exactly what the watchdog exists to judge, so wake it too.
          misses=$((misses + 1))
          log "NOT DELIVERED rc=$rc (miss $misses/$MAX_MISSES) $(printf '%s' "$out" | tr -d '\n' | cut -c1-200)"
          WID="$(cat "$WD_FILE" 2>/dev/null || true)"
          if [ -n "$WID" ] && [ "$WID" != "$wd_dead_id" ]; then
            wake_watchdog "conductor nudge not submitted (rc=$rc) — stalled composer" || true
          fi
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
