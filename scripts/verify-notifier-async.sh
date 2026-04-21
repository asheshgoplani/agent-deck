#!/usr/bin/env bash
# verify-notifier-async.sh — end-to-end proof that v1.7.45 drains the
# deferred-retry queue and delivers the [EVENT] banner to a real tmux pane.
#
# The "busy parent defers" logic is exhaustively covered by the unit tests
# in internal/session/transition_notifier_queue_test.go. This harness covers
# the complementary assertion that unit tests cannot: when the drain path
# fires, the tmux send-keys pipeline actually lands the banner in the parent's
# live pane, resolved through the real storage → notifier → SendSessionMessage
# → tmux chain.
#
# Assertions:
#   1. parent_session_id is persisted end-to-end through `agent-deck add
#      --parent` + `session set-parent` into the on-disk Instance record that
#      the notifier reads back at drain time.
#   2. A pre-seeded queue entry is drained by a single `notify-daemon --once`
#      invocation.
#   3. transition-notifier.log gains a delivery_result=sent row for the
#      previously-deferred event.
#   4. The queue file is empty after a successful drain.
#   5. The [EVENT] Child '…' banner text literally appears in the parent's
#      live tmux pane (confirming send-keys landed bytes where the operator
#      would see them).
#   6. notifier-missed.log stays empty on the happy path.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="${AGENT_DECK_BIN:-$REPO_ROOT/build/agent-deck}"

if ! command -v tmux >/dev/null; then
    echo "[SKIP] tmux not installed; skipping e2e harness." >&2
    exit 0
fi
if [[ ! -x "$BIN" ]]; then
    echo "[FAIL] agent-deck binary not found at $BIN; run 'make build' first." >&2
    exit 1
fi
if ! command -v python3 >/dev/null; then
    echo "[FAIL] python3 required for JSON field extraction." >&2
    exit 1
fi

RED='\033[31m'; GREEN='\033[32m'; YELLOW='\033[33m'; RESET='\033[0m'
pass() { printf "${GREEN}[PASS]${RESET} %s\n" "$*"; }
fail() { printf "${RED}[FAIL]${RESET} %s\n" "$*" >&2; FAILED=1; }
note() { printf "${YELLOW}[INFO]${RESET} %s\n" "$*"; }
log()  { printf '    %s\n' "$*"; }

FAILED=0

TMPDIR_E2E="$(mktemp -d -t agent-deck-notifier-e2e.XXXXXX)"
export HOME="$TMPDIR_E2E"
ADECK_DIR="$HOME/.agent-deck"
LOG="$ADECK_DIR/logs/transition-notifier.log"
QUEUE="$ADECK_DIR/runtime/transition-deferred-queue.json"
MISSED="$ADECK_DIR/logs/notifier-missed.log"
mkdir -p "$ADECK_DIR/logs" "$ADECK_DIR/runtime"

PROFILE="e2e"
GROUP="_notifier_e2e"
PARENT_DIR="$TMPDIR_E2E/parent"
CHILD_DIR="$TMPDIR_E2E/child"
mkdir -p "$PARENT_DIR" "$CHILD_DIR"

cleanup() {
    set +e
    note "Cleaning up e2e sessions in profile '$PROFILE'"
    for id in $("$BIN" -p "$PROFILE" list --json 2>/dev/null | python3 -c 'import json,sys; [print(x["id"]) for x in json.load(sys.stdin)]' 2>/dev/null); do
        "$BIN" -p "$PROFILE" session stop "$id" >/dev/null 2>&1 || true
        "$BIN" -p "$PROFILE" remove "$id" >/dev/null 2>&1 || true
    done
    rm -rf "$TMPDIR_E2E"
}
trap cleanup EXIT

note "HOME=$HOME (isolated)"
note "agent-deck dir: $ADECK_DIR"
note "agent-deck binary: $BIN"
note "agent-deck version: $("$BIN" version 2>&1 | head -1)"

# --- setup ---------------------------------------------------------------

note "Adding parent + child sessions"
"$BIN" -p "$PROFILE" add -t parent-e2e -g "$GROUP" -c shell "$PARENT_DIR" >/dev/null
"$BIN" -p "$PROFILE" add -t child-e2e -g "$GROUP" -c shell "$CHILD_DIR" >/dev/null

PARENT_ID="$("$BIN" -p "$PROFILE" list --json | python3 -c '
import json,sys
for s in json.load(sys.stdin):
  if s["title"] == "parent-e2e":
    print(s["id"]); break')"
CHILD_ID="$("$BIN" -p "$PROFILE" list --json | python3 -c '
import json,sys
for s in json.load(sys.stdin):
  if s["title"] == "child-e2e":
    print(s["id"]); break')"

if [[ -z "$PARENT_ID" || -z "$CHILD_ID" ]]; then
    fail "could not resolve IDs (parent=$PARENT_ID child=$CHILD_ID)"
    exit 1
