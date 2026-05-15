#!/usr/bin/env bash
# goal.sh — Phase 1 wrapper that creates a goal JSON and spawns the worker.
#
# Phase 1 is intentionally minimal: no subcommand parsing, no daemon, no
# cron auto-install. You run this once to start a goal, then run
# manager.py periodically (manually or via cron) to advance it.
#
# Usage:
#   goal.sh <id> <goal> <done-cmd> [worker-title] [workdir]
#
# Examples:
#   goal.sh test-marker \
#       "Create a test marker file" \
#       'test -f /tmp/goal-test-marker.txt'
#
#   goal.sh v160-release \
#       "Ship agent-deck v1.6.0 to GitHub Releases" \
#       'gh release view v1.6.0 -R asheshgoplani/agent-deck --json publishedAt | jq -e ".publishedAt != null"' \
#       pursue-v160 \
#       /home/ashesh-goplani/agent-deck
#
# Writes to:
#   ~/.agent-deck/goals/<id>.json
#
# Spawns:
#   agent-deck session "<worker-title>" running the worker contract prompt
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROMPT_TEMPLATE="$SCRIPT_DIR/prompts/worker.md"
GOALS_DIR="${GOALS_DIR:-$HOME/.agent-deck/goals}"

if [[ $# -lt 3 ]]; then
    sed -n '3,/^set -uo/p' "$0" | sed -n '3,/^set -uo/p' | head -25
    exit 2
fi

ID="$1"
GOAL="$2"
DONE_CMD="$3"
WORKER_TITLE="${4:-pursue-$ID}"
WORKDIR="${5:-$HOME}"

CHECK_INTERVAL_SECONDS="${CHECK_INTERVAL_SECONDS:-300}"     # manager polls every 5 min
MAX_IDLE_SECONDS="${MAX_IDLE_SECONDS:-3600}"               # nudge after 1h of silence
MAX_CYCLES="${MAX_CYCLES:-24}"                             # hard stop after 24 receipts
ESCALATE_AFTER="${ESCALATE_AFTER:-3}"                      # nudges before paging user
CONDUCTOR="${CONDUCTOR:-agent-deck}"
PROFILE="${PROFILE:-personal}"

[[ -f "$PROMPT_TEMPLATE" ]] || { echo "[pursue] missing prompt template: $PROMPT_TEMPLATE" >&2; exit 1; }
mkdir -p "$GOALS_DIR"
GOAL_PATH="$GOALS_DIR/${ID}.json"

if [[ -f "$GOAL_PATH" ]]; then
    echo "[pursue] goal already exists: $GOAL_PATH" >&2
    echo "[pursue] cancel it first or pick a different id" >&2
    exit 1
fi

NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# Render the worker prompt by substituting {PLACEHOLDERS}
PROMPT="$(cat "$PROMPT_TEMPLATE")"
PROMPT="${PROMPT//\{GOAL\}/$GOAL}"
PROMPT="${PROMPT//\{DONE_CMD\}/$DONE_CMD}"
PROMPT="${PROMPT//\{WORKDIR\}/$WORKDIR}"
PROMPT="${PROMPT//\{GOAL_ID\}/$ID}"
PROMPT="${PROMPT//\{CHECK_INTERVAL_SECONDS\}/$CHECK_INTERVAL_SECONDS}"
PROMPT="${PROMPT//\{MAX_CYCLES\}/$MAX_CYCLES}"

# Spawn the worker via agent-deck launch — we use add+start+send so we can
# capture the session id reliably (launch -m sometimes races with prompts).
echo "[pursue] spawning worker: $WORKER_TITLE in $WORKDIR" >&2

# Create the session
if ! agent-deck -p "$PROFILE" add "$WORKDIR" -t "$WORKER_TITLE" -c claude -g goal 2>&1 | grep -E "(Created|exists)" >&2; then
    :  # add may exit non-zero on "already exists"; we tolerate that
fi

agent-deck -p "$PROFILE" session start "$WORKER_TITLE" >&2 || true
sleep 2

# Look up the resolved session id
SESSION_ID="$(agent-deck -p "$PROFILE" list --json 2>/dev/null \
    | python3 -c "import sys,json; print(next((s['id'] for s in json.load(sys.stdin) if s.get('title')=='$WORKER_TITLE'), ''))")"

if [[ -z "$SESSION_ID" ]]; then
    echo "[pursue] could not resolve worker session id for title '$WORKER_TITLE'" >&2
    exit 1
fi

# Send the contract prompt (long, so write to a file and reference)
PROMPT_FILE="$(mktemp -t pursue-prompt-XXXXXX.md)"
printf '%s\n' "$PROMPT" > "$PROMPT_FILE"
agent-deck -p "$PROFILE" session send "$WORKER_TITLE" "$(cat "$PROMPT_FILE")" --no-wait -q >&2 || {
    echo "[pursue] failed to send contract prompt — worker session $SESSION_ID exists but is not yet driving" >&2
}
rm -f "$PROMPT_FILE"

# Write the goal JSON
python3 - "$GOAL_PATH" "$ID" "$GOAL" "$DONE_CMD" "$WORKER_TITLE" "$SESSION_ID" "$WORKDIR" "$CONDUCTOR" \
        "$CHECK_INTERVAL_SECONDS" "$MAX_IDLE_SECONDS" "$MAX_CYCLES" "$ESCALATE_AFTER" "$NOW" <<'PYEOF'
import json
import sys

path = sys.argv[1]
data = {
    "id": sys.argv[2],
    "goal": sys.argv[3],
    "done_cmd": sys.argv[4],
    "worker_session_title": sys.argv[5],
    "worker_session_id": sys.argv[6],
    "workdir": sys.argv[7],
    "conductor": sys.argv[8],
    "schedule": {
        "check_interval_seconds": int(sys.argv[9]),
        "max_idle_seconds": int(sys.argv[10]),
        "max_cycles": int(sys.argv[11]),
        "escalate_after_stuck_nudges": int(sys.argv[12]),
    },
    "state": {
        "status": "active",
        "created_at": sys.argv[13],
        "last_verified_at": None,
        "last_receipt_seen_at": None,
        "last_receipt_text": None,
        "cycles_completed": 0,
        "nudges_sent": 0,
        "escalated_at": None,
        "ended_at": None,
        "ended_reason": None,
    },
    "history": [
        {"ts": sys.argv[13], "event": "spawned", "detail": f"session_id={sys.argv[6]}"},
    ],
}
with open(path, "w", encoding="utf-8") as f:
    json.dump(data, f, indent=2)
    f.write("\n")
print(f"[pursue] wrote {path}")
PYEOF

echo "[pursue] goal '$ID' is active." >&2
echo "[pursue] worker session: $WORKER_TITLE (id: $SESSION_ID)" >&2
echo "[pursue] watch progress with:  agent-deck session output $WORKER_TITLE -q" >&2
echo "[pursue] advance the manager with:  python3 $SCRIPT_DIR/manager.py --verbose" >&2
