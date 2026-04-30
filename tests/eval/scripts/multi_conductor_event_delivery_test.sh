#!/usr/bin/env bash
# multi_conductor_event_delivery_test.sh
#
# Multi-conductor event-delivery regression harness for issue #824.
# For every conductor session on the host, spawns a disposable child,
# triggers a running -> waiting transition, then audits transition
# notifier output to verify the spec from #824:
#
#   1. A delivery_result=sent record exists for the child's transition
#      (within DELIVERY_GRACE_SECS of the transition).
#   2. The same fingerprint (child_session_id|from|to|timestamp) appears
#      EXACTLY ONCE in transition-notifier.log -> dedup contract.
#   3. The conductor's inbox file contains AT MOST ONE entry per
#      fingerprint -> inbox dedup contract.
#   4. notifier-missed.log holds AT MOST ONE re-fire entry for this
#      child -> retry-loop guard.
#
# DOES NOT modify real conductor state. Only adds, starts, stops, and
# removes its own ephemeral child sessions named
# "evt-test-from-<conductor-id-short>".
#
# Output: tests/eval/reports/multi_conductor_event_delivery_<ts>.md
# Exit:   0 on PASS, 1 on FAIL, 2 if no conductors found (skipped).
#
# Required env / deps: agent-deck (>= v1.7.73), jq, sha256sum.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
REPORT_DIR="$REPO_ROOT/tests/eval/reports"
TS="$(date +%Y%m%d-%H%M%S)"
REPORT="$REPORT_DIR/multi_conductor_event_delivery_$TS.md"
mkdir -p "$REPORT_DIR"

BIN="${AGENT_DECK_BIN:-$(command -v agent-deck || true)}"
PROFILE="${AGENT_DECK_PROFILE:-personal}"
WAIT_TIMEOUT="${WAIT_TIMEOUT:-90}"
DELIVERY_GRACE_SECS="${DELIVERY_GRACE_SECS:-30}"
AGENT_DECK_DIR="${AGENT_DECK_DIR:-$HOME/.agent-deck}"
LOG_DIR="$AGENT_DECK_DIR/logs"
INBOX_DIR="$AGENT_DECK_DIR/inboxes"
TRANSITION_LOG="$LOG_DIR/transition-notifier.log"
MISSED_LOG="$LOG_DIR/notifier-missed.log"

# --- preflight ---------------------------------------------------------------

if [[ -z "$BIN" || ! -x "$BIN" ]]; then
  echo "FAIL: agent-deck binary not found (set AGENT_DECK_BIN)" >&2
  exit 1
fi
for dep in jq sha256sum; do
  if ! command -v "$dep" >/dev/null 2>&1; then
    echo "FAIL: missing dependency: $dep" >&2
    exit 1
  fi
done

# --- helpers -----------------------------------------------------------------

log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }

# Compute a stable fingerprint key from the canonical event tuple.
# Mirrors the spec for #824: sha256(child_id|from|to|timestamp).
fingerprint() {
  local cid="$1" from="$2" to="$3" ts="$4"
  printf '%s|%s|%s|%s' "$cid" "$from" "$to" "$ts" | sha256sum | awk '{print $1}'
}

# Tail the transition log for entries matching child_session_id, return JSON
# array of records.
records_for_child() {
  local child_id="$1"
  if [[ ! -f "$TRANSITION_LOG" ]]; then
    printf '[]'
    return
  fi
  # Each line is one JSON record. Filter by child_session_id.
  jq -c --arg id "$child_id" 'select(.child_session_id == $id)' \
    "$TRANSITION_LOG" 2>/dev/null | jq -s '.'
}

inbox_records_for_child() {
  local conductor_id="$1" child_id="$2"
  local inbox_file="$INBOX_DIR/$conductor_id.jsonl"
  if [[ ! -f "$inbox_file" ]]; then
    printf '[]'
    return
  fi
  jq -c --arg id "$child_id" 'select(.child_session_id == $id)' \
    "$inbox_file" 2>/dev/null | jq -s '.'
}

