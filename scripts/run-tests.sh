#!/usr/bin/env bash
# Contain one official test invocation and reap abandoned agent-deck test roots.
set -uo pipefail

[ "$#" -gt 0 ] || { echo "usage: scripts/run-tests.sh <command> [args...]" >&2; exit 2; }

base="${AGENT_DECK_TEST_TMP_BASE:-${TMPDIR:-/tmp}}"
mkdir -p "$base" || exit 1
base=$(cd "$base" && pwd -P) || exit 1
uid=$(id -u)
now=$(date +%s)
max_age="${AGENT_DECK_TEST_TMP_MAX_AGE_SECONDS:-86400}"
marker_name='.agent-deck-test-root'
go_mod_cache=''
go_build_cache=''
if command -v go >/dev/null 2>&1; then
  go_mod_cache=$(go env GOMODCACHE 2>/dev/null || true)
  go_build_cache=$(go env GOCACHE 2>/dev/null || true)
fi

path_uid() {
  stat -f '%u' "$1" 2>/dev/null || stat -c '%u' "$1" 2>/dev/null
}

remove_managed_root() {
  local path="$1"
  case "$path" in "$base"/adtr-*) ;; *) return 1 ;; esac
  [ ! -L "$path" ] || return 1
  chmod -R u+w -- "$path" 2>/dev/null || true
  rm -rf -- "$path"
}

reap_stale_roots() {
  local candidate marker schema repo created pid resolved
  for candidate in "$base"/adtr-*; do
    [ -d "$candidate" ] || continue
    [ ! -L "$candidate" ] || continue
    marker="$candidate/$marker_name"
    [ -f "$marker" ] && [ ! -L "$marker" ] || continue
    [ "$(path_uid "$candidate")" = "$uid" ] || continue
    [ "$(path_uid "$marker")" = "$uid" ] || continue
    resolved=$(cd "$candidate" 2>/dev/null && pwd -P) || continue
    [ "$(dirname "$resolved")" = "$base" ] || continue

    schema=$(sed -n 's/^schema=//p' "$marker")
    repo=$(sed -n 's/^repo=//p' "$marker")
    created=$(sed -n 's/^created=//p' "$marker")
    pid=$(sed -n 's/^pid=//p' "$marker")
    [ "$schema" = 1 ] && [ "$repo" = agent-deck ] || continue
    case "$created:$pid" in *[!0-9:]*|:|*:|*:*:*) continue ;; esac
    [ "$((now - created))" -ge "$max_age" ] 2>/dev/null || continue
    kill -0 "$pid" 2>/dev/null && continue
    remove_managed_root "$resolved" || true
  done
}

reap_stale_roots
run_root=$(mktemp -d "$base/adtr-$$.XXXXXX") || exit 1
printf 'schema=1\nrepo=agent-deck\ncreated=%s\npid=%s\n' "$now" "$$" >"$run_root/$marker_name"

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  remove_managed_root "$run_root" || true
  exit "$status"
}
trap cleanup EXIT INT TERM

export TMPDIR="$run_root"
export GOTMPDIR="$run_root"
export PLAYWRIGHT_TMP_DIR="$run_root"
[ -z "$go_mod_cache" ] || export GOMODCACHE="$go_mod_cache"
[ -z "$go_build_cache" ] || export GOCACHE="$go_build_cache"
"$@"
