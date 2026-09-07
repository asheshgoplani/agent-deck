#!/usr/bin/env bash
# Deliver one trigger honoring mode. In "log" mode nothing leaves the machine.
# Override GHW_SEND_CMD to capture sends in tests.
set -euo pipefail
BIN="$(cd "$(dirname "$0")" && pwd)"
eval "$(python3 "$BIN/watcherlib.py" --shell)"
MSG="$1"
if [ "$GHW_MODE" = "log" ]; then
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) [send] mode=log would send: $MSG" >> "$GHW_LOG"
  exit 0
fi
if [ -n "${GHW_SEND_CMD:-}" ]; then
  exec bash -c "$GHW_SEND_CMD \"\$1\"" _ "$MSG"
fi
exec agent-deck -p "$GHW_PROFILE" session send "$GHW_CONDUCTOR" "$MSG" --no-wait