missed_records_for_child() {
  local child_id="$1"
  if [[ ! -f "$MISSED_LOG" ]]; then
    printf '[]'
    return
  fi
  # The missed log uses a similar JSONL format; tolerate either nested or
  # flat schemas by matching on substring of child_session_id.
  jq -c --arg id "$child_id" \
    'select((.child_session_id // .event.child_session_id // "") == $id)' \
    "$MISSED_LOG" 2>/dev/null | jq -s '.'
}

short_id() { printf '%s' "${1:0:8}"; }

# Poll session status until it reaches one of the accepted values, or
# WAIT_TIMEOUT elapses. Echoes the final status. Returns 0 if reached, 1
# if timed out.
wait_for_status() {
  local sid="$1"; shift
  local accept="$*"  # space-separated list
  local deadline=$(( $(date +%s) + WAIT_TIMEOUT ))
  local last=""
  while (( $(date +%s) < deadline )); do
    last=$("$BIN" -p "$PROFILE" session show -json "$sid" 2>/dev/null \
      | jq -r '.status // empty')
    for ok in $accept; do
      if [[ "$last" == "$ok" ]]; then
        printf '%s' "$last"
        return 0
      fi
    done
    sleep 2
  done
  printf '%s' "$last"
  return 1
}

# --- enumerate conductors ----------------------------------------------------

mapfile -t CONDUCTOR_LINES < <(
  "$BIN" -p "$PROFILE" list -json 2>/dev/null \
    | jq -r '.[] | select(.title | test("^conductor-|^agent-deck$")) | "\(.id)\t\(.title)"'
)

