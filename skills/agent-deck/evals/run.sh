#!/usr/bin/env bash
# Run every deterministic eval of the agent-deck skill. No model calls, no network,
# nothing written outside a temp dir; safe on a host.
#
#   skills/agent-deck/evals/run.sh [--binary PATH] [--answers FILE] [--log-dir DIR]
#
#   --binary PATH   agent-deck binary for the CLI drift check. Default: build this
#                   checkout into a temp dir when `go` is on PATH, else skip that check.
#   --answers FILE  also grade model answers (see RUNNER.md) with check.py grade.
#   --log-dir DIR   one log per step (default: a new temp dir, printed at the end).
#
# Exit 0 only when every step passed.
set -u
EVALS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKILL_DIR="$(dirname "$EVALS_DIR")"
REPO_DIR="$(cd "$SKILL_DIR/../.." && pwd)"
BINARY="" ANSWERS="" LOG_DIR=""
while [ $# -gt 0 ]; do
    case "$1" in
        --binary) BINARY="$2"; shift 2 ;;
        --answers) ANSWERS="$2"; shift 2 ;;
        --log-dir) LOG_DIR="$2"; shift 2 ;;
        -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done
abspath() { (cd "$(dirname "$1")" && printf '%s/%s\n' "$(pwd)" "$(basename "$1")"); }
[ -n "$BINARY" ] && BINARY="$(abspath "$BINARY")"
[ -n "$ANSWERS" ] && ANSWERS="$(abspath "$ANSWERS")"
[ -n "$LOG_DIR" ] && { mkdir -p "$LOG_DIR"; LOG_DIR="$(cd "$LOG_DIR" && pwd)"; }
TMP="$(mktemp -d -t skill-evals.XXXXXX)"   # left in place: holds the logs by default
LOG_DIR="${LOG_DIR:-$TMP/logs}"
mkdir -p "$LOG_DIR"

if [ -z "$BINARY" ] && command -v go >/dev/null && [ -f "$REPO_DIR/go.mod" ]; then
    if (cd "$REPO_DIR" && go build -o "$TMP/agent-deck" ./cmd/agent-deck) >"$LOG_DIR/build.log" 2>&1; then
        BINARY="$TMP/agent-deck"
    else
        echo "build failed, CLI drift check skipped (see $LOG_DIR/build.log)"
    fi
fi
BIN_ARGS=()
[ -n "$BINARY" ] && BIN_ARGS=(--binary "$BINARY")

declare -a NAMES=() RESULTS=()
step() {  # step <name> <command...>: run in a scratch HOME, log, record pass/fail
    local name="$1"; shift
    local home="$TMP/home-$name"
    mkdir -p "$home"
    local t0=$SECONDS
    if (cd "$SKILL_DIR" && env -u CLAUDE_CONFIG_DIR -u TMUX HOME="$home" TMUX_TMPDIR="$home" "$@") \
        >"$LOG_DIR/$name.log" 2>&1; then
        RESULTS+=("pass")
    else
        RESULTS+=("FAIL")
    fi
    NAMES+=("$name")
    printf '%-16s %s %4ss  %s\n' "$name" "${RESULTS[-1]}" "$((SECONDS - t0))" "$(tail -n 1 "$LOG_DIR/$name.log")"
}

step static        python3 -I evals/check.py static "${BIN_ARGS[@]}"
step mutate        python3 -I evals/check.py mutate "${BIN_ARGS[@]}"
step session-share bash tests/test_session_share_export_895_regression.sh
step launch-subagent bash tests/test_launch_subagent.sh
step retro         python3 -I -m unittest discover -s scripts/retro -p 'test_*.py'
step repro         python3 -I -m unittest discover -s scripts/repro -p 'test_*.py'
step goal          bash -c 'cd scripts/goal && python3 -m unittest discover -s tests'
[ -n "$ANSWERS" ] && step grade python3 -I evals/check.py grade "$ANSWERS"

fails=0
for r in "${RESULTS[@]}"; do [ "$r" = pass ] || fails=$((fails + 1)); done
echo "skill evals: $(( ${#RESULTS[@]} - fails ))/${#RESULTS[@]} steps passed; logs in $LOG_DIR"
[ "$fails" -eq 0 ]
