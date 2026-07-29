#!/usr/bin/env bash
#
# reap-stale-tmux.sh — reap leaked agent-deck TEST tmux servers and leaked
# eval-harness binaries.
#
# Test-spawned tmux servers outlive `go test` and hold one pty per pane. Enough
# of them and the host's pty pool runs dry: on 2026-07-18 roughly 50 leaked
# servers held 507 of 511 ptys and every attach on the machine failed with
# "fork failed: Device not configured".
#
# Leaked eval binaries are the same class of debt with a different shape: a
# `tests/eval` invocation that starts the TUI and never exits leaves an
# `agent-deck` process running out of its `agent-deck-eval-bin-*` temp dir,
# polling forever. A 2026-07-26 host sweep found four of them running since
# mid-July alongside eight leaked test tmux servers (#1747).
#
#
# ─── THE ONE RULE ────────────────────────────────────────────────────────────
#
# Identify what to kill by PATH. Never by process name, never by argv, never by
# pgrep/pkill.
#
# For tmux servers the path is the SOCKET PATH. For eval binaries it is the
# EXECUTABLE PATH, read from the kernel (`/proc/<pid>/exe`, or lsof's `txt`
# descriptor on darwin) — never from argv, and never from `ps -o comm`.
#
# A previous version of this script reaped `pgrep -fx "tmux -C"`, reasoning that
# genuine control clients are `tmux -C attach-session ...` and so could not
# match an exact-argv pattern. Two things made that false. Some agent-deck code
# really did spawn a bare `tmux -C` (fixed: internal/tmux/keysender.go now
# always passes an explicit `attach-session`). And on macOS a process keeps the
# argv it was exec'd with, so the DEFAULT-socket server auto-started by such a
# client was itself named exactly `tmux -C` — the main server, wearing a
# client's name.
#
# On 2026-07-26 at 13:35:04 this script matched three processes. One of them was
# the main tmux server. All ~65 live agent-deck sessions died at once.
#
# Argv is not identity. A path is: it says which server or which image, and the
# probe that reads it (`tmux -S <sock> list-sessions`, `readlink /proc/<pid>/exe`)
# is the same call that proves the target is real before anything is killed.
#
#
# ─── WHAT IT REAPS ───────────────────────────────────────────────────────────
#
# tmux servers, by socket path, under every scan root (see SCAN ROOTS):
#
#   1. <root>/tmux-<uid>/ad1031-*    — issue-1031 launch-race tests.
#   2. <root>/tmux-<uid>/ad-attach-* — tests/eval attach/restart isolation.
#   3. <root>/tmux-<uid>/ad-fork-*   — tests/eval fork-PI isolation.
#      (2 and 3 are `[tmux] socket_name` values the eval tests write into a
#      sandbox config, i.e. `tmux -L <name>`, so they land in the default base
#      dir the same way ad1031-* does.)
#   4. <root>/ad-tmux-*/  <root>/ad-sock-*/  <root>/agent-deck-test-sock-*/
#      — per-run socket dirs from testutil.IsolateTmuxSocket / ShortTmuxSocket
#      (and the latter's fallback name).
#
# Eval binaries, by executable path:
#
#   5. <root>/agent-deck-eval-bin-*/agent-deck — processes whose executing image
#      is one of these, older than AGENT_DECK_EVAL_BIN_MAX_AGE_SECONDS (1 day).
#      A leak whose temp dir has already been deleted is deliberately NOT
#      reaped: without the on-disk path there is no path-based identity left,
#      and argv matching is what caused the 2026-07-26 fleet kill.
#
# Every name above is minted exclusively by tests. The default socket
# (`default`) is excluded explicitly and by construction; so is every socket
# and every process this script was not told about.
#
#
# ─── SCAN ROOTS ──────────────────────────────────────────────────────────────
#
# $TMUX_TMPDIR, /tmp and $TMPDIR (deduplicated, symlinks resolved). tmux's
# default base is $TMUX_TMPDIR else /tmp; Go's os.MkdirTemp("") — which mints
# the eval-bin dirs — uses $TMPDIR, which on darwin is a per-user
# /var/folders/... path. Both places therefore have to be swept.
#
# AGENT_DECK_REAPER_TMP_ROOTS (colon-separated) REPLACES that default set
# entirely. It exists so the smoke test can point the whole script at its own
# sandbox; nothing is appended to it.
#
#
# ─── MODES ───────────────────────────────────────────────────────────────────
#
#   DRY_RUN=1            log every kill it would make, kill nothing.
#   --list-candidates    print the target list and exit (no probing, no kills):
#                          socket <path>
#                          evalbin <pid> <age-seconds> <exe>
#
# Covered by tests/ci/reap-stale-tmux.test.sh — see docs/tmux-reaper.md before
# re-enabling the launchd job.
#
# Usage: reap-stale-tmux.sh [--list-candidates]
set -uo pipefail

