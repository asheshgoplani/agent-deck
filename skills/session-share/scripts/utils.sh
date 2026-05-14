#!/bin/bash
# utils.sh - Shared utility functions for session-share skill

set -euo pipefail

# Encode a path the same way Claude Code does: both `/` and `.` become `-`.
# (See #895 — paths like /Users/first.last/... must encode to
# -Users-first-last-... to find the on-disk projects directory.)
encode_path() {
    local path="$1"
    echo "$path" | sed 's|^/|-|' | sed 's|[/.]|-|g'
}

# Decode an encoded path back to original
# WARNING: This is lossy - original hyphens in path names become slashes
# For our use case (session sharing), we mainly use encode_path
decode_path() {
    local encoded="$1"
    echo "$encoded" | sed 's|^-|/|' | sed 's|-|/|g'
}

# Get Claude projects directory
get_claude_projects_dir() {
    echo "$HOME/.claude/projects"
}

# Find JSONL file for a session ID in a project
find_session_file() {
    local session_id="$1"
    local project_path="$2"

    if [ -z "$session_id" ] || [ -z "$project_path" ]; then
        echo ""
        return 1
    fi

    local encoded_path=$(encode_path "$project_path")
    local projects_dir=$(get_claude_projects_dir)
    local session_file="$projects_dir/$encoded_path/$session_id.jsonl"

    if [ -f "$session_file" ]; then
        echo "$session_file"
    else
        echo ""
    fi
}

# Sanitize sensitive data from JSONL content
# Removes: API keys, tokens, absolute home paths, thinking blocks
sanitize_jsonl() {
    local content="$1"
    local home_path="$HOME"
    local username=$(whoami)

    # Escape special regex characters in home_path for sed
    local escaped_home=$(printf '%s\n' "$home_path" | sed 's/[[\.*^$()+?{|]/\\&/g')
    local escaped_username=$(printf '%s\n' "$username" | sed 's/[[\.*^$()+?{|]/\\&/g')

    echo "$content" | \
        # Remove common API key patterns
        sed -E 's/(api[_-]?key|apikey|token|secret|password|credential)["\s:=]+["\047]?[A-Za-z0-9_\-]{20,}["\047]?/\1="[REDACTED]"/gi' | \
        # Replace home directory with placeholder
        sed "s|$escaped_home|~|g" | \
        # Replace username in paths
        sed "s|/Users/$escaped_username|/Users/\$USER|g" | \
        # Remove thinking blocks (they contain internal reasoning)
        jq -c 'if (.message.content | type) == "array" then .message.content |= map(select(.type != "thinking")) else . end' 2>/dev/null || echo "$content"
}

# Generate export filename
generate_export_filename() {
    local title="$1"
    local date=$(date +%Y-%m-%d)
    local safe_title=$(echo "$title" | tr ' ' '-' | tr '[:upper:]' '[:lower:]' | tr -cd '[:alnum:]-' | head -c 30)
    echo "session-${date}-${safe_title}.json"
}

# Create export directory if needed
ensure_export_dir() {
    local export_dir="$HOME/session-shares"
    mkdir -p "$export_dir"
    echo "$export_dir"
}

# Extract session summary from JSONL (last few user messages)
extract_summary() {
    local jsonl_file="$1"

    # Get last 3 user messages as context
    grep '"type":"user"' "$jsonl_file" | \
        tail -5 | \
        jq -r '.message.content | if type == "array" then .[0].text // "" else . end' 2>/dev/null | \
        head -c 500
}

# Extract key decisions from assistant messages
extract_decisions() {
    local jsonl_file="$1"

    # Look for decision-like patterns in recent assistant messages
    grep '"type":"assistant"' "$jsonl_file" | \
        tail -10 | \
        jq -r '.message.content[] | select(.type == "text") | .text' 2>/dev/null | \
        grep -iE "(decided|choosing|will use|approach|going with|selected)" | \
        head -5
}

# Get list of files modified in session
extract_modified_files() {
    local jsonl_file="$1"

    grep '"type":"assistant"' "$jsonl_file" | \
        jq -r '.message.content[]? | select(.type == "tool_use") | select(.name == "Edit" or .name == "Write") | .input.file_path // .input.path' 2>/dev/null | \
        sort -u | \
        head -20
}