fi
log "parent id: $PARENT_ID"
log "child  id: $CHILD_ID"

"$BIN" -p "$PROFILE" session set-parent "$CHILD_ID" "$PARENT_ID" >/dev/null

PARENT_LINK="$("$BIN" -p "$PROFILE" session show "$CHILD_ID" --json 2>/dev/null | \
    grep -oE '"parent_session_id"[[:space:]]*:[[:space:]]*"[^"]*"' || true)"
if [[ "$PARENT_LINK" != *"$PARENT_ID"* ]]; then
    fail "parent_session_id not persisted; raw: $PARENT_LINK"
    exit 1
fi
pass "parent_session_id persisted on the child ($PARENT_LINK)"

note "Starting parent tmux pane (target of the injection)"
"$BIN" -p "$PROFILE" session start "$PARENT_ID" >/dev/null
sleep 2

TMUX_PARENT="$("$BIN" -p "$PROFILE" session show "$PARENT_ID" --json 2>/dev/null | python3 -c '
import json,sys
d = json.load(sys.stdin)
sess = d.get("tmux_session") or ""
if isinstance(sess, dict):
    sess = sess.get("name","")
print(sess)')"
if [[ -z "$TMUX_PARENT" ]]; then
    fail "parent tmux session did not come up"
    exit 1
fi
log "parent tmux session: $TMUX_PARENT"

# --- seed the deferred queue --------------------------------------------

NOW_RFC=$(date +"%Y-%m-%dT%H:%M:%S%:z")

note "Seeding transition-deferred-queue.json with a running→waiting event"
python3 - "$QUEUE" "$PARENT_ID" "$CHILD_ID" "$PROFILE" "$NOW_RFC" <<'PY'
import json, sys
path, parent_id, child_id, profile, now_rfc = sys.argv[1:]
entry = {
  "event": {
    "child_session_id": child_id,
    "child_title": "child-e2e",
    "profile": profile,
    "from_status": "running",
    "to_status": "waiting",
    "timestamp": now_rfc,
    "target_session_id": parent_id,
    "target_kind": "parent",
    "delivery_result": "deferred_target_busy",
  },
  "first_deferred_at": now_rfc,
  "attempts": 0,
}
with open(path, "w") as f:
  json.dump({"entries": [entry]}, f, indent=2)
PY
log "seeded queue:"
cat "$QUEUE" | sed 's/^/        /'

# --- drive the drain -----------------------------------------------------

note "Running notify-daemon --once to drain the queue"
"$BIN" -p "$PROFILE" notify-daemon --once >/dev/null
sleep 2

if [[ ! -f "$LOG" ]]; then
    fail "transition-notifier.log was not created — drain path did not fire"
    exit 1
fi
note "Delivery log after drain:"
tail -5 "$LOG" | sed 's/^/        /'

if grep -q "\"child_session_id\":\"$CHILD_ID\".*\"delivery_result\":\"sent\"" "$LOG"; then
    pass "drain dispatched the deferred transition: delivery_result=sent"
else
    fail "no sent entry for $CHILD_ID in delivery log"
fi

if [[ -f "$QUEUE" ]] && grep -q "\"child_session_id\": \"$CHILD_ID\"" "$QUEUE"; then
    fail "queue still contains $CHILD_ID after successful drain"
else
    pass "queue drained clean after successful dispatch"
fi

# --- confirm [EVENT] text landed in the parent's live tmux pane ----------

sleep 1
note "Capturing parent tmux pane (full scrollback)"
# -S -200 grabs up to 200 lines of scrollback so the banner is visible even
# if the pane's default visible window has scrolled past it.
PANE_CAPTURE="$(tmux capture-pane -pS -200 -t "$TMUX_PARENT" 2>&1 || true)"
EVENT_LINES="$(echo "$PANE_CAPTURE" | grep -n '\[EVENT\]' || true)"
if [[ -n "$EVENT_LINES" ]]; then
    echo "$EVENT_LINES" | sed 's/^/        /'
    pass "[EVENT] banner reached the parent's tmux pane — real send-keys delivery"
else
    echo "$PANE_CAPTURE" | tail -15 | sed 's/^/        /'
    fail "parent pane never received the [EVENT] banner"
fi

# --- missed.log must stay empty on the happy path ------------------------

if [[ -f "$MISSED" ]] && [[ -s "$MISSED" ]]; then
    note "notifier-missed.log contents:"
    cat "$MISSED" | sed 's/^/        /'
    fail "notifier-missed.log unexpectedly has entries"
else
    pass "notifier-missed.log is empty — no timeouts or busy-slot misses"
fi

echo
if (( FAILED )); then
    printf "${RED}HARNESS FAILED${RESET}\n"
    exit 1
fi
printf "${GREEN}HARNESS PASSED${RESET} — v1.7.45 queue drain delivers [EVENT] through a real tmux pane.\n"
