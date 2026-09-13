#!/usr/bin/env bash
# One scheduler tick: poll, dispatch, and flush the digest once a day.
# launchd (StartInterval) or a systemd timer runs this every cadence.events_seconds.
set -uo pipefail
BIN="$(cd "$(dirname "$0")" && pwd)"
eval "$(python3 "$BIN/watcherlib.py" --shell)"

"$BIN/poll-events.sh"
"$BIN/dispatcher.sh"

TODAY=$(date +%F)
LAST_FLUSH=$(sqlite3 "$GHW_DB" "SELECT v FROM gh_dispatch_state WHERE k='last_digest_flush';" 2>/dev/null || echo "")
if [ "$LAST_FLUSH" != "$TODAY" ] && [[ "$(date +%H:%M)" > "$GHW_DIGEST_FLUSH_TIME" || "$(date +%H:%M)" == "$GHW_DIGEST_FLUSH_TIME" ]]; then
  "$BIN/digest-flush.sh" && sqlite3 "$GHW_DB" "INSERT INTO gh_dispatch_state(k,v) VALUES('last_digest_flush','$TODAY')
                                               ON CONFLICT(k) DO UPDATE SET v=excluded.v;"
fi
