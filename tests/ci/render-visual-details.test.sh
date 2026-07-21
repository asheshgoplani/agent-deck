#!/usr/bin/env bash
# render-visual-details.test.sh — Tests for .github/scripts/render-visual-details.js,
# the renderer behind the "Visual Regression Results" section of the weekly
# regression issue.
#
# Regression guard for issue #1674: Playwright's JSON reporter nests specs in
# suites-inside-suites, and the old top-level-only walk rendered "No detailed
# results available." for a real failure. Also verifies that missing/empty/
# unparseable reports come back as noData (NO DATA, not FAIL).
#
# Exit 0 on pass, 1 on any failure. No dependencies beyond node.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RENDERER="$SCRIPT_DIR/../../.github/scripts/render-visual-details.js"
ERRORS=0
TESTS=0

check() {
  local desc="$1" expected="$2" actual="$3"
  TESTS=$((TESTS + 1))
  if [[ "$actual" == "$expected" ]]; then
    echo "PASS: $desc"
  else
    echo "FAIL: $desc" >&2
    echo "  expected: $expected" >&2
    echo "  actual:   $actual" >&2
    ERRORS=$((ERRORS + 1))
  fi
}

# run <raw-json-via-stdin> -> prints "noData specCount failedCount|details"
run_renderer() {
  node -e '
    const { renderVisualDetails } = require(process.argv[1]);
    let raw = require("fs").readFileSync(0, "utf8");
    if (raw === "@@MISSING@@\n" || raw === "@@MISSING@@") raw = null;
    const r = renderVisualDetails(raw);
    process.stdout.write(`${r.noData} ${r.specCount} ${r.failedCount}|${r.details}`);
  ' "$RENDERER"
}

# --- Case 1: real Playwright shape (file suite -> describe suite -> specs) ---
NESTED_JSON='{
  "suites": [{
    "title": "main-views.spec.ts", "specs": [],
    "suites": [{
      "title": "Main views visual baselines",
      "specs": [
        {"title": "fleet empty state", "ok": true, "tests": []},
        {"title": "settings drawer — desktop dark 1280x800", "ok": false,
         "tests": [{"results": [{"status": "failed",
           "errors": [{"message": "Error: expect(page).toHaveScreenshot failed\n726 pixels differ"}]}]}]}
      ]
    }]
  }]
}'
OUT=$(printf '%s' "$NESTED_JSON" | run_renderer)
META="${OUT%%|*}"; DETAILS="${OUT#*|}"
check "nested suites: counts" "false 2 1" "$META"
case "$DETAILS" in
  *":x: settings drawer"*) echo "PASS: nested suites: failed spec listed" ;;
  *) echo "FAIL: nested suites: failed spec missing from details: $DETAILS" >&2; ERRORS=$((ERRORS + 1)) ;;
esac
TESTS=$((TESTS + 1))
case "$DETAILS" in
  *"Error: expect(page).toHaveScreenshot failed"*) echo "PASS: nested suites: first error line included" ;;
  *) echo "FAIL: nested suites: error line missing: $DETAILS" >&2; ERRORS=$((ERRORS + 1)) ;;
esac
TESTS=$((TESTS + 1))

# --- Case 2: zero specs anywhere -> noData ---
OUT=$(printf '{"suites": [{"title": "x.spec.ts", "specs": [], "suites": []}]}' | run_renderer)
check "empty suites -> noData" "true 0 0" "${OUT%%|*}"

# --- Case 3: unparseable report -> noData ---
OUT=$(printf 'npm warn something\nnot json at all' | run_renderer)
check "garbage input -> noData" "true 0 0" "${OUT%%|*}"

# --- Case 4: missing report (null) -> noData ---
OUT=$(printf '@@MISSING@@' | run_renderer)
check "missing report -> noData" "true 0 0" "${OUT%%|*}"

# --- Case 5: all specs passing still renders a list (not noData) ---
OUT=$(printf '{"suites":[{"suites":[{"specs":[{"title":"a","ok":true,"tests":[]}]}]}]}' | run_renderer)
check "all-pass report -> data with 0 failures" "false 1 0" "${OUT%%|*}"

echo ""
if [[ $ERRORS -eq 0 ]]; then
  echo "All $TESTS render-visual-details tests passed."
  exit 0
else
  echo "$ERRORS of $TESTS render-visual-details tests failed." >&2
  exit 1
fi
