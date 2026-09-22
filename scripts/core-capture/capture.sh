#!/usr/bin/env bash
# Behaviour capture for the slice-1 registry commands (docs/core-registry.md):
# session start/stop/restart, list, group list. Runs one agent-deck binary
# through a fixed script against a seeded sandbox store and writes, per case,
# <n>.cmd (argv), <n>.out (stdout), <n>.err (stderr) and <n>.code (exit
# status), scrubbed.
#
# usage: capture.sh <agent-deck-binary> <out-dir> [VAR=value ...]
#   Run it once with the origin/main binary and once with the branch binary,
#   then `diff -r` the two out-dirs: the slice-1 done proof is an empty diff.
#   Extra VAR=value arguments are added to the binary's environment (e.g.
#   AGENT_DECK_CORE_REGISTRY=0 to capture the legacy path of a new binary).
#
# Sandbox: HOME/XDG under a fixed per-user dir (so stored paths are identical
# between runs), a private tmux server via TMUX_TMPDIR, no inherited TMUX or
# AGENTDECK_* env. Only sessions this script created are stopped, by name,
# through `agent-deck session stop` (never a raw tmux kill).
# Scrub rules: RFC3339 timestamps -> <TS>; "elapsed_ms"/"ts"/"status_pass_ms"
# /"tmux_calls" numbers -> 0; "started <n>s ago" -> "started Ns ago";
# the random tmux-name suffix agentdeck_<title>_<8 hex> -> _<RAND>; the
# sandbox root -> <SANDBOX>; the private tmux dir -> <TMUX>. Nothing else.
set -uo pipefail

BIN=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
OUT=$2
shift 2
EXTRA_ENV=("$@")

ROOT=${CAPTURE_ROOT:-${TMPDIR:-/tmp}/ad-core-capture-$(id -u)}
# SEED holds the seeded store. The first run creates it; every later run
# (before and after binaries alike) restores it, so session ids match.
SEED=${CAPTURE_SEED:-$ROOT.seed}
# tmux socket paths are length-limited (104 bytes on macOS): keep it short.
TMUXDIR=$(mktemp -d /tmp/adc.XXXXXX)
if [ -e "$ROOT" ]; then
	trash "$ROOT" 2>/dev/null || mv "$ROOT" "$ROOT.old.$$"
fi
mkdir -p "$ROOT/home" "$ROOT/proj/alpha" "$ROOT/proj/beta" "$ROOT/proj/gamma" "$OUT"
RAW_OUT="$TMUXDIR/raw.out"
RAW_ERR="$TMUXDIR/raw.err"

ad() {
	env -i PATH="$PATH" HOME="$ROOT/home" USER="${USER:-u}" TERM=xterm-256color LANG=en_US.UTF-8 \
		XDG_CONFIG_HOME="$ROOT/home/.config" XDG_DATA_HOME="$ROOT/home/.local/share" \
		XDG_CACHE_HOME="$ROOT/home/.cache" TMUX_TMPDIR="$TMUXDIR" TMPDIR="${TMPDIR:-/tmp}" \
		AGENT_DECK_ALLOW_OUTER_TMUX=1 ${EXTRA_ENV[@]+"${EXTRA_ENV[@]}"} "$BIN" "$@"
}

scrub() {
	sed -E \
		-e "s#$ROOT#<SANDBOX>#g" \
		-e "s#$TMUXDIR#<TMUX>#g" \
		-e 's/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})/<TS>/g' \
		-e 's/"(elapsed_ms|ts|status_pass_ms|tmux_calls)": ?[0-9]+/"\1": 0/g' \
		-e 's/started [0-9]+s ago/started Ns ago/g' \
		-e 's/(agentdeck_[A-Za-z0-9-]+)_[0-9a-f]{8}/\1_<RAND>/g'
}

N=0
run() {
	N=$((N + 1))
	local id
	id=$(printf '%02d' "$N")
	printf '%s\n' "$*" >"$OUT/$id.cmd"
	ad "$@" </dev/null >"$RAW_OUT" 2>"$RAW_ERR"
	echo $? >"$OUT/$id.code"
	scrub <"$RAW_OUT" >"$OUT/$id.out"
	scrub <"$RAW_ERR" >"$OUT/$id.err"
}

# Seed (not captured): three shell sessions in a nested group tree, plus a
# serial group (max_concurrent 1) with two sessions. Shell sessions never
# reach status "running", so the cap never queues here; the queue/drain path
# is covered by internal/core unit tests instead.
# Every captured run starts from the restored copy, never from the seeding
# process itself, so no run sees state left in memory by `add`.
if [ ! -d "$SEED" ]; then
	mkdir -p "$ROOT/proj/q1" "$ROOT/proj/q2"
	ad add "$ROOT/proj/alpha" -t alpha -c bash -g work >/dev/null 2>&1
	ad add "$ROOT/proj/beta" -t beta -c bash -g work/api >/dev/null 2>&1
	ad add "$ROOT/proj/gamma" -t gamma -c bash -g misc >/dev/null 2>&1
	ad group create serial >/dev/null 2>&1
	ad group update serial --max-concurrent 1 >/dev/null 2>&1
	ad add "$ROOT/proj/q1" -t q1 -c bash -g serial >/dev/null 2>&1
	ad add "$ROOT/proj/q2" -t q2 -c bash -g serial >/dev/null 2>&1
	mkdir -p "$SEED"
	cp -R "$ROOT/." "$SEED/"
	trash "$ROOT" 2>/dev/null || mv "$ROOT" "$ROOT.old.$$"
	mkdir -p "$ROOT"
fi
cp -R "$SEED/." "$ROOT/"

# Help and flag errors.
run session start --help
run session stop -h
run session restart --help
run list --help
run group list --help
run session start --json=bogus alpha
run list --bogus

# Reads on a stopped store.
run list
run ls
run list --json
run list --all
run list --all --json
run list --include-superseded
run group
run group list
run group list --json
run group list -q
run group list --json -q

# Error paths.
run session start nosuch
run session start nosuch --json
run session stop alpha
run session stop alpha --json
run session stop
run session restart
run session restart --json
run session restart nosuch --json
run session restart --all
run session restart --all --json

# Lifecycle.
run session start alpha
run session start alpha
run session start alpha --json
run session start beta --json
run session start gamma -q
run group list
run group list --json
run session restart alpha
run session restart alpha --json
run session restart alpha --force
run session restart beta --force --json
run session restart gamma --env FOO=bar --json
run session restart --all --json
run session restart --all
run session restart --all -q
run session stop alpha
run session stop beta --json
run session stop gamma -q
run session stop gamma --json
run list --json
run group list --json

# Queue and drain: serial allows one running session.
run session start q1 --json
run session start q2 --json
run session start q2
run group list
run session stop q1 --json
run session stop q2

# Envelope mode is new in slice 1, so it only exists on the branch binary:
# CAPTURE_ENVELOPE=1 records it for reference, outside the byte-identity set.
if [ "${CAPTURE_ENVELOPE:-0}" = 1 ]; then
	run list --json=envelope
	run group list --json=envelope
	run session start nosuch --json=envelope
	run session restart alpha --json=envelope
fi

# Leave nothing running: stop every seeded session through agent-deck itself
# (already-stopped ones just report "not running").
for title in alpha beta gamma q1 q2; do
	ad session stop "$title" >/dev/null 2>&1
done
echo "captured $N cases into $OUT"
