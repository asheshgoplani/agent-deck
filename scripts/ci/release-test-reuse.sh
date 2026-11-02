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
#   version-only  (rule R, ASK-164) the same-tree PR run above, when it is
#              a version-only release PR run (#3003): its shards were
#              skipped, its `Version-only release check` and gate jobs
#              concluded success, AND, checked here again from scratch:
#              the tag commit's parent P (the PR's base) has a push run of
#              go-test.yml on main whose gate job and every shard job
#              concluded success, and P's own
#              .github/scripts/version-only-diff.sh (never the tag's copy)
#              says P..tag changes only the `var Version` line and
#              CHANGELOG.md. The tag tree is then P's fully tested tree plus
#              one version string, which the owner accepts as a full run.
#              A base without the detector, a detector that says no or
#              fails, a red or missing base run, or a merge commit all mean
#              no: the release runs the shards.
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
VERSION_ONLY_JOB='Version-only release check'
DETECTOR_PATH=.github/scripts/version-only-diff.sh

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

# Rule R, part 1 (git only, no API): does the tag parent's own detector say
# parent..tag is version-only? Needs the parent's objects in the checkout
# (release.yml fetches depth 2). Sets VO_OK=1 and VO_VERSION on yes.
VO_OK=0
VO_VERSION=""
version_only_diff() {
  local detect
  case "${TAG_PARENTS:-}" in "" | *" "*) log "rule R: tag commit has ${TAG_PARENTS:-no} parent(s), not one"; return 0 ;; esac
  detect=$(mktemp "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/version-only-diff.XXXXXX") || return 0
  if ! git show "${TAG_PARENTS}:${DETECTOR_PATH}" >"$detect" 2>/dev/null; then
    log "rule R: base ${TAG_PARENTS} has no ${DETECTOR_PATH} (or it is not in this checkout)"
    rm -f "$detect"
    return 0
  fi
  if VO_VERSION=$(bash "$detect" diff "$TAG_PARENTS" "$TAG_SHA"); then
    VO_OK=1
    log "rule R: base ${TAG_PARENTS}'s detector says ${TAG_PARENTS}..${TAG_SHA} is version-only (${VO_VERSION})"
  else
    log "rule R: base ${TAG_PARENTS}'s detector says ${TAG_PARENTS}..${TAG_SHA} is not version-only"
  fi
  rm -f "$detect"
}

# vo_jobs_green RUN_ID -> 0 when the run is a version-only PR pass: exactly
# one gate job and one version-only job, both success on the latest attempt,
# and no shard job that concluded anything but skipped or success.
vo_jobs_green() {
  local jobs
  jobs=$(gh api "repos/${REPO}/actions/runs/$1/jobs?filter=latest&per_page=100" \
    --jq '.jobs[] | "\(.conclusion // "none")\t\(.name)"') || return 1
  printf '%s\n' "$jobs" | awk -F'\t' -v gate="$GATE_JOB" -v vo="$VERSION_ONLY_JOB" '
    $2 == gate { g++; if ($1 != "success") bad = 1 }
    $2 == vo { v++; if ($1 != "success") bad = 1 }
    index($2, "Test shard (") == 1 { if ($1 != "skipped" && $1 != "success") bad = 1 }
    END { exit !(g == 1 && v == 1 && !bad) }'
}

# base_push_state -> "green URL" | "pending URL" | "none -": the tag parent's
# push run of go-test.yml on main with the gate and every shard green.
base_push_state() {
  local runs id status conclusion head_sha branch path url state="none -"
  runs=$(runs_for "$TAG_PARENTS" push) || { log "rule R: could not list push runs for base ${TAG_PARENTS}"; echo "none -"; return; }
  while read -r id status conclusion head_sha branch path url; do
    [ -n "${id:-}" ] || continue
    if [ "$head_sha" != "$TAG_PARENTS" ] || [ "$path" != "$WORKFLOW_PATH" ] || [ "$branch" != main ]; then
      log "rule R: skip base run ${url}: head ${head_sha} branch ${branch} path ${path}"
      continue
    fi
    if [ "$status" != completed ]; then
      log "rule R: base run ${url} is ${status}"
      state="pending $url"
      continue
    fi
    if [ "$conclusion" != success ]; then
      log "rule R: skip base run ${url}: concluded ${conclusion}"
      continue
    fi
    if ! jobs_green "$id"; then
      log "rule R: skip base run ${url}: the gate job or a shard job did not conclude success"
      continue
    fi
    echo "green $url"
    return
  done <<<"$runs"
  echo "$state"
}

deadline=$(( $(date +%s) + WAIT_SECONDS ))
heads=$(same_tree_heads)
[ -z "$heads" ] || version_only_diff
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
        if [ "$match" = same-tree ] && [ "$VO_OK" = 1 ] && vo_jobs_green "$id"; then
          read -r bstate burl <<<"$(base_push_state)"
          case "$bstate" in
            green)
              answer true "$url" version-only "Reusing ${url} (version-only, rule R): the release PR's version-only check passed on the tree of tag commit ${TAG_SHA} (tree ${TAG_TREE}), its base ${TAG_PARENTS} passed every shard in ${burl}, and the base's detector says the tag changes only the Version line (${VO_VERSION}) and CHANGELOG.md; the release does not run the suite again." ;;
            pending)
              pending="$burl"
              continue ;;
          esac
          log "skip run ${url} (version-only): base ${TAG_PARENTS} has no push run with every shard green"
          continue
        fi
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
