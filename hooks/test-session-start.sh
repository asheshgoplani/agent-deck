#!/usr/bin/env bash
# Tests for the SessionStart hook's routing and output shape.
#
# Covers everything that needs no tmux server: syntax, exit status, which
# preamble is chosen, and the two JSON envelope shapes. The tmux-backed cases
# (a real parented child, a real unparented session) stay manual — they need a
# live agent-deck and are documented in the plan's T11 section.
#
# Run:  bash hooks/test-session-start.sh

set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
HOOK="$HERE/session-start"
fails=0

ok()   { printf 'ok   — %s\n' "$1"; }
fail() { printf 'FAIL — %s\n' "$1"; fails=$((fails + 1)); }

check() { # check <description> <condition-exit-status>
  if [ "$2" -eq 0 ]; then ok "$1"; else fail "$1"; fi
}

# 1. The script is executable and syntactically valid.
[ -x "$HOOK" ]; check "hook is executable" $?
bash -n "$HOOK" >/dev/null 2>&1; check "hook parses (bash -n)" $?

# 2. hooks.json is valid JSON and points at this hook.
if command -v python3 >/dev/null 2>&1; then
  python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$HERE/hooks.json" >/dev/null 2>&1
  check "hooks.json is valid JSON" $?
  python3 - "$HERE/hooks.json" <<'PY' >/dev/null 2>&1
import json, sys
d = json.load(open(sys.argv[1]))
entries = d["hooks"]["SessionStart"]
assert len(entries) == 1, entries
m = entries[0]["matcher"].split("|")
# A restarted child fires SessionStart with source `resume`; without it the
# executor preamble silently disappears for the rest of that child's life.
for src in ("startup", "clear", "compact", "resume"):
    assert src in m, f"matcher missing {src}: {m}"
assert entries[0]["hooks"][0]["command"].find("hooks/session-start") != -1
PY
  check "hooks.json matcher covers startup|clear|compact|resume" $?
else
  printf 'skip — python3 unavailable, hooks.json checks skipped\n'
fi

# 3. Both preambles exist and are non-empty.
[ -s "$HERE/preamble-child.md" ];       check "preamble-child.md is non-empty" $?
[ -s "$HERE/preamble-interactive.md" ]; check "preamble-interactive.md is non-empty" $?

# 4. Outside tmux the hook exits 0 and degrades to the interactive preamble.
#    (TMUX unset is the "not under agent-deck at all" case.)
out="$(env -u TMUX -u CLAUDE_PLUGIN_ROOT "$HOOK" 2>/dev/null)"; rc=$?
[ "$rc" -eq 0 ]; check "exits 0 outside tmux" $?
printf '%s' "$out" | grep -q 'dispatched executor'; rc_child=$?
[ "$rc_child" -ne 0 ]; check "outside tmux does NOT inject the child preamble" $?
[ -n "$out" ]; check "outside tmux still injects something" $?

# 5. Envelope shape switches on CLAUDE_PLUGIN_ROOT.
plain="$(env -u TMUX -u CLAUDE_PLUGIN_ROOT "$HOOK" 2>/dev/null)"
printf '%s' "$plain" | grep -q '"additionalContext"'; rc_a=$?
printf '%s' "$plain" | grep -q 'hookSpecificOutput';  rc_b=$?
{ [ "$rc_a" -eq 0 ] && [ "$rc_b" -ne 0 ]; }
check "without CLAUDE_PLUGIN_ROOT: bare additionalContext only" $?

claude="$(env -u TMUX CLAUDE_PLUGIN_ROOT=/tmp/x "$HOOK" 2>/dev/null)"
printf '%s' "$claude" | grep -q '"hookEventName": "SessionStart"'
check "with CLAUDE_PLUGIN_ROOT: hookSpecificOutput envelope" $?

# 6. Output is valid JSON in both shapes (the preambles contain quotes,
#    backticks and newlines — this is what escape_for_json exists for).
if command -v python3 >/dev/null 2>&1; then
  printf '%s' "$plain"  | python3 -c 'import json,sys; json.load(sys.stdin)' >/dev/null 2>&1
  check "bare envelope is valid JSON" $?
  printf '%s' "$claude" | python3 -c 'import json,sys; json.load(sys.stdin)' >/dev/null 2>&1
  check "Claude envelope is valid JSON" $?
fi

# 7. A missing preamble file must not fail the session.
tmp="$(mktemp -d)"
cp "$HOOK" "$tmp/session-start"
( env -u TMUX -u CLAUDE_PLUGIN_ROOT "$tmp/session-start" >/dev/null 2>&1 )
check "exits 0 when preamble files are absent" $?
rm -rf "$tmp"

printf '\n'
if [ "$fails" -eq 0 ]; then
  printf 'ALL PASS\n'; exit 0
else
  printf '%d FAILED\n' "$fails"; exit 1
fi
