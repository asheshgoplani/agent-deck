#!/usr/bin/env bash
# Decide whether a pull request is a version-only release commit whose base
# already passed the full Go suite (ASK-161, issue #2929).
#
# A release PR changes only the Version line in cmd/agent-deck/main.go and
# CHANGELOG.md. When its base sha already has a green push "Go tests" run,
# re-running every shard on the same tree plus one string constant costs
# ~16 minutes and tests nothing new. .github/workflows/go-test.yml calls
# this script from its "Detect Go-relevant changes" job; when both checks
# below say yes, the shards are skipped and the "Version-only release check"
# job gates the PR instead. Any doubt means no: the full shards run.
#
# Usage:
#   version-only-diff.sh diff <base> <head>
#       Exit 0 and print the new version when the diff base..head touches
#       only CHANGELOG.md and the single `var Version = "X.Y.Z"` line of
#       cmd/agent-deck/main.go (parsed from the patch, not the file names),
#       the version goes up, and CHANGELOG.md at head has its heading.
#       Exit 1 with the reason on stderr otherwise.
#   version-only-diff.sh pick-run <runs.json> <base-sha> <repo>
#       Read the GitHub API answer for go-test.yml runs and print the id of
#       the newest successful push run on main at exactly <base-sha> in
#       <repo>. Exit 1 when there is none.
#   version-only-diff.sh shards-green <jobs.json>
#       Read the GitHub API answer for that run's jobs. Exit 0 only when it
#       has at least one "Test shard (...)" job, every one of them concluded
#       success (not skipped), and "Full test suite (PR gate)" passed.
#
# The script only reads git objects and the JSON files it is given; the
# workflow does the API calls with the read-only GITHUB_TOKEN, so fork PRs
# take the same path without any secret.

set -euo pipefail

MAIN_GO="cmd/agent-deck/main.go"
CHANGELOG="CHANGELOG.md"
# The whole Version line: capture the version, keep the rest to compare.
VERSION_RE='^var Version = "([0-9]+\.[0-9]+\.[0-9]+)"(.*)$'

no() {
  echo "version-only: no: $*" >&2
  exit 1
}

