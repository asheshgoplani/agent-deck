#!/usr/bin/env bash
# Standalone checks for teardown-gate.sh. Run: bash teardown_gate_test.sh
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
GATE="$HERE/teardown-gate.sh"
fails=0

check() {
  local name=$1 expected_rc=$2 expected_text=$3 actual_rc=$4 actual_text=$5
  if [ "$actual_rc" != "$expected_rc" ]; then
    echo "FAIL $name: exit $actual_rc, want $expected_rc"; echo "$actual_text" | sed 's/^/     /'
    fails=$((fails + 1)); return
  fi
  if ! printf '%s' "$actual_text" | grep -q "$expected_text"; then
    echo "FAIL $name: output missing /$expected_text/"; echo "$actual_text" | sed 's/^/     /'
    fails=$((fails + 1)); return
  fi
  echo "ok   $name"
}

# A repository with a run directory, one run-owned worktree, and one unrelated
# worktree that the gate must leave alone.
setup() {
  root=$(mktemp -d "${TMPDIR:-/tmp}/teardown-gate-test.XXXXXX")
  repo="$root/repo"
  mkdir -p "$repo"
  git -C "$repo" init -q -b main
  git -C "$repo" config user.email t@example.com
  git -C "$repo" config user.name t
  echo hi >"$repo/f"; git -C "$repo" add f; git -C "$repo" commit -qm init
  run_dir="$repo/.agent-deck/$RUN_ID/orchestrate"
  mkdir -p "$run_dir"
  echo '{"children":[]}' >"$root/empty.json"
}

RUN_ID=2026-08-20-gate

# --- clean run: no worktrees, no children, no orphan scratch ---
setup
out=$(GATE_SESSIONS_JSON="$root/empty.json" bash "$GATE" --repo "$repo" --run-dir "$run_dir" 2>&1); rc=$?
check "clean run passes" 0 "VERDICT: clean" "$rc" "$out"

# --- an unregistered run-owned worktree is caught, an unrelated one is not ---
git -C "$repo" worktree add -q "$repo/.worktrees/$RUN_ID-task" -b task main
git -C "$repo" worktree add -q "$repo/.worktrees/someone-elses" -b other main
out=$(GATE_SESSIONS_JSON="$root/empty.json" bash "$GATE" --repo "$repo" --run-dir "$run_dir" 2>&1); rc=$?
check "unregistered worktree is residue" 1 "RESIDUE worktree" "$rc" "$out"
check "unregistered worktree is named as unregistered" 1 "cleanup never saw it" "$rc" "$out"
if printf '%s' "$out" | grep -q "someone-elses"; then
  echo "FAIL foreign worktree must not be reported"; fails=$((fails + 1))
else
  echo "ok   foreign worktree ignored"
fi

# --- .needs-attention converts every finding into a KEEP ---
touch "$run_dir/.needs-attention"
out=$(GATE_SESSIONS_JSON="$root/empty.json" bash "$GATE" --repo "$repo" --run-dir "$run_dir" 2>&1); rc=$?
check "needs-attention keeps residue" 0 "KEEP worktree" "$rc" "$out"
rm "$run_dir/.needs-attention"

# --- a surviving child of this run is residue; the conductor itself is not ---
setup
echo "conductor-id-1" >"$run_dir/.conductor-id"
CHILDREN='{"children":[
  {"id":"impl-id-1","title":"impl-'"$RUN_ID"'","archived":false},
  {"id":"conductor-id-1","title":"conductor-'"$RUN_ID"'","archived":false},
  {"id":"old-id","title":"impl-2026-01-01-other","archived":false},
  {"id":"gone-id","title":"review-'"$RUN_ID"'","archived":true}]}'
printf '%s' "$CHILDREN" >"$root/children.json"
out=$(GATE_SESSIONS_JSON="$root/children.json" bash "$GATE" --repo "$repo" --run-dir "$run_dir" 2>&1); rc=$?
check "live child is residue" 1 "impl-$RUN_ID" "$rc" "$out"
for unwanted in conductor-id-1 old-id gone-id; do
  if printf '%s' "$out" | grep -q "RESIDUE session.*$unwanted"; then
    echo "FAIL $unwanted must not be reported as residue"; fails=$((fails + 1))
  else
    echo "ok   $unwanted excluded"
  fi
done

# --- usage errors ---
out=$(bash "$GATE" 2>&1); rc=$?
check "missing --repo is a usage error" 2 "usage:" "$rc" "$out"
out=$(bash "$GATE" --repo "$root/nope" 2>&1); rc=$?
check "missing repo is an environment error" 2 "no such repo root" "$rc" "$out"

[ "$fails" -eq 0 ] && { echo "PASS"; exit 0; }
echo "$fails check(s) failed"; exit 1
