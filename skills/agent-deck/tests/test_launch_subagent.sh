#!/usr/bin/env bash
# Hermetic tests for scripts/launch-subagent.sh.
#
# A stub `agent-deck` on PATH records every call (one line per call, args
# separated by " | ") and prints canned output, so no real agent-deck, tmux
# or Claude is touched. Each case runs under a scratch HOME.

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LAUNCH_SH="$SCRIPT_DIR/../scripts/launch-subagent.sh"

if [ ! -f "$LAUNCH_SH" ]; then
    echo "FAIL: cannot locate $LAUNCH_SH" >&2
    exit 99
fi
if ! command -v jq >/dev/null 2>&1; then
    echo "FAIL: jq is required by launch-subagent.sh" >&2
    exit 99
fi

ROOT=$(mktemp -d)
cleanup() {
    if command -v gio >/dev/null 2>&1; then
        gio trash "$ROOT" 2>/dev/null || rm -rf "$ROOT"
    else
        rm -rf "$ROOT"
    fi
}
trap cleanup EXIT

# --- stub agent-deck ---------------------------------------------------------
mkdir -p "$ROOT/bin"
cat > "$ROOT/bin/agent-deck" <<'STUB'
#!/usr/bin/env bash
# Record argv.
line=""
for a in "$@"; do line="${line}${line:+ | }${a//$'\n'/\\n}"; done
echo "$line" >> "$AD_LOG"

args=" $* "
case "$args" in
    *" config get "*)
        exit 0 ;;                               # key unset: prints nothing
    *" session current "*)
        if [ -n "${AD_NOT_IN_SESSION:-}" ]; then
            echo '{"success":false,"error":"not in a tmux session","code":"NOT_FOUND"}'
            exit 1
        fi
        printf '%s\n' "$AD_CURRENT_JSON"
        exit 0 ;;
    *" launch "*)
        echo "launched"; exit 0 ;;
    *" session show "*)
        printf 'Status: waiting\nTmux: agentdeck_stub_1\n'; exit 0 ;;
    *" session output "*)
        echo "CANNED-CHILD-RESPONSE"; exit 0 ;;
esac
echo "stub: unexpected call: $*" >&2
exit 3
STUB
chmod +x "$ROOT/bin/agent-deck"

pass_count=0
fail_count=0

run_case() {
    local name="$1"
    shift
    if "$@"; then
        echo "ok - $name"
        pass_count=$((pass_count + 1))
    else
        echo "not ok - $name" >&2
        fail_count=$((fail_count + 1))
    fi
}

# run_launch <case-dir> <current-json> [script args...]
# Sets RC, OUT (stdout), ERR (stderr) and LOG (recorded calls).
run_launch() {
    local dir="$1" cur="$2"
    shift 2
    mkdir -p "$dir/home"
    : > "$dir/calls.log"
    RC=0
    env -u TMUX -u AGENT_DECK_SESSION_ID -u AGENTDECK_INSTANCE_ID \
        HOME="$dir/home" XDG_CONFIG_HOME="$dir/home/.config" \
        PATH="$ROOT/bin:$PATH" AD_LOG="$dir/calls.log" AD_CURRENT_JSON="$cur" \
        ${AD_NOT_IN_SESSION:+AD_NOT_IN_SESSION=1} \
        bash "$LAUNCH_SH" "$@" >"$dir/out" 2>"$dir/err" || RC=$?
    OUT=$(cat "$dir/out")
    ERR=$(cat "$dir/err")
    LOG=$(cat "$dir/calls.log")
}

current_json() {
    # $1 = parent path ("" for none)
    jq -nc --arg p "$1" '{session:"parent-sess",title:"parent-sess",profile:"work",
        id:"c5bfd4b4",group:"team/workers",path:(if $p == "" then null else $p end)}'
}

# --- 1: usage — both Title and Prompt are required ---------------------------
test_usage_requires_title_and_prompt() {
    local d="$ROOT/c1"
    run_launch "$d" "$(current_json "$ROOT")" "OnlyTitle" --tool codex
    if [ "$RC" -ne 1 ]; then echo "  expected exit 1, got $RC" >&2; return 1; fi
    case "$ERR" in *'Usage: launch-subagent.sh "Title" "Prompt"'*) ;; *)
        echo "  usage line missing from stderr: $ERR" >&2; return 1 ;; esac
    if [ -n "$LOG" ]; then echo "  agent-deck was called before args were checked: $LOG" >&2; return 1; fi
    return 0
}

