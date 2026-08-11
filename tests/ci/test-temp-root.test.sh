#!/usr/bin/env bash
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$ROOT/scripts/run-tests.sh"
SANDBOX="$(cd "$(mktemp -d /tmp/ad-test-root.XXXXXX)" && pwd -P)"
trap 'rm -rf "$SANDBOX"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }

[ -x "$SCRIPT" ] || fail "$SCRIPT is not executable"
grep -qF 'scripts/run-tests.sh go test -race -v ./...' "$ROOT/Makefile" || fail "make test bypasses managed temp root"
pass "make test uses managed temp root"

OBSERVED="$SANDBOX/observed"
CALLER_HOME="$SANDBOX/caller-home"
mkdir -p "$CALLER_HOME"
HOME="$CALLER_HOME" AGENT_DECK_TEST_TMP_BASE="$SANDBOX" "$SCRIPT" sh -c \
  'printf "%s\n%s\n%s\n%s\n%s\n%s\n" "$TMPDIR" "$GOTMPDIR" "$PLAYWRIGHT_TMP_DIR" "$HOME" "$(go env GOMODCACHE)" "$(go env GOCACHE)" >"$1"; mkdir -p "$TMPDIR/child"' \
  sh "$OBSERVED" || fail "wrapped success command failed"

RUN_ROOT=$(sed -n '1p' "$OBSERVED")
[ "$RUN_ROOT" = "$(sed -n '2p' "$OBSERVED")" ] || fail "GOTMPDIR did not use run root"
[ "$RUN_ROOT" = "$(sed -n '3p' "$OBSERVED")" ] || fail "browser temp did not use run root"
[ "$CALLER_HOME" = "$(sed -n '4p' "$OBSERVED")" ] || fail "wrapper clobbered package-owned HOME isolation boundary"
case "$(sed -n '5p' "$OBSERVED")" in "$RUN_ROOT"/*) fail "Go module cache was placed in run root" ;; esac
case "$(sed -n '6p' "$OBSERVED")" in "$RUN_ROOT"/*) fail "Go build cache was placed in run root" ;; esac
case "$RUN_ROOT" in "$SANDBOX"/adtr-*) ;; *) fail "unexpected run root: $RUN_ROOT" ;; esac
[ ! -e "$RUN_ROOT" ] || fail "successful run root survived"
pass "successful commands are contained and cleaned"

set +e
AGENT_DECK_TEST_TMP_BASE="$SANDBOX" "$SCRIPT" sh -c 'mkdir -p "$TMPDIR/read-only"; : >"$TMPDIR/read-only/file"; chmod -R a-w "$TMPDIR/read-only"; exit 37'
STATUS=$?
set -e
[ "$STATUS" -eq 37 ] || fail "exit status changed: $STATUS"
[ -z "$(find "$SANDBOX" -mindepth 1 -maxdepth 1 -name 'adtr-*' -print)" ] || fail "failed run root survived"
pass "failure status is preserved and read-only files are cleaned"

STALE="$SANDBOX/adtr-stale"
FRESH="$SANDBOX/adtr-fresh"
FOREIGN="$SANDBOX/adtr-foreign"
LIVE="$SANDBOX/adtr-live"
LINK="$SANDBOX/adtr-link"
mkdir -p "$STALE" "$FRESH" "$FOREIGN" "$LIVE"
printf 'schema=1\nrepo=agent-deck\ncreated=1\npid=99999999\n' >"$STALE/.agent-deck-test-root"
printf 'schema=1\nrepo=agent-deck\ncreated=%s\npid=99999999\n' "$(date +%s)" >"$FRESH/.agent-deck-test-root"
printf 'schema=1\nrepo=someone-else\ncreated=1\npid=99999999\n' >"$FOREIGN/.agent-deck-test-root"
printf 'schema=1\nrepo=agent-deck\ncreated=1\npid=%s\n' "$$" >"$LIVE/.agent-deck-test-root"
ln -s "$STALE" "$LINK"

AGENT_DECK_TEST_TMP_BASE="$SANDBOX" "$SCRIPT" true || fail "reaper invocation failed"
[ ! -e "$STALE" ] || fail "stale marked root survived"
[ -d "$FRESH" ] || fail "fresh root was removed"
[ -d "$FOREIGN" ] || fail "foreign root was removed"
[ -d "$LIVE" ] || fail "live root was removed"
[ -L "$LINK" ] || fail "symlink candidate was removed"
pass "only stale owned roots are reaped"

DECOY="$SANDBOX/unrelated"
mkdir -p "$DECOY"
AGENT_DECK_TEST_TMP_BASE="$SANDBOX" "$SCRIPT" true || fail "decoy invocation failed"
[ -d "$DECOY" ] || fail "unrelated directory was removed"
pass "unrelated temporary directories are preserved"
