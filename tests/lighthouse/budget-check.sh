#!/usr/bin/env bash
# Lighthouse CI local budget check.
# Runs lhci collect + assert against .lighthouserc.json.
# Requires: make build (Go binary), npx (Node.js).
#
# Usage:
#   ./tests/lighthouse/budget-check.sh
#
# Exit codes:
#   0 = all assertions passed
#   1 = one or more assertions failed (hard error or warn)
#   2 = setup failure (binary missing, server failed to start)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

BINARY="./build/agent-deck"
PORT=19999
SERVER_URL="http://127.0.0.1:${PORT}"
CONFIG=".lighthouserc.json"
LHCI_VERSION="0.15.1"
SERVER_PID=""

cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    echo "[budget-check] Stopping test server (PID $SERVER_PID)..."
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

# --- Pre-flight checks ---

if [ ! -f "$CONFIG" ]; then
  echo "ERROR: $CONFIG not found in repo root. Run from the repository root directory."
  exit 2
fi

if [ ! -x "$BINARY" ]; then
  echo "ERROR: $BINARY not found. Run 'make build' first."
  exit 2
fi

if ! command -v npx &>/dev/null; then
  echo "ERROR: npx not found. Install Node.js (>= 18) first."
  exit 2
fi

# --- Start test server ---

echo "[budget-check] Starting test server on port $PORT..."
AGENTDECK_PROFILE=_test "$BINARY" web --listen "127.0.0.1:${PORT}" --token test &
SERVER_PID=$!

echo "[budget-check] Waiting for /healthz (max 30s)..."
for i in $(seq 1 30); do
  if curl -sf "${SERVER_URL}/healthz" > /dev/null 2>&1; then
    echo "[budget-check] Server ready after ${i}s."
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "ERROR: Server did not become ready within 30 seconds."
    exit 2
  fi
  sleep 1
done

# --- Pre-warm ---

echo "[budget-check] Pre-warming server..."
curl -s "${SERVER_URL}/" > /dev/null
sleep 1

# --- Run Lighthouse CI ---

echo "[budget-check] Running lhci collect (${LHCI_VERSION})..."
npx "@lhci/cli@${LHCI_VERSION}" collect --config="$CONFIG"

echo "[budget-check] Running lhci assert..."
ASSERT_EXIT=0
npx "@lhci/cli@${LHCI_VERSION}" assert --config="$CONFIG" || ASSERT_EXIT=$?

if [ "$ASSERT_EXIT" -eq 0 ]; then
  echo "[budget-check] All assertions passed."
else
  echo "[budget-check] Assertions failed (exit code $ASSERT_EXIT)."
  echo "[budget-check] If thresholds need updating, run: ./tests/lighthouse/calibrate.sh"
fi

exit "$ASSERT_EXIT"
