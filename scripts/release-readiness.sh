#!/usr/bin/env bash
# release-readiness.sh: run before pushing a release tag. Exit 0 = ready.
#
# Usage (from the checkout that will be tagged):
#   scripts/release-readiness.sh [--fixes vX.Y.Z] [--owner-ok]
#
# Checks, all reported, any failure exits 1:
#   1. Release gap: the last tag must be at least 24 h old, unless this
#      release fixes it (--fixes <that tag>).
#   2. Version: Version in cmd/agent-deck/main.go is above the last tag.
#   3. Soak: every commit since the last tag that touches the update,
#      auto-install or deploy paths, with its merge time. One merged less
#      than 12 h ago refuses, unless the owner agreed (--owner-ok).
#   4. Update path end to end: TestReleaseReadiness_UpdatePath builds the
#      candidate, serves it as a fake release and lets an older build update
#      itself (manual and auto-install) in a throwaway HOME. It must PASS.
#
# Why: 24 releases came within 24 h of the previous one, 11 of them fixing
# it, and the auto-update rollout was fixed three times after shipping.
#
# Environment:
#   RELEASE_READINESS_RUNNER  command that runs go test with `-C <dir> <args>`
#                             (default: deck-test when on PATH, else go test)
#   RELEASE_READINESS_NOW     unix time to use as "now" (tests only)
set -uo pipefail

GAP_HOURS=24
SOAK_HOURS=12
E2E_TEST=TestReleaseReadiness_UpdatePath

# Paths whose changes ship through the updater, the installer or a remote
# deploy. (`agent-deck update` itself is in main.go, too broad to list; its
# logic is in update_cli.go and internal/update.)
SOAK_PATHS=(
  internal/update
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
    -h|--help) sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $1 (see --help)" >&2; exit 2 ;;
  esac
done

root=$(git rev-parse --show-toplevel 2>/dev/null) || { echo "not inside a git checkout" >&2; exit 2; }
cd "$root" || exit 2
now=${RELEASE_READINESS_NOW:-$(date +%s)}
problems=()
refuse() { problems+=("$1"); echo "  REFUSE: $1"; }
when() { TZ=Europe/Berlin date -d "@$1" '+%Y-%m-%d %H:%M %Z' 2>/dev/null || date -r "$1" '+%Y-%m-%d %H:%M %Z'; }
hours_since() { echo $(( (now - $1) / 3600 )); }

if git remote get-url origin >/dev/null 2>&1; then
  git fetch --quiet --tags origin 2>/dev/null || echo "warning: git fetch --tags origin failed; using the local tags"
fi

head_sha=$(git rev-parse HEAD)
echo "candidate: $head_sha"
if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
  refuse "the checkout has uncommitted changes; the check must run on the exact commit you tag"
fi

last_tag=$(git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD 2>/dev/null || true)
if [ -n "$last_tag" ] && [ "$(git rev-parse "$last_tag^{commit}")" = "$head_sha" ]; then
  # Tagged locally but not pushed yet: compare with the tag before it.
  echo "HEAD is already tagged $last_tag; checking it against the previous tag"
  last_tag=$(git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD^ 2>/dev/null || true)
fi
if [ -z "$last_tag" ]; then
  echo "no earlier v* tag reachable from HEAD; gap and soak checks skipped"
fi

# 1. Release gap.
echo "1. release gap (>= ${GAP_HOURS} h since the last tag)"
if [ -n "$last_tag" ]; then
  tag_time=$(git for-each-ref --format='%(creatordate:unix)' "refs/tags/$last_tag")
  age=$(hours_since "$tag_time")
  echo "  last tag $last_tag at $(when "$tag_time"), ${age}h ago"
  if [ -n "$fixes" ] && [ "$fixes" != "$last_tag" ]; then
    refuse "--fixes names $fixes but the last tag is $last_tag"
  elif [ $(( now - tag_time )) -lt $(( GAP_HOURS * 3600 )) ]; then
    if [ "$fixes" = "$last_tag" ]; then
      echo "  allowed: this release fixes $last_tag"
    else
      refuse "last tag $last_tag is ${age}h old (< ${GAP_HOURS} h); wait, or pass --fixes $last_tag if this release fixes it"
    fi
  fi
fi

# 2. Version bump.
echo "2. version"
version=$(sed -n 's/^var Version = "\([0-9][0-9.]*\)".*/\1/p' cmd/agent-deck/main.go | head -1)
if [ -z "$version" ]; then
  refuse "no var Version = \"X.Y.Z\" in cmd/agent-deck/main.go"
elif [ -n "$last_tag" ]; then
  prev=${last_tag#v}
  top=$(printf '%s\n%s\n' "$prev" "$version" | sort -V | tail -1)
  if [ "$version" = "$prev" ] || [ "$top" != "$version" ]; then
    refuse "Version $version in cmd/agent-deck/main.go is not above $last_tag"
  else
    echo "  v$version (last tag $last_tag)"
  fi
else
  echo "  v$version"
fi

# 3. Soak of update, auto-install and deploy changes.
echo "3. soak (update/auto-install/deploy changes merged >= ${SOAK_HOURS} h ago)"
if [ -n "$last_tag" ]; then
  unsoaked=0; listed=0
  while IFS=$'\t' read -r sha ctime subject; do
    [ -n "$sha" ] || continue
    listed=$((listed + 1))
    age=$(hours_since "$ctime")
    mark=""
    if [ $(( now - ctime )) -lt $(( SOAK_HOURS * 3600 )) ]; then mark="  <- soaked ${age}h"; unsoaked=$((unsoaked + 1)); fi
    echo "  ${sha:0:10} merged $(when "$ctime") (${age}h ago) $subject$mark"
  done < <(git log --first-parent --format='%H%x09%ct%x09%s' "$last_tag..HEAD" -- "${SOAK_PATHS[@]}" ':(exclude,glob)**/*_test.go' ':(exclude,glob)**/testdata/**')
  [ "$listed" = 0 ] && echo "  none since $last_tag"
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
run_go_test() {
  local dir=$1; shift
  (cd "$dir" && go test "$@")
}
runner=${RELEASE_READINESS_RUNNER:-}
if [ -z "$runner" ]; then
  if command -v deck-test >/dev/null 2>&1; then runner=deck-test; else runner=run_go_test; fi
fi
log=$(mktemp "${TMPDIR:-/tmp}/release-readiness.XXXXXX")
$runner -C "$root" -count=1 -v -run "^${E2E_TEST}\$" ./internal/releasetests/ >"$log" 2>&1
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
