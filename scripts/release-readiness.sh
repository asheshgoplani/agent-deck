#!/usr/bin/env bash
# release-readiness.sh: run before pushing a release tag. Exit 0 = ready.
#
# Usage (from the checkout that will be tagged):
#   scripts/release-readiness.sh [--fixes vX.Y.Z] [--owner-ok]
#
# Checks, all reported, any failure exits 1:
#   1. Release gap: the newest v* tag (any branch) must be at least 24 h
#      old, unless this release fixes it (--fixes <that tag>).
#   2. Version: Version in cmd/agent-deck/main.go is above every v* tag.
#   3. Soak: every commit since the last tag on HEAD's history that touches
#      the update, auto-install or deploy paths, with the time it reached
#      the branch. One less than 12 h ago refuses, unless the owner agreed
#      (--owner-ok).
#   4. Update path end to end: TestReleaseReadiness_UpdatePath builds the
#      candidate, serves it as a fake release and lets an older build update
#      itself (manual and auto-install) in a throwaway HOME. It must PASS.
#
# Why: 24 releases came within 24 h of the previous one, 11 of them fixing
# it, and the auto-update rollout was fixed three times after shipping.
#
# Environment:
#   RELEASE_READINESS_RUNNER  executable that runs go test with `-C <dir> <args>`
#                             (default: deck-test when on PATH, else go test)
#   RELEASE_READINESS_NOW     unix time to use as "now" (tests only)
set -uo pipefail

GAP_HOURS=24
SOAK_HOURS=12
E2E_TEST=TestReleaseReadiness_UpdatePath

# Paths whose changes ship through the updater, the installer or a remote
# deploy, including the TUI's auto-install trigger, the binary watcher and
# self-restart, the ssh/remote deploy, and the child environment the
# unattended install runs with: the files the #2245, #2252 and #2271 bugs
# lived in. (`agent-deck update` itself is in main.go, too broad to list;
# its logic is in update_cli.go and internal/update.) Git pathspecs; quoted
# so bash does not expand the glob.
SOAK_PATHS=(
  internal/update
  internal/childenv
  ':(glob)internal/ui/update_*'
  internal/ui/binary_watch.go
  internal/ui/restart.go
  ':(glob)internal/ui/home*.go'
  ':(glob)internal/session/instance_restart.go'
  internal/session/ssh.go
  cmd/agent-deck/remote_cmd.go
  cmd/agent-deck/update_cli.go
  cmd/agent-deck/notify_daemon_cmd.go
  cmd/agent-deck/remote_update_output.go
  internal/session/update_timer.go
  internal/session/remote_update.go
  internal/session/remote_nudge.go
  install.sh
  uninstall.sh
  .goreleaser.yml
  .github/workflows/release.yml
  scripts/launchd
  tools/dist-local
)

fixes=""
owner_ok=0
while [ $# -gt 0 ]; do
  case $1 in
    --fixes) [ $# -ge 2 ] || { echo "--fixes needs a tag" >&2; exit 2; }; fixes=$2; shift 2 ;;
    --owner-ok) owner_ok=1; shift ;;
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    *) echo "unknown argument: $1 (see --help)" >&2; exit 2 ;;
  esac
done

root=$(git rev-parse --show-toplevel 2>/dev/null) || { echo "not inside a git checkout" >&2; exit 2; }
cd "$root" || exit 2
now=${RELEASE_READINESS_NOW:-$(date +%s)}
problems=()
refuse() { problems+=("$1"); echo "  REFUSE: $1"; }
when() { TZ=Europe/Berlin date -d "@$1" '+%Y-%m-%d %H:%M %Z' 2>/dev/null || TZ=Europe/Berlin date -r "$1" '+%Y-%m-%d %H:%M %Z'; }
hours_since() { echo $(( (now - $1) / 3600 )); }

