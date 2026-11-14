#!/bin/bash
# import.sh - Import a shared Claude session file
#
# Usage: import.sh <file-path> [options]
#
# Options:
#   --title <name>     Override session title (default: from file)
#   --project <path>   Import to specific project (default: current directory)
#   --no-start         Don't start the session after import
#
# Examples:
#   import.sh ~/Downloads/session-2024-01-20-feature.json
#   import.sh session.json --title "Continued Work"
#   import.sh session.json --project /path/to/project

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/utils.sh"

# Cleanup function for error cases
CLEANUP_FILES=()
cleanup_on_error() {
    for file in "${CLEANUP_FILES[@]}"; do
        if [ -f "$file" ]; then
            rm -f "$file"
        fi
    done
}
trap 'cleanup_on_error' ERR

# Parse arguments
INPUT_FILE=""
TITLE_OVERRIDE=""
PROJECT_PATH=""
NO_START=false

while [ $# -gt 0 ]; do
    case "$1" in
        --title)
            if [ -z "${2:-}" ] || [[ "${2:-}" == --* ]]; then
                echo "Error: --title requires an argument" >&2
                exit 1
            fi
            TITLE_OVERRIDE="$2"
            shift 2
            ;;
        --project)
            if [ -z "${2:-}" ] || [[ "${2:-}" == --* ]]; then
                echo "Error: --project requires an argument" >&2
                exit 1
            fi
            PROJECT_PATH="$2"
            shift 2
            ;;
        --no-start)
            NO_START=true
            shift
            ;;
        *)
            if [ -z "$INPUT_FILE" ]; then
                INPUT_FILE="$1"
            fi
            shift
            ;;
    esac
done

if [ -z "$INPUT_FILE" ]; then
    echo "Usage: import.sh <file-path> [--title name] [--project path]" >&2
    exit 1
fi

