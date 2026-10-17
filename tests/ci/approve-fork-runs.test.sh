#!/usr/bin/env bash
# approve-fork-runs.test.sh: tests for scripts/maintainer/approve-fork-runs.sh.
#
# A stub `gh` is put first on PATH. It answers each call from canned fixtures
# (already in the shape the real `gh --jq` filter would print) and appends every
# POST to a log, so the test can count exactly which runs the script approved.
# Nothing here talks to GitHub.
#
# Fixture world:
#   PR 101  safe fork PR, head aaa1, runs 11 12 on aaa1, plus stale run 13 (old head)
#   PR 102  fork PR touching .github/workflows, head bbb1, run 21 (SENSITIVE)
#   PR 103  safe fork PR, head ccc1, run 31; its head moves to ccc2 before approval
#   run 41  head ddd1 belongs to no open PR (closed or superseded): stale
#
# Exit 0 on pass, 1 on any failure. Needs only bash and coreutils.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$ROOT/scripts/maintainer/approve-fork-runs.sh"
ERRORS=0
TMP="$(mktemp -d)"
cleanup() {
  if command -v trash >/dev/null 2>&1; then trash "$TMP"; else rm -rf "$TMP"; fi
}
trap cleanup EXIT

[ -f "$SCRIPT" ] || { echo "FAIL $SCRIPT not found"; exit 1; }

mkdir -p "$TMP/bin" "$TMP/fx"
TAB=$'\t'
# Runs: id, head_sha, workflow name, head repo.
{
  echo "11${TAB}aaa1${TAB}CI${TAB}alice/agent-deck"
  echo "12${TAB}aaa1${TAB}Lint${TAB}alice/agent-deck"
  echo "13${TAB}aaa0${TAB}CI${TAB}alice/agent-deck"
  echo "21${TAB}bbb1${TAB}CI${TAB}bob/agent-deck"
  echo "31${TAB}ccc1${TAB}CI${TAB}carol/agent-deck"
  echo "41${TAB}ddd1${TAB}CI${TAB}dave/agent-deck"
} > "$TMP/fx/runs.tsv"
# Open fork PRs: number, head sha, author, title.
{
  echo "101${TAB}aaa1${TAB}alice${TAB}fix(ui): safe change"
  echo "102${TAB}bbb1${TAB}bob${TAB}ci: touch a workflow"
  echo "103${TAB}ccc1${TAB}carol${TAB}fix(session): another safe change"
} > "$TMP/fx/prs.tsv"
printf 'internal/ui/home.go\n' > "$TMP/fx/diff-101"
printf '.github/workflows/go-test.yml\ninternal/ui/home.go\n' > "$TMP/fx/diff-102"
printf 'internal/session/instance.go\n' > "$TMP/fx/diff-103"
# Live heads on re-read: 103 moved after listing.
echo aaa1 > "$TMP/fx/head-101"
echo bbb1 > "$TMP/fx/head-102"
echo ccc2 > "$TMP/fx/head-103"

cat > "$TMP/bin/gh" <<'STUB'
#!/usr/bin/env bash
# Stub gh: canned answers by argument pattern; every POST is logged.
set -euo pipefail
fx="$GH_STUB_FX"
args="$*"
echo "$args" >> "$GH_STUB_CALLS"
case "$args" in
  *"-X POST"*|*"--method POST"*)
    echo "$args" >> "$GH_STUB_POSTS"
    # A fixture file fail-<run id> makes that approve POST fail.
    id="${args#*/actions/runs/}"; id="${id%%/*}"
    [ ! -e "$fx/fail-$id" ] || exit 1 ;;
  "api --paginate repos/"*"/actions/runs?status=action_required"*)
    cat "$fx/runs.tsv" ;;
  "api --paginate repos/"*"/pulls?state=open"*)
    cat "$fx/prs.tsv" ;;
  "api repos/"*"/pulls/"*)
    n="${args#api repos/*/pulls/}"; n="${n%% *}"
    cat "$fx/head-$n" ;;
  "pr diff "*)
    n="${args#pr diff }"; n="${n%% *}"
    cat "$fx/diff-$n" ;;
  *)
    echo "gh stub: unexpected call: $args" >&2; exit 97 ;;
esac
STUB
chmod +x "$TMP/bin/gh"

export GH_STUB_FX="$TMP/fx"
export REPO="example/agent-deck"

# run_case <name> [args...]: runs the script, output in $TMP/<name>.out,
# POST log in $TMP/<name>.posts, exit code in $TMP/<name>.rc.
run_case() {
  local name="$1"; shift
  : > "$TMP/$name.posts"
  : > "$TMP/$name.calls"
  local rc=0
  GH_STUB_POSTS="$TMP/$name.posts" GH_STUB_CALLS="$TMP/$name.calls" \
    PATH="$TMP/bin:$PATH" bash "$SCRIPT" "$@" > "$TMP/$name.out" 2>&1 || rc=$?
  echo "$rc" > "$TMP/$name.rc"
}