# HOME is not guaranteed: a launchd agent inherits almost no environment, and
# the plist that ran this script until 2026-07-26 set none — so the default log
# path expanded to "/.agent-deck/logs/..." and every line was silently dropped.
# Fall back to a writable path and say so instead of logging into the void.
# HOME itself is deliberately NOT set here: tmux would then read a config from
# whatever fallback dir we picked (a world-writable /tmp/.tmux.conf is not a
# thing this script should ever load).
home_missing=0
log_home="${HOME:-}"
if [ -z "$log_home" ]; then
  home_missing=1
  log_home="${TMPDIR:-/tmp}"
fi
LOG="${AGENT_DECK_REAPER_LOG:-$log_home/.agent-deck/logs/tmux-reaper.log}"
PTY_WARN_THRESHOLD="${AGENT_DECK_PTY_WARN_THRESHOLD:-400}"
EVAL_BIN_MAX_AGE_SECONDS="${AGENT_DECK_EVAL_BIN_MAX_AGE_SECONDS:-86400}"
DRY_RUN="${DRY_RUN:-0}"
LIST_ONLY=0
[ "${1:-}" = "--list-candidates" ] && LIST_ONLY=1

mkdir -p "$(dirname "$LOG")" 2>/dev/null || true
ts() { date "+%F %T"; }
# In DRY_RUN the same line goes to stderr, so an interactive dry run shows its
# work without the caller having to tail the log. stdout stays clean because
# --list-candidates writes its machine-readable list there.
log() {
  echo "$(ts) $*" >>"$LOG"
  [ "$DRY_RUN" = "1" ] && echo "$(ts) $*" >&2
  return 0
}

[ "$home_missing" = "1" ] && log "WARNING HOME unset; logging to $LOG"

uid=$(id -u)

# Resolve a dir through any symlink before handing it to find. On macOS /tmp is
# a symlink to private/tmp, and find(1) does not descend into a symlinked
# starting point — searching "/tmp" directly silently matches nothing.
resolve_dir() { (cd "$1" 2>/dev/null && pwd -P) || printf '%s\n' "$1"; }

scan_roots() {
  if [ -n "${AGENT_DECK_REAPER_TMP_ROOTS:-}" ]; then
    printf '%s\n' "${AGENT_DECK_REAPER_TMP_ROOTS//:/$'\n'}" |
      while IFS= read -r root; do
        [ -n "$root" ] && [ -d "$root" ] && resolve_dir "$root"
      done
    return
  fi
  for root in "${TMUX_TMPDIR:-}" /tmp "${TMPDIR:-}"; do
    [ -n "$root" ] && [ -d "$root" ] && resolve_dir "$root"
  done
}

# Deduplicate while preserving order; /tmp and $TMPDIR are the same dir on most
# Linux hosts and there is no point sweeping it twice.
roots=$(scan_roots | awk 'NF && !seen[$0]++')

# Socket NAME families that live directly in a tmux base dir (`tmux -L <name>`).
socket_name_families=('ad1031-*' 'ad-attach-*' 'ad-fork-*')
# Per-run socket DIR families (`tmux -S <dir>/s` or TMUX_TMPDIR=<dir>).
socket_dir_families=('ad-tmux-*' 'ad-sock-*' 'agent-deck-test-sock-*')

