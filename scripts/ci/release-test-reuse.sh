#!/usr/bin/env bash
# release.yml `reuse` job (#2929): decide whether the commit a release tag
# points at already has a green `Go tests` (go-test.yml) result, so the
# release does not run the same race suite a second time.
#
# A result counts only when GitHub ran the full shard set on the SAME code:
#
#   exact-sha  a `push` run of go-test.yml on main whose head_sha is the tag
#              commit itself.
#   same-tree  a `pull_request` run of go-test.yml for a same-repository PR
#              into main that was squash-merged AS the tag commit, run on that
#              PR's head branch, where the PR head has the tag commit's tree
#              AND the tag commit's single parent.
#              A pull_request run tests GitHub's merge of the head into the
#              base of that moment. main only moves forward, so that base is
#              an ancestor of the tag commit's parent, which is the head's
#              parent: the merge adds nothing and the tested tree is the head
#              tree, which is the tag tree. A head with another parent (merged
#              after main moved) does not qualify, whatever its tree.
#
# And the run itself must be a success whose `Full test suite (PR gate)` job
# and every `Test shard (...)` job (at least one) concluded success on the
# latest attempt. A run where the shards were skipped (a "no Go changes"
# pass) never counts.
#
# A candidate still running is waited for until REUSE_WAIT_SECONDS. Anything
# else (no candidate, a red or unverifiable one, an API error, the deadline)
# answers reuse=false and the release runs the go-test.yml shards itself. This
# script never fails the release on its own: it can only skip a duplicate run.
#
# Env: GH_TOKEN, REPO (owner/name), TAG_SHA (the tag's commit, already checked
# by ref-check), GITHUB_OUTPUT, optional GITHUB_STEP_SUMMARY,
# REUSE_WAIT_SECONDS (default 1200), REUSE_POLL_SECONDS (default 30).
# Writes reuse=true|false, run_url, match to GITHUB_OUTPUT.

set -uo pipefail

: "${REPO:?REPO is required}"
: "${TAG_SHA:?TAG_SHA is required}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}"
WAIT_SECONDS=${REUSE_WAIT_SECONDS:-1200}
POLL_SECONDS=${REUSE_POLL_SECONDS:-30}
WORKFLOW_PATH=.github/workflows/go-test.yml
GATE_JOB='Full test suite (PR gate)'

log() { echo "$*" >&2; }

summary() {
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf '%s\n' "$*" >>"$GITHUB_STEP_SUMMARY"
  fi
}

answer() { # reuse run_url match reason
  {
    echo "reuse=$1"
    echo "run_url=$2"
    echo "match=$3"
  } >>"$GITHUB_OUTPUT"
  log "$4"
  summary "### Release test gate" "" "$4"
  exit 0
}

no_reuse() { answer false "" none "No reusable green Go tests result for ${TAG_SHA}: $1. Running the go-test.yml shards on the tag."; }

# commit_info SHA -> "tree parent1 [parent2...]" via the API, or fails.
commit_info() {
  gh api "repos/${REPO}/git/commits/$1" --jq '[.tree.sha] + [.parents[].sha] | join(" ")'
}

tag_info=$(commit_info "$TAG_SHA") || no_reuse "could not read commit ${TAG_SHA}"
read -r TAG_TREE TAG_PARENTS <<<"$tag_info"
[ -n "${TAG_TREE:-}" ] || no_reuse "commit ${TAG_SHA} has no tree in the API answer"
log "tag commit ${TAG_SHA} tree ${TAG_TREE} parents ${TAG_PARENTS:-<none>}"