ok() { echo "ok   $1"; }
bad() { echo "FAIL $1"; ERRORS=$((ERRORS + 1)); }

expect_rc() {
  local name="$1" want="$2" got
  got="$(cat "$TMP/$name.rc")"
  if [ "$got" = "$want" ]; then ok "$name: exit $want"; else bad "$name: exit $got, want $want"; sed 's/^/     | /' "$TMP/$name.out"; fi
}
expect_posts() {
  # expect_posts <name> <space separated run ids, sorted>
  local name="$1" want="$2" got
  got="$(sed -n 's#.*/actions/runs/\([0-9]*\)/approve.*#\1#p' "$TMP/$name.posts" | sort -n | tr '\n' ' ' | sed 's/ $//')"
  if [ "$got" = "$want" ]; then ok "$name: approved [$want]"; else bad "$name: approved [$got], want [$want]"; fi
}
expect_out() {
  if grep -qF -- "$2" "$TMP/$1.out"; then ok "$1: output has '$2'"; else bad "$1: output lacks '$2'"; sed 's/^/     | /' "$TMP/$1.out"; fi
}

# 1. Dry run is the default and makes zero POSTs.
run_case dryrun
expect_rc dryrun 0
expect_posts dryrun ""
expect_out dryrun "6 total, 2 on superseded or closed heads"
expect_out dryrun "#101"
expect_out dryrun "SENSITIVE"
expect_out dryrun "dry run: nothing approved"
if grep -q 'pulls/[0-9]' "$TMP/dryrun.calls"; then bad "dryrun: re-read heads although not approving"; else ok "dryrun: no head re-reads"; fi

# 2. --approve: safe current-head runs only. Stale 13 and 41 never, SENSITIVE 102
#    held, 103 skipped because its head moved.
run_case approve --approve
expect_rc approve 0
expect_posts approve "11 12"
expect_out approve "held: sensitive paths"
expect_out approve "skipped: head moved"

# 3. --include 102 releases the SENSITIVE PR. Stale runs still never approved.
run_case include --approve --include 102
expect_rc include 0
expect_posts include "11 12 21"

# 4. --pr limits to one PR.
run_case only --approve --pr 102 --include 102
expect_rc only 0
expect_posts only "21"
if grep -q '#101' "$TMP/only.out"; then bad "only: listed PR 101 despite --pr 102"; else ok "only: other PRs not listed"; fi

# 5. --include without --approve still writes nothing.
run_case include_dry --include 102
expect_rc include_dry 0
expect_posts include_dry ""

# 6. A failing diff lookup counts as SENSITIVE (fail closed), not safe.
mv "$TMP/fx/diff-101" "$TMP/fx/diff-101.bak"
run_case diff_fail --approve
mv "$TMP/fx/diff-101.bak" "$TMP/fx/diff-101"
expect_posts diff_fail ""
expect_out diff_fail "held: sensitive paths"

# 7. A failed approve POST is reported, not counted, and makes the exit code 1.
touch "$TMP/fx/fail-12"
run_case post_fail --approve
trash_or_rm() { if command -v trash >/dev/null 2>&1; then trash "$1"; else rm -f "$1"; fi; }
trash_or_rm "$TMP/fx/fail-12"
expect_rc post_fail 1
expect_out post_fail "failed to approve run 12"
expect_out post_fail "approved 1 of 2 runs on aaa1"
expect_out post_fail "approved runs: 1"

# 8. Unknown argument is rejected before any gh call.
run_case badarg --bogus
expect_rc badarg 2
if [ -s "$TMP/badarg.calls" ]; then bad "badarg: gh was called"; else ok "badarg: no gh calls"; fi

# 9. Without REPO, the repo is parsed from the git origin remote.
git -C "$TMP" init -q repo
git -C "$TMP/repo" remote add origin https://github.com/someone/forked-deck.git/
: > "$TMP/origin.posts"; : > "$TMP/origin.calls"
( cd "$TMP/repo" && env -u REPO GH_STUB_POSTS="$TMP/origin.posts" GH_STUB_CALLS="$TMP/origin.calls" \
    PATH="$TMP/bin:$PATH" bash "$SCRIPT" > "$TMP/origin.out" 2>&1 ) || true
if grep -q 'repos/someone/forked-deck/actions/runs' "$TMP/origin.calls"; then ok "origin: repo parsed from remote"; else bad "origin: repo not parsed from remote"; sed 's/^/     | /' "$TMP/origin.calls"; fi

echo
if [ "$ERRORS" -eq 0 ]; then echo "PASS: approve-fork-runs"; else echo "FAILED: $ERRORS check(s)"; exit 1; fi