# candidate_sockets prints every socket path eligible for reaping, one per line.
# Everything here is a test-only naming convention; nothing else is listed, so
# nothing else can be killed. The `default` filter lives in here rather than in
# the reap loop so that --list-candidates shows exactly the final target set.
candidate_sockets() {
  printf '%s\n' "$roots" | while IFS= read -r root; do
    [ -n "$root" ] || continue

    # 1-3. By socket NAME under this root's tmux base dir.
    for pattern in "${socket_name_families[@]}"; do
      find "$root/tmux-$uid" -maxdepth 1 -type s -name "$pattern" 2>/dev/null
    done

    # 4. Per-run isolated socket dirs from the Go test helpers. The socket sits
    #    either directly in the dir (ShortTmuxSocket, an explicit `-S <dir>/s`)
    #    or one level down in tmux-<uid>/ (IsolateTmuxSocket, which points
    #    TMUX_TMPDIR at the dir and lets tmux nest as usual).
    for pattern in "${socket_dir_families[@]}"; do
      find "$root" -maxdepth 1 -type d -name "$pattern" 2>/dev/null |
        while IFS= read -r dir; do
          find "$dir" -maxdepth 2 -type s 2>/dev/null
        done
    done
  done | while IFS= read -r sock; do
    [ -n "$sock" ] || continue
    # Never the default socket, whatever else may have matched.
    [ "$(basename "$sock")" = "default" ] && continue
    printf '%s\n' "$sock"
  done | sort -u
}

# ─── eval-bin identity ───────────────────────────────────────────────────────

# eval_bin_executables prints every on-disk eval binary under the scan roots.
eval_bin_executables() {
  printf '%s\n' "$roots" | while IFS= read -r root; do
    [ -n "$root" ] || continue
    find "$root" -maxdepth 2 -type f \
      -path "$root/agent-deck-eval-bin-*/agent-deck" 2>/dev/null
  done | sort -u
}

# exe_matches reports whether pid $1 is EXECUTING the image at path $2, asking
# the kernel — /proc/<pid>/exe on linux, lsof's txt descriptor on darwin. Never
# argv, never `ps -o comm`.
exe_matches() {
  local pid="$1" exe="$2" path
  if [ -d /proc/"$pid" ]; then
    path=$(readlink "/proc/$pid/exe" 2>/dev/null) || return 1
    [ "${path% (deleted)}" = "$exe" ]
    return
  fi
  command -v lsof >/dev/null 2>&1 || return 1
  # darwin lsof reports txt for mapped images too, so test membership rather
  # than the first line — but only txt: a file the process merely has open
  # must never qualify it as a target.
  lsof -a -p "$pid" -d txt -Fn 2>/dev/null | sed -n 's/^n//p' | grep -qxF -- "$exe"
}

# path_identity_available reports whether this host can prove which image a pid
# is running. Without it, eval-bin reaping is skipped entirely — there is no
# argv fallback, by design.
path_identity_available() {
  [ -d /proc/$$ ] || command -v lsof >/dev/null 2>&1
}

# pids_for_exe prints pids whose executing image is exactly $1.
pids_for_exe() {
  local exe="$1" pid proc
  if [ -d /proc/$$ ]; then
    for proc in /proc/[0-9]*; do
      pid=${proc#/proc/}
      exe_matches "$pid" "$exe" && printf '%s\n' "$pid"
    done
    return 0
  fi
  # darwin: ask which pids hold this file open at all, then confirm each one is
  # actually *executing* it.
  lsof -t -- "$exe" 2>/dev/null | while IFS= read -r pid; do
    [ -n "$pid" ] || continue
    exe_matches "$pid" "$exe" && printf '%s\n' "$pid"
  done
}

# proc_age_seconds prints a pid's elapsed run time in seconds. `ps -o etime`
# is the portable spelling (linux `etimes` does not exist on darwin) and comes
# back as [[dd-]hh:]mm:ss.
proc_age_seconds() {
  local etime days=0 rest h=0 m=0 s=0 a b c
  etime=$(ps -p "$1" -o etime= 2>/dev/null | tr -d '[:space:]') || return 1
  [ -n "$etime" ] || return 1
  rest="$etime"
  case "$rest" in *-*)
    days="${rest%%-*}"
    rest="${rest#*-}"
    ;;
  esac
  IFS=: read -r a b c <<<"$rest"
  if [ -n "${c:-}" ]; then
    h="$a" m="$b" s="$c"
  else
    m="${a:-0}" s="${b:-0}"
  fi
  printf '%s\n' "$((10#${days:-0} * 86400 + 10#${h:-0} * 3600 + 10#${m:-0} * 60 + 10#${s:-0}))"
}

