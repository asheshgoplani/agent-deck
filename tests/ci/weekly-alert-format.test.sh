#!/usr/bin/env bash
# weekly-alert-format.test.sh — Validates the weekly regression issue body format.
# Usage:
#   ./weekly-alert-format.test.sh < issue-body.txt
#   ./weekly-alert-format.test.sh /path/to/issue-body.txt
#
# Exit 0 on pass, 1 on any validation failure.
# Designed to be called by the weekly-regression workflow as a sanity gate
# before posting the issue to GitHub.

set -euo pipefail

ERRORS=0

fail() {
  echo "FAIL: $1" >&2
  ERRORS=$((ERRORS + 1))
}

pass() {
  echo "PASS: $1"
}

# Read input from file argument or stdin
if [[ $# -ge 1 && -f "$1" ]]; then
  INPUT=$(cat "$1")
else
  INPUT=$(cat)
fi

if [[ -z "$INPUT" ]]; then
  echo "FAIL: Empty input — no issue body provided" >&2
  exit 1
fi

# Extract first line as title, rest as body
TITLE=$(echo "$INPUT" | head -1)
BODY=$(echo "$INPUT" | tail -n +2)

# --- Check 1: Title format ---
if echo "$TITLE" | grep -qP '^Weekly regression check: [0-9]+ failure\(s\) detected \[[0-9]{4}-[0-9]{2}-[0-9]{2}\]$'; then
  pass "Title format matches expected pattern"
else
  fail "Title does not match pattern: 'Weekly regression check: N failure(s) detected [YYYY-MM-DD]'"
  fail "  Got: '$TITLE'"
fi

# --- Check 2: Required section headings ---
REQUIRED_SECTIONS=(
  "## Summary"
  "## Visual Regression Results"
  "## Lighthouse CI Results"
  "## Artifacts"
  "## Run Details"
)

for section in "${REQUIRED_SECTIONS[@]}"; do
  if echo "$BODY" | grep -qF "$section"; then
    pass "Section found: $section"
  else
    fail "Missing required section: $section"
  fi
done

# --- Check 3: Summary section content ---
if echo "$BODY" | grep -qP -- '- \*\*Visual regression:\*\* (PASS|FAIL)'; then
  pass "Summary contains visual regression status line"
else
  fail "Summary missing '- **Visual regression:** (PASS|FAIL)' line"
fi

if echo "$BODY" | grep -qP -- '- \*\*Lighthouse CI:\*\* (PASS|FAIL)'; then
  pass "Summary contains Lighthouse CI status line"
else
  fail "Summary missing '- **Lighthouse CI:** (PASS|FAIL)' line"
fi

# --- Check 4: Artifacts section content ---
if echo "$BODY" | grep -qE '(https://github\.com/.*/actions/runs/[0-9]+/artifacts|\$ARTIFACTS_URL|artifacts/[0-9]+)'; then
  pass "Artifacts section contains artifact URL or placeholder"
else
  fail "Artifacts section missing GitHub Actions artifact URL or \$ARTIFACTS_URL placeholder"
fi

# --- Check 5: Run Details content ---
for field in "Workflow run:" "Branch:" "Commit:"; do
  if echo "$BODY" | grep -qF "$field"; then
    pass "Run Details contains: $field"
  else
    fail "Run Details missing: $field"
  fi
done

# --- Check 6: Labels line ---
if echo "$BODY" | grep -qF "Labels: regression, automated"; then
  pass "Labels line present"
else
  fail "Missing 'Labels: regression, automated' line"
fi

# --- Final verdict ---
echo ""
if [[ $ERRORS -eq 0 ]]; then
  echo "ALL CHECKS PASSED"
  exit 0
else
  echo "FAILED: $ERRORS check(s) failed" >&2
  exit 1
fi
