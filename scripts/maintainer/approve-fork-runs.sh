#!/usr/bin/env bash
# approve-fork-runs.sh: list, and on request approve, fork PR workflow runs that
# GitHub holds at `action_required` ("Approve and run") for first-time contributors.
#
# Maintainer-run only. Default is a read-only dry run: nothing is written unless
# --approve is passed (and, for PRs that touch CI plumbing, an explicit --include).
#
# Safety model:
#   * Only runs whose head_sha is the CURRENT head of an OPEN fork PR are ever
#     approved. Runs on superseded or closed heads are counted as stale and are
#     never approved (nor deleted) by this script.
#   * A PR whose diff touches a CI-sensitive path (SENSITIVE_RE) is held unless
#     named with --include, because `pull_request` runs execute the workflow
#     files and scripts from the PR's own merge ref: read those by hand first.
#     If the diff cannot be listed, the PR is treated as sensitive.
#   * The PR head is re-read right before approving; if it moved, the PR is skipped.
#   * Approved fork `pull_request` runs still get a read-only GITHUB_TOKEN and no
#     repo secrets; approval does not change that. It does spend runner minutes
#     and lets the PR's code execute on a GitHub-hosted runner.
#
# Usage:
#   approve-fork-runs.sh                    # dry run: table of waiting PRs
#   approve-fork-runs.sh --approve          # approve current-head runs of safe PRs
#   approve-fork-runs.sh --approve --include 2551 --include 2554
#   approve-fork-runs.sh --pr 2550          # limit to one PR (repeatable)
# Env: REPO (owner/name; default parsed from the git origin remote),
#      SENSITIVE_RE (extended regex over changed paths).
set -euo pipefail

SENSITIVE_RE="${SENSITIVE_RE:-^(\.github/|Makefile|\.golangci|\.goreleaser|scripts/ci-|tests/ci/|go\.mod|go\.sum)}"
APPROVE=0
INCLUDE=()
ONLY=()

die() { echo "approve-fork-runs: $*" >&2; exit 2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --approve) APPROVE=1 ;;
    --include|--pr)
      [ $# -ge 2 ] || die "$1 needs a PR number"
      case "$2" in '' | *[!0-9]*) die "$1 needs a PR number, got '$2'" ;; esac
      if [ "$1" = --include ]; then INCLUDE+=("$2"); else ONLY+=("$2"); fi
      shift ;;
    -h|--help) sed -n '2,27p' "$0"; exit 0 ;;
    *) die "unknown arg: $1" ;;
  esac
  shift
done

if [ -z "${REPO:-}" ]; then
  origin="$(git remote get-url origin 2>/dev/null || true)"
  REPO="$(printf '%s\n' "${origin%/}" | sed -nE 's#^.*github\.com[:/]+([^/]+/[^/]+)$#\1#p' | sed -E 's#\.git$##')"
  [ -n "$REPO" ] || die "set REPO=owner/name (could not parse a GitHub origin remote: '${origin}')"
fi

# Bash 3.2 treats "${arr[@]}" of an empty array as unbound under set -u, so
# callers check the length first.
in_list() { local x="$1" y; shift; for y in "$@"; do [ "$x" = "$y" ] && return 0; done; return 1; }

# One TSV row per held run: id, head_sha, workflow name, head repo.
runs="$(gh api --paginate "repos/$REPO/actions/runs?status=action_required&per_page=100" \
  --jq '.workflow_runs[] | [.id, .head_sha, .name, .head_repository.full_name] | @tsv')"
# One TSV row per open PR whose head lives in a fork: number, head sha, author, title.
prs="$(gh api --paginate "repos/$REPO/pulls?state=open&per_page=100" \
  --jq '.[] | select(.head.repo.fork == true) | [.number, .head.sha, .user.login, .title] | @tsv')"

heads="$(printf '%s\n' "$prs" | cut -f2 | tr '\n' ' ')"
total="$(printf '%s\n' "$runs" | awk 'NF{c++} END{print c+0}')"
stale="$(printf '%s\n' "$runs" | awk -F'\t' -v heads="$heads" '
  BEGIN { n = split(heads, a, " "); for (i = 1; i <= n; i++) h[a[i]] = 1 }
  NF && !($2 in h) { c++ } END { print c+0 }')"
echo "action_required runs: $total total, $stale on superseded or closed heads (never approved by this script)"
# The runs API returns at most 1000 results when filtering by status; runs past
# that cap are not listed and so are simply not approved this time.
if [ "$total" -ge 1000 ]; then
  echo "note: GitHub caps this listing at 1000 runs; re-run after approving to reach the rest"
fi
echo
printf '%-6s %-16s %-5s %-9s %s\n' PR AUTHOR RUNS CLASS TITLE

approved=0
failed=0
while IFS=$'\t' read -r pr sha author title <&3; do
  [ -n "$pr" ] || continue
  if [ ${#ONLY[@]} -gt 0 ] && ! in_list "$pr" "${ONLY[@]}"; then continue; fi
  ids="$(printf '%s\n' "$runs" | awk -F'\t' -v s="$sha" '$2 == s { print $1 }')"
  [ -n "$ids" ] || continue
  n="$(printf '%s\n' "$ids" | wc -l | tr -d ' ')"

  if files="$(gh pr diff "$pr" -R "$REPO" --name-only)"; then
    hits="$(printf '%s\n' "$files" | grep -E "$SENSITIVE_RE" || true)"
  else
    hits="(diff unavailable, treated as sensitive)"
  fi
  class=safe
  [ -z "$hits" ] || class=SENSITIVE
  printf '%-6s %-16s %-5s %-9s %s\n' "#$pr" "$author" "$n" "$class" "${title:0:60}"
  if [ -n "$hits" ]; then
    printf '%s\n' "$hits" | sed 's/^/         touches: /'
  fi

  [ "$APPROVE" -eq 1 ] || continue
  if [ "$class" = SENSITIVE ] && { [ ${#INCLUDE[@]} -eq 0 ] || ! in_list "$pr" "${INCLUDE[@]}"; }; then
    echo "         held: sensitive paths; re-run with --include $pr after reading the diff"
    continue
  fi
  # Re-check the head right before approving so a push in between is not approved blind.
  live="$(gh api "repos/$REPO/pulls/$pr" --jq .head.sha || true)"
  if [ "$live" != "$sha" ]; then
    echo "         skipped: head moved to ${live:0:8} since listing"
    continue
  fi
  ok=0
  for id in $ids; do
    if gh api -X POST "repos/$REPO/actions/runs/$id/approve" --silent; then
      ok=$((ok + 1))
    else
      failed=$((failed + 1))
      echo "         failed to approve run $id" >&2
    fi
  done
  approved=$((approved + ok))
  echo "         approved $ok of $n runs on ${sha:0:8}"
done 3<<EOF
$prs
EOF

echo
if [ "$APPROVE" -eq 1 ]; then
  echo "approved runs: $approved"
  [ "$failed" -eq 0 ] || { echo "failed approvals: $failed" >&2; exit 1; }
else
  echo "dry run: nothing approved (pass --approve)"
fi
