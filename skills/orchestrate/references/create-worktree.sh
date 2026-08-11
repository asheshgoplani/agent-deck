#!/bin/sh
set -eu

repo=
run_dir=
run_id=
task=
branch=
base=

usage() {
  echo "usage: create-worktree.sh --repo DIR --run-dir DIR --run-id ID --task SLUG --branch BRANCH --base REF" >&2
  exit 2
}

while [ "$#" -gt 0 ]; do
  [ "$#" -ge 2 ] || usage
  case "$1" in
    --repo) repo=$2 ;;
    --run-dir) run_dir=$2 ;;
    --run-id) run_id=$2 ;;
    --task) task=$2 ;;
    --branch) branch=$2 ;;
    --base) base=$2 ;;
    *) usage ;;
  esac
  shift 2
done

[ -n "$repo" ] && [ -n "$run_dir" ] && [ -n "$run_id" ] && \
  [ -n "$task" ] && [ -n "$branch" ] && [ -n "$base" ] || usage
case "$run_id" in *[!a-zA-Z0-9._-]*|'') usage ;; esac
case "$task" in *[!a-zA-Z0-9._-]*|'') usage ;; esac

root=$(git -C "$repo" worktree list --porcelain | awk '/^worktree / { print substr($0, 10); exit }')
[ -n "$root" ] || { echo "cannot resolve root worktree: $repo" >&2; exit 1; }
base_sha=$(git -C "$root" rev-parse --verify "$base^{commit}")
worktrees_root="$root/.worktrees"
worktree="$worktrees_root/$run_id-$task"
mkdir -p "$worktrees_root" "$run_dir"

case "$worktree" in "$worktrees_root"/*) ;; *) echo "invalid worktree path" >&2; exit 2 ;; esac
[ ! -e "$worktree" ] || { echo "worktree path already exists: $worktree" >&2; exit 1; }
git -C "$root" show-ref --verify --quiet "refs/heads/$branch" && {
  echo "branch already exists: $branch" >&2
  exit 1
}

git -C "$root" worktree add "$worktree" -b "$branch" "$base_sha" >&2
printf '%s\t%s\t%s\n' "$worktree" "$branch" "$root" >>"$run_dir/worktrees.tsv"
printf '%s\n' "$worktree"
