#!/bin/bash
# export.sh - Export current Claude session to a portable file
#
# Usage: export.sh [options]
#
# Options:
#   --session <id>     Export specific session (default: current)
#   --output <path>    Output file path (default: ~/session-shares/session-<date>-<title>.json)
#   --include-thinking Include thinking blocks (default: stripped)
#   --no-sanitize      Don't sanitize sensitive data
#
# Examples:
#   export.sh                              # Export current session
#   export.sh --session abc123             # Export specific session
#   export.sh --output /tmp/my-session.json

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/utils.sh"

# Parse arguments
SESSION_ID=""
OUTPUT_PATH=""
INCLUDE_THINKING=false
NO_SANITIZE=false

while [ $# -gt 0 ]; do
    case "$1" in
        --session)
            SESSION_ID="$2"
            shift 2
            ;;
        --output)
            OUTPUT_PATH="$2"
            shift 2
            ;;
        --include-thinking)
            INCLUDE_THINKING=true
            shift
            ;;
        --no-sanitize)
            NO_SANITIZE=true
            shift
            ;;
        *)
            echo "Unknown option: $1" >&2
            exit 1
            ;;
    esac
done

# Detect current session if not specified
if [ -z "$SESSION_ID" ]; then
    echo "Detecting current session..."
    CURRENT_JSON=$(agent-deck session current --json 2>/dev/null | grep -v '^20' || echo "{}")
    SESSION_ID=$(echo "$CURRENT_JSON" | jq -r '.claudeSessionId // empty')
    PROJECT_PATH=$(echo "$CURRENT_JSON" | jq -r '.projectPath // empty')
    SESSION_TITLE=$(echo "$CURRENT_JSON" | jq -r '.session // "exported"')

    if [ -z "$SESSION_ID" ] || [ "$SESSION_ID" = "null" ]; then
        echo "Error: Could not detect current Claude session." >&2
        echo "Make sure you're in an agent-deck session with an active Claude conversation." >&2
        exit 1
    fi
else
    # If session ID provided, try to find project path
    PROJECT_PATH=$(pwd)
    SESSION_TITLE="exported"
fi

echo "Session ID: $SESSION_ID"
echo "Project: $PROJECT_PATH"

# Find the JSONL file
JSONL_FILE=$(find_session_file "$SESSION_ID" "$PROJECT_PATH")

if [ -z "$JSONL_FILE" ] || [ ! -f "$JSONL_FILE" ]; then
    echo "Error: Could not find session file for ID: $SESSION_ID" >&2
    echo "Looked in: $(get_claude_projects_dir)/$(encode_path "$PROJECT_PATH")/" >&2
    exit 1
fi

echo "Found session file: $JSONL_FILE"
echo "Size: $(du -h "$JSONL_FILE" | cut -f1)"

# Generate output path if not specified
if [ -z "$OUTPUT_PATH" ]; then
    EXPORT_DIR=$(ensure_export_dir)
    FILENAME=$(generate_export_filename "$SESSION_TITLE")
    OUTPUT_PATH="$EXPORT_DIR/$FILENAME"
fi

# Read and process JSONL
echo "Processing session data..."

# Count messages
TOTAL_LINES=$(wc -l < "$JSONL_FILE" | tr -d ' ')
USER_MSGS=$(grep -c '"type":"user"' "$JSONL_FILE" || echo 0)
ASSISTANT_MSGS=$(grep -c '"type":"assistant"' "$JSONL_FILE" || echo 0)

echo "  Total records: $TOTAL_LINES"
echo "  User messages: $USER_MSGS"
echo "  Assistant messages: $ASSISTANT_MSGS"

# Extract context
SUMMARY=$(extract_summary "$JSONL_FILE")
MODIFIED_FILES=$(extract_modified_files "$JSONL_FILE")

# Create a temporary file for processed content
TEMP_FILE=$(mktemp)
trap 'rm -f "$TEMP_FILE"' EXIT

# Process JSONL content
if [ "$NO_SANITIZE" = "false" ]; then
    echo "Sanitizing sensitive data..."
fi

while IFS= read -r line || [ -n "$line" ]; do
    if [ -n "$line" ]; then
        processed_line="$line"

        # Sanitize if needed
        if [ "$NO_SANITIZE" = "false" ]; then
            processed_line=$(sanitize_jsonl "$processed_line")
        fi

        # Strip thinking blocks if needed
        if [ "$INCLUDE_THINKING" = "false" ]; then
            # Real Claude Code JSONL has records where message.content is a
            # plain string (slash-command output, local-command caveats, etc.).
            # Only iterate `.message.content[]` when it's an array, otherwise
            # pass the record through unchanged — there are no thinking blocks
            # in a string payload to strip. (#895)
            filtered=$(echo "$processed_line" | jq -c 'if (.message.content | type) == "array" then .message.content |= map(select(.type != "thinking")) else . end' 2>/dev/null)
            if [ -n "$filtered" ]; then
                processed_line="$filtered"
            fi
        fi

        echo "$processed_line" >> "$TEMP_FILE"
    fi
done < "$JSONL_FILE"

# Create export JSON
echo "Creating export file..."

# Convert JSONL to JSON array and save to temp file
MESSAGES_FILE=$(mktemp)
if ! jq -s '.' "$TEMP_FILE" > "$MESSAGES_FILE" 2>/dev/null; then
    echo "[]" > "$MESSAGES_FILE"
fi

# Build export object using slurpfile to handle large message arrays
# This avoids "Argument list too long" errors for large sessions
if ! jq -n \
    --arg version "1.0" \
    --arg exported_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg exported_by "$(whoami)" \
    --arg session_id "$SESSION_ID" \
    --arg session_title "$SESSION_TITLE" \
    --arg original_project "$PROJECT_PATH" \
    --arg summary "$SUMMARY" \
    --slurpfile messages "$MESSAGES_FILE" \
    --arg modified_files "$MODIFIED_FILES" \
    '{
        version: $version,
        exported_at: $exported_at,
        exported_by: $exported_by,
        session: {
            id: $session_id,
            title: $session_title,
            original_project: $original_project
        },
        context: {
            summary: $summary,
            modified_files: ($modified_files | split("\n") | map(select(. != "")))
        },
        messages: $messages[0],
        stats: {
            total_messages: ($messages[0] | length),
            user_messages: ($messages[0] | map(select(.type == "user")) | length),
            assistant_messages: ($messages[0] | map(select(.type == "assistant")) | length)
        }
    }' > "$OUTPUT_PATH"; then
    rm -f "$MESSAGES_FILE"
    echo "Error: Failed to create export JSON" >&2
    exit 1
fi

rm -f "$MESSAGES_FILE"

# Get final file size
FILE_SIZE=$(du -h "$OUTPUT_PATH" | cut -f1)

echo ""
echo "=========================================="
echo "Session exported successfully!"
echo "=========================================="
echo ""
echo "Output file: $OUTPUT_PATH"
echo "File size: $FILE_SIZE"
echo ""
echo "To share: Send this file to another developer via Slack, email, AirDrop, etc."
echo ""
echo "They can import it with:"
echo "  /session-share:import $OUTPUT_PATH"
echo ""