cmd_diff() {
  local base=$1 head=$2
  git rev-parse --verify --quiet "$base^{commit}" >/dev/null || no "base $base is not a commit"
  git rev-parse --verify --quiet "$head^{commit}" >/dev/null || no "head $head is not a commit"

  local gitdiff=(git -c core.quotePath=false diff --no-ext-diff --no-textconv --no-renames --no-color)

  # 1. File list: modifications of the two allowlisted files only, and
  #    main.go must be one of them.
  local status path saw_main=0 saw_any=0
  while IFS=$'\t' read -r status path; do
    [ -n "$status$path" ] || continue
    saw_any=1
    [ "$status" = "M" ] || no "$path has status $status (only modifications are allowed)"
    case "$path" in
      "$CHANGELOG") ;;
      "$MAIN_GO") saw_main=1 ;;
      *) no "$path is outside the allowlist ($CHANGELOG, $MAIN_GO)" ;;
    esac
  done < <("${gitdiff[@]}" --name-status "$base" "$head")
  [ "$saw_any" = 1 ] || no "empty diff"
  [ "$saw_main" = 1 ] || no "$MAIN_GO is unchanged (no version bump)"

  # 2. Modes: regular files on both sides, no mode change.
  local raw
  while read -r raw; do
    [ -n "$raw" ] || continue
    case "$raw" in
      ":100644 100644 "*) ;;
      *) no "mode change or non-regular file: $raw" ;;
    esac
  done < <("${gitdiff[@]}" --raw "$base" "$head")

  # 3. The main.go patch: exactly one hunk with exactly one removed and one
  #    added line, nothing else in the header than diff/index/---/+++.
  local patch
  patch=$("${gitdiff[@]}" -U0 "$base" "$head" -- "$MAIN_GO")
  local summary
  summary=$(printf '%s\n' "$patch" | awk '
    BEGIN { inbody = 0; hunks = 0; minus = 0; plus = 0; bad = "" }
    /^@@ / { inbody = 1; hunks++; next }
    !inbody {
      if ($0 ~ /^diff --git / || $0 ~ /^index [0-9a-f]+\.\.[0-9a-f]+ 100644$/ || $0 ~ /^--- a\// || $0 ~ /^\+\+\+ b\//) next
      bad = "header line: " $0; exit
    }
    /^-/ { minus++; old = substr($0, 2); next }
    /^\+/ { plus++; new = substr($0, 2); next }
    { bad = "unexpected patch line: " $0; exit }
    END {
      if (bad != "") { print "BAD\t" bad; exit }
      print "OK\t" hunks "\t" minus "\t" plus
      print "OLD\t" old
      print "NEW\t" new
    }')
  local verdict hunks minus plus
  IFS=$'\t' read -r verdict hunks minus plus <<<"$(printf '%s\n' "$summary" | sed -n 1p)"
  [ "$verdict" = "OK" ] || no "$MAIN_GO patch: $(printf '%s\n' "$summary" | sed -n 1p | cut -f2-)"
  [ "$hunks" = 1 ] && [ "$minus" = 1 ] && [ "$plus" = 1 ] ||
    no "$MAIN_GO patch has $hunks hunk(s), $minus removed and $plus added line(s); want 1, 1, 1"
  local old_line new_line
  old_line=$(printf '%s\n' "$summary" | sed -n 's/^OLD\t//p')
  new_line=$(printf '%s\n' "$summary" | sed -n 's/^NEW\t//p')

  [[ "$old_line" =~ $VERSION_RE ]] || no "removed line is not the Version line: $old_line"
  local old_ver=${BASH_REMATCH[1]} old_rest=${BASH_REMATCH[2]}
  [[ "$new_line" =~ $VERSION_RE ]] || no "added line is not the Version line: $new_line"
  local new_ver=${BASH_REMATCH[1]} new_rest=${BASH_REMATCH[2]}
  [ "$old_rest" = "$new_rest" ] || no "the Version line changed beyond the version string"
  [ "$old_ver" != "$new_ver" ] || no "version unchanged ($old_ver)"
  local top
  top=$(printf '%s\n%s\n' "$old_ver" "$new_ver" | sort -V | tail -1)
  [ "$top" = "$new_ver" ] || no "version goes down ($old_ver -> $new_ver)"

  # 4. Exactly one Version line in the new main.go, and the changelog names it.
  local count
  count=$(git show "$head:$MAIN_GO" | grep -cE '^var Version = ' || true)
  [ "$count" = 1 ] || no "$MAIN_GO at head has $count Version lines"
  # grep without -q: an early exit would SIGPIPE git show under pipefail.
  git show "$head:$CHANGELOG" | grep -F "## [$new_ver]" >/dev/null || no "$CHANGELOG has no '## [$new_ver]' heading"

  echo "$new_ver"
}

cmd_pick_run() {
  local runs=$1 base=$2 repo=$3
  local id
  id=$(jq -r --arg sha "$base" --arg repo "$repo" '
    [ .workflow_runs[]?
      | select(.head_sha == $sha
               and .event == "push"
               and .head_branch == "main"
               and .status == "completed"
               and .conclusion == "success"
               and .path == ".github/workflows/go-test.yml"
               and .repository.full_name == $repo
               and .head_repository.full_name == $repo) ]
    | sort_by(.run_started_at) | last | .id // empty' "$runs")
  [ -n "$id" ] || no "no successful push Go tests run on main at $base"
  echo "$id"
}

cmd_shards_green() {
  local jobs=$1
  local verdict
  verdict=$(jq -r '
    [ .jobs[]? | select(.name | startswith("Test shard (")) ] as $s
    | [ .jobs[]? | select(.name == "Full test suite (PR gate)") ] as $g
    | if ($s | length) == 0 then "no shard jobs"
      elif ([ $s[] | select(.conclusion != "success") ] | length) > 0
        then "shard not green: " + ([ $s[] | select(.conclusion != "success") | .name + "=" + (.conclusion // "null") ] | join(", "))
      elif ($g | length) != 1 or $g[0].conclusion != "success" then "gate job not green"
      else "ok " + ($s | length | tostring) end' "$jobs")
  case "$verdict" in
    ok\ *) echo "$verdict shards" ;;
    *) no "$verdict" ;;
  esac
}

case "${1:-}" in
  diff) [ $# = 3 ] || no "usage: $0 diff <base> <head>"; cmd_diff "$2" "$3" ;;
  pick-run) [ $# = 4 ] || no "usage: $0 pick-run <runs.json> <base-sha> <repo>"; cmd_pick_run "$2" "$3" "$4" ;;
  shards-green) [ $# = 2 ] || no "usage: $0 shards-green <jobs.json>"; cmd_shards_green "$2" ;;
  *) no "usage: $0 diff|pick-run|shards-green ..." ;;
esac