# --- 2: working dir — --path wins, else the parent session's path -----------
test_workdir_resolution() {
    local d="$ROOT/c2" parent_dir="$ROOT/c2-parent" explicit="$ROOT/c2-explicit"
    mkdir -p "$parent_dir"
    run_launch "$d" "$(current_json "$parent_dir")" "Kid" "Do X" --path "$explicit"
    if [ "$RC" -ne 0 ]; then echo "  --path run exited $RC: $ERR" >&2; return 1; fi
    case "$LOG" in *"launch | $explicit | -t | Kid"*) ;; *)
        echo "  --path not used as the launch dir: $LOG" >&2; return 1 ;; esac
    [ -d "$explicit" ] || { echo "  --path dir was not created" >&2; return 1; }

    run_launch "$d" "$(current_json "$parent_dir")" "Kid" "Do X"
    if [ "$RC" -ne 0 ]; then echo "  no-path run exited $RC: $ERR" >&2; return 1; fi
    case "$LOG" in *"launch | $parent_dir | -t | Kid"*) ;; *)
        echo "  parent path not inherited: $LOG" >&2; return 1 ;; esac
    case "$OUT" in *"Path:    $parent_dir"*) ;; *)
        echo "  summary does not show the inherited path" >&2; return 1 ;; esac
    return 0
}

# --- 3: parent linking — explicit --parent, parent profile and group ---------
test_launch_links_parent() {
    local d="$ROOT/c3"
    run_launch "$d" "$(current_json "$ROOT")" "Kid" "Do Y" --tool codex --mcp exa --mcp firecrawl
    if [ "$RC" -ne 0 ]; then echo "  exited $RC: $ERR" >&2; return 1; fi
    local launch_line
    launch_line=$(grep -F ' | launch | ' "$d/calls.log")
    case "$launch_line" in "-p | work | launch | "*) ;; *)
        echo "  launch not run in the parent's profile: $launch_line" >&2; return 1 ;; esac
    local want
    for want in "--parent | parent-sess" "-c | codex" "-m | Do Y" "-g | team/workers" \
                "--mcp | exa" "--mcp | firecrawl"; do
        case "$launch_line" in *"$want"*) ;; *)
            echo "  launch is missing '$want': $launch_line" >&2; return 1 ;; esac
    done
    return 0
}

# --- 4: --wait — reads the child's output with -q (inbox notice kept) -------
test_wait_reads_output_quietly() {
    local d="$ROOT/c4"
    run_launch "$d" "$(current_json "$ROOT")" "Kid" "Answer Z" --wait --timeout 30
    if [ "$RC" -ne 0 ]; then echo "  exited $RC: $ERR" >&2; return 1; fi
    grep -qF -- "-p | work | session | output | -q | Kid" "$d/calls.log" || {
        echo "  output not read with 'session output -q': $LOG" >&2; return 1; }
    case "$OUT" in *"=== Response ==="*"CANNED-CHILD-RESPONSE"*) ;; *)
        echo "  response not printed: $OUT" >&2; return 1 ;; esac
    return 0
}

# --- 5: not in an agent-deck session — clear error, nothing launched ---------
test_fails_outside_session() {
    local d="$ROOT/c5"
    AD_NOT_IN_SESSION=1 run_launch "$d" "" "Kid" "Do Z"
    if [ "$RC" -eq 0 ]; then echo "  expected a non-zero exit" >&2; return 1; fi
    case "$ERR" in *"Error: Not in an agent-deck session"*) ;; *)
        echo "  missing error message: $ERR" >&2; return 1 ;; esac
    if grep -qF ' | launch | ' "$d/calls.log"; then
        echo "  a session was launched anyway: $LOG" >&2; return 1
    fi
    return 0
}

run_case "usage: Title and Prompt are required, nothing is called" \
    test_usage_requires_title_and_prompt
run_case "working dir: --path wins, else the parent session's path" \
    test_workdir_resolution
run_case "launch links the parent: --parent, parent profile, parent group, MCPs" \
    test_launch_links_parent
run_case "--wait reads the child's reply with session output -q" \
    test_wait_reads_output_quietly
run_case "outside an agent-deck session: error and no launch" \
    test_fails_outside_session

echo "---"
echo "passed: $pass_count, failed: $fail_count"
[ "$fail_count" -eq 0 ]
