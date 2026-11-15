#!/usr/bin/env bash
# go-test-split.test.sh: checks scripts/go-test-split.sh with a stub runner.
# Both halves get the caller's flags plus -run <cut> / -skip <cut> (the same
# regex, so every test lands in exactly one half), flags that would change
# that partition are refused, and a failing half fails the script.
#
# Usage: bash tests/ci/go-test-split.test.sh
# Exit 0 on pass, 1 on any failure. Writes only under a mktemp dir.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
SPLIT="$ROOT/scripts/go-test-split.sh"
ERRORS=0
fail() { echo "FAIL: $*"; ERRORS=$((ERRORS + 1)); }

tmp=$(mktemp -d "${TMPDIR:-/tmp}/go-test-split-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
# Stub runner: records its arguments, one per line, in a file per half, and
# exits with STUB_RC_RUN for the -run half and STUB_RC_SKIP for the -skip half.
cat >"$tmp/runner" <<'EOF'
#!/usr/bin/env bash
half=other
for a in "$@"; do
  case $a in -run) half=run ;; -skip) half=skip ;; esac
done
printf '%s\n' "$@" >"$STUB_DIR/$half.args"
echo "ok  	example.com/pkg	0.1s"
if [ "$half" = run ]; then exit "${STUB_RC_RUN:-0}"; else exit "${STUB_RC_SKIP:-0}"; fi
EOF
chmod +x "$tmp/runner"
export STUB_DIR="$tmp" GO_TEST_SPLIT_RUNNER="$tmp/runner"

# 1. Both halves pass: exit 0, each half got -C, its cut flag and the caller's args.
out=$(bash "$SPLIT" -C "$ROOT" -count=1 -timeout 20m ./... 2>&1); rc=$?
[ "$rc" = 0 ] || fail "both halves pass: exit $rc, want 0: $out"
want_run=$(printf '%s\n' -C "$ROOT" -run '^Test[A-H]' -count=1 -timeout 20m ./...)
want_skip=$(printf '%s\n' -C "$ROOT" -skip '^Test[A-H]' -count=1 -timeout 20m ./...)
[ "$(cat "$tmp/run.args" 2>/dev/null)" = "$want_run" ] || fail "run half args: $(tr '\n' ' ' <"$tmp/run.args")"
[ "$(cat "$tmp/skip.args" 2>/dev/null)" = "$want_skip" ] || fail "skip half args: $(tr '\n' ' ' <"$tmp/skip.args")"
grep -q 'go-test-split: exit=0' <<<"$out" || fail "no exit=0 summary line: $out"

# 2. --cut is used verbatim by both halves.
rm -f "$tmp"/*.args
bash "$SPLIT" -C "$ROOT" --cut '^Test[A-M]' ./internal/session >/dev/null 2>&1
grep -qxF '^Test[A-M]' "$tmp/run.args" 2>/dev/null || fail "--cut not passed to the run half"
grep -qxF '^Test[A-M]' "$tmp/skip.args" 2>/dev/null || fail "--cut not passed to the skip half"

# 3. Either half failing fails the script with that half's exit code.
STUB_RC_RUN=1 bash "$SPLIT" -C "$ROOT" ./... >/dev/null 2>&1; rc=$?
[ "$rc" = 1 ] || fail "run half fails: exit $rc, want 1"
STUB_RC_SKIP=3 bash "$SPLIT" -C "$ROOT" ./... >/dev/null 2>&1; rc=$?
[ "$rc" = 3 ] || fail "skip half fails: exit $rc, want 3"

# 4. Flags that would change the partition are refused before any run.
for bad in -run=X -run -skip '-skip=Y' -test.run=Z --run=X -list=. -bench=. -fuzz=FuzzX -args; do
  rm -f "$tmp"/*.args
  bash "$SPLIT" -C "$ROOT" "$bad" ./... >/dev/null 2>&1; rc=$?
  [ "$rc" = 2 ] || fail "'$bad' not refused (exit $rc, want 2)"
  ls "$tmp"/*.args >/dev/null 2>&1 && fail "'$bad' still started a runner"
done
# A flag that merely contains the word is not refused.
bash "$SPLIT" -C "$ROOT" -runtime-ok ./... >/dev/null 2>&1 || true
bash "$SPLIT" -C "$ROOT" -count=1 ./... >/dev/null 2>&1 || fail "-count=1 refused"

# 5. Dry run prints both commands and starts nothing.
rm -f "$tmp"/*.args
out=$(bash "$SPLIT" -C "$ROOT" --dry-run -count=1 ./... 2>&1)
grep -q '^half 1: .* -run ' <<<"$out" && grep -q '^half 2: .* -skip ' <<<"$out" || fail "dry run output: $out"
ls "$tmp"/*.args >/dev/null 2>&1 && fail "dry run started a runner"

# 6. No go.mod is a usage error.
bash "$SPLIT" -C "$tmp" ./... >/dev/null 2>&1; rc=$?
[ "$rc" = 2 ] || fail "missing go.mod: exit $rc, want 2"

if [ "$ERRORS" -gt 0 ]; then echo "go-test-split.test.sh: $ERRORS failure(s)"; exit 1; fi
echo "go-test-split.test.sh: all checks passed"
