#!/usr/bin/env bash
#
# capability-e2e.sh: run the capability-level E2E suite, emit a JSON manifest,
# and regenerate the HTML dashboard from it.
#
# This is the single entry point referenced by the capability-e2e strategy
# (docs/testing/2026-05-26-capability-e2e-strategy.md section 4). It is safe to
# run standalone today; wiring it into release.yml is a one-line add later
# (Wave 3).
#
# Usage:
#   scripts/capability-e2e.sh            # Tier F fast gate (default)
#   scripts/capability-e2e.sh --nightly  # also run Tier N (real agents, creds)
#
# Exit status: non-zero if any Tier F capability failed (so it can gate a
# release) or if the test runner itself errored. Tier N failures are reported
# but do not fail the fast gate.
set -euo pipefail

cd "$(dirname "$0")/.."

TAGS="capability_e2e"
if [[ "${1:-}" == "--nightly" ]]; then
  TAGS="capability_e2e capability_nightly"
  echo "capability-e2e: including Tier N (nightly) capabilities"
fi

# Isolated-socket discipline (belt to the harness suspenders): refuse to run
# inside a live tmux session unless the isolation marker is set. The harness
# unsets TMUX per-process and forces a per-test socket, but a stray run from a
# real conductor pane must never risk the user's real tmux server.
if [[ -n "${TMUX:-}" && -z "${AGENT_DECK_TEST_ISOLATED:-}" ]]; then
  echo "capability-e2e: refusing to run with TMUX set and AGENT_DECK_TEST_ISOLATED unset." >&2
  echo "  Run from outside tmux, or export AGENT_DECK_TEST_ISOLATED=1 if you know the run is isolated." >&2
  exit 2
fi

MANIFEST="docs/status/capability-e2e-manifest.json"
DASHBOARD="docs/status/capability-dashboard.html"
RESULTS="$(mktemp -t cap-e2e-results.XXXXXX.json)"
trap 'rm -f "$RESULTS"' EXIT

echo "capability-e2e: running suite (tags: $TAGS)"
# Capture go test -json regardless of pass/fail; the manifest must reflect both.
TEST_STATUS=0
go test -tags "$TAGS" -race -count=1 -timeout 8m -json ./tests/capability/... >"$RESULTS" || TEST_STATUS=$?

echo "capability-e2e: rendering manifest + dashboard"
# capability-report exits non-zero if a Tier F capability failed.
REPORT_STATUS=0
go run ./tools/capability-report \
  --json-input "$RESULTS" \
  --manifest "$MANIFEST" \
  --dashboard "$DASHBOARD" || REPORT_STATUS=$?

if [[ "$TEST_STATUS" -ne 0 ]]; then
  echo "capability-e2e: test runner exited $TEST_STATUS" >&2
fi
if [[ "$REPORT_STATUS" -ne 0 ]]; then
  echo "capability-e2e: a Tier F capability regressed; failing the gate" >&2
  exit 1
fi
if [[ "$TEST_STATUS" -ne 0 ]]; then
  exit "$TEST_STATUS"
fi
echo "capability-e2e: all fast-gate capabilities green"
