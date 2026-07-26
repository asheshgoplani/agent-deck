#!/usr/bin/env bash
#
# reap-stale-tmux.test.sh — DRY_RUN smoke test for scripts/reap-stale-tmux.sh.
#
# Issue #1744. This is the script class that killed the whole agent-deck fleet
# twice on 2026-07-26 by matching processes on argv, so it does not get to run
# unattended again without automated proof of two things:
#
#   1. it FINDS every test-only socket/process family it claims to reap, and
#   2. it never, under any seeding, names the DEFAULT tmux socket as a target.
#
# Everything happens inside one mktemp sandbox. The script is pointed at that
# sandbox through AGENT_DECK_REAPER_TMP_ROOTS, so the host's real socket dir
# (${TMUX_TMPDIR:-/tmp}/tmux-<uid>) is not even in scope — and every invocation
# passes DRY_RUN=1, so nothing anywhere is killed. The tmux servers this test
# starts are its own, on explicit sandbox socket paths, and teardown kills each
# one through the same socket path it started it with (never kill-server on the
# default socket, never a pkill).
#
# Usage: tests/ci/reap-stale-tmux.test.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$ROOT/scripts/reap-stale-tmux.sh"

failures=0
pass() { echo "PASS: $1"; }
fail() {
  echo "FAIL: $1" >&2
  failures=$((failures + 1))
}
skip() { echo "SKIP: $1"; }

[ -f "$SCRIPT" ] || {
  echo "FAIL: $SCRIPT not found" >&2
  exit 1
}
bash -n "$SCRIPT" || {
  echo "FAIL: $SCRIPT does not parse" >&2
  exit 1
}

uid=$(id -u)

# Resolve the sandbox through symlinks (/tmp -> /private/tmp on darwin): the
# script resolves its roots the same way, and the assertions compare paths.
# Keep the prefix short — unix socket paths cap at 104 bytes on darwin.
sandbox=$(cd "$(mktemp -d /tmp/ad-reap-t.XXXXXX)" && pwd -P)
root_a="$sandbox/a"
root_b="$sandbox/b"
log="$sandbox/reaper.log"
mkdir -p "$root_a/tmux-$uid" "$root_b/tmux-$uid"

# The one path that must never show up anywhere in the script's output.
real_default="${TMUX_TMPDIR:-/tmp}/tmux-$uid/default"

servers=()
fake_pids=()

tmux_q() { env -u TMUX -u TMUX_TMPDIR tmux "$@"; }

cleanup() {
  local rc=$?
  local sock pid leaked=0
  for sock in ${servers[@]+"${servers[@]}"}; do
    tmux_q -S "$sock" kill-server >/dev/null 2>&1
    # Do not discard the outcome: a surviving test server is exactly the leak
    # this whole script exists to prevent.
    if tmux_q -S "$sock" list-sessions >/dev/null 2>&1; then
      echo "FAIL: leaked test tmux server at $sock" >&2
      leaked=1
    fi
  done
  for pid in ${fake_pids[@]+"${fake_pids[@]}"}; do
    kill -KILL "$pid" 2>/dev/null
  done
  case "$sandbox" in
  /tmp/ad-reap-t.* | /private/tmp/ad-reap-t.*) rm -rf "$sandbox" ;;
  *) echo "WARNING refusing to remove unexpected sandbox path $sandbox" >&2 ;;
  esac
  [ "$leaked" = "1" ] && [ "$rc" = "0" ] && rc=1
  exit "$rc"
}
trap cleanup EXIT

# start_server brings up a real tmux server on an explicit sandbox socket path.
start_server() {
  local sock="$1"
  mkdir -p "$(dirname "$sock")"
  if ! tmux_q -S "$sock" new-session -d -s smoke "sleep 900" 2>/dev/null; then
    return 1
  fi
  servers+=("$sock")
}

# ── seed: one live server per socket family the script claims to reap ────────

if ! command -v tmux >/dev/null 2>&1; then
  skip "tmux not on PATH — socket families not exercised"
  tmux_present=0
else
  tmux_present=1
fi

# Socket NAME families, in a tmux base dir (what `tmux -L <name>` produces).
sock_ad1031="$root_a/tmux-$uid/ad1031-smoke"
sock_adattach="$root_a/tmux-$uid/ad-attach-smoke"
sock_adfork="$root_b/tmux-$uid/ad-fork-smoke"
# Socket DIR families (testutil.IsolateTmuxSocket / ShortTmuxSocket).
sock_adtmux="$root_a/ad-tmux-smoke/tmux-$uid/s"
sock_adsock="$root_a/ad-sock-smoke/s"
sock_testsock="$root_a/agent-deck-test-sock-smoke/s"
# Decoys named `default`: one inside a reaped DIR family (where the dir scan
# lists every socket it finds, so only the name filter can save it) and one in
# a tmux base dir. Neither may ever be a target.
sock_default_in_family="$root_a/ad-tmux-smoke/tmux-$uid/default"
sock_default_in_base="$root_a/tmux-$uid/default"

