#!/bin/bash
# launch-subagent.sh - Launch a sub-agent as child of current session
#
# Usage: launch-subagent.sh "Title" "Prompt" [options]
#
# Options:
#   --mcp <name>     Attach MCP (can repeat)
#   --tool <type>    Agent tool: claude, codex, gemini (default: claude)
#   --path <dir>     Working directory for the agent (default: /tmp/<title>)
#   --wait           Poll until complete, return output
#   --timeout <sec>  Wait timeout (default: 300)
#
# Examples:
#   launch-subagent.sh "Research" "Find info about X"
#   launch-subagent.sh "Task" "Do Y" --mcp exa --mcp firecrawl
#   launch-subagent.sh "Query" "Answer Z" --wait --timeout 120
#   launch-subagent.sh "Consult" "Review this approach" --tool codex --wait
#   launch-subagent.sh "Review" "Review the session_cmd.go" --tool codex --path /path/to/project --wait

set -e

# Parse arguments
TITLE=""
PROMPT=""
TOOL="claude"
WORK_PATH=""
MCPS=()
WAIT=false
TIMEOUT=300

while [ $# -gt 0 ]; do
    case "$1" in
        --mcp)
            MCPS+=("$2")
            shift 2
            ;;
        --tool)
            TOOL="$2"
            shift 2
            ;;
        --path)
            WORK_PATH="$2"
            shift 2
            ;;
        --wait)
            WAIT=true
            shift
            ;;
        --timeout)
            TIMEOUT="$2"
            shift 2
            ;;
        *)
            if [ -z "$TITLE" ]; then
                TITLE="$1"
            elif [ -z "$PROMPT" ]; then
                PROMPT="$1"
            fi
            shift
            ;;
    esac
done

if [ -z "$TITLE" ] || [ -z "$PROMPT" ]; then
    echo "Usage: launch-subagent.sh \"Title\" \"Prompt\" [--tool codex] [--path /dir] [--mcp name] [--wait]" >&2
    exit 1
fi

# Detect current session (filter out log lines starting with year)
CURRENT_JSON=$(agent-deck session current --json 2>/dev/null | grep -v '^20')
PARENT=$(echo "$CURRENT_JSON" | jq -r '.session')
PROFILE=$(echo "$CURRENT_JSON" | jq -r '.profile')
PARENT_PATH=$(echo "$CURRENT_JSON" | jq -r '.path')
PARENT_GROUP=$(echo "$CURRENT_JSON" | jq -r '.group // empty')

if [ -z "$PARENT" ] || [ "$PARENT" = "null" ]; then
    echo "Error: Not in an agent-deck session" >&2
    exit 1
fi

# Determine work directory: --path flag > parent session path > /tmp fallback
if [ -n "$WORK_PATH" ]; then
    WORK_DIR="$WORK_PATH"
elif [ -n "$PARENT_PATH" ] && [ "$PARENT_PATH" != "null" ]; then
    WORK_DIR="$PARENT_PATH"
else
    SAFE_TITLE=$(echo "$TITLE" | tr ' ' '-' | tr '[:upper:]' '[:lower:]' | tr -cd '[:alnum:]-')
    WORK_DIR="/tmp/${SAFE_TITLE}"
fi
mkdir -p "$WORK_DIR"

# Launch session and send initial prompt in one command
LAUNCH_CMD=(agent-deck -p "$PROFILE" launch "$WORK_DIR" -t "$TITLE" --parent "$PARENT" -c "$TOOL" -m "$PROMPT")
[ -n "$PARENT_GROUP" ] && LAUNCH_CMD+=(-g "$PARENT_GROUP")
for mcp in "${MCPS[@]}"; do
    LAUNCH_CMD+=(--mcp "$mcp")
done
"${LAUNCH_CMD[@]}"

# Get tmux session name (used for optional --wait fallback capture)
TMUX_SESSION=$(agent-deck -p "$PROFILE" session show "$TITLE" 2>/dev/null | grep '^Tmux:' | awk '{print $2}')

echo ""
echo "Sub-agent launched:"
echo "  Title:   $TITLE"
echo "  Tool:    $TOOL"
echo "  Parent:  $PARENT"
echo "  Profile: $PROFILE"
echo "  Path:    $WORK_DIR"
if [ ${#MCPS[@]} -gt 0 ]; then
    echo "  MCPs:    ${MCPS[*]}"
fi
echo ""
echo "Check output with: agent-deck session output \"$TITLE\""

# If --wait, poll until complete
if [ "$WAIT" = "true" ]; then
    echo ""
    echo "Waiting for completion (timeout: ${TIMEOUT}s)..."

    START_TIME=$(date +%s)
    while true; do
        STATUS=$(agent-deck -p "$PROFILE" session show "$TITLE" 2>/dev/null | grep '^Status:' | awk '{print $2}')

        if [ "$STATUS" = "◐" ] || [ "$STATUS" = "waiting" ]; then
            echo "Complete!"
            echo ""
            echo "=== Response ==="
            # Try native output first, fall back to full scrollback capture
            if ! agent-deck -p "$PROFILE" session output "$TITLE" 2>/dev/null; then
                tmux capture-pane -t "$TMUX_SESSION" -p -S - 2>/dev/null
            fi
            exit 0
        fi

        ELAPSED=$(($(date +%s) - START_TIME))
        if [ $ELAPSED -ge $TIMEOUT ]; then
            echo "Timeout after ${TIMEOUT}s (session still running)" >&2
            echo "Check later with: agent-deck session output \"$TITLE\""
            exit 1
        fi

        sleep 5
    done
fi
