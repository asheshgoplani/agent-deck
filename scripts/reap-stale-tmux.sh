#!/usr/bin/env bash
#
# reap-stale-tmux.sh — reap leaked agent-deck TEST tmux servers.
#
# Test-spawned tmux servers outlive `go test` and hold one pty per pane. Enough
# of them and the host's pty pool runs dry: on 2026-07-18 roughly 50 leaked
# servers held 507 of 511 ptys and every attach on the machine failed with
# "fork failed: Device not configured".
#
#
# ─── THE ONE RULE ────────────────────────────────────────────────────────────
#
# Identify servers by SOCKET PATH. Never by process name, never by argv, never
# by pgrep/pkill.
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
# Argv is not identity. A socket path is: it says which server, and the probe
# that reads it (`tmux -S <sock> list-sessions`) is the same call that proves
# the server is reachable before anything is killed.
#
#
# ─── WHAT IT REAPS ───────────────────────────────────────────────────────────
#
#   1. ad1031-*  — sockets from the issue-1031 launch-race tests, under tmux's
#                  default base dir (the tests strip TMUX* from their children).
#   2. /tmp/ad-tmux-*/  and  /tmp/ad-sock-*/  — per-run socket dirs from
#                  testutil.IsolateTmuxSocket / ShortTmuxSocket.
#
# Both are minted exclusively by tests. The default socket (`default`) is
# excluded explicitly and by construction; so is every socket this script was
# not told about.
#
# Set DRY_RUN=1 to log what would be reaped without killing anything.
#
# Usage: reap-stale-tmux.sh
set -uo pipefail

LOG="${AGENT_DECK_REAPER_LOG:-$HOME/.agent-deck/logs/tmux-reaper.log}"
PTY_WARN_THRESHOLD="${AGENT_DECK_PTY_WARN_THRESHOLD:-400}"
DRY_RUN="${DRY_RUN:-0}"

mkdir -p "$(dirname "$LOG")" 2>/dev/null || true
ts() { date "+%F %T"; }
log() { echo "$(ts) $*" >>"$LOG"; }

command -v tmux >/dev/null 2>&1 || exit 0

# Resolve /tmp through any symlink before handing it to find. On macOS /tmp is
# a symlink to private/tmp, and find(1) does not descend into a symlinked
# starting point — searching "/tmp" directly silently matches nothing.
tmp_root=$(cd /tmp 2>/dev/null && pwd -P) || tmp_root=/tmp

# tmux's default socket base: $TMUX_TMPDIR, else /tmp. Sockets live one level
# down in tmux-<uid>/. Resolve it from THIS script's env deliberately — the
# leaking tests run with TMUX* stripped, so their servers land under /tmp.
default_base="${TMUX_TMPDIR:-$tmp_root}/tmux-$(id -u)"

# candidate_sockets prints every socket path eligible for reaping, one per line.
# Everything here is a test-only naming convention; nothing else is listed, so
# nothing else can be killed.
candidate_sockets() {
  # 1. issue-1031 launch-race test servers, by socket NAME under the default base.
  find "$default_base" -maxdepth 1 -name 'ad1031-*' -type s 2>/dev/null

  # 2. Per-run isolated socket dirs from the Go test helpers. The socket sits
  #    either directly in the dir (ShortTmuxSocket, an explicit `-S <dir>/s`)
  #    or one level down in tmux-<uid>/ (IsolateTmuxSocket, which points
  #    TMUX_TMPDIR at the dir and lets tmux nest as usual).
  find "$tmp_root" -maxdepth 1 -type d \
    \( -name 'ad-tmux-*' -o -name 'ad-sock-*' \) 2>/dev/null |
    while IFS= read -r dir; do
      find "$dir" -maxdepth 2 -type s 2>/dev/null
    done
}

reaped=0
skipped=0
while IFS= read -r sock; do
  [ -n "$sock" ] || continue
  # Never the default socket, whatever else may have matched.
  [ "$(basename "$sock")" = "default" ] && continue

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
done < <(candidate_sockets | sort -u)

[ "$reaped" -gt 0 ] && log "reap pass complete: $reaped server(s) reaped, $skipped stale socket(s) ignored"

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