if (( ${#CONDUCTOR_LINES[@]} == 0 )); then
  log "no conductors found on host (profile=$PROFILE) — skipping"
  {
    echo "# Multi-Conductor Event Delivery Report"
    echo
    echo "- Timestamp: $TS"
    echo "- Profile: $PROFILE"
    echo "- Result: **SKIPPED** (no conductors found)"
  } > "$REPORT"
  echo "REPORT: $REPORT"
  exit 2
fi

log "found ${#CONDUCTOR_LINES[@]} conductor(s)"

# --- run per-conductor probe -------------------------------------------------

PASS_COUNT=0
FAIL_COUNT=0
declare -a ROWS

cleanup_child() {
  local child_id="$1"
  if [[ -n "$child_id" ]]; then
    "$BIN" -p "$PROFILE" session stop "$child_id" >/dev/null 2>&1 || true
    "$BIN" -p "$PROFILE" remove "$child_id" >/dev/null 2>&1 || true
  fi
}

for line in "${CONDUCTOR_LINES[@]}"; do
  CID="${line%%$'\t'*}"
  CTITLE="${line##*$'\t'}"
  SHORT="$(short_id "$CID")"
  CHILD_TITLE="evt-test-from-$SHORT"
  CHILD_ID=""
  STATUS_REASON=""

  log "conductor=$CTITLE id_short=$SHORT — probing"

  # Pick a group: re-use conductor's current group; fallback to 'agent-deck'
  # for the agent-deck conductor.
  CGROUP="$("$BIN" -p "$PROFILE" session show -json "$CID" 2>/dev/null \
    | jq -r '.group // empty')"
  if [[ -z "$CGROUP" ]]; then
    CGROUP="agent-deck"
  fi

  ADD_OUT="$("$BIN" -p "$PROFILE" add \
      -t "$CHILD_TITLE" \
      -g "$CGROUP" \
      --parent "$CID" \
      -c claude \
      -json \
      "$REPO_ROOT" 2>&1 || true)"
  CHILD_ID="$(printf '%s' "$ADD_OUT" | jq -r '.id // empty' 2>/dev/null)"

  if [[ -z "$CHILD_ID" ]]; then
    STATUS_REASON="add-failed: $(printf '%s' "$ADD_OUT" | head -c 120)"
    ROWS+=("FAIL|$CTITLE|$SHORT|$STATUS_REASON")
    FAIL_COUNT=$(( FAIL_COUNT + 1 ))
    continue
  fi

  # mark probe start so we can window the audit
  PROBE_START_TS="$(date -u +%FT%TZ)"

  if ! "$BIN" -p "$PROFILE" session start "$CHILD_ID" >/dev/null 2>&1; then
    STATUS_REASON="session-start-failed"
    cleanup_child "$CHILD_ID"
    ROWS+=("FAIL|$CTITLE|$SHORT|$STATUS_REASON")
    FAIL_COUNT=$(( FAIL_COUNT + 1 ))
    continue
  fi

  # Send a tiny prompt that should drive running -> waiting quickly.
  "$BIN" -p "$PROFILE" session send "$CHILD_ID" \
    --no-wait "say pong and stop" >/dev/null 2>&1 || true

  if ! FINAL_STATUS="$(wait_for_status "$CHILD_ID" waiting idle)"; then
    STATUS_REASON="transition-timeout (last=$FINAL_STATUS)"
    cleanup_child "$CHILD_ID"
    ROWS+=("FAIL|$CTITLE|$SHORT|$STATUS_REASON")
    FAIL_COUNT=$(( FAIL_COUNT + 1 ))
    continue
  fi

  # Allow the notifier a short grace window to log + dispatch.
  sleep "$DELIVERY_GRACE_SECS"

  RECORDS_JSON="$(records_for_child "$CHILD_ID")"
  INBOX_JSON="$(inbox_records_for_child "$CID" "$CHILD_ID")"
  MISSED_JSON="$(missed_records_for_child "$CHILD_ID")"

  SENT_COUNT="$(printf '%s' "$RECORDS_JSON" \
    | jq '[.[] | select(.delivery_result == "sent")] | length')"

  # Dedup check: count duplicate fingerprints in the log.
  DUP_COUNT="$(printf '%s' "$RECORDS_JSON" | jq -r '
    [.[] | "\(.child_session_id)|\(.from_status)|\(.to_status)|\(.timestamp)"]
    | group_by(.)
    | map(select(length > 1))
    | length
  ')"

  INBOX_DUPS="$(printf '%s' "$INBOX_JSON" | jq -r '
    [.[] | "\(.child_session_id)|\(.from_status)|\(.to_status)|\(.timestamp)"]
    | group_by(.)
    | map(select(length > 1))
    | length
  ')"

  REFIRE_COUNT="$(printf '%s' "$MISSED_JSON" | jq 'length')"

  REASON_PARTS=()
  if (( SENT_COUNT < 1 )); then
    REASON_PARTS+=("no-sent-delivery")
  fi
  if (( DUP_COUNT > 0 )); then
    REASON_PARTS+=("log-duplicates=$DUP_COUNT")
  fi
  if (( INBOX_DUPS > 0 )); then
    REASON_PARTS+=("inbox-duplicates=$INBOX_DUPS")
  fi
  if (( REFIRE_COUNT > 1 )); then
    REASON_PARTS+=("refire-loop=$REFIRE_COUNT")
  fi

  if (( ${#REASON_PARTS[@]} == 0 )); then
    PASS_COUNT=$(( PASS_COUNT + 1 ))
    ROWS+=("PASS|$CTITLE|$SHORT|sent=$SENT_COUNT dup=$DUP_COUNT inbox_dup=$INBOX_DUPS refire=$REFIRE_COUNT")
  else
    FAIL_COUNT=$(( FAIL_COUNT + 1 ))
    ROWS+=("FAIL|$CTITLE|$SHORT|$(IFS=,; printf '%s' "${REASON_PARTS[*]}")")
  fi

  cleanup_child "$CHILD_ID"
done

# --- write report ------------------------------------------------------------

{
  echo "# Multi-Conductor Event Delivery Report"
  echo
  echo "- Timestamp: $TS"
  echo "- Profile: $PROFILE"
  echo "- Conductors probed: ${#CONDUCTOR_LINES[@]}"
  echo "- PASS: $PASS_COUNT"
  echo "- FAIL: $FAIL_COUNT"
  echo
  echo "## Per-conductor results"
  echo
  echo "| Result | Conductor | Short ID | Detail |"
  echo "|--------|-----------|----------|--------|"
  for row in "${ROWS[@]}"; do
    IFS='|' read -r r t s d <<<"$row"
    echo "| $r | $t | $s | $d |"
  done
  echo
  echo "_Generated by tests/eval/scripts/multi_conductor_event_delivery_test.sh_"
} > "$REPORT"

echo "REPORT: $REPORT"

if (( FAIL_COUNT > 0 )); then
  exit 1
fi
exit 0
