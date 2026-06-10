#!/usr/bin/env bash
# fake-claude.sh — CI-safe stub used by scripts/verify-session-persistence.sh.
#
# Contract:
#   - Writes the full argv ("$@") as one line to $AGENT_DECK_VERIFY_ARGV_OUT
#     (default: /tmp/adeck-verify-argv.$PPID). One invocation appends one line.
#   - Then sleeps in a portable loop so the tmux pane stays alive while the
#     harness runs its assertions.
#   - Never mutates user state. Never needs network.
#
# Env:
#   AGENT_DECK_VERIFY_ARGV_OUT — tempfile path; default /tmp/adeck-verify-argv.$PPID
#
# The verify harness sets AGENT_DECK_VERIFY_USE_STUB=1 and prepends this
# directory to PATH, so the real agent-deck binary launches THIS script
# whenever it would otherwise launch `claude`.
set -euo pipefail

OUT="${AGENT_DECK_VERIFY_ARGV_OUT:-/tmp/adeck-verify-argv.$PPID}"
mkdir -p "$(dirname "$OUT")"
# One line per invocation, space-separated argv. Quote-preservation is not
# required here — the harness only greps for --resume / --session-id tokens.
printf '%s\n' "$*" >> "$OUT"

while :; do
  sleep 86400
done
