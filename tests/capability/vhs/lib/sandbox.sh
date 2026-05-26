# shellcheck shell=bash
#
# sandbox.sh: sourced by every capability VHS tape to stand up an ISOLATED
# agent-deck environment before the demo runs. It mirrors the Wave 1 sandbox
# discipline (tests/capability/capability_test.go + tests/eval/harness) in pure
# shell so a recording can never touch the user's real tmux server or sessions.
#
# What it guarantees:
#   * a scratch HOME / XDG dirs under a fresh mktemp dir (no real config read)
#   * a per-recording tmux socket via AGENT_DECK_TMUX_SOCKET (never the default)
#   * TMUX unset, so a spawned pane can't join the user's live tmux server
#   * the deterministic echobot agent installed + registered as a custom tool
#   * the built agent-deck binary on PATH as `agent-deck` (typed verbatim in the
#     demo, so the recording shows the real command a user would run)
#   * an EXIT trap that kills the scratch tmux server and removes the scratch dir
#
# Inputs (exported by scripts/capability-videos.sh, with safe fallbacks):
#   CAPVID_BIN   absolute path to a prebuilt agent-deck binary (built once)
#   CAPVID_REPO  repo root (to locate testdata/echobot.sh and to build if needed)
#
# Usage inside a tape (under a Hide block so setup stays off-camera):
#   Type `source <repo>/tests/capability/vhs/lib/sandbox.sh` Enter

set -u

# --- refuse to run against a real tmux server -------------------------------
# Belt to the AGENT_DECK_TMUX_SOCKET suspenders: if we are somehow inside a live
# tmux and not explicitly marked isolated, bail before creating any session.
if [ -n "${TMUX:-}" ] && [ -z "${AGENT_DECK_TEST_ISOLATED:-}" ]; then
  echo "capability-vhs sandbox: refusing to run with TMUX set (would risk real sessions)" >&2
  return 1 2>/dev/null || exit 1
fi

# --- locate the repo --------------------------------------------------------
# Prefer the injected repo root; otherwise derive it from this script's path
# (tests/capability/vhs/lib/sandbox.sh -> four levels up is the repo root).
if [ -n "${CAPVID_REPO:-}" ]; then
  _capvid_repo="$CAPVID_REPO"
else
  _capvid_lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  _capvid_repo="$(cd "$_capvid_lib_dir/../../../.." && pwd)"
fi

# --- resolve (or build) the binary ------------------------------------------
_capvid_bin="${CAPVID_BIN:-$_capvid_repo/tests/capability/vhs/.bin/agent-deck}"
if [ ! -x "$_capvid_bin" ]; then
  echo "capability-vhs sandbox: building agent-deck (no prebuilt binary at $_capvid_bin)..." >&2
  mkdir -p "$(dirname "$_capvid_bin")"
  ( cd "$_capvid_repo" && go build -o "$_capvid_bin" ./cmd/agent-deck ) || {
    echo "capability-vhs sandbox: build failed" >&2
    return 1 2>/dev/null || exit 1
  }
fi

# --- scratch HOME + isolated socket -----------------------------------------
_capvid_scratch="$(mktemp -d -t capvid-sandbox.XXXXXX)"
export HOME="$_capvid_scratch/home"
export XDG_CONFIG_HOME="$HOME/.config"
export XDG_STATE_HOME="$HOME/.local/state"
mkdir -p "$HOME" "$XDG_CONFIG_HOME" "$XDG_STATE_HOME"

# Per-recording tmux socket. Isolation is driven by `[tmux].socket_name` in the
# scratch config.toml below (read once at startup by tmux.SetDefaultSocketName,
# then threaded as `-L <name>` onto every tmux call). A unique name per recording
# means panes spawn on a throwaway server, never the user's default — verified by
# code (internal/tmux/socket.go), unlike the legacy AGENT_DECK_TMUX_SOCKET env
# which the binary does not read. We still export the name for the cleanup trap.
_capvid_socket="capvid-$$-$(date +%s)"
export CAPVID_TMUX_SOCKET="$_capvid_socket"
export AGENT_DECK_TEST_ISOLATED=1
unset TMUX TMUX_PANE

# Deterministic, color-free output so the recording is crisp and stable.
export AGENTDECK_COLOR=none
export NO_COLOR=1

# --- put the binary on PATH as `agent-deck` ---------------------------------
# A symlink (not an alias) so the name resolves the same way it would for a real
# user typing the command, and so child processes inherit it via PATH.
_capvid_bindir="$_capvid_scratch/bin"
mkdir -p "$_capvid_bindir"
ln -sf "$_capvid_bin" "$_capvid_bindir/agent-deck"
export PATH="$_capvid_bindir:$PATH"

# --- install the deterministic echo agent + register it as a tool -----------
# Same stand-in the Go suite uses (tests/capability/testdata/echobot.sh): prints
# a ready marker, echoes each line back as ECHO:<line>. prompt_patterns wires it
# into agent-deck's readiness gate exactly as a real claude prompt would be.
mkdir -p "$HOME/.agent-deck"
cp "$_capvid_repo/tests/capability/testdata/echobot.sh" "$HOME/.agent-deck/echobot.sh"
chmod +x "$HOME/.agent-deck/echobot.sh"
cat > "$HOME/.agent-deck/config.toml" <<TOML
[tmux]
# Isolate every tmux server this recording spawns onto a unique named socket
# (tmux -L), so a render can never touch the user's default tmux server. This is
# the mechanism the binary actually honors (internal/tmux/socket.go).
socket_name = "$_capvid_socket"

[tools.echobot]
command = "$HOME/.agent-deck/echobot.sh"
icon = "E"
prompt_patterns = ["ECHOBOT READY"]
busy_patterns = ["WORKING"]
TOML

# A scratch project directory for sessions to run in.
export CAPVID_PROJECT="$HOME/project"
mkdir -p "$CAPVID_PROJECT"

# --- teardown ---------------------------------------------------------------
# Fires when the tape's shell exits at end of recording: kill the scratch tmux
# server (so no pane lingers) and remove the scratch tree.
_capvid_cleanup() {
  "$_capvid_bin" --help >/dev/null 2>&1 || true
  tmux -L "$_capvid_socket" kill-server >/dev/null 2>&1 || true
  rm -rf "$_capvid_scratch" >/dev/null 2>&1 || true
}
trap _capvid_cleanup EXIT

# Quiet success marker (visible if a tape forgets to Hide this line).
clear
