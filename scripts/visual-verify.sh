#!/usr/bin/env bash
# visual-verify.sh — Capture screenshots of agent-deck TUI for visual verification.
#
# Usage:
#   ./scripts/visual-verify.sh [output-dir]
#
# Requires: tmux, freeze (charmbracelet), agent-deck binary at ./build/agent-deck
#
# Captures screenshots at key TUI states:
#   1. Main screen (empty state)
#   2. New session dialog
#   3. Settings panel
#   4. Main screen with a session running
#   5. Help overlay (if available)
#
# Each screenshot is a PNG in output-dir (default: /tmp/visual-verify).
# A verification checklist is written to output-dir/CHECKLIST.md.
#
# To verify with Claude Code, have it Read each PNG and compare against the checklist.

set -euo pipefail

OUTPUT_DIR="${1:-/tmp/visual-verify}"
BINARY="./build/agent-deck"
SESSION="visual_verify_$$"
PROFILE="_visual_verify"
CAPTURE_DELAY=1.5  # seconds to wait after each action

# --- Preflight ---

if ! command -v freeze &>/dev/null; then
    echo "ERROR: freeze not found. Install: https://github.com/charmbracelet/freeze"
    echo "  curl -sL https://github.com/charmbracelet/freeze/releases/download/v0.2.2/freeze_0.2.2_Linux_x86_64.tar.gz | tar xz"
    echo "  sudo mv freeze_0.2.2_Linux_x86_64/freeze /usr/local/bin/freeze"
    exit 1
fi

if ! command -v tmux &>/dev/null; then
    echo "ERROR: tmux not found."
    exit 1
fi

if [ ! -x "$BINARY" ]; then
    echo "ERROR: $BINARY not found or not executable. Run: make build"
    exit 1
fi

mkdir -p "$OUTPUT_DIR"

# --- Helpers ---

capture() {
    local name="$1"
    local file="$OUTPUT_DIR/${name}.png"
    tmux capture-pane -t "$SESSION" -p -e | freeze --language ansi --output "$file" 2>/dev/null
    local size
    size=$(du -h "$file" | cut -f1)
    echo "  Captured: $name ($size)"
}

wait_for_tui() {
    sleep "$CAPTURE_DELAY"
}

send_key() {
    tmux send-keys -t "$SESSION" "$@"
}

cleanup() {
    echo "Cleaning up..."
    tmux kill-session -t "$SESSION" 2>/dev/null || true
    # Remove test profile DB
    rm -f "$HOME/.agent-deck/profiles/$PROFILE/state.db" 2>/dev/null || true
    rmdir "$HOME/.agent-deck/profiles/$PROFILE" 2>/dev/null || true
}

trap cleanup EXIT

# --- Main ---

echo "=== Agent Deck Visual Verification ==="
echo "Output: $OUTPUT_DIR"
echo ""

# 1. Launch agent-deck in a tmux session
echo "[1/5] Launching agent-deck..."
tmux kill-session -t "$SESSION" 2>/dev/null || true
AGENTDECK_PROFILE="$PROFILE" tmux new-session -d -s "$SESSION" -x 120 -y 40 "$BINARY"
wait_for_tui
capture "01_main_empty"
echo "  -> Main screen with empty state"

# 2. Open New Session dialog
echo "[2/5] Opening New Session dialog..."
send_key n
wait_for_tui
capture "02_new_session_dialog"
echo "  -> New Session dialog form"

# Close dialog
send_key Escape
sleep 0.5

# 3. Open Settings panel
echo "[3/5] Opening Settings panel..."
send_key S
wait_for_tui
capture "03_settings_panel"
echo "  -> Settings panel"

# Close settings
send_key Escape
sleep 0.5

# 4. Create a session (type name, press Enter to create)
echo "[4/5] Creating a test session..."
send_key n
sleep 0.5
# Type session name
send_key -l "test-visual"
sleep 0.3
# Tab to accept defaults, then Enter to create
send_key Enter
sleep 3  # give the session time to launch
capture "04_session_running"
echo "  -> Main screen with session running"

# 5. Open help (? key)
echo "[5/5] Opening help overlay..."
send_key '?'
wait_for_tui
capture "05_help_overlay"
echo "  -> Help overlay"

# Close help
send_key Escape
sleep 0.5

# --- Write Checklist ---

cat > "$OUTPUT_DIR/CHECKLIST.md" << 'CHECKLIST'
# Agent Deck Visual Verification Checklist

Read each screenshot PNG and verify the items below. Report PASS/FAIL for each.

## 01_main_empty.png — Main Screen (Empty State)
- [ ] Header bar visible with "Agent Deck" title
- [ ] Version number displayed (top-right, e.g. "v0.27.5")
- [ ] Dual-pane layout: SESSIONS (left) and PREVIEW (right)
- [ ] "conductor" group visible in sidebar
- [ ] Status bar at bottom with keyboard shortcuts (Tab, n/N, g, r, d)
- [ ] Filter bar visible (All, status counts)
- [ ] No garbled text, broken Unicode, or rendering artifacts
- [ ] Tokyo Night dark theme colors (dark background, blue/cyan accents)

## 02_new_session_dialog.png — New Session Dialog
- [ ] Dialog title "New Session" visible
- [ ] "in group: conductor" label
- [ ] Name field with placeholder text
- [ ] Path field showing current directory
- [ ] Command selector: shell, claude, gemini, opencode, codex, pi
- [ ] "claude" is highlighted/selected by default
- [ ] Checkboxes: "Create in worktree", "Run in Docker sandbox"
- [ ] Claude Options section: Session type (New/Continue/Resume), Skip permissions, Chrome mode, Teammate mode
- [ ] Navigation hints at bottom: Tab, arrows, Enter, Esc
- [ ] Dialog has visible border/frame
- [ ] No text clipping or overflow

## 03_settings_panel.png — Settings Panel
- [ ] "Settings" title with "[Esc] Close"
- [ ] THEME section: Dark/Light/System options
- [ ] DEFAULT TOOL section: Claude/Gemini/OpenCode/Codex/Pi/None
- [ ] CLAUDE section with "Dangerous mode" and config directory
- [ ] GEMINI section with "YOLO mode"
- [ ] CODEX section with "YOLO mode"
- [ ] UPDATES section: startup check, auto-install toggles
- [ ] LOGS section: file size, lines to keep
- [ ] GLOBAL SEARCH section: enabled, search tier, recent days
- [ ] PREVIEW section visible (or indicated with "more below")
- [ ] Dashed separator line under title
- [ ] No overlapping or misaligned text

## 04_session_running.png — Main Screen with Session
- [ ] At least one session visible in the sidebar
- [ ] Session shows a status indicator (starting/running/waiting)
- [ ] Preview pane shows session content or output
- [ ] Status bar updated (session count > 0)
- [ ] No error indicators or crash messages

## 05_help_overlay.png — Help Overlay
- [ ] Help content visible (keyboard shortcuts listed)
- [ ] Overlay properly covers/dims the background
- [ ] Key bindings are readable and properly aligned
- [ ] Esc to close hint visible
CHECKLIST

echo ""
echo "=== Done ==="
echo "Screenshots: $(ls "$OUTPUT_DIR"/*.png 2>/dev/null | wc -l) files in $OUTPUT_DIR/"
echo "Checklist:   $OUTPUT_DIR/CHECKLIST.md"
echo ""
echo "To verify with Claude Code, run:"
echo "  claude 'Read each PNG in $OUTPUT_DIR/ and verify against $OUTPUT_DIR/CHECKLIST.md'"
