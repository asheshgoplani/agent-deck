#!/usr/bin/env bash
# fake-tool.sh — Generic tool stub for verify-command-override.sh.
#
# Symlinked as hermes, gemini, opencode, codex, copilot, claude.
# Logs the full invocation (basename + args) to $AGENT_DECK_VERIFY_ARGV_OUT,
# then sleeps so the tmux pane stays alive for assertions.
#
# Contract:
#   - Appends one line per invocation: "<basename> <args...>"
#   - Never mutates user state. Never needs network.
set -euo pipefail

OUT="${AGENT_DECK_VERIFY_ARGV_OUT:-/tmp/adeck-verify-argv.$$}"
mkdir -p "$(dirname "$OUT")"
printf '%s %s\n' "$(basename "$0")" "$*" >> "$OUT"

exec sleep infinity
