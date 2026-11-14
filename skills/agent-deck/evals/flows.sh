#!/usr/bin/env bash
# Agent-flow evals: drive the send / child-completion / inbox flows the skill
# teaches against a real agent-deck binary, a real tmux server and a fake Claude.
# No model calls, no network. Everything runs in a throwaway HOME on a PRIVATE
# tmux socket; a tmux guard on PATH refuses any call that does not name it.
#
#   skills/agent-deck/evals/flows.sh [--binary PATH] [--work-dir DIR] [--log-dir DIR]
#                                    [--socket NAME] [--only a,b] [--keep]
#
#   --binary PATH   agent-deck binary under test. Default: build this checkout.
#   --work-dir DIR  parent of the throwaway sandbox (default: $TMPDIR, else /tmp).
#                   Keep it short: the tmux socket path must fit in 100 bytes.
#   --log-dir DIR   one log per eval (default: a new dir under --work-dir).
#   --socket NAME   private tmux socket name (default: agent-deck-evals-<pid>).
#   --only a,b      run only these evals (isolation always runs last).
#   --keep          keep the throwaway HOME for inspection (the private tmux
#                   server is still stopped).
#
# Exit 0 only when every eval passed. See RUNNER.md "Agent-flow evals".
set -u
EVALS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$EVALS_DIR/../../.." && pwd)"
BINARY="" WORK_DIR="${TMPDIR:-/tmp}" LOG_DIR="" SOCK="agent-deck-evals-$$" ONLY="" KEEP=""
while [ $# -gt 0 ]; do
    case "$1" in
        --binary) BINARY="$2"; shift 2 ;;
        --work-dir) WORK_DIR="$2"; shift 2 ;;
        --log-dir) LOG_DIR="$2"; shift 2 ;;
        --socket) SOCK="$2"; shift 2 ;;
        --only) ONLY=",$2,"; shift 2 ;;
        --keep) KEEP=1; shift ;;
        -h|--help) sed -n '2,19p' "$0"; exit 0 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done
