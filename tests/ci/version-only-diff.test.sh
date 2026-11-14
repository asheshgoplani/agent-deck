#!/usr/bin/env bash
# version-only-diff.test.sh: tests for .github/scripts/version-only-diff.sh,
# the go-test.yml step that lets a release PR (Version line + CHANGELOG.md
# only, base already green) skip the shards. Builds throwaway git repos in a
# temp dir and feeds hand-written GitHub API JSON. Every case that is not a
# plain version bump must say no, so the full shards run. Exit 0 on pass, 1
# on any failure. Needs bash, git and jq.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DETECT="$SCRIPT_DIR/../../.github/scripts/version-only-diff.sh"
TMP="$(mktemp -d)"
# KEEP_TMP=1 leaves the temp repos for inspection.
[ "${KEEP_TMP:-}" = 1 ] || trap 'rm -rf "$TMP"' EXIT
ERRORS=0
OUT=""
CODE=0

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

expect() {
  local label="$1" want="$2" match="${3:-}"
  if [ "$CODE" -ne "$want" ]; then
    echo "FAIL $label: exit $CODE, want $want; output: $OUT"
    ERRORS=$((ERRORS + 1)); return
  fi
  if [ -n "$match" ] && ! printf '%s\n' "$OUT" | grep -qF -- "$match"; then
    echo "FAIL $label: output lacks '$match': $OUT"
    ERRORS=$((ERRORS + 1)); return
  fi
  echo "ok   $label"
}

VLINE='var Version = "%s" // overridden at build time via -ldflags "-X main.Version=..."'

# new_repo <dir>: a base commit with main.go at 1.2.3, a changelog, a README.
new_repo() {
  local d="$TMP/$1"
  mkdir -p "$d/cmd/agent-deck"
  git -C "$d" init -q -b main
  {
    echo 'package main'
    echo
    echo 'import "fmt"'
    echo
    # shellcheck disable=SC2059
    printf "$VLINE\n" 1.2.3
    echo
    echo 'func main() { fmt.Println(Version) }'
  } >"$d/cmd/agent-deck/main.go"
  printf '# Changelog\n\n## [Unreleased]\n\n## [1.2.3] - 2026-01-01\n\n- old\n' >"$d/CHANGELOG.md"
  echo readme >"$d/README.md"
  git -C "$d" add -A
  git -C "$d" commit -qm base
  git -C "$d" tag base
  REPO="$d"
}

set_version() {
  local v="$1"
  # shellcheck disable=SC2059
  local line
  line=$(printf "$VLINE" "$v")
  sed -i "s|^var Version = .*|$line|" "$REPO/cmd/agent-deck/main.go"
}

add_changelog() {
  sed -i "s|^## \[Unreleased\]|## [Unreleased]\n\n## [$1] - 2026-10-31\n\n- new|" "$REPO/CHANGELOG.md"
}

commit() { git -C "$REPO" add -A && git -C "$REPO" commit -qm head; }

run_diff() {
  CODE=0
  OUT="$(cd "$REPO" && bash "$DETECT" diff base HEAD 2>&1)" || CODE=$?
}

# --- diff: the one shape that says yes ---------------------------------
new_repo ok; set_version 1.2.4; add_changelog 1.2.4; commit; run_diff
expect "version line + changelog" 0 "1.2.4"

# The real release commit shape (two-digit patch, 1.16.34 -> 1.16.35).
new_repo real; set_version 1.16.34; add_changelog 1.16.34; commit; git -C "$REPO" tag -f base >/dev/null
set_version 1.16.35; add_changelog 1.16.35; commit; run_diff
expect "1.16.34 -> 1.16.35" 0 "1.16.35"

# A changelog far bigger than a pipe buffer, heading near the top: the
# heading check must not die of SIGPIPE under pipefail (the real
# CHANGELOG.md is several hundred KB).
new_repo bigchlog; seq 1 200000 | sed 's/^/- entry /' >>"$REPO/CHANGELOG.md"; commit; git -C "$REPO" tag -f base >/dev/null
set_version 1.2.4; add_changelog 1.2.4; commit; run_diff
expect "large changelog" 0 "1.2.4"

# --- diff: everything else says no -----------------------------------
new_repo other; set_version 1.2.4; add_changelog 1.2.4; echo x >>"$REPO/README.md"; commit; run_diff
expect "another file changed" 1 "README.md is outside the allowlist"

new_repo extra; set_version 1.2.4; add_changelog 1.2.4; echo '// x' >>"$REPO/cmd/agent-deck/main.go"; commit; run_diff
expect "main.go changed beyond the Version line" 1 "want 1, 1, 1"

new_repo swap; add_changelog 1.2.4
sed -i 's|^func main() { fmt.Println(Version) }|func main() { fmt.Println("pwned") }|' "$REPO/cmd/agent-deck/main.go"; commit; run_diff
expect "one other main.go line swapped, no version change" 1 "not the Version line"

new_repo comment; add_changelog 1.2.4
sed -i 's|^var Version = .*|var Version = "1.2.4" // changed comment|' "$REPO/cmd/agent-deck/main.go"; commit; run_diff
expect "Version line comment changed" 1 "beyond the version string"

