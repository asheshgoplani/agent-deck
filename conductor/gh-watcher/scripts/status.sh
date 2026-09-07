#!/usr/bin/env bash
# Liveness and queue snapshot. Exit 0 when the last successful poll is fresh,
# exit 2 when stale (older than cadence.stale_after_seconds) so a heartbeat
# rule can turn it into a NEED line.
set -euo pipefail
BIN="$(cd "$(dirname "$0")" && pwd)"
eval "$(python3 "$BIN/watcherlib.py" --shell)"

NOW=$(date +%s)
LAST=$(cat "$GHW_LIVENESS" 2>/dev/null || echo 0)
AGE=$(( NOW - LAST ))
DEPTH=$(sqlite3 "$GHW_DB" "SELECT COUNT(*) FROM gh_deliver_queue WHERE status='pending';" 2>/dev/null || echo 0)
STATE=fresh
[ "$AGE" -gt "$GHW_STALE_AFTER" ] && STATE=stale
echo "gh-watcher repo=$GHW_REPO mode=$GHW_MODE last_poll_age=${AGE}s liveness=$STATE pending=$DEPTH"
[ "$STATE" = "fresh" ]  || exit 2
