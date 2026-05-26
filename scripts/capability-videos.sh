#!/usr/bin/env bash
#
# capability-videos.sh: render the capability VHS tapes into web-embeddable
# WebM (plus GIF) and write a small manifest mapping capability -> video.
#
# This is an ON-DEMAND / NIGHTLY artifact, NOT part of the release gate: VHS
# rendering spins up a headless browser per tape and takes tens of seconds each,
# far too slow for the fast gate. scripts/capability-e2e.sh remains the gate;
# this script just produces the watchable proof videos a maintainer can play.
#
# Each tape records the REAL agent-deck binary against an isolated sandbox
# (scratch HOME + a per-recording tmux -L socket via [tmux].socket_name, TMUX
# unset). The headline clip, tui-walkthrough.tape, drives the actual interactive
# TUI — navigate the session list, attach into a live pane, watch the echobot
# echo a token back on screen — so you SEE the interface work, not CLI text. The
# echobot reply is genuine agent output; nothing on screen is hardcoded. VHS only
# supplies the paced terminal, the headless render, and the WebM encode.
#
# Usage:
#   scripts/capability-videos.sh            # render all tapes
#   scripts/capability-videos.sh echo-roundtrip lifecycle-stop   # subset by name
#
# Requirements (already installed on the build host): vhs, ttyd, ffmpeg, and an
# X display (DISPLAY). Set up Xvfb if running truly headless:
#   xvfb-run -a scripts/capability-videos.sh
set -euo pipefail

cd "$(dirname "$0")/.."
REPO="$PWD"

TAPE_DIR="tests/capability/vhs"
OUT_DIR="docs/status/videos"
MANIFEST="$OUT_DIR/manifest.json"
BIN="$TAPE_DIR/.bin/agent-deck"

if ! command -v vhs >/dev/null 2>&1; then
  echo "capability-videos: vhs not found on PATH (install charmbracelet/vhs)" >&2
  exit 2
fi

# Same refusal guard as scripts/capability-e2e.sh: never render from inside a
# live tmux unless explicitly marked isolated. The tapes force a per-recording
# socket, but a stray run from a real conductor pane must not risk it.
if [[ -n "${TMUX:-}" && -z "${AGENT_DECK_TEST_ISOLATED:-}" ]]; then
  echo "capability-videos: refusing to run with TMUX set and AGENT_DECK_TEST_ISOLATED unset." >&2
  echo "  Run from outside tmux, or export AGENT_DECK_TEST_ISOLATED=1 if the run is isolated." >&2
  exit 2
fi

mkdir -p "$OUT_DIR" "$(dirname "$BIN")"

echo "capability-videos: building agent-deck binary -> $BIN"
go build -o "$BIN" ./cmd/agent-deck

# Exported into the tape shell (VHS passes the environment through to ttyd).
# The tapes source $CAPVID_REPO/tests/capability/vhs/lib/sandbox.sh, which reads
# CAPVID_BIN for the prebuilt binary so the recording never waits on a build.
export CAPVID_REPO="$REPO"
export CAPVID_BIN="$REPO/$BIN"

# Select tapes: explicit args (by basename, with or without .tape) or all.
declare -a TAPES
if [[ $# -gt 0 ]]; then
  for name in "$@"; do
    name="${name%.tape}"
    TAPES+=("$TAPE_DIR/$name.tape")
  done
else
  for t in "$TAPE_DIR"/*.tape; do
    TAPES+=("$t")
  done
fi

# ffprobe a rendered file's duration in seconds (rounded to 1 decimal), or "".
probe_duration() {
  local f="$1"
  command -v ffprobe >/dev/null 2>&1 || { echo ""; return; }
  ffprobe -v error -show_entries format=duration \
    -of default=noprint_wrappers=1:nokey=1 "$f" 2>/dev/null \
    | awk '{printf "%.1f", $1}'
}

entries=()
for tape in "${TAPES[@]}"; do
  if [[ ! -f "$tape" ]]; then
    echo "capability-videos: tape not found: $tape" >&2
    exit 1
  fi
  cap="$(basename "$tape" .tape)"
  echo "capability-videos: rendering $cap ..."
  vhs "$tape"

  webm="$OUT_DIR/$cap.webm"
  if [[ ! -s "$webm" ]]; then
    echo "capability-videos: expected non-empty $webm after rendering $tape" >&2
    exit 1
  fi
  dur="$(probe_duration "$webm")"
  size="$(wc -c < "$webm" | tr -d ' ')"
  echo "capability-videos:   -> $webm (${size} bytes, ${dur}s)"
  gif=""
  [[ -s "$OUT_DIR/$cap.gif" ]] && gif="$cap.gif"
  entries+=("$(printf '{"capability":"%s","webm":"%s","gif":"%s","seconds":%s,"bytes":%s}' \
    "$cap" "$cap.webm" "$gif" "${dur:-0}" "$size")")
done

# Write the manifest (capability -> video path + duration + size).
{
  echo "{"
  echo "  \"generated\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\","
  echo "  \"videos\": ["
  for i in "${!entries[@]}"; do
    sep=","
    [[ $i -eq $((${#entries[@]} - 1)) ]] && sep=""
    echo "    ${entries[$i]}$sep"
  done
  echo "  ]"
  echo "}"
} > "$MANIFEST"

echo "capability-videos: wrote manifest $MANIFEST (${#entries[@]} videos)"
echo "capability-videos: done. Open docs/status/capability-videos.html to watch."
