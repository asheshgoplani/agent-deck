#!/usr/bin/env bash
# go-test-shards.test.sh: checks that the shard matrix in
# .github/workflows/go-test.yml runs every Go test exactly once. The suite is
# cut by package group and, for the big groups, by test name (-run / -skip), so
# a typo in a name range, a range that two shards both claim, or a REST filter
# that is too wide can silently drop tests or run them twice. This reads the
# matrix (name, packages, run, skip) and the REST filter straight from the
# workflow, lists every top-level Test/Example/Fuzz function in the repo's
# *_test.go files, and asserts each one lands in exactly one shard. It then
# feeds broken copies of the workflow (a gap, an overlap, an unanchored REST
# filter) and expects each to be refused. The PROOF shard re-runs a tagged
# subset on purpose and is not part of the partition.
#
# Usage: bash tests/ci/go-test-shards.test.sh [workflow.yml]
# Exit 0 on pass, 1 on any failure. Needs bash, grep, awk, sort; writes no files.

set -euo pipefail
export LC_ALL=C

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
WF="${1:-$ROOT/.github/workflows/go-test.yml}"
MOD="$(awk '$1 == "module" { print $2; exit }' "$ROOT/go.mod")"
ERRORS=0

# Every top-level test function as "<import path> <name>". Go runs TestX only
# when X is empty or does not start with a lower-case letter; the same rule
# holds for Example and Fuzz. testdata, vendor, and dirs starting with . or _
# are not packages. TestMain is the package's entry point, not a test.
list_tests() {
  (cd "$ROOT" && grep -rEo --include='*_test.go' --exclude-dir=.git \
    '^func (Test|Example|Fuzz)([^a-z(][A-Za-z0-9_]*)?\(' .) |
    awk -v mod="$MOD" -F: '
      {
        path = $1; sub(/^\.\//, "", path)
        if (path ~ /(^|\/)(testdata|vendor|[._][^\/]*)\//) next
        dir = path; sub(/\/[^\/]*$/, "", dir)
        if (dir == path) dir = ""
        name = $2; sub(/^func /, "", name); sub(/\($/, "", name)
        if (name == "TestMain") next
        print (dir == "" ? mod : mod "/" dir), name
      }' | sort -u
}

# The shard matrix as "name<TAB>packages<TAB>run<TAB>skip", one shard a line.
parse_matrix() {
  awk '
    function flush() { if (name != "") printf "%s\t%s\t%s\t%s\n", name, pkgs, run, skip }
    function val(s) { sub(/^[^:]*:[ \t]*/, "", s); gsub(/^'\''|'\''$/, "", s); return s }
    /^        shard:[ \t]*$/ { in_m = 1; next }
    in_m && /^    steps:/ { flush(); in_m = 0; name = ""; next }
    !in_m || /^[ \t]*#/ { next }
    /^[ \t]*- name:/ { flush(); name = val($0); pkgs = run = skip = ""; next }
    /^[ \t]*packages:/ { pkgs = val($0); next }
    /^[ \t]*run:/ { run = val($0); next }
    /^[ \t]*skip:/ { skip = val($0); next }
  ' "$1"
}

# The REST filter: the grep -vE pattern of the Resolve packages step.
rest_filter() {
  grep -oE "go list \./\.\.\. \| grep -vE '[^']+'" "$1" | head -n 1 | sed -E "s/.*grep -vE '([^']+)'/\1/"
}

ALL_TESTS="$(list_tests)"

# check_partition <workflow>: prints one line per problem, nothing when clean.
check_partition() {
  local text rest matrix
  # Read once: the mutated copies below come in as one-shot pipes.
  text="$(cat "$1")"
  rest="$(rest_filter /dev/stdin <<<"$text")"
  matrix="$(parse_matrix /dev/stdin <<<"$text")"
  if [ -z "$rest" ]; then echo "no REST filter found in $1"; return; fi
  if [ -z "$matrix" ]; then echo "no shard matrix found in $1"; return; fi
  printf '%s\n' "$matrix" | awk -F'\t' 'seen[$1]++ { print "duplicate shard name " $1 }'
  # -run / -skip are RE2, the check below uses awk ERE: only allow the subset
  # where both agree (anchors, literals, bracket ranges, alternation).
  printf '%s\n' "$matrix" | awk -F'\t' '
    ($3 != "" && $3 !~ /^[][A-Za-z0-9_^$|()-]+$/) || ($4 != "" && $4 !~ /^[][A-Za-z0-9_^$|()-]+$/) {
      print "shard " $1 ": run/skip pattern uses syntax this check cannot mirror" }'
  {
    printf '%s\n' "$matrix"
    echo "--tests--"
    printf '%s\n' "$ALL_TESTS"
  } | awk -F'\t' -v mod="$MOD" -v rest="$rest" '
    !done && $0 == "--tests--" { done = 1; FS = " "; next }
    !done {
      if ($2 == "PROOF") next
      n++; sname[n] = $1; srun[n] = $3; sskip[n] = $4; spk[n] = ""
      if ($2 == "REST") { srest[n] = 1; next }
      k = split($2, parts, " ")
      for (i = 1; i <= k; i++) {
        p = parts[i]; sub(/^\.\//, "", p)
        if (p ~ /\/\.\.\.$/) { sub(/\/\.\.\.$/, "", p); re = "^" mod "/" p "(/|$)" }
        else re = "^" mod "/" p "$"
        spk[n] = spk[n] (spk[n] == "" ? "" : "|") re
      }
      next
    }
    {
      pkg = $1; name = $2; hits = ""; c = 0
      for (s = 1; s <= n; s++) {
        inpkg = srest[s] ? (pkg !~ rest) : (pkg ~ spk[s])
        if (!inpkg) continue
        if (srun[s] != "" && name !~ srun[s]) continue
        if (sskip[s] != "" && name ~ sskip[s]) continue
        c++; hits = hits " " sname[s]
      }
      if (c == 0) print "not run by any shard: " pkg " " name
      if (c > 1) print "run by " c " shards (" substr(hits, 2) "): " pkg " " name
    }'
}

expect_clean() {
  local label="$1" wf="$2" out
  out="$(check_partition "$wf")"
  if [ -z "$out" ]; then
    echo "ok   $label: $(printf '%s\n' "$ALL_TESTS" | wc -l | tr -d ' ') tests, each in exactly one shard"
  else
    echo "FAIL $label: $(printf '%s\n' "$out" | wc -l | tr -d ' ') problem(s)"
    printf '%s\n' "$out" | head -n 20 | sed 's/^/     /'
    ERRORS=$((ERRORS + 1))
  fi
}

expect_refused() {
  local label="$1" wf="$2" want="$3" out
  out="$(check_partition "$wf")"
  # A here-string, not a pipe: grep -q exits early and pipefail would turn the
  # writer's SIGPIPE into a miss.
  if grep -qF -- "$want" <<<"$out"; then
    echo "ok   $label: refused ($want)"
  else
    echo "FAIL $label: expected a problem containing: $want"
    ERRORS=$((ERRORS + 1))
  fi
}

if [ -z "$ALL_TESTS" ]; then
  echo "FAIL found no test functions under $ROOT"
  exit 1
fi

expect_clean "workflow matrix" "$WF"

# The self-checks below only make sense against the real workflow layout.
if [ $# -eq 0 ]; then
  # A name-range shard that drops a letter leaves those tests unrun.
  first_run="$(parse_matrix "$WF" | awk -F'\t' '$3 ~ /^\^Test\[[A-Z]-[A-Z]\]$/ { print $3; exit }')"
  if [ -n "$first_run" ]; then
    narrowed="$(printf '%s' "$first_run" | sed -E 's/\[([A-Z])-([A-Z])\]/[\1-\1]/')"
    expect_refused "gap in a -run range" \
      <(sed "s/run: '$(printf '%s' "$first_run" | sed 's/[][^$]/\\&/g')'/run: '$narrowed'/" "$WF") "not run by any shard"
    # Dropping a complement's -skip makes it rerun the named shards' tests.
    if grep -qE "^[[:space:]]+skip:" "$WF"; then
      expect_refused "complement without -skip" <(grep -vE "^[[:space:]]+skip:" "$WF") "run by 2 shards"
    fi
  else
    echo "FAIL no '^Test[X-Y]' run range found to mutate"
    ERRORS=$((ERRORS + 1))
  fi
  # An unanchored REST filter swallows sibling packages (internal/sessionref
  # matched /internal/session) and nothing runs them.
  expect_refused "unanchored REST filter" \
    <(sed -E "/go list \.\/\.\.\. \| grep -vE/s/'[^']+'/'\/cmd\/|\/internal\/session|\/internal\/ui|\/internal\/web|\/tests\/'/" "$WF") \
    "not run by any shard: $MOD/internal/sessionref"
fi

if [ "$ERRORS" -gt 0 ]; then
  echo "$ERRORS check(s) failed"
  exit 1
fi
echo "all checks passed"
