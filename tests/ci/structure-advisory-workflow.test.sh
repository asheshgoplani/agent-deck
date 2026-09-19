#!/usr/bin/env bash
# Guards issue #2148's maintainer-requested revisions to the structure
# advisory workflow (PR #2149's review hold, 2026-09-06): a real merge base
# instead of the base branch tip, bounded/validated summary reads, and
# pinned/scoped-down checkouts and binary download.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKFLOW="$ROOT/.github/workflows/structure-advisory.yml"
ERRORS=0

fail() {
  echo "FAIL: $1" >&2
  ERRORS=$((ERRORS + 1))
}

pass() {
  echo "PASS: $1"
}

if [[ ! -f "$WORKFLOW" ]]; then
  fail "workflow file not found: $WORKFLOW"
  exit 1
fi

# (1) Real merge base, not the base branch's own tip.
if grep -Fq 'ref: ${{ github.event.pull_request.base.sha }}' "$WORKFLOW"; then
  fail "must not check out github.event.pull_request.base.sha directly (base branch tip, not the PR's merge base)"
else
  pass "does not check out the base branch tip directly"
fi

if grep -Fq 'git merge-base' "$WORKFLOW"; then
  pass "computes a real merge base via git merge-base"
else
  fail "must compute a real merge base (e.g. via git merge-base)"
fi

# (2) Summary step bounds file reads and validates numeric values.
SUMMARY_STEP=$(awk '/- name: Write summary/{f=1} f{print}' "$WORKFLOW")

if grep -Fq 'txt = open(name).read().strip()' <<<"$SUMMARY_STEP"; then
  fail "summary step must not read a whole log file into memory before truncating"
else
  pass "summary step does not perform an unbounded whole-file read"
fi

if grep -Fq 'MAX_LOG_BYTES' <<<"$SUMMARY_STEP" || grep -Eq '\.seek\(' <<<"$SUMMARY_STEP"; then
  pass "summary step bounds the log read itself (not only the displayed tail)"
else
  fail "summary step must bound the file read (e.g. seek from the end / read(N)), not just slice after reading"
fi

if grep -Fq 'is_number' <<<"$SUMMARY_STEP" || grep -Fq 'isinstance' <<<"$SUMMARY_STEP"; then
  pass "summary step validates numeric fields before subtracting"
else
  fail "summary step must validate that base/head values are numeric before computing a delta"
fi

# (3) Checkout and binary provenance.
CHECKOUT_COUNT=$(grep -cF 'uses: actions/checkout@' "$WORKFLOW" || true)
PERSIST_FALSE_COUNT=$(grep -cF 'persist-credentials: false' "$WORKFLOW" || true)
if [[ "$PERSIST_FALSE_COUNT" -ge "$CHECKOUT_COUNT" ]] && [[ "$CHECKOUT_COUNT" -gt 0 ]]; then
  pass "every checkout step sets persist-credentials: false ($PERSIST_FALSE_COUNT/$CHECKOUT_COUNT)"
else
  fail "every actions/checkout step must set persist-credentials: false ($PERSIST_FALSE_COUNT/$CHECKOUT_COUNT)"
fi

if grep -Eq 'SENTRUX_SHA256|sha256sum -c' "$WORKFLOW"; then
  pass "sentrux binary download is checksum-verified against a pinned value"
else
  fail "sentrux binary download must be checksum- or signature-verified against a pinned value"
fi

if grep -Fq 'SENTRUX_VERSION:' "$WORKFLOW"; then
  pass "sentrux release is pinned to a fixed version"
else
  fail "sentrux release must be pinned to a fixed version"
fi

if [[ "$ERRORS" -ne 0 ]]; then
  exit 1
fi