expected_targets=(
  "$sock_ad1031"
  "$sock_adattach"
  "$sock_adfork"
  "$sock_adtmux"
  "$sock_adsock"
  "$sock_testsock"
)
forbidden_targets=(
  "$sock_default_in_family"
  "$sock_default_in_base"
  "$real_default"
)

seeded=0
if [ "$tmux_present" = "1" ]; then
  for sock in "${expected_targets[@]}" "$sock_default_in_family" "$sock_default_in_base"; do
    if start_server "$sock"; then
      seeded=$((seeded + 1))
    else
      fail "could not start sandbox tmux server at $sock"
    fi
  done
fi

# A socket FILE with no server behind it: must be ignored, never reported as a
# reap. Bound and closed by python3; the file survives the process.
stale_sock="$root_a/ad-sock-stale/s"
mkdir -p "$(dirname "$stale_sock")"
if command -v python3 >/dev/null 2>&1 &&
  python3 -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1]); s.close()' "$stale_sock" 2>/dev/null; then
  stale_seeded=1
else
  stale_seeded=0
  skip "python3 unavailable — stale-socket case not exercised"
fi

# ── seed: a leaked eval binary, plus a same-named decoy elsewhere ────────────
#
# Both fakes are named `agent-deck`; only one of them sits in an
# agent-deck-eval-bin-* dir. That is the 2026-07-26 lesson as an assertion:
# identity is the executable path, never the process name.
#
# The binary is compiled rather than copied from /bin/sleep: darwin SIGKILLs a
# copy of a platform binary on exec (code signing), so a copy cannot stand in
# for a long-lived leak there. cp is kept as a fallback for hosts with no
# compiler, and the launch is verified either way.

# Can this host prove which image a pid is running? Mirrors the script's own
# test; without it the script skips eval-bin reaping by design.
if [ -d /proc/$$ ] || command -v lsof >/dev/null 2>&1; then
  identity_ok=1
else
  identity_ok=0
fi

cc_bin=$(command -v cc || command -v clang || command -v gcc || true)
sleep_bin=$(command -v sleep || true)
eval_exe="$root_a/agent-deck-eval-bin-smoke/agent-deck"
decoy_exe="$root_a/not-an-eval-bin/agent-deck"
eval_pid=""
decoy_pid=""

cat >"$sandbox/fake.c" <<'EOF'
#include <unistd.h>
int main(void) {
  for (;;) {
    sleep(60);
  }
  return 0;
}
EOF

make_fake_bin() {
  local dest="$1"
  mkdir -p "$(dirname "$dest")"
  if [ -n "$cc_bin" ] && "$cc_bin" -o "$dest" "$sandbox/fake.c" 2>/dev/null && [ -x "$dest" ]; then
    return 0
  fi
  [ -n "$sleep_bin" ] || return 1
  cp "$sleep_bin" "$dest" 2>/dev/null && chmod +x "$dest" && return 0
  return 1
}

# launch_fake starts $1 and prints its pid, or nothing if it did not survive.
launch_fake() {
  local exe="$1" pid
  # stdout must not be the caller's command-substitution pipe: the fake outlives
  # this function and would hold the pipe open forever.
  "$exe" 900 >/dev/null 2>&1 &
  pid=$!
  # Give the kernel a moment to reject an unsigned/copied image before
  # treating the process as a usable stand-in.
  sleep 0.5
  if kill -0 "$pid" 2>/dev/null; then
    printf '%s\n' "$pid"
    return 0
  fi
  wait "$pid" 2>/dev/null
  return 1
}

if make_fake_bin "$eval_exe"; then
  eval_pid=$(launch_fake "$eval_exe" || true)
  [ -n "$eval_pid" ] && fake_pids+=("$eval_pid")
fi
if make_fake_bin "$decoy_exe"; then
  decoy_pid=$(launch_fake "$decoy_exe" || true)
  [ -n "$decoy_pid" ] && fake_pids+=("$decoy_pid")
fi

# ── invoke ───────────────────────────────────────────────────────────────────

# Every run is DRY_RUN, scoped to the sandbox roots, logging into the sandbox,
# with the pty warning silenced so a busy CI host cannot add noise. $eval_age
# is the age gate under test and is set by each caller.
eval_age=86400
run_reaper() {
  env -u TMUX -u TMUX_TMPDIR \
    AGENT_DECK_REAPER_TMP_ROOTS="$root_a:$root_b" \
    AGENT_DECK_REAPER_LOG="$log" \
    AGENT_DECK_PTY_WARN_THRESHOLD=1000000 \
    AGENT_DECK_EVAL_BIN_MAX_AGE_SECONDS="$eval_age" \
    DRY_RUN=1 \
    bash "$SCRIPT" "$@"
}

# 1. Candidate listing, eval-bin age gate wide open so the fresh fake counts.
eval_age=0
listing=$(run_reaper --list-candidates 2>/dev/null)

if [ "$tmux_present" = "1" ] && [ "$seeded" -gt 0 ]; then
  for sock in "${expected_targets[@]}"; do
    if grep -qxF "socket $sock" <<<"$listing"; then
      pass "lists $(basename "$(dirname "$sock")")/$(basename "$sock")"
    else
      fail "socket family not listed as a target: $sock"
    fi
  done