case "$SOCK" in
    ""|default|*/*|*" "*) echo "refusing tmux socket name '$SOCK': it must be a private -L name" >&2; exit 2 ;;
esac
REAL_TMUX="$(command -v tmux || true)"
[ -n "$REAL_TMUX" ] || { echo "tmux not found: agent-flow evals need a real tmux" >&2; exit 2; }
command -v python3 >/dev/null || { echo "python3 not found" >&2; exit 2; }

# tmux socket paths are limited to ~100 bytes, so the sandbox lives in a short
# path (--work-dir), never under the caller's HOME.
mkdir -p "$WORK_DIR" && WORK_DIR="$(cd "$WORK_DIR" && pwd)" || { echo "cannot use --work-dir $WORK_DIR" >&2; exit 2; }
sock_path="$WORK_DIR/adflow.XXXXXX/tmux/tmux-$(id -u)/$SOCK"
[ "${#sock_path}" -le 100 ] || { echo "tmux socket path would be ${#sock_path} bytes (max 100): use a shorter --work-dir or --socket" >&2; exit 2; }
ROOT="$(mktemp -d "$WORK_DIR/adflow.XXXXXX")"
H="$ROOT/home"
if [ -z "$LOG_DIR" ]; then LOG_DIR="$(mktemp -d "$WORK_DIR/agent-flow-evals.XXXXXX")"; fi
mkdir -p "$LOG_DIR" && LOG_DIR="$(cd "$LOG_DIR" && pwd)"
GUARD_LOG="$LOG_DIR/tmux-calls.log"
: >"$GUARD_LOG"

trash() { if command -v gio >/dev/null 2>&1; then gio trash "$1" 2>/dev/null || rm -rf "$1"; else rm -rf "$1"; fi; }
stop_private_server() {
    # Only ever the private socket, never the default server.
    env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$ROOT/tmux" "$REAL_TMUX" -L "$SOCK" kill-server >/dev/null 2>&1 || true
}
cleanup() {
    stop_private_server
    if [ -n "$KEEP" ]; then echo "kept sandbox: $ROOT"; else trash "$ROOT"; fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

if [ -z "$BINARY" ]; then
    command -v go >/dev/null || { echo "go not found; pass --binary" >&2; exit 2; }
    (cd "$REPO_DIR" && go build -o "$ROOT/agent-deck" ./cmd/agent-deck) >"$LOG_DIR/build.log" 2>&1 \
        || { echo "build failed (see $LOG_DIR/build.log)" >&2; exit 2; }
    BINARY="$ROOT/agent-deck"
fi
BINARY="$(cd "$(dirname "$BINARY")" && pwd)/$(basename "$BINARY")"

mkdir -p "$ROOT/bin" "$ROOT/tmux" "$H/project" "$H/.config/agent-deck" "$H/.cache" "$H/.local/share" "$H/.local/state"
cp "$EVALS_DIR/flows/fake-claude" "$ROOT/bin/claude"
cp "$EVALS_DIR/flows/tmux-guard" "$ROOT/bin/tmux"
ln -s "$BINARY" "$ROOT/bin/agent-deck"
chmod 700 "$ROOT/bin/claude" "$ROOT/bin/tmux"
cat >"$H/.config/agent-deck/config.toml" <<EOF
[tmux]
socket_name = "$SOCK"
launch_in_user_scope = false
[telemetry]
disabled = true
[updates]
auto_update = false
check_enabled = false
EOF

# Allowlisted environment: nothing from the caller's agent identity, TMUX,
# accounts or XDG paths leaks in.
ENV=(env -i
    HOME="$H" XDG_CONFIG_HOME="$H/.config" XDG_CACHE_HOME="$H/.cache"
    XDG_DATA_HOME="$H/.local/share" XDG_STATE_HOME="$H/.local/state"
    PATH="$ROOT/bin:/usr/local/bin:/usr/bin:/bin" SHELL=/bin/bash
    LANG=C.UTF-8 LC_ALL=C.UTF-8 TERM=xterm-256color NO_COLOR=1
    AGENTDECK_SKIP_UPDATE_CHECK=1 AGENTDECK_TELEMETRY=0 DO_NOT_TRACK=1
    TMUX_TMPDIR="$ROOT/tmux" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
    AGENT_FLOW_BINARY="$BINARY" AGENT_FLOW_SOCKET="$SOCK"
    AGENT_FLOW_REAL_TMUX="$REAL_TMUX" AGENT_FLOW_TMUX_LOG="$GUARD_LOG")

LOG=/dev/null
ad() {  # ad <args...>: run agent-deck in the sandbox; output to stdout, echoed to the eval log
    local out rc
    out="$(cd "$H/project" && timeout 90 "${ENV[@]}" "$BINARY" "$@" 2>&1)"; rc=$?
    { printf '$ agent-deck %s  (rc=%d)\n%s\n' "$*" "$rc" "$out"; } >>"$LOG"
    printf '%s' "$out"
    return $rc
}
jq_py() {  # jq_py <python-expr over d> : read JSON from stdin
    python3 -I -c "import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(3)
r=($1)
print('' if r is None else r)"
}
fail() { echo "FAIL: $*" >>"$LOG"; echo "$*"; return 1; }
poll() {  # poll <seconds> <command...>: retry until it succeeds
    local end=$((SECONDS + $1)); shift
    while :; do
        "$@" && return 0
        [ $SECONDS -ge $end ] && return 1
        sleep 0.5
    done
}

PARENT="" PARENT2=""
add_parent() {  # add_parent <title>: add + start a fake-Claude session, print its id
    ad add -c claude -t "$1" "$H/project" >/dev/null || return 1
    ad session start "$1" >/dev/null || return 1
    ad session show --json "$1" | jq_py 'd["id"]'
}
launch_child() {  # launch_child <parent-id> <title> <message>: print the child id
    ad launch --json --parent "$1" -t "$2" -c claude -m "$3" "$H/project" | jq_py 'd["id"]'
}
child_row() {  # child_row <parent> <child> <field>
    ad session children --json "$1" | jq_py "next((c.get('$3') or '' for c in d.get('children') or [] if c['id']=='$2'), 'ABSENT')"
}
output_has() { ad session output "$1" | grep -qF -- "$2"; }
tick() { ad notify-daemon --once >/dev/null; }
child_done_is() {  # child_done_is <parent> <child> <status>
    tick; [ "$(child_row "$1" "$2" done_status)" = "$3" ]
}
# finished_events <drain-json> <child>: completion events for that child, one "status|summary" per line
finished_events() {
    printf '%s' "$1" | python3 -I -c "import json,sys
for e in json.load(sys.stdin) or []:
    if e.get('child_session_id')=='$2' and (e.get('kind')=='finished' or e.get('done_status')):
        print(e.get('done_status',''),e.get('done_summary',''),sep='|')"
}

# --- evals -------------------------------------------------------------------

eval_send_reply() {  # session send (default: waits for the composer) reaches the agent; output reads its reply
    PARENT="$(add_parent flow-parent)" || return 1
    [ -n "$PARENT" ] || fail "parent session has no id" || return 1
    ad session send flow-parent "EVAL_ECHO_ping$$" >/dev/null || fail "session send exited non-zero" || return 1
    poll 30 output_has flow-parent "echo EVAL_ECHO_ping$$" || fail "reply to the sent message never appeared in session output" || return 1
    echo "send delivered; session output shows the agent's reply"
}

child_completes() {  # child_completes <title> <token> <status> <summary>
    [ -n "$PARENT" ] || PARENT="$(add_parent flow-parent)" || return 1
    local child drain events
    child="$(launch_child "$PARENT" "$1" "$2")"
    [ -n "$child" ] || fail "launch --parent returned no child id" || return 1
    [ "$(ad session show --json "$child" | jq_py 'd.get("parent_session_id")')" = "$PARENT" ] \
        || fail "child is not linked to its parent" || return 1
    poll 40 child_done_is "$PARENT" "$child" "$3" \
        || fail "session children never showed done_status=$3 (last: '$(child_row "$PARENT" "$child" done_status)')" || return 1
    [ "$(child_row "$PARENT" "$child" done_summary)" = "$4" ] \
        || fail "done_summary is '$(child_row "$PARENT" "$child" done_summary)', want '$4'" || return 1
    drain="$(ad inbox drain --json "$PARENT")" || fail "inbox drain failed" || return 1
    events="$(finished_events "$drain" "$child")"
    [ "$events" = "$3|$4" ] || fail "inbox drain: want exactly one completion '$3|$4', got '$events'" || return 1
    tick
    drain="$(ad inbox drain --json "$PARENT")" || fail "second inbox drain failed" || return 1
    [ -z "$(finished_events "$drain" "$child")" ] || fail "completion was delivered again after it was drained" || return 1
    echo "children: done_status=$3; inbox delivered it once; second drain empty"
}
eval_child_done_ok()    { child_completes flow-child-ok    EVAL_DONE_OK    ok   "eval child finished ok"; }
eval_child_done_fail()  { child_completes flow-child-fail  EVAL_DONE_FAIL  fail "eval child failed on purpose"; }
eval_child_last_wins()  { child_completes flow-child-retry EVAL_DONE_RETRY ok   "retry succeeded"; }

child_not_done() {  # child_not_done <title> <token> <reply-fragment>
    [ -n "$PARENT" ] || PARENT="$(add_parent flow-parent)" || return 1
    local child drain
    child="$(launch_child "$PARENT" "$1" "$2")"
    [ -n "$child" ] || fail "launch --parent returned no child id" || return 1
    poll 40 output_has "$child" "$3" || fail "child never replied" || return 1
    poll 20 eval '[ "$(child_row "$PARENT" "$child" status)" = waiting ]' || fail "child never reached waiting" || return 1
    tick; tick
    [ -z "$(child_row "$PARENT" "$child" done_status)" ] \
        || fail "a turn without a valid sentinel was reported as done_status='$(child_row "$PARENT" "$child" done_status)'" || return 1
    drain="$(ad inbox drain --json "$PARENT")" || fail "inbox drain failed" || return 1
    [ -z "$(finished_events "$drain" "$child")" ] || fail "inbox carried a completion for a child that never asserted one" || return 1
    echo "child replied and is waiting; no completion in children or inbox"
}
eval_child_bad_sentinel() { child_not_done flow-child-bad  EVAL_DONE_BAD     "status=maybe"; }
eval_child_no_sentinel()  { child_not_done flow-child-echo EVAL_ECHO_nodone  "echo EVAL_ECHO_nodone"; }

eval_inbox_routing() {  # a completion reaches its own parent's inbox, never another parent's
    [ -n "$PARENT" ] || PARENT="$(add_parent flow-parent)" || return 1
    PARENT2="$(add_parent flow-parent-2)" || return 1
    ad inbox drain --json "$PARENT" >/dev/null
    local child drain
    child="$(launch_child "$PARENT2" flow-child-p2 EVAL_DONE_OK)"
    [ -n "$child" ] || fail "launch --parent returned no child id" || return 1
    poll 40 child_done_is "$PARENT2" "$child" ok || fail "second parent's child never showed done_status=ok" || return 1
    [ "$(child_row "$PARENT" "$child" id)" = ABSENT ] || fail "child is listed under the wrong parent" || return 1
    drain="$(ad inbox drain --json "$PARENT")" || return 1
    [ -z "$(finished_events "$drain" "$child")" ] || fail "completion leaked into another parent's inbox" || return 1
    drain="$(ad inbox drain --json "$PARENT2")" || return 1
    [ "$(finished_events "$drain" "$child")" = "ok|eval child finished ok" ] || fail "own parent's inbox lacks the completion" || return 1
    echo "completion delivered to its own parent only"
}

eval_isolation() {  # every tmux call named the private socket; the server lives in the sandbox
    local refused calls named
    cat "$GUARD_LOG" >>"$LOG"
    calls="$(grep -c . "$GUARD_LOG")"
    named="$(grep -c -- "-L $SOCK " "$GUARD_LOG")"
    refused="$(grep -c '^REFUSED' "$GUARD_LOG")"
    [ "$calls" -gt 0 ] || fail "agent-deck made no tmux calls; nothing was proven" || return 1
    [ "$refused" -eq 0 ] || fail "$refused tmux call(s) did not name -L $SOCK (see tmux-calls.log)" || return 1
    # The guard itself must fail closed: a call without -L is refused before
    # the real tmux runs (its own log, so it does not count as agent-deck's).
    local rc=0
    env -i PATH=/usr/bin:/bin AGENT_FLOW_SOCKET="$SOCK" AGENT_FLOW_REAL_TMUX=/bin/false \
        AGENT_FLOW_TMUX_LOG="$ROOT/guard-selftest.log" "$ROOT/bin/tmux" -u list-sessions 2>>"$LOG" || rc=$?
    [ "$rc" -eq 64 ] && grep -q '^REFUSED socket=<default>' "$ROOT/guard-selftest.log" \
        || fail "tmux guard did not refuse a default-socket call (rc=$rc)" || return 1
    [ -S "$ROOT/tmux/tmux-$(id -u)/$SOCK" ] || fail "private server socket not under the sandbox TMUX_TMPDIR" || return 1
    local listed
    listed="$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$ROOT/tmux" "$REAL_TMUX" -L "$SOCK" list-sessions -F '#{session_name}' 2>&1)"
    echo "private server sessions: $listed" >>"$LOG"
    case "$listed" in *agentdeck_flow-parent*) ;; *) fail "agent-deck sessions are not on the private server" || return 1 ;; esac
    echo "$calls tmux calls, $named named -L $SOCK, the rest -V, 0 refused; guard refuses default"
}

EVALS=(send_reply child_done_ok child_done_fail child_last_wins child_bad_sentinel child_no_sentinel inbox_routing isolation)
declare -a NAMES=() RESULTS=()
for e in "${EVALS[@]}"; do
    name="${e//_/-}"
    if [ -n "$ONLY" ] && [ "$e" != isolation ] && [[ "$ONLY" != *",$name,"* && "$ONLY" != *",$e,"* ]]; then continue; fi
    LOG="$LOG_DIR/$name.log"; : >"$LOG"
    t0=$SECONDS
    # Not in a subshell: evals share the parent sessions they create.
    if "eval_$e" >"$LOG.msg" 2>>"$LOG"; then RESULTS+=(pass); else RESULTS+=(FAIL); fi
    msg="$(cat "$LOG.msg")"; trash "$LOG.msg"
    NAMES+=("$name")
    printf '%-20s %s %4ss  %s\n' "$name" "${RESULTS[-1]}" "$((SECONDS - t0))" "$(printf '%s' "$msg" | tail -n 1)"
done

fails=0
for r in "${RESULTS[@]}"; do [ "$r" = pass ] || fails=$((fails + 1)); done
echo "agent-flow evals: $(( ${#RESULTS[@]} - fails ))/${#RESULTS[@]} passed; logs in $LOG_DIR"
[ "$fails" -eq 0 ]
