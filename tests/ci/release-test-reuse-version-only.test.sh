#!/usr/bin/env bash
# release-test-reuse-version-only.test.sh: tests for rule R in
# scripts/ci/release-test-reuse.sh (ASK-164). A version-only release PR
# (#3003) skips the shards on its own run, so the release may reuse that run
# only when the tag commit's parent (the PR's base) has a push Go tests run
# with every shard green, and the BASE's .github/scripts/version-only-diff.sh
# says parent..tag changes only the Version line and CHANGELOG.md.
#
# Builds a throwaway git repo (real shas, so the detector reads real objects)
# and answers `gh api` from JSON fixtures, like the Go test of the same
# script. Exit 0 on pass, 1 on any failure. Needs bash, git and jq.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$SCRIPT_DIR/../.."
REUSE="$ROOT/scripts/ci/release-test-reuse.sh"
DETECT="$ROOT/.github/scripts/version-only-diff.sh"
TMP="$(mktemp -d)"
[ "${KEEP_TMP:-}" = 1 ] || trap 'rm -rf "$TMP"' EXIT
ERRORS=0
R=acme/agent-deck

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

# Fake gh: `gh api <path> [--jq expr]` answers $FAKE_GH_DIR/<key>.json, or
# <key>.1.json, <key>.2.json ... in order (the last repeats).
mkdir -p "$TMP/bin"
cat >"$TMP/bin/gh" <<'GH'
#!/usr/bin/env bash
[ "$1" = api ] || { echo "fake gh: only api" >&2; exit 2; }
path=$2; shift 2
expr=.
if [ "${1:-}" = --jq ]; then expr=$2; fi
key=$(printf %s "$path" | tr -c 'A-Za-z0-9' _)
dir=$FAKE_GH_DIR
if [ -f "$dir/$key.1.json" ]; then
  n=$(( $(cat "$dir/$key.count" 2>/dev/null || echo 0) + 1 ))
  echo "$n" > "$dir/$key.count"
  while [ "$n" -gt 1 ] && [ ! -f "$dir/$key.$n.json" ]; do n=$((n - 1)); done
  f="$dir/$key.$n.json"
else
  f="$dir/$key.json"
fi
[ -f "$f" ] || { echo "fake gh: HTTP 404 for $path" >&2; exit 1; }
jq -r "$expr" "$f"
GH
chmod +x "$TMP/bin/gh"

VLINE='var Version = "%s" // overridden at build time via -ldflags "-X main.Version=..."'

# new_repo DIR [detector-source]: commit P (1.2.3, with the detector), then
# the release commit T (1.2.4 + changelog heading). Sets P, T, TREE, D.
new_repo() {
  D="$TMP/$1"
  local det=${2:-$DETECT}
  mkdir -p "$D/cmd/agent-deck" "$D/.github/scripts"
  git -C "$D" init -q -b main
  # shellcheck disable=SC2059
  printf "package main\n\n$VLINE\n\nfunc main() {}\n" 1.2.3 >"$D/cmd/agent-deck/main.go"
  printf '# Changelog\n\n## [1.2.3] - 2026-01-01\n\n- old\n' >"$D/CHANGELOG.md"
  if [ "$det" != none ]; then cp "$det" "$D/.github/scripts/version-only-diff.sh"; fi
  echo readme >"$D/README.md"
  git -C "$D" add -A && git -C "$D" commit -qm base
  P=$(git -C "$D" rev-parse HEAD)
}
release_commit() { # release_commit [extra-file]
  # Portable in-place edit: BSD sed (macOS) takes -i's next arg as a suffix.
  sed 's/"1.2.3"/"1.2.4"/' "$D/cmd/agent-deck/main.go" >"$D/main.go.tmp" &&
    mv "$D/main.go.tmp" "$D/cmd/agent-deck/main.go"
  printf '# Changelog\n\n## [1.2.4] - 2026-02-01\n\n- new\n\n## [1.2.3] - 2026-01-01\n\n- old\n' >"$D/CHANGELOG.md"
  if [ -n "${1:-}" ]; then echo change >>"$D/$1"; fi
  git -C "$D" add -A && git -C "$D" commit -qm release
  T=$(git -C "$D" rev-parse HEAD)
  TREE=$(git -C "$D" rev-parse "HEAD^{tree}")
}

