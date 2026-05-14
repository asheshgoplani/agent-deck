#!/usr/bin/env bash
# Regression tests for https://github.com/asheshgoplani/agent-deck/issues/895
# Two bugs caused `export.sh` to silently exit 5 on real Claude Code sessions:
#   - Bug 1: encode_path didn't mirror Claude Code's `.` -> `-` encoding,
#            so projects whose path contains `.` couldn't be located.
#   - Bug 2: jq filter at export.sh:130 assumed `.message.content` was always
#            an array; on a string-shaped record, jq errored and `set -euo
#            pipefail` killed the script with exit 5.

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXPORT_SH="$SCRIPT_DIR/../scripts/export.sh"

if [ ! -x "$EXPORT_SH" ] && [ ! -f "$EXPORT_SH" ]; then
    echo "FAIL: cannot locate $EXPORT_SH" >&2
    exit 99
fi

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

make_fixture() {
    # Args: $1 = project-dir-template (mktemp template, no `.` if you want to
    #            isolate bug 2 from bug 1)
    # Echoes: "<HOME> <PROJECT> <UUID>"
    local template="$1"
    local home_dir project_dir uuid
    home_dir=$(mktemp -d)
    project_dir=$(mktemp -d -p /tmp "$template")
    uuid="11111111-2222-3333-4444-555555555555"

    # Mirror Claude Code's real encoding: both `/` and `.` become `-`.
    local encoded
    encoded=$(echo "$project_dir" | sed 's|^/|-|' | sed 's|[/.]|-|g')
    mkdir -p "$home_dir/.claude/projects/$encoded"

    # One user record whose message.content is a STRING (the bug 2 trigger
    # — Claude Code emits these for slash-command output, local-command
    # caveats, etc.) plus one normal assistant record.
    cat > "$home_dir/.claude/projects/$encoded/$uuid.jsonl" <<JSONL
{"type":"user","message":{"role":"user","content":"<local-command-stdout>example</local-command-stdout>"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"acknowledged"}]}}
JSONL

    echo "$home_dir $project_dir $uuid"
}

# --- Test 1: bug 2 — exit 5 on string-shaped message.content ---
test_export_handles_string_content() {
    local fx home_dir project_dir uuid output rc
    fx=$(make_fixture "tdd895-strcontent-XXXXXX")
    read -r home_dir project_dir uuid <<<"$fx"
    output="$home_dir/out.json"

    rc=0
    ( cd "$project_dir" && HOME="$home_dir" bash "$EXPORT_SH" \
        --session "$uuid" --no-sanitize --output "$output" >/dev/null 2>&1 ) \
        || rc=$?

    rm -rf "$home_dir" "$project_dir"

    if [ "$rc" -ne 0 ]; then
        echo "  expected exit 0, got $rc (regression for #895 bug 2)" >&2
        return 1
    fi
    return 0
}

# --- Test 2: bug 1 — encode_path must convert `.` to `-` ---
# Sourced helper test; doesn't run the full export.
test_encode_path_handles_dot() {
    local result
    # shellcheck source=../scripts/utils.sh
    result=$(bash -c '
        set -e
        source "$1"
        encode_path "/Users/first.last/Projects/my-app"
    ' _ "$SCRIPT_DIR/../scripts/utils.sh")

    local expected="-Users-first-last-Projects-my-app"
    if [ "$result" != "$expected" ]; then
        echo "  expected $expected, got $result (regression for #895 bug 1)" >&2
        return 1
    fi
    return 0
}

run_case "export.sh exits 0 when message.content is a string (#895 bug 2)" \
    test_export_handles_string_content
run_case "encode_path converts '.' to '-' to match Claude Code (#895 bug 1)" \
    test_encode_path_handles_dot

echo "---"
echo "passed: $pass_count, failed: $fail_count"
[ "$fail_count" -eq 0 ]
