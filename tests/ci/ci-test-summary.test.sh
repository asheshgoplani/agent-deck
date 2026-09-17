#!/usr/bin/env bash
# ci-test-summary.test.sh — Tests for scripts/ci-test-summary.py (#2133), the
# job summary writer behind the go-test workflow's "Summarise failing tests"
# step. Feeds a synthetic gotestsum JSON stream and checks the rendered table:
# final-fail tests get a row, known flakes are labelled, rerun-passed tests are
# listed as candidates, package-only failures get a package row, and the repro
# line is exact. Exit 0 on pass, 1 on any failure. Needs only python3.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SUMMARY="$SCRIPT_DIR/../../scripts/ci-test-summary.py"
ERRORS=0
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

check() {
  local label="$1" pattern="$2" file="$3"
  if grep -qF -- "$pattern" "$file"; then
    echo "ok   $label"
  else
    echo "FAIL $label: expected to find: $pattern"
    ERRORS=$((ERRORS + 1))
  fi
}

check_absent() {
  local label="$1" pattern="$2" file="$3"
  if grep -qF -- "$pattern" "$file"; then
    echo "FAIL $label: did not expect: $pattern"
    ERRORS=$((ERRORS + 1))
  else
    echo "ok   $label"
  fi
}

echo 'module example.com/mod' > "$TMP/go.mod"
printf '# comment\nTestFlaky\ninternal/b TestScoped\n' > "$TMP/known-flaky.txt"

M=example.com/mod
cat > "$TMP/out.json" <<JSON
{"Action":"run","Package":"$M/internal/a","Test":"TestFlaky"}
{"Action":"fail","Package":"$M/internal/a","Test":"TestFlaky"}
{"Action":"fail","Package":"$M/internal/a","Test":"TestNew"}
{"Action":"fail","Package":"$M/internal/a","Test":"TestNew/sub case"}
{"Action":"fail","Package":"$M/internal/a"}
{"Action":"fail","Package":"$M/internal/b","Test":"TestScoped"}
{"Action":"fail","Package":"$M/internal/b","Test":"TestRecover"}
{"Action":"fail","Package":"$M/internal/b"}
{"Action":"pass","Package":"$M/internal/b","Test":"TestRecover"}
{"Action":"pass","Package":"$M/internal/c","Test":"TestFine"}
{"Action":"pass","Package":"$M/internal/c"}
{"Action":"fail","Package":"$M/internal/d"}
not json at all
JSON

OUT="$TMP/summary.md"
GITHUB_STEP_SUMMARY="$OUT" python3 "$SUMMARY" --json "$TMP/out.json" \
  --known-flaky "$TMP/known-flaky.txt" --go-mod "$TMP/go.mod"

check "bare-name known flake" '| `./internal/a` | `TestFlaky` | known flake |' "$OUT"
check "new failure" '| `./internal/a` | `TestNew` | new failure |' "$OUT"
check "subtest repro" "go test ./internal/a -run '^TestNew$/^sub\\ case$' -race -count=1" "$OUT"
check "top-level repro" "go test ./internal/a -run '^TestNew$' -race -count=1" "$OUT"
check "package-scoped known flake" '| `./internal/b` | `TestScoped` | known flake |' "$OUT"
check "package-only failure row" '| `./internal/d` | `(package)` | new failure | `go test ./internal/d -race -count=1` |' "$OUT"
check_absent "no package row when tests named" '| `./internal/a` | `(package)`' "$OUT"
check "rerun recovery listed" '`./internal/b` `TestRecover`' "$OUT"
check_absent "recovered test not in table" '| `TestRecover` |' "$OUT"
check_absent "passing test absent" 'TestFine' "$OUT"
check "counts" '5 failing, 3 not in `tests/known-flaky.txt`.' "$OUT"

# Missing JSON: still exits 0 and writes a note.
OUT2="$TMP/missing.md"
GITHUB_STEP_SUMMARY="$OUT2" python3 "$SUMMARY" --json "$TMP/nope.json" --go-mod "$TMP/go.mod"
check "missing json note" 'No gotestsum JSON' "$OUT2"

# All green: says so.
echo "{\"Action\":\"pass\",\"Package\":\"$M/internal/c\",\"Test\":\"TestFine\"}" > "$TMP/green.json"
OUT3="$TMP/green.md"
GITHUB_STEP_SUMMARY="$OUT3" python3 "$SUMMARY" --json "$TMP/green.json" --go-mod "$TMP/go.mod"
check "green summary" 'No failing tests' "$OUT3"

if [ "$ERRORS" -ne 0 ]; then
  echo "$ERRORS check(s) failed"; cat "$OUT"; exit 1
fi
echo "all checks passed"