H=2222222222222222222222222222222222222222
REF=release/v1.2.4

fx() { # fx <api path> <json>
  local key
  key=$(printf %s "$1" | tr -c 'A-Za-z0-9' _)
  printf '%s\n' "$2" >"$FX/$key.json"
}
fxn() { # fxn <api path> <n> <json>
  local key
  key=$(printf %s "$1" | tr -c 'A-Za-z0-9' _)
  printf '%s\n' "$3" >"$FX/$key.$2.json"
}
run() { # run <id> <status> <conclusion> <head_sha> <branch>
  local c=null
  [ "$3" = - ] || c="\"$3\""
  printf '{"id":%s,"status":"%s","conclusion":%s,"head_sha":"%s","head_branch":"%s","path":".github/workflows/go-test.yml","html_url":"https://github.com/%s/actions/runs/%s"}' \
    "$1" "$2" "$c" "$4" "$5" "$R" "$1"
}
runs() { local IFS=,; echo "{\"workflow_runs\":[$*]}"; }
jobs() { # jobs name conclusion ...
  local out="" sep=""
  while [ $# -ge 2 ]; do out+="$sep{\"name\":\"$1\",\"conclusion\":\"$2\"}"; sep=,; shift 2; done
  echo "{\"jobs\":[$out]}"
}
GREEN_BASE=$(jobs "Detect Go-relevant changes" success "Test shard (cmd-a)" success "Test shard (session)" success "Full test suite (PR gate)" success)
RED_BASE=$(jobs "Detect Go-relevant changes" success "Test shard (cmd-a)" success "Test shard (session)" failure "Full test suite (PR gate)" failure)
VO_PR=$(jobs "Detect Go-relevant changes" success "Test shard (\${{ matrix.shard.name }})" skipped "Version-only release check" success "Full test suite (PR gate)" success)
VO_PR_RED=$(jobs "Detect Go-relevant changes" success "Test shard (\${{ matrix.shard.name }})" skipped "Version-only release check" failure "Full test suite (PR gate)" failure)
NOGO_PR=$(jobs "Detect Go-relevant changes" success "Test shard (\${{ matrix.shard.name }})" skipped "Full test suite (PR gate)" success)

# scenario: fixtures for the usual shape. The tag T is the squash merge of
# PR head H (same tree, parent P) from $REF; PR run 900 is a version-only
# pass; base push run 800 is green; T's own push run 700 is still running.
scenario() {
  FX="$TMP/fx-$1"; mkdir -p "$FX"
  local parents=${3:-$P} pj="" sep="" p
  for p in $parents; do pj+="$sep{\"sha\":\"$p\"}"; sep=,; done
  fx "repos/$R/git/commits/$T" "{\"tree\":{\"sha\":\"$TREE\"},\"parents\":[$pj]}"
  fx "repos/$R/git/commits/$H" "{\"tree\":{\"sha\":\"$TREE\"},\"parents\":[{\"sha\":\"$P\"}]}"
  fx "repos/$R/commits/$T/pulls" "[{\"merged_at\":\"2026-02-01T00:00:00Z\",\"merge_commit_sha\":\"$T\",\"head\":{\"sha\":\"$H\",\"ref\":\"$REF\",\"repo\":{\"full_name\":\"$R\"}},\"base\":{\"ref\":\"main\"}}]"
  fx "repos/$R/actions/workflows/go-test.yml/runs?head_sha=$T&event=push&per_page=30" "$(runs "$(run 700 in_progress - "$T" main)")"
  fx "repos/$R/actions/workflows/go-test.yml/runs?head_sha=$H&event=pull_request&per_page=30" "$(runs "$(run 900 completed success "$H" "$REF")")"
  fx "repos/$R/actions/runs/900/jobs?filter=latest&per_page=100" "${2:-$VO_PR}"
  fx "repos/$R/actions/workflows/go-test.yml/runs?head_sha=$P&event=push&per_page=30" "$(runs "$(run 800 completed success "$P" main)")"
  fx "repos/$R/actions/runs/800/jobs?filter=latest&per_page=100" "$GREEN_BASE"
}

# check <label> <want reuse> <want match> [log fragment]: run the script in
# the repo $D with the fixtures in $FX. REUSE_WAIT_SECONDS=0 means a still
# running candidate gives up at once, unless the case sets WAIT.
check() {
  local label=$1 want=$2 match=$3 frag=${4:-} out log code=0
  out="$FX/output"; : >"$out"
  log=$(cd "$D" && PATH="$TMP/bin:$PATH" FAKE_GH_DIR="$FX" GH_TOKEN=x REPO=$R TAG_SHA=$T \
    GITHUB_OUTPUT="$out" REUSE_WAIT_SECONDS="${WAIT:-0}" REUSE_POLL_SECONDS=0 \
    bash "$REUSE" 2>&1) || code=$?
  local got gotm
  got=$(sed -n 's/^reuse=//p' "$out"); gotm=$(sed -n 's/^match=//p' "$out")
  if [ "$code" != 0 ] || [ "$got" != "$want" ] || [ "$gotm" != "$match" ]; then
    echo "FAIL $label: exit $code reuse=$got match=$gotm, want reuse=$want match=$match"
    printf '%s\n' "$log" | sed 's/^/     /'
    ERRORS=$((ERRORS + 1)); return
  fi
  if [ -n "$frag" ] && ! printf '%s\n' "$log" | grep -qF -- "$frag"; then
    echo "FAIL $label: log lacks '$frag'"; printf '%s\n' "$log" | sed 's/^/     /'
    ERRORS=$((ERRORS + 1)); return
  fi
  echo "ok   $label"
}

# 1. The release PR shape: reuse the version-only PR run, without waiting
#    for the tag's own push run.
new_repo ok; release_commit; scenario ok
check "version-only PR run on a fully green base is reused" true version-only "rule R"

# Every "no" case must get past the commit lookup, so its log names the
# rule that said no (not a fixture error).
# 2. A base whose push run has a red shard: full path.
new_repo red; release_commit; scenario red
fx "repos/$R/actions/runs/800/jobs?filter=latest&per_page=100" "$RED_BASE"
check "base push run with a red shard is not reused" false none "rule R: skip base run https://github.com/acme/agent-deck/actions/runs/800: the gate job or a shard job"

# 3. Base push run concluded failure overall.
new_repo redrun; release_commit; scenario redrun
fx "repos/$R/actions/workflows/go-test.yml/runs?head_sha=$P&event=push&per_page=30" "$(runs "$(run 800 completed failure "$P" main)")"
check "base push run that failed is not reused" false none "concluded failure"

# 4. No base push run at all.
new_repo nobase; release_commit; scenario nobase
fx "repos/$R/actions/workflows/go-test.yml/runs?head_sha=$P&event=push&per_page=30" '{"workflow_runs":[]}'
check "base without a push run is not reused" false none "has no push run with every shard green"

# 5. Base push run on a branch other than main.
new_repo branch; release_commit; scenario branch
fx "repos/$R/actions/workflows/go-test.yml/runs?head_sha=$P&event=push&per_page=30" "$(runs "$(run 800 completed success "$P" feature)")"
check "base push run not on main is not reused" false none "branch feature"

# 6. The release commit also touches a Go file: not version-only.
new_repo gofile; release_commit cmd/agent-deck/main.go; scenario gofile
check "release commit with another Go change is not reused" false none "not version-only"

# 7. The release commit also touches another file.
new_repo extra; release_commit README.md; scenario extra
check "release commit touching README.md is not reused" false none "not version-only"

# 8. The base has no detector (3003 not landed there): full path.
new_repo nodet none; release_commit; scenario nodet
check "base without the detector is not reused" false none "has no .github/scripts/version-only-diff.sh"

# 9. The detector comes from the base, not the tag: a base detector that
#    says no wins over the tag's (which would say yes).
printf '#!/usr/bin/env bash\necho "version-only: no: stub" >&2\nexit 1\n' >"$TMP/no-detector.sh"
new_repo basedet "$TMP/no-detector.sh"; cp "$DETECT" "$D/.github/scripts/version-only-diff.sh"
release_commit .github/scripts/version-only-diff.sh; scenario basedet
check "base detector saying no is not overridden by the tag's" false none "is not version-only"

# 10. The PR run's version-only check failed.
new_repo vored; release_commit; scenario vored "$VO_PR_RED"
fx "repos/$R/actions/workflows/go-test.yml/runs?head_sha=$H&event=pull_request&per_page=30" "$(runs "$(run 900 completed failure "$H" "$REF")")"
check "PR run whose version-only check failed is not reused" false none "(same-tree): concluded failure"

# 11. A PR run that passed only because nothing Go-relevant changed
#     (no shards, no version-only job) does not count.
new_repo nogo; release_commit; scenario nogo "$NOGO_PR"
check "PR run without shards or the version-only job is not reused" false none "(same-tree): the gate job or a shard job"

# 12. A merge commit (two parents) never qualifies.
new_repo merge; release_commit; scenario merge "" "$P $H"
check "two-parent tag commit is not reused" false none

# 13. The PR head has another parent than the tag commit.
new_repo parent; release_commit; scenario parent
fx "repos/$R/git/commits/$H" "{\"tree\":{\"sha\":\"$TREE\"},\"parents\":[{\"sha\":\"5555555555555555555555555555555555555555\"}]}"
check "PR head with another parent is not reused" false none "parent 5555"

# 14. The PR run is on another branch than the PR head branch.
new_repo prbranch; release_commit; scenario prbranch
fx "repos/$R/actions/workflows/go-test.yml/runs?head_sha=$H&event=pull_request&per_page=30" "$(runs "$(run 900 completed success "$H" someone/else)")"
check "PR run on another branch is not reused" false none "not the PR head branch"

# 15. The base push run is still running: wait for it, then reuse.
new_repo wait; release_commit; scenario wait
fxn "repos/$R/actions/workflows/go-test.yml/runs?head_sha=$P&event=push&per_page=30" 1 "$(runs "$(run 800 in_progress - "$P" main)")"
fxn "repos/$R/actions/workflows/go-test.yml/runs?head_sha=$P&event=push&per_page=30" 2 "$(runs "$(run 800 completed success "$P" main)")"
WAIT=60 check "base push run still running is waited for" true version-only

# 16. ...and gives up at the deadline.
new_repo waitdl; release_commit; scenario waitdl
fx "repos/$R/actions/workflows/go-test.yml/runs?head_sha=$P&event=push&per_page=30" "$(runs "$(run 800 in_progress - "$P" main)")"
check "base push run still running at the deadline is not reused" false none "actions/runs/800 still running after 0s"

# 17. API error on the base run lookup.
new_repo apierr; release_commit; scenario apierr
key=$(printf %s "repos/$R/actions/workflows/go-test.yml/runs?head_sha=$P&event=push&per_page=30" | tr -c 'A-Za-z0-9' _)
mv "$FX/$key.json" "$FX/$key.gone"
check "API error on the base run lookup is not reused" false none "could not list push runs for base"

# 18. Unchanged rules: the tag's own green push run is still exact-sha.
new_repo exact; release_commit; scenario exact
fx "repos/$R/actions/workflows/go-test.yml/runs?head_sha=$T&event=push&per_page=30" "$(runs "$(run 700 completed success "$T" main)")"
fx "repos/$R/actions/runs/700/jobs?filter=latest&per_page=100" "$GREEN_BASE"
check "tag push run green is still exact-sha" true exact-sha

if [ "$ERRORS" -gt 0 ]; then echo "$ERRORS case(s) failed"; exit 1; fi
echo "all release-test-reuse version-only cases pass"
