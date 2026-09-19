#!/usr/bin/env bash
# replace-keeps-order.eval.sh — end-to-end check that a replacement session
# can take the replaced session's position in its group through the CLI
# alone (`session set <id> order <n>`, `session show --json` .order/.pin).
#
# Replays the sequence a session-replace procedure runs:
#   1. read the old row's order and pin
#   2. free the title (rename the old row) and add the clone under it
#   3. set the clone's order to the old position, copy the pin
#   4. verify clone at p, old at p+1
#   5. remove the old row, verify the clone still at p
#
# Runs against a disposable HOME/AGENTDECK_PROFILE so it can't clobber the
# real user state. No tmux is started (`add` only registers rows).
#
# Exit 0 on pass, 1 on first failure. All output goes to stdout so the CI
# log and the release report can grep for PASS/FAIL lines.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BIN="${AGENT_DECK_BIN:-$REPO_ROOT/agent-deck}"

if [[ ! -x "$BIN" ]]; then
  echo "FAIL: binary not built at $BIN (set AGENT_DECK_BIN or run 'go build -o agent-deck ./cmd/agent-deck')" >&2
  exit 1
fi

SANDBOX="$(mktemp -d -t agent-deck-eval-order-XXXXXX)"
cleanup() {
  rm -rf "$SANDBOX" || true
}
trap cleanup EXIT

export HOME="$SANDBOX/home"
export AGENTDECK_PROFILE="eval_order"
mkdir -p "$HOME/proj"

pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1" >&2; exit 1; }

add_row() {
  "$BIN" add "$HOME/proj" -t "$1" -c claude --no-parent -g Personal --json 2>/dev/null \
    | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])'
}
show_field() {
  "$BIN" session show "$1" --json 2>/dev/null \
    | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("'"$2"'", ""))'
}
expect_order() {
  local got
  got=$(show_field "$1" order)
  if [[ "$got" == "$2" ]]; then
    pass "$3: order of $1 is $2"
  else
    fail "$3: order of $1 is '$got', expected $2"
  fi
}

# Group: brain first, a sibling below it. Both at raw sort_order 0, so the
# rowid decides, like every launch-appended row in a real deck.
OLD=$(add_row "Personal assistant")
SIB=$(add_row "Spadek po Tatusiu")
expect_order "$OLD" 0 "baseline"
expect_order "$SIB" 1 "baseline"

"$BIN" session set "$OLD" pin top -q >/dev/null 2>&1 || fail "set pin top on old row"
[[ "$(show_field "$OLD" pin)" == "top" ]] && pass "show --json reports pin top" || fail "show --json pin"

# Step 1: read the old position and pin.
P=$(show_field "$OLD" order)
PIN=$(show_field "$OLD" pin)

# Step 2: free the title, add the clone under it. Same title at the same
# path is refused while the old row exists, so the rename comes first.
"$BIN" session set "$OLD" title "Personal assistant (replaced)" -q >/dev/null 2>&1 || fail "rename old row"
CLONE=$(add_row "Personal assistant")
expect_order "$CLONE" 2 "clone appended last before placement"

# Step 3: place the clone.
"$BIN" session set "$CLONE" order "$P" -q >/dev/null 2>&1 || fail "set order $P on clone"
if [[ -n "$PIN" ]]; then
  "$BIN" session set "$CLONE" pin "$PIN" -q >/dev/null 2>&1 || fail "set pin $PIN on clone"
fi

# Step 4: clone at p, old right after it, sibling after both.
expect_order "$CLONE" "$P" "after placement"
expect_order "$OLD" $((P + 1)) "after placement"
expect_order "$SIB" 2 "after placement"
[[ "$(show_field "$CLONE" pin)" == "$PIN" ]] && pass "clone carries pin '$PIN'" || fail "clone pin"

# Step 5: retire the old row; nothing before the clone moves.
"$BIN" session remove "$OLD" --force -q >/dev/null 2>&1 || fail "remove old row"
expect_order "$CLONE" "$P" "after removal"
expect_order "$SIB" 1 "after removal"

# Bad values are refused and change nothing.
if "$BIN" session set -- "$CLONE" order -1 >/dev/null 2>&1; then
  fail "order -1 accepted"
fi
if "$BIN" session set "$CLONE" order x >/dev/null 2>&1; then
  fail "order x accepted"
fi
expect_order "$CLONE" "$P" "after refused values"
pass "negative and non-integer order refused"

echo "ALL PASS"
