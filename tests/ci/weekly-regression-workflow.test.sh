#!/usr/bin/env bash
# Guards the visual-regression step's JSON output and exit-code contract.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKFLOW="$ROOT/.github/workflows/weekly-regression.yml"
ERRORS=0

fail() {
  echo "FAIL: $1" >&2
  ERRORS=$((ERRORS + 1))
}

pass() {
  echo "PASS: $1"
}

VISUAL_STEP=$(awk '
  /- name: Run visual regression tests/ { in_step = 1 }
  in_step { print }
  in_step && /- name: Regenerate visual baselines/ { exit }
' "$WORKFLOW")

if grep -Fq 'PLAYWRIGHT_JSON_OUTPUT_DIR: /tmp' <<<"$VISUAL_STEP" &&
  grep -Fq 'PLAYWRIGHT_JSON_OUTPUT_NAME: visual-results.json' <<<"$VISUAL_STEP"; then
  pass "Playwright writes JSON directly to the artifact path"
else
  fail "visual step must write Playwright JSON directly to /tmp/visual-results.json"
fi

if grep -Fq -- '--reporter=json' <<<"$VISUAL_STEP"; then
  pass "visual step selects Playwright's JSON reporter"
else
  fail "visual step must use Playwright's JSON reporter"
fi

if grep -Fq 'tee /tmp/visual-results.json' <<<"$VISUAL_STEP"; then
  fail "tee pollutes the JSON report with stderr"
else
  pass "JSON report does not pass through tee"
fi

RUN_COUNT=$(grep -cF 'npx playwright test --config=pw-visual-regression.config.ts' <<<"$VISUAL_STEP" || true)
if [[ "$RUN_COUNT" == "1" ]]; then
  pass "visual suite runs exactly once"
else
  fail "visual suite must run once, got $RUN_COUNT invocations"
fi

if grep -Fq '|| true' <<<"$VISUAL_STEP"; then
  fail "visual command must preserve its nonzero exit for the step outcome"
else
  pass "visual command preserves its exit code"
fi

if [[ "$ERRORS" -ne 0 ]]; then
  exit 1
fi
