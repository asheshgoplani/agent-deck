#!/usr/bin/env bash
# Edge case tests for the weekly-alert-format.test.sh validation script.
# Runs several inputs and verifies expected pass/fail outcomes.
# Exit 0 if all edge cases produce expected results, 1 otherwise.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VALIDATOR="$SCRIPT_DIR/weekly-alert-format.test.sh"
ERRORS=0
TESTS=0
TODAY=$(date +%Y-%m-%d)

expect_fail() {
  local desc="$1"
  shift
  TESTS=$((TESTS + 1))
  if printf '%s' "$@" | "$VALIDATOR" > /dev/null 2>&1; then
    echo "FAIL: Expected failure but got success: $desc"
    ERRORS=$((ERRORS + 1))
  else
    echo "PASS: Correctly failed: $desc"
  fi
}

expect_pass() {
  local desc="$1"
  shift
  TESTS=$((TESTS + 1))
  if printf '%s' "$@" | "$VALIDATOR" > /dev/null 2>&1; then
    echo "PASS: Correctly passed: $desc"
  else
    echo "FAIL: Expected success but got failure: $desc"
    ERRORS=$((ERRORS + 1))
  fi
}

# Build a valid body for reuse
VALID_BODY="Weekly regression check: 1 failure(s) detected [${TODAY}]
## Summary

- **Visual regression:** FAIL
- **Lighthouse CI:** PASS
- **Total failures:** 1
- **Date:** ${TODAY}

## Visual Regression Results

- :x: Main screen baseline

## Lighthouse CI Results

All assertions passed.

## Artifacts

- [Test artifacts](https://github.com/asheshgoplani/agent-deck/actions/runs/99999/artifacts)

## Run Details

- **Workflow run:** https://github.com/asheshgoplani/agent-deck/actions/runs/99999
- **Branch:** main
- **Commit:** abc1234 — chore: test

Labels: regression, automated"

echo "=== Edge Case Tests for weekly-alert-format.test.sh ==="
echo ""

# --- Edge case 1: Empty input ---
expect_fail "Empty input" ""

# --- Edge case 2: Title only, no body ---
expect_fail "Title only" "Weekly regression check: 1 failure(s) detected [${TODAY}]"

# --- Edge case 3: Missing Visual Regression section ---
MISSING_VISUAL="Weekly regression check: 1 failure(s) detected [${TODAY}]
## Summary

- **Visual regression:** FAIL
- **Lighthouse CI:** PASS

## Lighthouse CI Results

All passed.

## Artifacts

- [Test artifacts](https://github.com/asheshgoplani/agent-deck/actions/runs/99999/artifacts)

## Run Details

- **Workflow run:** https://github.com/asheshgoplani/agent-deck/actions/runs/99999
- **Branch:** main
- **Commit:** abc1234 — chore: test

Labels: regression, automated"
expect_fail "Missing Visual Regression Results section" "$MISSING_VISUAL"

# --- Edge case 4: Wrong title format ---
WRONG_TITLE="Weekly check: 1 failures [${TODAY}]
## Summary

- **Visual regression:** FAIL
- **Lighthouse CI:** PASS

## Visual Regression Results

Details here.

## Lighthouse CI Results

Details here.

## Artifacts

- [Test artifacts](https://github.com/asheshgoplani/agent-deck/actions/runs/99999/artifacts)

## Run Details

- **Workflow run:** https://github.com/asheshgoplani/agent-deck/actions/runs/99999
- **Branch:** main
- **Commit:** abc1234 — chore: test

Labels: regression, automated"
expect_fail "Wrong title format" "$WRONG_TITLE"

# --- Edge case 5: Both PASS statuses (valid structure, no failures) ---
BOTH_PASS="Weekly regression check: 0 failure(s) detected [${TODAY}]
## Summary

- **Visual regression:** PASS
- **Lighthouse CI:** PASS
- **Total failures:** 0
- **Date:** ${TODAY}

## Visual Regression Results

All baselines match.

## Lighthouse CI Results

All assertions passed.

## Artifacts

- [Test artifacts](https://github.com/asheshgoplani/agent-deck/actions/runs/99999/artifacts)

## Run Details

- **Workflow run:** https://github.com/asheshgoplani/agent-deck/actions/runs/99999
- **Branch:** main
- **Commit:** abc1234 — chore: test

Labels: regression, automated"
expect_pass "Both PASS statuses (valid structure)" "$BOTH_PASS"

# --- Edge case 6: Valid body (the standard case) ---
expect_pass "Standard valid body with failures" "$VALID_BODY"

# --- Edge case 7: Large body with 55 test results ---
LARGE_VISUAL=""
for i in $(seq 1 55); do
  LARGE_VISUAL="${LARGE_VISUAL}
- :x: Test case ${i}: baseline mismatch"
done

LARGE_BODY="Weekly regression check: 2 failure(s) detected [${TODAY}]
## Summary

- **Visual regression:** FAIL
- **Lighthouse CI:** FAIL
- **Total failures:** 2
- **Date:** ${TODAY}

## Visual Regression Results
${LARGE_VISUAL}

## Lighthouse CI Results

\`\`\`
FAIL: first-contentful-paint score 0.3
FAIL: total-blocking-time score 0.2
\`\`\`

## Artifacts

- [Test artifacts](https://github.com/asheshgoplani/agent-deck/actions/runs/99999/artifacts)

## Run Details

- **Workflow run:** https://github.com/asheshgoplani/agent-deck/actions/runs/99999
- **Branch:** main
- **Commit:** abc1234 — chore: test

Labels: regression, automated"
expect_pass "Large body with 55 test results" "$LARGE_BODY"

# --- Final verdict ---
echo ""
echo "=== Results: $((TESTS - ERRORS))/${TESTS} passed ==="
if [[ $ERRORS -eq 0 ]]; then
  echo "ALL EDGE CASE TESTS PASSED"
  exit 0
else
  echo "FAILED: $ERRORS edge case(s) failed" >&2
  exit 1
fi
