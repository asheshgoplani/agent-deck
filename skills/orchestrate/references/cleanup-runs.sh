#!/bin/sh
set -eu

# Remove disposable git worktrees from inactive orchestrate runs while keeping
# reports, prompts, screenshots, and other small run artifacts.

root=${AGENTDECK_ORCHESTRATE_DIR:-}
days=7
apply=false
one_run=

usage() {
  echo "usage: cleanup-runs.sh [--apply] [--days N] [--run DIR]" >&2
  exit 2
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --apply) apply=true ;;
    --days) [ "$#" -ge 2 ] || usage; days=$2; shift ;;
    --run) [ "$#" -ge 2 ] || usage; one_run=$2; shift ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
  shift
done

case "$days" in *[!0-9]*|'') usage ;; esac
[ -n "$root" ] || { echo "cleanup-runs: set AGENTDECK_ORCHESTRATE_DIR to <repo>/.agent-deck" >&2; exit 2; }
[ -d "$root" ] || exit 0
command -v sqlite3 >/dev/null 2>&1 || { echo "cleanup-runs: sqlite3 is required" >&2; exit 1; }

sessions_file=$(mktemp "${TMPDIR:-/tmp}/agent-deck-orchestrate-sessions.XXXXXX")
dbs_file=$(mktemp "${TMPDIR:-/tmp}/agent-deck-orchestrate-dbs.XXXXXX")
trap 'rm -f "$sessions_file" "$dbs_file"' EXIT HUP INT TERM

# Read registries directly: `agent-deck list` performs status probes and can
# take minutes on a large fleet. Treat every unarchived registry entry as live,
# which intentionally errs on the side of retention.
for state_root in "$HOME/.agent-deck" "${XDG_DATA_HOME:-$HOME/.local/share}/agent-deck"; do
  [ -d "$state_root" ] || continue
  find "$state_root" -maxdepth 3 -name state.db -type f -print >>"$dbs_file"
done
[ -s "$dbs_file" ] || { echo "cleanup-runs: no Agent Deck state database found" >&2; exit 1; }
while IFS= read -r db; do
  [ "$(sqlite3 -readonly "$db" "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='instances';")" = 1 ] || continue
  sqlite3 -readonly "$db" \
    "SELECT project_path FROM instances WHERE archived_at = 0
     UNION SELECT worktree_path FROM instances WHERE archived_at = 0 AND worktree_path != '';" \
    >>"$sessions_file" || exit 1
done <"$dbs_file"

is_live_run() {
  grep -Fqx "$1" "$sessions_file" || grep -Fq "$1/" "$sessions_file"
}

is_old_enough() {
  [ -n "$one_run" ] && return 0
  cutoff=$(($(date +%s) - days * 86400))
  modified=$(stat -f %m "$1" 2>/dev/null || stat -c %Y "$1" 2>/dev/null)
  [ "$modified" -le "$cutoff" ]
}

remove_worktree() {
  worktree=$1

  # Ignored build products are disposable, but tracked or staged edits are not.
  if ! git -C "$worktree" diff --quiet --ignore-submodules -- ||
     ! git -C "$worktree" diff --cached --quiet --ignore-submodules --; then
    echo "SKIP modified worktree: $worktree" >&2
    return
  fi

  if ! $apply; then
    size=$(du -sh "$worktree" 2>/dev/null | awk '{print $1}')
    echo "WOULD REMOVE ${size:-?}: $worktree"
    return
  fi

  common_dir=$(git -C "$worktree" rev-parse --git-common-dir 2>/dev/null || true)
  git_dir=$(git -C "$worktree" rev-parse --git-dir 2>/dev/null || true)
  if [ -n "$common_dir" ] && [ -n "$git_dir" ] && [ "$common_dir" != "$git_dir" ]; then
    repo=$(git -C "$worktree" rev-parse --path-format=absolute --git-common-dir)
    repo=${repo%/.git}
    git -C "$repo" worktree remove --force "$worktree"
  else
    # A self-contained clone inside a run has no parent worktree registry.
    rm -rf -- "$worktree"
  fi
  echo "REMOVED: $worktree"
}

remove_artifact() {
  artifact=$1
  if ! $apply; then
    size=$(du -sh "$artifact" 2>/dev/null | awk '{print $1}')
    echo "WOULD REMOVE artifact ${size:-?}: $artifact"
    return
  fi
  rm -rf -- "$artifact"
  echo "REMOVED artifact: $artifact"
}

if [ -n "$one_run" ]; then
  case "$one_run" in "$root"/*) ;; *) echo "cleanup-runs: --run must be inside $root" >&2; exit 2 ;; esac
  runs=$one_run
else
  runs=$(find "$root" -mindepth 1 -maxdepth 1 -type d -print)
fi

printf '%s\n' "$runs" | while IFS= read -r run; do
  [ -n "$run" ] || continue
  control="$run/orchestrate"
  [ -d "$control" ] || { echo "SKIP unrecognized run layout: $run" >&2; continue; }
  [ ! -e "$control/.needs-attention" ] || { echo "KEEP needs-attention: $run"; continue; }
  is_old_enough "$run" || continue
  if is_live_run "$run"; then
    echo "KEEP live: $run"
    continue
  fi

  if [ -f "$control/worktrees.tsv" ]; then
    while IFS="$(printf '\t')" read -r candidate branch repo; do
      [ -n "$candidate" ] || continue
      [ -n "$branch" ] || { echo "SKIP registry row without branch: $candidate" >&2; continue; }
      case "$candidate" in "$repo/.worktrees/"*) ;; *) echo "SKIP untrusted registered path: $candidate" >&2; continue ;; esac
      [ -d "$candidate" ] && remove_worktree "$candidate"
    done <"$control/worktrees.tsv"
  fi

  # Select only outermost repositories so nested dependencies are never treated
  # as independent deletion targets.
  find "$run" -mindepth 2 -maxdepth 7 \
    \( -name node_modules -o -name .next -o -name build -o -name dist \) -prune -o \
    -name .git -print | while IFS= read -r marker; do
    candidate=${marker%/.git}
    parent=$candidate
    nested=false
    while [ "$parent" != "$run" ]; do
      parent=${parent%/*}
      [ -e "$parent/.git" ] && { nested=true; break; }
    done
    $nested || remove_worktree "$candidate"
  done

  # Test result bundles and reproducible dependency/build caches frequently
  # outweigh the worktrees themselves. They are never final-report evidence;
  # screenshots and ordinary files remain untouched.
  find "$run" -mindepth 2 -type d \
    \( -name node_modules -o -name .next -o -name .turbo -o \
       -name DerivedData -o -name '*.xcresult' \) \
    -prune -print | while IFS= read -r artifact; do
      remove_artifact "$artifact"
    done
done
