#!/usr/bin/env bash
# title-lock.eval.sh — end-to-end verification for #697 (v1.7.52).
#
# What this covers (that unit tests can't):
#   1. Real CLI binary accepts `--title-lock` on `agent-deck add`.
#   2. Title-lock survives SQLite round-trip via `session show`.
#   3. The hook-handler path (`agent-deck hook-handler`) is a no-op when
#      TitleLocked=true, even with a matching Claude session-name file.
#   4. Default (unlocked) behaviour is unchanged: the same hook event DOES
#      rewrite the title.
#   5. `session set-title-lock off` re-enables sync.
#
# Runs against a disposable HOME/AGENTDECK_PROFILE so it can't clobber the
# real user state. Designed for the smoke tier (per-PR).
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

SANDBOX="$(mktemp -d -t agent-deck-eval-697-XXXXXX)"
cleanup() {
  rm -rf "$SANDBOX" || true
}
trap cleanup EXIT

export HOME="$SANDBOX/home"
export AGENTDECK_PROFILE="eval_697"
mkdir -p "$HOME/.claude/sessions" "$HOME/proj"

PASSES=0
FAILS=0
pass() { echo "PASS: $1"; PASSES=$((PASSES + 1)); }
fail() { echo "FAIL: $1" >&2; FAILS=$((FAILS + 1)); }

# ─────────────────────────────────────────────────────────────────────────────
# Case A: --title-lock on `add` persists TitleLocked=true and blocks sync
# ─────────────────────────────────────────────────────────────────────────────
"$BIN" add "$HOME/proj" --quick -c claude --title-lock 2>&1 | head -20

INSTANCE_ID=$("$BIN" list --json 2>/dev/null | python3 -c 'import sys,json; d=json.load(sys.stdin); print((d[0] if d else {}).get("id",""))' || true)
if [[ -z "$INSTANCE_ID" ]]; then
  fail "could not resolve instance id after add --title-lock"
  exit 1
fi

# Force the title to a conductor-style value
"$BIN" session set "$INSTANCE_ID" title "SCRUM-351" -q >/dev/null 2>&1 || {
  fail "session set title failed"; exit 1;
}

# Seed a Claude sessions meta file that would trigger a rename
SID="eval-697-sid"
cat >"$HOME/.claude/sessions/99999.json" <<JSON
{"pid":99999,"sessionId":"$SID","cwd":"$HOME/proj","name":"auto-refresh-task-lists"}
JSON

# Fire the hook-handler as Claude would: stdin payload + instance-id env
PAYLOAD=$(printf '{"session_id":"%s","hook_event_name":"Stop"}' "$SID")
printf '%s' "$PAYLOAD" | env AGENTDECK_INSTANCE_ID="$INSTANCE_ID" "$BIN" hook-handler >/dev/null 2>&1 || true

TITLE_AFTER=$("$BIN" session show "$INSTANCE_ID" --json 2>/dev/null | \
  python3 -c 'import sys,json; d=json.load(sys.stdin); print((d.get("data") or d).get("title",""))')

if [[ "$TITLE_AFTER" == "SCRUM-351" ]]; then
  pass "title-lock: Claude rename blocked (title stayed 'SCRUM-351')"
else
  fail "title-lock: title leaked to '$TITLE_AFTER' (expected 'SCRUM-351')"
fi

# ─────────────────────────────────────────────────────────────────────────────
# Case B: turning title-lock OFF allows the next hook to apply the rename
# ─────────────────────────────────────────────────────────────────────────────
"$BIN" session set-title-lock "$INSTANCE_ID" off -q >/dev/null 2>&1 || {
  fail "session set-title-lock off failed"; exit 1;
}

printf '%s' "$PAYLOAD" | env AGENTDECK_INSTANCE_ID="$INSTANCE_ID" "$BIN" hook-handler >/dev/null 2>&1 || true

TITLE_AFTER2=$("$BIN" session show "$INSTANCE_ID" --json 2>/dev/null | \
  python3 -c 'import sys,json; d=json.load(sys.stdin); print((d.get("data") or d).get("title",""))')

if [[ "$TITLE_AFTER2" == "auto-refresh-task-lists" ]]; then
  pass "title-lock off: Claude rename applied ('$TITLE_AFTER2')"
else
  fail "title-lock off: expected 'auto-refresh-task-lists', got '$TITLE_AFTER2'"
fi

# ─────────────────────────────────────────────────────────────────────────────
# Case C: turning title-lock back ON freezes the current title
# ─────────────────────────────────────────────────────────────────────────────
"$BIN" session set "$INSTANCE_ID" title "SCRUM-351" -q >/dev/null 2>&1
"$BIN" session set-title-lock "$INSTANCE_ID" on -q >/dev/null 2>&1

# Swap the Claude metadata name again
cat >"$HOME/.claude/sessions/99999.json" <<JSON
{"pid":99999,"sessionId":"$SID","cwd":"$HOME/proj","name":"something-completely-different"}
JSON

printf '%s' "$PAYLOAD" | env AGENTDECK_INSTANCE_ID="$INSTANCE_ID" "$BIN" hook-handler >/dev/null 2>&1 || true

TITLE_AFTER3=$("$BIN" session show "$INSTANCE_ID" --json 2>/dev/null | \
  python3 -c 'import sys,json; d=json.load(sys.stdin); print((d.get("data") or d).get("title",""))')

if [[ "$TITLE_AFTER3" == "SCRUM-351" ]]; then
  pass "title-lock re-on: subsequent Claude rename blocked"
else
  fail "title-lock re-on: title leaked to '$TITLE_AFTER3' (expected 'SCRUM-351')"
fi

echo ""
echo "Summary: $PASSES passed, $FAILS failed"
if [[ $FAILS -eq 0 ]]; then
  echo "ALL CHECKS PASSED"
  exit 0
else
  echo "FAILED"
  exit 1
fi
