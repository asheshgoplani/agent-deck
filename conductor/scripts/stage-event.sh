#!/usr/bin/env bash
# Append one stage event for a PR to the stage events JSONL file.
# Usage: stage-event.sh <pr> <stage> [note]
# Schema and stage list: conductor/STAGE-EVENTS.md
set -euo pipefail

STAGES="arrived discovered triaged review_started verdict ci_green merged closed"

usage() {
    echo "usage: $(basename "$0") <pr> <stage> [note]" >&2
    echo "stages: ${STAGES}" >&2
    exit 2
}

[ $# -ge 2 ] && [ $# -le 3 ] || usage

pr="$1"
stage="$2"
note="${3:-}"

case "$pr" in
    ''|*[!0-9]*) echo "error: pr must be a positive integer, got '$pr'" >&2; exit 2 ;;
esac

valid=0
for s in $STAGES; do
    [ "$s" = "$stage" ] && valid=1
done
if [ "$valid" -ne 1 ]; then
    echo "error: unknown stage '$stage'" >&2
    echo "stages: ${STAGES}" >&2
    exit 2
fi

events_file="${STAGE_EVENTS_FILE:-./stage-events.jsonl}"
actor="${STAGE_EVENTS_ACTOR:-${GITHUB_ACTOR:-${USER:-conductor}}}"
repo="${STAGE_EVENTS_REPO:-${GITHUB_REPOSITORY:-asheshgoplani/agent-deck}}"

mkdir -p "$(dirname "$events_file")"

PR="$pr" STAGE="$stage" NOTE="$note" ACTOR="$actor" REPO="$repo" \
python3 - >> "$events_file" <<'PY'
import datetime
import json
import os

event = {
    "pr": int(os.environ["PR"]),
    "stage": os.environ["STAGE"],
    "ts": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "actor": os.environ["ACTOR"],
    "repo": os.environ["REPO"],
}
if os.environ.get("NOTE"):
    event["note"] = os.environ["NOTE"]
print(json.dumps(event, separators=(", ", ": ")))
PY