# candidate_eval_bins prints "<pid> <age-seconds> <exe>" for every process that
# is executing a leaked eval binary and is older than the age threshold.
candidate_eval_bins() {
  path_identity_available || return 0
  local exe pid age
  while IFS= read -r exe; do
    [ -n "$exe" ] || continue
    while IFS= read -r pid; do
      [ -n "$pid" ] || continue
      [ "$pid" = "$$" ] && continue
      # Same-user only. The reaper never touches another account's processes.
      [ "$(ps -p "$pid" -o uid= 2>/dev/null | tr -d '[:space:]')" = "$uid" ] || continue
      age=$(proc_age_seconds "$pid") || continue
      [ "$age" -ge "$EVAL_BIN_MAX_AGE_SECONDS" ] || continue
      printf '%s %s %s\n' "$pid" "$age" "$exe"
    done < <(pids_for_exe "$exe")
  done < <(eval_bin_executables)
}

# ─── --list-candidates ───────────────────────────────────────────────────────

# Announced in every mode, listing included, so a host that cannot prove which
# image a pid runs says so instead of quietly reaping nothing.
if ! path_identity_available; then
  log "WARNING no path-based process identity available (no /proc, no lsof); skipping eval-bin reaping"
fi

if [ "$LIST_ONLY" = "1" ]; then
  while IFS= read -r sock; do
    [ -n "$sock" ] && printf 'socket %s\n' "$sock"
  done < <(candidate_sockets)
  while IFS= read -r line; do
    [ -n "$line" ] && printf 'evalbin %s\n' "$line"
  done < <(candidate_eval_bins)
  exit 0
fi

# ─── reap: tmux servers ──────────────────────────────────────────────────────

reaped=0
skipped=0
if command -v tmux >/dev/null 2>&1; then
  while IFS= read -r sock; do
    [ -n "$sock" ] || continue

    # Probe by socket path. This both identifies the server and proves it is
    # reachable; a stale socket file with no listener is silently dropped.
    if ! sessions=$(tmux -S "$sock" list-sessions -F '#{session_name}' 2>/dev/null); then
      skipped=$((skipped + 1))
      continue
    fi
    count=$(printf '%s\n' "$sessions" | grep -c . || true)

    if [ "$DRY_RUN" = "1" ]; then
      log "DRY_RUN would reap $sock ($count session(s))"
      reaped=$((reaped + 1))
      continue
    fi

    # Kill through the SAME socket path we probed. No pid, no name, no argv.
    if tmux -S "$sock" kill-server 2>/dev/null; then
      log "reaped $sock ($count session(s))"
      reaped=$((reaped + 1))
    else
      log "WARNING kill-server failed for $sock"
    fi
  done < <(candidate_sockets)
fi

# ─── reap: leaked eval binaries ──────────────────────────────────────────────

killed_bins=0
if path_identity_available; then
  while read -r pid age exe; do
    [ -n "${pid:-}" ] || continue
    if [ "$DRY_RUN" = "1" ]; then
      log "DRY_RUN would kill eval-bin pid $pid (age ${age}s, exe $exe)"
      killed_bins=$((killed_bins + 1))
      continue
    fi
    # TERM first so the TUI can restore the terminal, then KILL if it lingers.
    kill -TERM "$pid" 2>/dev/null
    for _ in 1 2 3 4 5; do
      kill -0 "$pid" 2>/dev/null || break
      sleep 1
    done
    if kill -0 "$pid" 2>/dev/null; then
      kill -KILL "$pid" 2>/dev/null
      log "killed eval-bin pid $pid SIGKILL (age ${age}s, exe $exe)"
    else
      log "killed eval-bin pid $pid SIGTERM (age ${age}s, exe $exe)"
    fi
    killed_bins=$((killed_bins + 1))
  done < <(candidate_eval_bins)
fi

if [ "$reaped" -gt 0 ] || [ "$killed_bins" -gt 0 ] || [ "$DRY_RUN" = "1" ]; then
  log "reap pass complete: $reaped server(s) reaped, $skipped stale socket(s) ignored, $killed_bins eval-bin process(es) killed"
fi

# Early warning well before the host cap: leaks build slowly, so alert with
# room to act rather than at the point of failure.
ptys=$(ps -axo tty= | grep -c '^ttys' || true)
if [ "$ptys" -gt "$PTY_WARN_THRESHOLD" ]; then
  log "WARNING pty usage high: $ptys (threshold $PTY_WARN_THRESHOLD)"
  if command -v osascript >/dev/null 2>&1; then
    osascript -e "display notification \"PTY usage at $ptys — tmux leak building again?\" with title \"tmux-reaper\"" 2>/dev/null || true
  fi
fi

exit 0