# Without the remote's tags the gap check could miss a tag pushed from
# another machine, so a failed fetch refuses.
if git remote get-url origin >/dev/null 2>&1; then
  if ! fetch_err=$(git fetch --quiet --tags origin 2>&1); then
    refuse "git fetch --tags origin failed, so the last tag may be missing here: ${fetch_err//$'\n'/ }"
  fi
fi

head_sha=$(git rev-parse HEAD)
echo "candidate: $head_sha"
# Untracked files count too: the end-to-end test builds the whole checkout.
if [ -n "$(git status --porcelain)" ]; then
  refuse "the checkout has uncommitted or untracked files; the check must run on the exact commit you tag"
fi

# first_tag <git tag --sort key>: the first v* tag in that order that is
# not on HEAD (a tag on HEAD is this release, tagged but not pushed yet).
first_tag() {
  local t
  while IFS= read -r t; do
    [ -n "$t" ] || continue
    [ "$(git rev-parse "$t^{commit}")" = "$head_sha" ] && continue
    echo "$t"; return
  done < <(git tag --list 'v[0-9]*' --sort="$1")
}
head_tags=$(git tag --list 'v[0-9]*' --points-at HEAD | tr '\n' ' ')
if [ -n "$head_tags" ]; then
  echo "HEAD is already tagged ${head_tags% }; checking it against the previous tag"
fi
# last_tag: the newest tag on any branch (a hotfix tag on a release branch
# counts for the gap). top_tag: the highest version. base_tag: the last tag
# on HEAD's history, where the soak range starts.
last_tag=$(first_tag -creatordate)
top_tag=$(first_tag -v:refname)
base_tag=$(git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD 2>/dev/null || true)
if [ -n "$base_tag" ] && [ "$(git rev-parse "$base_tag^{commit}")" = "$head_sha" ]; then
  base_tag=$(git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD^ 2>/dev/null || true)
fi
if [ -z "$last_tag" ]; then
  echo "no earlier v* tag; gap and version checks against a tag skipped"
fi
if [ -z "$base_tag" ]; then
  echo "no earlier v* tag reachable from HEAD; soak check skipped"
fi

# 1. Release gap.
echo "1. release gap (>= ${GAP_HOURS} h since the last tag)"
if [ -n "$last_tag" ]; then
  tag_time=$(git for-each-ref --format='%(creatordate:unix)' "refs/tags/$last_tag")
  age=$(hours_since "$tag_time")
  echo "  last tag $last_tag at $(when "$tag_time"), ${age}h ago"
  if [ $(( now - tag_time )) -lt $(( GAP_HOURS * 3600 )) ]; then
    if [ "$fixes" = "$last_tag" ]; then
      echo "  allowed: this release fixes $last_tag"
    elif [ -n "$fixes" ]; then
      refuse "--fixes names $fixes but the last tag is $last_tag"
    else
      refuse "last tag $last_tag is ${age}h old (< ${GAP_HOURS} h); wait, or pass --fixes $last_tag if this release fixes it"
    fi
  elif [ -n "$fixes" ]; then
    echo "  --fixes $fixes not needed: the last tag is over ${GAP_HOURS} h old"
  fi
fi

# 2. Version bump.
echo "2. version"
version=$(sed -n 's/^var Version = "\([0-9][0-9.]*\)".*/\1/p' cmd/agent-deck/main.go | head -1)
if [ -z "$version" ]; then
  refuse "no var Version = \"X.Y.Z\" in cmd/agent-deck/main.go"
