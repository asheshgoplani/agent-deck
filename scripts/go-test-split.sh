#!/usr/bin/env bash
# go-test-split.sh: run one `go test` command as two halves side by side,
# cut by test name, so a full local run is not one long serial package.
#
# Usage (from a checkout):
#   scripts/go-test-split.sh [-C <dir>] [--cut <regex>] [--dry-run] [go test args...]
#   scripts/go-test-split.sh -count=1 -timeout 20m ./...
#
# Why: internal/session runs its tests one after another (~630 s in a
# deck-test container) and sets the wall time of a full local run (~660 s);
# cmd/agent-deck is next (~510 s). go test runs each package in one process,
# so more CPUs in one container do not help. CI already cuts these packages
# by test name (go-test.yml session-a/b/c, cmd-a/b); this does the same for a
# local run with two runners.
#
# The halves are an exact partition of the top-level tests, examples and
# fuzz seeds of every package in the command:
#   half 1: go test -run '<cut>'  <args>
#   half 2: go test -skip '<cut>' <args>
# Every test runs in exactly one half, with the same flags. Nothing is
# selected or dropped. A test that only fails when a test of the other half
# ran first in the same process can pass here; CI's shards cut the same way
# and its shared-state proof job covers that, and the single full run
# (`deck-test ./...`) stays the check before merge and release.
#
# Arguments that would change the partition (-run, -skip, -list, -bench,
# -fuzz, -args) are refused: run the single command instead.
#
# Runner: GO_TEST_SPLIT_RUNNER, an executable taking `-C <dir> <go test
# args>`; default deck-test when on PATH (two containers, the most a lane may
# use), else plain go test in <dir>.
#
# Output: each half's full output goes to its own log; the script prints the
# package result lines, any FAIL lines, the time per half and the log paths.
# Exit 0 when both halves pass, else the first non-zero half's exit code
# (2 for usage errors).
set -uo pipefail

CUT='^Test[A-H]'
dir=.
dry=0
while [ $# -gt 0 ]; do
  case $1 in
    -C) [ $# -ge 2 ] || { echo "go-test-split: -C needs a directory" >&2; exit 2; }; dir=$2; shift 2 ;;
    --cut) [ $# -ge 2 ] || { echo "go-test-split: --cut needs a regex" >&2; exit 2; }; CUT=$2; shift 2 ;;
    --dry-run) dry=1; shift ;;
    -h|--help) sed -n '2,/^set -uo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
    *) break ;;
  esac
done
[ -n "$CUT" ] || { echo "go-test-split: --cut must not be empty" >&2; exit 2; }
for a in "$@"; do
  if [[ $a =~ ^--?(test\.)?(run|skip|list|bench|fuzz)(=.*)?$ ]] || [ "$a" = -args ] || [ "$a" = --args ]; then
    echo "go-test-split: '$a' would change the partition; run the single go test command instead" >&2
    exit 2
  fi
done
[ -r "$dir/go.mod" ] || { echo "go-test-split: no go.mod in $dir (pass -C <checkout>)" >&2; exit 2; }

# run_go_test -C <dir> <go test args>: the runner used without deck-test.
run_go_test() {
  local d=$2; shift 2
  (cd "$d" && go test "$@")
}
runner=${GO_TEST_SPLIT_RUNNER:-}
if [ -z "$runner" ]; then
  if command -v deck-test >/dev/null 2>&1; then runner=deck-test; else runner=run_go_test; fi
fi

if [ "$dry" = 1 ]; then
  printf 'half 1: %s -C %s -run %q' "$runner" "$dir" "$CUT"; printf ' %q' "$@"; echo
  printf 'half 2: %s -C %s -skip %q' "$runner" "$dir" "$CUT"; printf ' %q' "$@"; echo
  exit 0
fi

out=$(mktemp -d "${TMPDIR:-/tmp}/go-test-split.XXXXXX")
start=$(date +%s)
( s=$(date +%s); "$runner" -C "$dir" -run "$CUT" "$@" >"$out/half1.log" 2>&1; echo "$? $(( $(date +%s) - s ))" >"$out/half1.rc" ) &
p1=$!
( s=$(date +%s); "$runner" -C "$dir" -skip "$CUT" "$@" >"$out/half2.log" 2>&1; echo "$? $(( $(date +%s) - s ))" >"$out/half2.rc" ) &
p2=$!
trap 'kill "$p1" "$p2" 2>/dev/null; exit 130' INT TERM HUP
wait "$p1" "$p2"
trap - INT TERM HUP

rc=0
for h in 1 2; do
  read -r hrc hsecs <"$out/half$h.rc" 2>/dev/null || { hrc=1; hsecs=?; }
  if [ "$h" = 1 ]; then flag="-run '$CUT'"; else flag="-skip '$CUT'"; fi
  echo "== half $h ($flag): exit=$hrc ${hsecs}s, log $out/half$h.log"
  grep -E '^(ok|FAIL|---[[:space:]]FAIL|panic:)[[:space:]]' "$out/half$h.log" | sed 's/^/  /'
  if [ "$hrc" != 0 ]; then
    [ "$rc" != 0 ] || rc=$hrc
    tail -n 30 "$out/half$h.log" | sed 's/^/  | /'
  fi
done
echo "go-test-split: exit=$rc wall_s=$(( $(date +%s) - start ))"
exit "$rc"
