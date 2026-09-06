#!/usr/bin/env bash
# Deliver at most one message per invocation.
#  - priority=0 (immediate) bypasses the pacing gate.
#  - priority>=10 only when (now - last_send_ts) >= min_interval_seconds.
#  - pending depth above high_water: one burst summary replaces the whole queue.
# At-least-once: rows are marked 'sent' only after the send succeeds.
set -euo pipefail

BIN="$(cd "$(dirname "$0")" && pwd)"
eval "$(python3 "$BIN/watcherlib.py" --shell)"

ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
log() { echo "$(ts) [dispatcher] $*" >> "$GHW_LOG"; }

[ -f "$GHW_DB" ] || exit 0

NOW=$(date +%s)
LAST=$(sqlite3 "$GHW_DB" "SELECT v FROM gh_dispatch_state WHERE k='last_send_ts';" 2>/dev/null || echo "0")
LAST=${LAST:-0}
SINCE=$(( NOW - LAST ))
DEPTH=$(sqlite3 "$GHW_DB" "SELECT COUNT(*) FROM gh_deliver_queue WHERE status='pending';")

# Burst summary: one message with counts and item numbers instead of popping a row.
if [ "$DEPTH" -gt "$GHW_HIGH_WATER" ]; then
  python3 "$BIN/burst-summary.py"
  exit $?
fi

SEQ=$(sqlite3 "$GHW_DB" "SELECT seq FROM gh_deliver_queue WHERE status='pending' AND priority=0
                         ORDER BY enqueued_at ASC LIMIT 1;")
if [ -z "$SEQ" ]; then
  [ "$SINCE" -lt "$GHW_MIN_INTERVAL" ] && exit 0
  SEQ=$(sqlite3 "$GHW_DB" "SELECT seq FROM gh_deliver_queue WHERE status='pending' AND priority>=10
                           ORDER BY priority ASC, enqueued_at ASC LIMIT 1;")
  [ -z "$SEQ" ] && exit 0
fi

MSG=$(sqlite3 "$GHW_DB" "SELECT message FROM gh_deliver_queue WHERE seq=$SEQ;")
if [ -z "$MSG" ]; then
  log "ERROR seq=$SEQ has empty message; marking sent to avoid a loop"
  sqlite3 "$GHW_DB" "UPDATE gh_deliver_queue SET status='sent', sent_at=datetime('now') WHERE seq=$SEQ;"
  exit 0
fi

if "$BIN/send.sh" "$MSG" >/dev/null 2>&1; then
  sqlite3 "$GHW_DB" "UPDATE gh_deliver_queue SET status='sent', sent_at=datetime('now') WHERE seq=$SEQ;
                     INSERT INTO gh_dispatch_state(k,v) VALUES('last_send_ts','$NOW')
                     ON CONFLICT(k) DO UPDATE SET v=excluded.v;"
  log "sent seq=$SEQ mode=$GHW_MODE depth_was=$DEPTH"
else
  log "WARN send failed seq=$SEQ (will retry next tick)"
  exit 1
fi
