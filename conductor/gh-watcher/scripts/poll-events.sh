#!/usr/bin/env bash
# Poll /repos/<owner>/<repo>/events with ETag dedup, ingest new rows, classify.
# Idempotent: a 304 short-circuits. Every successful round trip (200 or 304)
# refreshes the liveness file so the heartbeat can tell "quiet" from "dead".
set -euo pipefail

BIN="$(cd "$(dirname "$0")" && pwd)"
eval "$(python3 "$BIN/watcherlib.py" --shell)"

ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
log() { echo "$(ts) [poll-events] $*" >> "$GHW_LOG"; }
mark_alive() { date +%s > "$GHW_LIVENESS"; }

mkdir -p "$(dirname "$GHW_DB")" "$(dirname "$GHW_LOG")" "$(dirname "$GHW_LIVENESS")"
sqlite3 "$GHW_DB" < "$BIN/../schema.sql" >/dev/null

ETAG=$(sqlite3 "$GHW_DB" "SELECT COALESCE(etag,'') FROM gh_poll_cursor WHERE name='events';" 2>/dev/null || echo "")

HDR=()
[ -n "$ETAG" ] && HDR+=(-H "If-None-Match: $ETAG")

# Overwritten every tick; kept on disk so the last raw response is inspectable.
RESP_FILE="$GHW_HOME/.last-response"

# gh api exits non-zero on 304; inspect the status line before treating that as an error.
set +e
# ${HDR[@]+...} keeps bash 3.2 happy with an empty array under set -u.
gh api ${HDR[@]+"${HDR[@]}"} -i "/repos/$GHW_REPO/events?per_page=100" > "$RESP_FILE" 2>/dev/null
RC=$?
set -e
STATUS=$(head -n1 "$RESP_FILE" | awk '{print $2}')
if [ "$STATUS" = "304" ]; then
  mark_alive
  exit 0
fi
if [ "$RC" -ne 0 ] || [ "$STATUS" != "200" ]; then
  log "ERROR gh api rc=$RC status=${STATUS:-none}"
  exit 1
fi

HDR_END=$(grep -n -m1 '^[[:space:]]*$' "$RESP_FILE" | cut -d: -f1 || echo 0)
if [ "${HDR_END:-0}" -eq 0 ]; then
  log "ERROR no header/body split"
  exit 1
fi

NEW_ETAG=$(head -n "$HDR_END" "$RESP_FILE" | awk 'BEGIN{IGNORECASE=1} /^etag:/ {print $2; exit}' | tr -d '\r"')
if [ -n "$NEW_ETAG" ]; then
  sqlite3 "$GHW_DB" "INSERT INTO gh_poll_cursor(name,etag,updated_at) VALUES('events','$NEW_ETAG',CURRENT_TIMESTAMP)
                     ON CONFLICT(name) DO UPDATE SET etag=excluded.etag, updated_at=CURRENT_TIMESTAMP;" || true
fi
mark_alive

BODY=$(tail -n +$((HDR_END + 1)) "$RESP_FILE")
if [ -z "$BODY" ] || [ "$BODY" = "[]" ]; then
  exit 0
fi

echo "$BODY" | python3 "$BIN/ingest-events.py" 2>>"$GHW_LOG" || log "ingest failed"
python3 "$BIN/classify.py" 2>>"$GHW_LOG" || log "classify failed"