new_repo expr; add_changelog 1.2.4
sed -i 's|^var Version = .*|var Version = "1.2.4" + os.Getenv("X")|' "$REPO/cmd/agent-deck/main.go"; commit; run_diff
expect "Version line turned into an expression" 1 "beyond the version string"

new_repo down; set_version 1.2.2; add_changelog 1.2.2; commit; run_diff
expect "version goes down" 1 "goes down"

new_repo nochlog; set_version 1.2.4; commit; run_diff
expect "no changelog heading for the new version" 1 "no '## [1.2.4]' heading"

new_repo chlogonly; add_changelog 1.2.4; commit; run_diff
expect "changelog only (not a release commit)" 1 "is unchanged"

new_repo mode; set_version 1.2.4; add_changelog 1.2.4; chmod +x "$REPO/cmd/agent-deck/main.go"; commit; run_diff
expect "main.go mode change" 1 "mode change"

new_repo newgo; set_version 1.2.4; add_changelog 1.2.4; echo 'package main' >"$REPO/cmd/agent-deck/extra.go"; commit; run_diff
expect "new Go file" 1 "extra.go has status A"

new_repo rename; set_version 1.2.4; add_changelog 1.2.4; git -C "$REPO" mv CHANGELOG.md CHANGES.md; commit; run_diff
expect "changelog renamed" 1 "status"

new_repo empty; git -C "$REPO" commit -q --allow-empty -m head; run_diff
expect "empty diff" 1 "empty diff"

# --- pick-run ----------------------------------------------------------
SHA=cdd00b509315f3b5a615dcaa92331d8166bdf0a3
R=asheshgoplani/agent-deck
run_json() { # id sha event branch conclusion repo started
  printf '{"id":%s,"head_sha":"%s","event":"%s","head_branch":"%s","status":"completed","conclusion":"%s","path":".github/workflows/go-test.yml","repository":{"full_name":"%s"},"head_repository":{"full_name":"%s"},"run_started_at":"%s"}' \
    "$1" "$2" "$3" "$4" "$5" "$6" "$6" "$7"
}
pick() {
  printf '{"workflow_runs":[%s]}' "$1" >"$TMP/runs.json"
  CODE=0; OUT="$(bash "$DETECT" pick-run "$TMP/runs.json" "$SHA" "$R" 2>&1)" || CODE=$?
}

pick "$(run_json 41158827633 $SHA push main success $R 2026-10-31T02:11:11Z)"
expect "green push run on main" 0 "41158827633"

pick "$(run_json 1 $SHA push main failure $R 2026-10-31T02:11:11Z),$(run_json 2 $SHA push main success $R 2026-10-31T03:00:00Z)"
expect "newest green run chosen over a red one" 0 "2"

pick "$(run_json 3 $SHA push main cancelled $R 2026-10-31T02:11:11Z)"
expect "only a cancelled run" 1 "no successful push"

pick "$(run_json 4 $SHA pull_request main success $R 2026-10-31T02:11:11Z)"
expect "only a pull_request run" 1 "no successful push"

pick "$(run_json 5 deadbeef push main success $R 2026-10-31T02:11:11Z)"
expect "green run on another sha" 1 "no successful push"

pick "$(run_json 6 $SHA push feature success $R 2026-10-31T02:11:11Z)"
expect "green run on another branch" 1 "no successful push"

pick "$(run_json 7 $SHA push main success someone/fork 2026-10-31T02:11:11Z)"
expect "green run in a fork" 1 "no successful push"

pick ""
expect "no runs at all" 1 "no successful push"

# --- shards-green --------------------------------------------------------
job() { printf '{"name":"%s","conclusion":"%s"}' "$1" "$2"; }
green() {
  printf '{"jobs":[%s]}' "$1" >"$TMP/jobs.json"
  CODE=0; OUT="$(bash "$DETECT" shards-green "$TMP/jobs.json" 2>&1)" || CODE=$?
}
ALL="$(job 'Detect Go-relevant changes' success),$(job 'Test shard (cmd-a)' success),$(job 'Test shard (session)' success),$(job 'Test shard (proof)' success)"

green "$ALL,$(job 'Full test suite (PR gate)' success)"
expect "every shard and the gate green" 0 "ok 3 shards"

green "$ALL,$(job 'Test shard (rest)' failure),$(job 'Full test suite (PR gate)' success)"
expect "one shard red" 1 "Test shard (rest)=failure"

green "$ALL,$(job 'Test shard (rest)' skipped),$(job 'Full test suite (PR gate)' success)"
expect "one shard skipped" 1 "Test shard (rest)=skipped"

green "$(job 'Detect Go-relevant changes' success),$(job 'Full test suite (PR gate)' success)"
expect "no shards ran (gate passed on a non-Go diff)" 1 "no shard jobs"

green "$ALL,$(job 'Full test suite (PR gate)' failure)"
expect "gate job red" 1 "gate job not green"

green "$ALL"
expect "gate job missing" 1 "gate job not green"

if [ "$ERRORS" -ne 0 ]; then
  echo "$ERRORS failure(s)"
  exit 1
fi
echo "all version-only-diff cases pass"
