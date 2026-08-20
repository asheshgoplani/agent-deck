#!/usr/bin/env bash
# Run-end residue detector for orchestrate.
#
#   bash "$RUN_DIR/teardown-gate.sh" --repo <repo-root> [--run-dir DIR]
#
# Exit 0 = clean, 1 = residue found, 2 = usage/environment error. It only looks
# and prints: every deletion stays with the cleanup child, every judgement
# stays with the conductor. Nothing here writes to the repository.
#
# Why it exists: per-task cleanup runs on the success path only, and it deletes
# exactly the worktrees a task recorded in worktrees.tsv. Three kinds of
# residue slip past that, and nothing else ever collects them:
#
#   sessions   a child is deleted when its output is read; an abandoned round
#              leaves the row behind
#   worktrees  a checkout made outside create-worktree.sh is in no tsv, so no
#              cleanup child was ever handed its path. On 2026-08-20, eight of
#              the nine stale worktrees in agent-deck were unregistered ones
#   temp       .agent-deck/tmp/<session-id> is removed by the remove /
#              worktree-finish lifecycle intent. A killed pane or a pruned
#              registry row never reaches that path, and cleanup-runs.sh cannot
#              see these at all — it walks <run>/orchestrate/ layouts only. The
#              same repository had 335 such directories holding 1.1 GB
#
# Needs-attention runs are residue on purpose: their sessions, worktrees and
# scratch stay intact for a human, so they print as KEEP and never fail the
# gate.
set -uo pipefail

repo=
run_dir=

usage() {
  echo "usage: teardown-gate.sh --repo <repo-root> [--run-dir DIR]" >&2
  exit 2
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo) [ "$#" -ge 2 ] || usage; repo=$2; shift ;;
    --run-dir) [ "$#" -ge 2 ] || usage; run_dir=$2; shift ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
  shift
done

[ -n "$run_dir" ] || run_dir="$(cd "$(dirname "$0")" && pwd)"
[ -n "$repo" ] || usage
[ -d "$repo" ] || { echo "teardown-gate: no such repo root: $repo" >&2; exit 2; }
[ -d "$run_dir" ] || { echo "teardown-gate: no such run dir: $run_dir" >&2; exit 2; }

# $RUN_DIR is <repo>/.agent-deck/<run-id>[/orchestrate]. The directory that
# names the run is the run id, and that id prefixes every worktree it owns and
# suffixes every child title it launched.
run_root=$run_dir
[ "$(basename "$run_root")" = orchestrate ] && run_root=$(dirname "$run_root")
run_id=$(basename "$run_root")

work=$(mktemp -d "${TMPDIR:-/tmp}/agent-deck-teardown.XXXXXX") || exit 2
trap 'rm -rf "$work"' EXIT HUP INT TERM
: >"$work/findings"

# Findings accumulate in a file, not a variable: every scan below reads from a
# pipe, and a counter incremented inside a pipeline lives in a subshell and is
# gone by the time the verdict is printed.
report() { printf 'RESIDUE %-9s %s\n' "$1" "$2" | tee -a "$work/findings"; }

needs_attention=false
[ -e "$run_dir/.needs-attention" ] && needs_attention=true
$needs_attention && echo "KEEP needs-attention: $run_root — sessions, worktrees and scratch stay for the user"

# 1. Sessions. Titles carry the run id (`impl-<run-id>`, `watchdog-<run-id>`),
#    which is what separates this run's children from the rest of a busy deck.
#    The conductor's own id and its watchdog's are excluded: the conductor is
#    still running this gate, and both retire after the final report.
self_ids=$(cat "$run_dir/.conductor-id" "$run_dir/.watchdog-id" 2>/dev/null | tr -d ' ')
# $GATE_SESSIONS_JSON exists so the scan is testable against a canned file. It
# is a path, not a command: a command read from the environment would be split
# on whitespace and shred any JSON it produced.
if [ -n "${GATE_SESSIONS_JSON:-}" ]; then
  sessions_json=$(cat "$GATE_SESSIONS_JSON" 2>/dev/null)