# Resolve file path
if [[ "$INPUT_FILE" != /* ]]; then
    INPUT_FILE="$(pwd)/$INPUT_FILE"
fi

if [ ! -f "$INPUT_FILE" ]; then
    echo "Error: File not found: $INPUT_FILE" >&2
    exit 1
fi

echo "Importing session from: $INPUT_FILE"

# Parse export file
echo "Reading export file..."

VERSION=$(jq -r '.version // "unknown"' "$INPUT_FILE")
EXPORTED_BY=$(jq -r '.exported_by // "unknown"' "$INPUT_FILE")
EXPORTED_AT=$(jq -r '.exported_at // "unknown"' "$INPUT_FILE")
SESSION_ID=$(jq -r '.session.id // empty' "$INPUT_FILE")
SESSION_TITLE=$(jq -r '.session.title // "Imported Session"' "$INPUT_FILE")
ORIGINAL_PROJECT=$(jq -r '.session.original_project // empty' "$INPUT_FILE")
SUMMARY=$(jq -r '.context.summary // ""' "$INPUT_FILE")

if [ -z "$SESSION_ID" ]; then
    echo "Error: Invalid export file - no session ID found" >&2
    exit 1
fi

# Validate session ID format (UUID or alphanumeric with hyphens/underscores)
if ! [[ "$SESSION_ID" =~ ^[a-zA-Z0-9_-]+$ ]]; then
    echo "Error: Invalid session ID format: $SESSION_ID" >&2
    exit 1
fi

echo ""
echo "Session Details:"
echo "  ID: $SESSION_ID"
echo "  Title: $SESSION_TITLE"
echo "  Exported by: $EXPORTED_BY"
echo "  Exported at: $EXPORTED_AT"
echo "  Original project: $ORIGINAL_PROJECT"
if [ -n "$SUMMARY" ]; then
    echo "  Summary: ${SUMMARY:0:100}..."
fi

# Use title override if provided
if [ -n "$TITLE_OVERRIDE" ]; then
    SESSION_TITLE="$TITLE_OVERRIDE"
fi

# Use current directory if no project specified
if [ -z "$PROJECT_PATH" ]; then
    PROJECT_PATH=$(pwd)
fi

echo ""
echo "Import destination: $PROJECT_PATH"

# Detect current profile (filter out debug timestamp lines that start with year digits)
CURRENT_OUTPUT=$(agent-deck session current --json 2>&1 || true)
PROFILE=$(echo "$CURRENT_OUTPUT" | grep -E '^\s*\{' | head -1 | jq -r '.profile // "default"' 2>/dev/null || echo "default")
echo "Profile: $PROFILE"

# Create the Claude projects directory for this project
ENCODED_PATH=$(encode_path "$PROJECT_PATH")
DEST_DIR="$(get_claude_projects_dir)/$ENCODED_PATH"
mkdir -p "$DEST_DIR"

# Extract and write JSONL
DEST_FILE="$DEST_DIR/$SESSION_ID.jsonl"

echo ""
echo "Writing session file to: $DEST_FILE"

# Check if file already exists
if [ -f "$DEST_FILE" ]; then
    echo "Warning: Session file already exists. Creating backup..."
    cp "$DEST_FILE" "$DEST_FILE.backup.$(date +%s)"
fi

# Track file for cleanup on error
CLEANUP_FILES+=("$DEST_FILE")

# Extract messages and write as JSONL
if ! jq -c '.messages[]' "$INPUT_FILE" > "$DEST_FILE"; then
    echo "Error: Failed to extract messages from export file" >&2
    exit 1
fi

# Verify written file
if [ ! -s "$DEST_FILE" ]; then
    echo "Error: Written file is empty or missing" >&2
    exit 1
fi

WRITTEN_LINES=$(wc -l < "$DEST_FILE" | tr -d ' ')
echo "Written $WRITTEN_LINES records"

# Remove from cleanup list since write succeeded
CLEANUP_FILES=()

# Create agent-deck session
echo ""
echo "Creating agent-deck session..."

# Build the session title with "Imported:" prefix
IMPORT_TITLE="Imported: $SESSION_TITLE"

# Check if session with this title already exists (use --arg to prevent command injection)
EXISTING=$(agent-deck -p "$PROFILE" list --json 2>/dev/null | jq -r --arg title "$IMPORT_TITLE" '.[] | select(.title == $title) | .id' || echo "")

if [ -n "$EXISTING" ]; then
    echo "Session '$IMPORT_TITLE' already exists. Updating..."
    # Just update the session to point to the new session ID
    agent-deck -p "$PROFILE" session set "$IMPORT_TITLE" claude-session-id "$SESSION_ID"
else
    # Create new session
    agent-deck -p "$PROFILE" add -t "$IMPORT_TITLE" -c claude "$PROJECT_PATH"

    # Poll for session creation (up to 10 attempts, 0.5s each)
    SESSION_FOUND=false
    for i in {1..10}; do
        if agent-deck -p "$PROFILE" list --json 2>/dev/null | jq -e --arg title "$IMPORT_TITLE" '.[] | select(.title == $title)' > /dev/null 2>&1; then
            SESSION_FOUND=true
            break
        fi
        sleep 0.5
    done

    if [ "$SESSION_FOUND" = "false" ]; then
        echo "Warning: Session creation may not have completed. Proceeding anyway..." >&2
    fi

    # Set the Claude session ID so it resumes the imported session
    agent-deck -p "$PROFILE" session set "$IMPORT_TITLE" claude-session-id "$SESSION_ID"
fi

echo ""
echo "=========================================="
echo "Session imported successfully!"
echo "=========================================="
echo ""
echo "Session: $IMPORT_TITLE"
echo "Claude Session ID: $SESSION_ID"
echo ""

if [ "$NO_START" = "true" ]; then
    echo "Session created but not started (--no-start)"
    echo ""
    echo "To start: agent-deck session start \"$IMPORT_TITLE\""
    echo "Or: Open agent-deck TUI and press Enter on the session"
else
    echo "Starting session..."
    agent-deck -p "$PROFILE" session start "$IMPORT_TITLE"

    echo ""
    echo "Session is now running. It will resume from the imported conversation."
    echo ""
    echo "To attach: agent-deck session attach \"$IMPORT_TITLE\""
    echo "Or: Open agent-deck TUI and press Enter on the session"
fi
