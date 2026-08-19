#!/bin/sh
# Snapshot and verify a repository's PRIMARY checkout around a child that is
# not supposed to touch it.
#
#   primary-checkout-guard.sh snapshot --repo DIR --run-dir DIR [--label NAME]
#   primary-checkout-guard.sh verify   --repo DIR --run-dir DIR [--label NAME]
#
# Why this exists: every delivery child gets its own worktree, but deploy,
# build, and merge children are launched ad hoc and inherit whatever checkout
# they were pointed at. In one run a deploy child fast-forwarded a primary
# checkout 15 commits while nobody was looking. Nothing was lost that time —
# the checkout happened to be 0 ahead — but the same move over a dirty or
# ahead primary destroys work that exists in exactly one place.
#
# `snapshot` records HEAD, the checked-out branch, and whether the tree was
# dirty, into $run_dir/primary-<label>.snapshot. `verify` re-reads the same
# three and exits non-zero on any difference, printing what moved. Run
# snapshot immediately before launching such a child and verify when it
# reports done, BEFORE archiving it — the worktree is still there to inspect.
set -eu

mode=${1:-}
[ -n "$mode" ] && shift 2>/dev/null || true

repo=
run_dir=
label=primary

usage() {
  echo "usage: primary-checkout-guard.sh snapshot|verify --repo DIR --run-dir DIR [--label NAME]" >&2
  exit 2
}

case "$mode" in
  snapshot|verify) ;;
  *) usage ;;
esac

while [ "$#" -gt 0 ]; do
  [ "$#" -ge 2 ] || usage
  case "$1" in
    --repo) repo=$2 ;;
    --run-dir) run_dir=$2 ;;
    --label) label=$2 ;;
    *) usage ;;
  esac
  shift 2
done

[ -n "$repo" ] && [ -n "$run_dir" ] || usage
case "$label" in *[!a-zA-Z0-9._-]*|'') usage ;; esac

# The first `git worktree list` entry is the main worktree even when this runs
# from inside a linked one.
root=$(git -C "$repo" worktree list --porcelain | awk '/^worktree / { print substr($0, 10); exit }')
[ -n "$root" ] || { echo "cannot resolve root worktree: $repo" >&2; exit 1; }

head=$(git -C "$root" rev-parse HEAD)
branch=$(git -C "$root" rev-parse --abbrev-ref HEAD)
if [ -n "$(git -C "$root" status --porcelain)" ]; then dirty=dirty; else dirty=clean; fi

snapshot="$run_dir/primary-$label.snapshot"

if [ "$mode" = snapshot ]; then
  mkdir -p "$run_dir"
  printf 'root\t%s\nhead\t%s\nbranch\t%s\ntree\t%s\n' "$root" "$head" "$branch" "$dirty" >"$snapshot"
  printf 'primary %s pinned at %s (%s, %s)\n' "$root" "$head" "$branch" "$dirty"
  exit 0
fi

[ -f "$snapshot" ] || { echo "no snapshot to verify: $snapshot" >&2; exit 1; }
was_head=$(awk -F'\t' '$1=="head"{print $2}' "$snapshot")
was_branch=$(awk -F'\t' '$1=="branch"{print $2}' "$snapshot")
was_tree=$(awk -F'\t' '$1=="tree"{print $2}' "$snapshot")

rc=0
[ "$head" = "$was_head" ] || { printf 'PRIMARY MOVED: HEAD %s -> %s\n' "$was_head" "$head" >&2; rc=1; }
[ "$branch" = "$was_branch" ] || { printf 'PRIMARY MOVED: branch %s -> %s\n' "$was_branch" "$branch" >&2; rc=1; }
[ "$dirty" = "$was_tree" ] || { printf 'PRIMARY MOVED: tree %s -> %s\n' "$was_tree" "$dirty" >&2; rc=1; }

if [ "$rc" -eq 0 ]; then
  printf 'primary %s unchanged at %s (%s, %s)\n' "$root" "$head" "$branch" "$dirty"
else
  printf 'primary checkout: %s\n' "$root" >&2
fi
exit "$rc"
