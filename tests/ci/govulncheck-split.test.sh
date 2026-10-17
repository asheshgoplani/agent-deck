#!/usr/bin/env bash
# govulncheck-split.test.sh: tests for .github/scripts/govulncheck-split.sh,
# the step in the govulncheck workflow that turns `govulncheck -format json`
# output into one summary row and one annotation per OSV. Feeds hand-written
# JSON streams from tests/ci/fixtures/govulncheck/ and checks the exit code
# (1 only when a called OSV exists, 2 when the input is not a govulncheck
# stream), the ::error / ::notice lines and the markdown table. Exit 0 on pass,
# 1 on any failure. Needs only bash and jq; writes no files.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SPLIT="$SCRIPT_DIR/../../.github/scripts/govulncheck-split.sh"
FIX="$SCRIPT_DIR/fixtures/govulncheck"
ERRORS=0
OUT=""
CODE=0

run() {
  CODE=0
  OUT="$(env -u GITHUB_STEP_SUMMARY bash "$SPLIT" "$1" 2>&1)" || CODE=$?
}

expect_code() {
  local label="$1" want="$2"
  if [ "$CODE" -eq "$want" ]; then
    echo "ok   $label: exit $want"
  else
    echo "FAIL $label: exit $CODE, want $want"
    ERRORS=$((ERRORS + 1))
  fi
}

expect_count() {
  local label="$1" pattern="$2" want="$3" got
  got="$(printf '%s\n' "$OUT" | grep -c -- "$pattern" || true)"
  if [ "$got" -eq "$want" ]; then
    echo "ok   $label: $want line(s) matching $pattern"
  else
    echo "FAIL $label: $got line(s) matching $pattern, want $want"
    ERRORS=$((ERRORS + 1))
  fi
}

expect_line() {
  local label="$1" line="$2"
  if printf '%s\n' "$OUT" | grep -qxF -- "$line"; then
    echo "ok   $label"
  else
    echo "FAIL $label: expected exact line: $line"
    ERRORS=$((ERRORS + 1))
  fi
}

# Two OSVs: GO-2026-0001 is called (it also has an imported-level finding,
# which must not produce a second row), GO-2026-0002 is only required.
run "$FIX/called.json"
expect_code called 1
expect_count called '^::error ' 1
expect_count called '^::notice ' 1
expect_line called-error '::error title=govulncheck::GO-2026-0001 HTTP/2 server crash in golang.org/x/net (fixed in v0.60.0)'
expect_line called-notice '::notice title=govulncheck::GO-2026-0002 Header parsing issue in golang.org/x/text (required, fixed in v0.30.0)'
expect_line called-header '| OSV | Module / package | Found | Fixed | Level | Example trace |'
expect_line called-row '| GO-2026-0001 | golang.org/x/net/http2 | v0.58.0 | v0.60.0 | called | watcher.SlackAdapter.HealthCheck -> http.Client.Do -> http2.Transport.RoundTrip |'
expect_line required-row '| GO-2026-0002 | golang.org/x/text | v0.29.0 | v0.30.0 | required |  |'
expect_count called-rows '^| GO-' 2

# Required and imported only: informational, never fails the job.
run "$FIX/required-only.json"
expect_code required-only 0
expect_count required-only '^::error ' 0
expect_count required-only '^::notice ' 2
expect_line imported-row '| GO-2026-0004 | example.com/imported/fs | v0.1.0 | none | imported |  |'
expect_line imported-notice '::notice title=govulncheck::GO-2026-0004 Path traversal in example.com/imported (imported, fixed in none)'

# A clean scan: config and progress only.
run "$FIX/empty.json"
expect_code empty 0
expect_count empty '^::error ' 0
expect_count empty '^::notice ' 0
expect_line empty-message 'No vulnerabilities found.'

# Not a govulncheck stream (scan crashed before writing anything): must fail
# closed, never pass silently.
run /dev/null
expect_code no-config 2
expect_count no-config '^::error ' 1

if [ "$ERRORS" -gt 0 ]; then
  echo "$ERRORS check(s) failed"
  exit 1
fi
echo "all govulncheck-split checks passed"