else
  sessions_json=$(agent-deck session children --json 2>/dev/null)
fi
if [ -z "$sessions_json" ]; then
  echo "SKIP sessions: agent-deck returned nothing — check the deck by hand" >&2
elif ! command -v jq >/dev/null 2>&1; then
  echo "SKIP sessions: jq is required" >&2
else
  printf '%s' "$sessions_json" | jq -r --arg run "$run_id" '
    (.children // .data.children // [])[]
    | select((.archived // false) | not)
    | select((.title // "") | endswith("-" + $run))
    | "\(.id)\t\(.title)"' 2>/dev/null | while IFS="$(printf '\t')" read -r id title; do
      [ -n "$id" ] || continue
      printf '%s\n' "$self_ids" | grep -Fqx "$id" && continue
      if $needs_attention; then echo "KEEP session: $title ($id)"; continue; fi
      report session "$title ($id) — agent-deck session remove $id --force"
    done
fi

# 2. Worktrees. Read from `git worktree list`, not from worktrees.tsv: the tsv
#    is the cleanup child's target list, so trusting it here would leave the
#    gate blind to exactly the checkouts that were never registered.
git -C "$repo" worktree list --porcelain 2>/dev/null \
  | sed -n 's/^worktree //p' | while IFS= read -r wt; do
      case "$(basename "$wt")" in "$run_id"-*) ;; *) continue ;; esac
      if $needs_attention; then echo "KEEP worktree: $wt"; continue; fi
      branch=$(git -C "$wt" rev-parse --abbrev-ref HEAD 2>/dev/null || echo '?')
      report worktree "$wt [$branch] — recorded in worktrees.tsv? $(grep -Fq "$wt" "$run_dir/worktrees.tsv" 2>/dev/null && echo yes || echo 'NO, cleanup never saw it')"
    done

# 3. Session temp. An orphan is a directory under .agent-deck/tmp whose id is
#    in no registry: the lifecycle intent that would have removed it can never
#    fire again, so it is leaked for good. Live sessions keep theirs, including
#    this conductor's.
tmp_root="$repo/.agent-deck/tmp"
if [ -d "$tmp_root" ] && ! $needs_attention; then
  if ! command -v sqlite3 >/dev/null 2>&1; then
    echo "SKIP temp: sqlite3 is required to tell an orphan from a live session" >&2
  else
    for state_root in "$HOME/.agent-deck" "${XDG_DATA_HOME:-$HOME/.local/share}/agent-deck"; do
      [ -d "$state_root" ] || continue
      find "$state_root" -maxdepth 3 -name state.db -type f -print
    done >"$work/dbs"
    : >"$work/known"
    while IFS= read -r db; do
      [ "$(sqlite3 -readonly "$db" "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='instances';" 2>/dev/null)" = 1 ] || continue
      sqlite3 -readonly "$db" "SELECT id FROM instances;" 2>/dev/null >>"$work/known"
    done <"$work/dbs"
    if [ ! -s "$work/known" ]; then
      # No registry means every directory would look orphaned. Report nothing
      # rather than nominate live scratch for deletion.
      echo "SKIP temp: no Agent Deck registry found — cannot tell orphans from live scratch" >&2
    else
      for dir in "$tmp_root"/*; do
        [ -d "$dir" ] || continue
        id=$(basename "$dir")
        grep -Fqx "$id" "$work/known" && continue
        size=$(du -sh "$dir" 2>/dev/null | awk '{print $1}')
        report temp "$dir (${size:-?}) — no registry row; nothing will ever collect it"
      done
    fi
  fi
fi

count=$(wc -l <"$work/findings" | tr -d ' ')
if [ "$count" -eq 0 ]; then
  echo "VERDICT: clean — no sessions, worktrees or scratch left from $run_id"
  exit 0
fi
echo "VERDICT: residue — $count item(s) above are still on this host"
exit 1