elif [ -n "$top_tag" ]; then
  prev=${top_tag#v}
  top=$(printf '%s\n%s\n' "$prev" "$version" | sort -V | tail -1)
  if [ "$version" = "$prev" ] || [ "$top" != "$version" ]; then
    refuse "Version $version in cmd/agent-deck/main.go is not above $top_tag"
  else
    echo "  v$version (highest tag $top_tag)"
  fi
else
  echo "  v$version"
fi

# 3. Soak of update, auto-install and deploy changes.
# Merge time: git records no push time, so a change counts as reached the
# branch at the committer time of the first-parent commit that brought it
# (the merge commit, or GitHub's squash/rebase commit), and never earlier
# than any first-parent commit below it: a commit cannot have landed before
# the commits it sits on, whatever its own date says.
echo "3. soak (update/auto-install/deploy changes merged >= ${SOAK_HOURS} h ago)"
if [ -n "$base_tag" ]; then
  unsoaked=0; listed=0
  touching=$(git log --first-parent --format=%H "$base_tag..HEAD" -- "${SOAK_PATHS[@]}" ':(exclude,glob)**/*_test.go' ':(exclude,glob)**/testdata/**')
  reached=$(git log -1 --format=%ct "$base_tag^{commit}")
  while IFS=$'\t' read -r sha ctime subject; do
    [ -n "$sha" ] || continue
    [ "$ctime" -gt "$reached" ] && reached=$ctime
    printf '%s\n' "$touching" | grep -qx "$sha" || continue
    listed=$((listed + 1))
    age=$(hours_since "$reached")
    mark=""; dated=""
    [ "$reached" != "$ctime" ] && dated=" (commit dated $(when "$ctime"))"
    if [ $(( now - reached )) -lt $(( SOAK_HOURS * 3600 )) ]; then mark="  <- soaked ${age}h"; unsoaked=$((unsoaked + 1)); fi
    echo "  ${sha:0:10} merged $(when "$reached") (${age}h ago)$dated $subject$mark"
  done < <(git log --first-parent --reverse --format='%H%x09%ct%x09%s' "$base_tag..HEAD")
  [ "$listed" = 0 ] && echo "  none since $base_tag"
  if [ "$unsoaked" -gt 0 ]; then
    if [ "$owner_ok" = 1 ]; then
      echo "  allowed: $unsoaked change(s) under ${SOAK_HOURS} h, the owner agreed (--owner-ok)"
    else
      refuse "$unsoaked update/auto-install/deploy change(s) merged less than ${SOAK_HOURS} h ago; let them soak on the deck box, or pass --owner-ok with the owner's agreement"
    fi
  fi
fi

# 4. Update and auto-install paths, end to end.
echo "4. update path end to end ($E2E_TEST)"
# run_go_test -C <dir> <go test args>: the runner used without deck-test.
run_go_test() {
  [ "${1:-}" = -C ] && [ $# -ge 2 ] || { echo "run_go_test: want -C <dir> <args>" >&2; return 2; }
  local dir=$2; shift 2
  (cd "$dir" && go test "$@")
}
runner=${RELEASE_READINESS_RUNNER:-}
if [ -z "$runner" ]; then
  if command -v deck-test >/dev/null 2>&1; then runner=deck-test; else runner=run_go_test; fi
fi
log=$(mktemp "${TMPDIR:-/tmp}/release-readiness.XXXXXX")
"$runner" -C "$root" -count=1 -v -run "^${E2E_TEST}\$" ./internal/releasetests/ >"$log" 2>&1
rc=$?
grep -E -- "--- (PASS|FAIL|SKIP): ${E2E_TEST}|^(ok|FAIL)[[:space:]]" "$log" | sed 's/^/  /'
echo "  test output: $log"
if [ "$rc" != 0 ] || ! grep -q -- "--- PASS: ${E2E_TEST} " "$log" || grep -q -E -- "--- (FAIL|SKIP): ${E2E_TEST}" "$log"; then
  tail -n 40 "$log" | sed 's/^/  | /'
  refuse "$E2E_TEST did not pass (exit $rc)"
fi

echo
if [ ${#problems[@]} -gt 0 ]; then
  echo "NOT READY: ${#problems[@]} problem(s)"
  for p in "${problems[@]}"; do echo "  - $p"; done
  exit 1
fi
echo "READY: tag v$version at $head_sha"