fi

for sock in "${forbidden_targets[@]}"; do
  if grep -qF -- "$sock" <<<"$listing"; then
    fail "default socket listed as a target: $sock"
  else
    pass "never targets default socket: $sock"
  fi
done

# Nothing outside the sandbox may appear at all — the strongest form of "the
# host's real sockets are out of scope".
outside_re="^socket $sandbox/|^evalbin [0-9]+ [0-9]+ $sandbox/"
outside=$(grep -Ev "$outside_re" <<<"$listing" | grep -c . || true)
if [ "$outside" = "0" ]; then
  pass "every target lives under the sandbox roots"
else
  fail "listing contains $outside target(s) outside the sandbox:"
  grep -Ev "$outside_re" <<<"$listing" >&2
fi

if [ "$stale_seeded" = "1" ]; then
  if grep -qxF "socket $stale_sock" <<<"$listing"; then
    pass "stale socket file is a candidate (reachability is decided by probe)"
  else
    fail "stale socket file missing from candidates: $stale_sock"
  fi
fi

# 2. eval-bin identity: matched by executable path, not by process name.
if [ "$identity_ok" = "0" ]; then
  skip "host has neither /proc nor lsof — eval-bin identity not exercised"
elif [ -z "$eval_pid" ]; then
  skip "could not launch a fake eval binary — eval-bin cases not exercised"
elif grep -qE "^evalbin $eval_pid [0-9]+ $eval_exe$" <<<"$listing"; then
  pass "leaked eval binary listed by executable path (pid $eval_pid)"
else
  fail "leaked eval binary not listed: pid $eval_pid exe $eval_exe"
fi

if [ -n "$decoy_pid" ] && [ "$identity_ok" = "1" ]; then
  if grep -qF "evalbin $decoy_pid " <<<"$listing"; then
    fail "process matched by NAME, not path: decoy pid $decoy_pid ($decoy_exe)"
  else
    pass "same-named process outside an eval-bin dir is never a target"
  fi
fi

# 3. The age gate: with the production threshold the fresh fake is too young.
eval_age=86400
young=$(run_reaper --list-candidates 2>/dev/null | grep -c '^evalbin ' || true)
if [ -n "$eval_pid" ] && [ "$identity_ok" = "1" ]; then
  if [ "$young" = "0" ]; then
    pass "eval-bin age gate spares processes younger than the threshold"
  else
    fail "age gate ignored: $young eval-bin target(s) at the 1-day threshold"
  fi
fi

# 4. Full DRY_RUN pass: log every kill it would make, kill nothing.
: >"$log"
eval_age=0
run_reaper >/dev/null 2>&1
log_body=$(cat "$log" 2>/dev/null || true)

if [ "$tmux_present" = "1" ] && [ "$seeded" -gt 0 ]; then
  for sock in "${expected_targets[@]}"; do
    if grep -qF "DRY_RUN would reap $sock " <<<"$log_body"; then
      pass "DRY_RUN reports $sock"
    else
      fail "DRY_RUN did not report reachable target $sock"
    fi
  done
  if grep -qE "DRY_RUN would reap .*/default " <<<"$log_body"; then
    fail "DRY_RUN reported a default socket as a reap target"
  else
    pass "DRY_RUN never reports a default socket"
  fi
  if grep -qE "^[0-9-]+ [0-9:]+ reaped " <<<"$log_body"; then
    fail "DRY_RUN logged a real reap"
  else
    pass "DRY_RUN logs no real reap"
  fi
  # The seeded servers must all still be up: DRY_RUN kills nothing.
  alive=0
  for sock in "${servers[@]}"; do
    tmux_q -S "$sock" list-sessions >/dev/null 2>&1 && alive=$((alive + 1))
  done
  if [ "$alive" = "$seeded" ]; then
    pass "DRY_RUN killed nothing ($alive/$seeded sandbox servers still up)"
  else
    fail "DRY_RUN killed servers: only $alive/$seeded still up"
  fi
fi

if [ -n "$eval_pid" ] && [ "$identity_ok" = "1" ]; then
  if grep -qF "DRY_RUN would kill eval-bin pid $eval_pid " <<<"$log_body"; then
    pass "DRY_RUN reports the leaked eval binary"
  else
    fail "DRY_RUN did not report leaked eval-bin pid $eval_pid"
  fi
  if kill -0 "$eval_pid" 2>/dev/null; then
    pass "DRY_RUN left the eval-bin process running"
  else
    fail "DRY_RUN killed the eval-bin process"
  fi
fi

if grep -qF "reap pass complete" <<<"$log_body"; then
  pass "DRY_RUN writes a summary line"
else
  fail "DRY_RUN wrote no summary line"
fi

# The default socket path, in any form, must not appear in the log either.
if grep -qF -- "$real_default" <<<"$log_body"; then
  fail "log mentions the real default socket path: $real_default"
else
  pass "log never mentions the real default socket path"
fi

echo
if [ "$failures" -ne 0 ]; then
  echo "$failures check(s) failed" >&2
  exit 1
fi
echo "all reap-stale-tmux.sh smoke checks passed"