# same_tree_heads: "head_sha head_ref" lines for PR heads that qualify for
# the same-tree rule.
same_tree_heads() {
  local prs head ref base repo info tree parents
  # Only a single-parent (squash or rebase) tag commit can qualify.
  case "${TAG_PARENTS:-}" in "" | *" "*) return 0 ;; esac
  prs=$(gh api "repos/${REPO}/commits/${TAG_SHA}/pulls" \
    --jq '.[] | select(.merged_at != null and .merge_commit_sha == "'"${TAG_SHA}"'") | "\(.head.sha) \(.head.ref) \(.base.ref) \(.head.repo.full_name)"') || return 0
  while read -r head ref base repo; do
    [ -n "${head:-}" ] || continue
    if [ "$repo" != "$REPO" ]; then
      log "skip PR head ${head}: from ${repo}, not ${REPO}"
      continue
    fi
    if [ "$base" != main ]; then
      log "skip PR head ${head}: merged into base ${base}, not main"
      continue
    fi
    if [ -z "$ref" ] || [ "$ref" = null ]; then
      log "skip PR head ${head}: no head branch in the API answer"
      continue
    fi
    info=$(commit_info "$head") || continue
    read -r tree parents <<<"$info"
    if [ "$tree" != "$TAG_TREE" ]; then
      log "skip PR head ${head}: tree ${tree} != tag tree ${TAG_TREE}"
      continue
    fi
    if [ "$parents" != "$TAG_PARENTS" ]; then
      log "skip PR head ${head}: parent ${parents} != tag parent ${TAG_PARENTS}"
      continue
    fi
    echo "$head $ref"
  done <<<"$prs"
}

# runs_for SHA EVENT -> lines "id status conclusion head_sha head_branch path url"
runs_for() {
  gh api "repos/${REPO}/actions/workflows/go-test.yml/runs?head_sha=$1&event=$2&per_page=30" \
    --jq '.workflow_runs[] | "\(.id) \(.status) \(.conclusion // "none") \(.head_sha) \(.head_branch) \(.path) \(.html_url)"'
}

# jobs_green RUN_ID -> 0 when the gate job and every shard job concluded
# success on the latest attempt.
jobs_green() {
  local jobs
  jobs=$(gh api "repos/${REPO}/actions/runs/$1/jobs?filter=latest&per_page=100" \
    --jq '.jobs[] | "\(.conclusion // "none")\t\(.name)"') || return 1
  printf '%s\n' "$jobs" | awk -F'\t' -v gate="$GATE_JOB" '
    $2 == gate { gate_seen = 1; if ($1 != "success") bad = 1 }
    index($2, "Test shard (") == 1 { shards++; if ($1 != "success") bad = 1 }
    END { exit !(gate_seen && shards > 0 && !bad) }'
}

deadline=$(( $(date +%s) + WAIT_SECONDS ))
heads=$(same_tree_heads)
while :; do
  pending=""
  # Each candidate: "<sha> <event> <match> <branch the run must be on>"
  candidates="${TAG_SHA} push exact-sha main"
  while read -r h ref; do
    [ -n "${h:-}" ] && candidates+=$'\n'"${h} pull_request same-tree ${ref}"
  done <<<"$heads"
  while read -r sha event match want_branch; do
    runs=$(runs_for "$sha" "$event") || { log "could not list ${event} runs for ${sha}"; continue; }
    while read -r id status conclusion head_sha branch path url; do
      [ -n "${id:-}" ] || continue
      if [ "$head_sha" != "$sha" ] || [ "$path" != "$WORKFLOW_PATH" ]; then
        log "skip run ${url}: head ${head_sha} path ${path}"
        continue
      fi
      if [ "$event" = push ] && [ "$branch" != main ]; then
        log "skip run ${url}: push to ${branch}, not main"
        continue
      fi
      if [ "$event" = pull_request ] && [ "$branch" != "$want_branch" ]; then
        log "skip run ${url}: on branch ${branch}, not the PR head branch ${want_branch}"
        continue
      fi
      if [ "$status" != completed ]; then
        log "run ${url} (${match}) is ${status}"
        pending="$url"
        continue
      fi
      if [ "$conclusion" != success ]; then
        log "skip run ${url} (${match}): concluded ${conclusion}"
        continue
      fi
      if ! jobs_green "$id"; then
        log "skip run ${url} (${match}): the gate job or a shard job did not conclude success"
        continue
      fi
      answer true "$url" "$match" "Reusing ${url} (${match}): Go tests passed on the code of tag commit ${TAG_SHA} (tree ${TAG_TREE}); the release does not run the suite again."
    done <<<"$runs"
  done <<<"$candidates"
  [ -n "$pending" ] || no_reuse "no completed green candidate run"
  if [ "$(date +%s)" -ge "$deadline" ]; then
    no_reuse "candidate ${pending} still running after ${WAIT_SECONDS}s"
  fi
  log "waiting ${POLL_SECONDS}s for ${pending}"
  sleep "$POLL_SECONDS"
done
