# tui-walkthrough.tape — the headline capability clip: drive the REAL TUI.
#
# This records the interactive agent-deck TUI being navigated, not CLI commands
# typed at a prompt. You watch the actual interface work: the session list with
# live status, the selection moving, attaching into a session's real tmux pane,
# the echobot agent receiving a token and echoing "ECHO:<token>" back ON SCREEN,
# then detaching and quitting. Nothing on screen is faked — the echo is genuine
# agent output read from a live pane (see tests/capability/roundtrip_test.go).
#
# Keybindings are confirmed from internal/ui/home.go + internal/ui/hotkeys.go:
#   Down / j        move the selection down the session list
#   Enter           attach into the selected session's live tmux pane
#   Ctrl+Q          detach from the pane back to the list (default detach key)
#   q               quit the TUI cleanly
#
# Rendered by scripts/capability-videos.sh, which exports CAPVID_REPO / CAPVID_BIN
# / CAPVID_PROJECT and builds the binary once. The Hide block sources the shared
# sandbox (isolated HOME + a per-recording tmux -L socket via [tmux].socket_name,
# TMUX unset) off camera, so no real session is ever touched. VHS runs in ttyd —
# not inside tmux — so the TUI's "don't run inside tmux" guard does not trip.

Output "docs/status/videos/tui-walkthrough.webm"
Output "docs/status/videos/tui-walkthrough.gif"

Set Shell "bash"
Set FontSize 16
Set Width 1500
Set Height 820
Set Padding 20
Set TypingSpeed 55ms

# --- off-camera sandbox: isolated HOME + tmux socket, two real echobot sessions
Hide
Type "source $CAPVID_REPO/tests/capability/vhs/lib/sandbox.sh"
Enter
Sleep 2s
Type "agent-deck launch $CAPVID_PROJECT -c echobot -t cap-echo-1 >/dev/null 2>&1"
Enter
Sleep 1500ms
Type "agent-deck launch $CAPVID_PROJECT -c echobot -t cap-echo-2 >/dev/null 2>&1"
Enter
Sleep 1500ms
Type "clear"
Enter
Show

# --- launch the interactive TUI (bare command, no subcommand opens the TUI) ---
Type "agent-deck"
Sleep 600ms
Enter
Sleep 3500ms

# --- navigate: move the selection down onto the first echobot session ---
Down
Sleep 2500ms

# --- attach into the live session pane (real tmux attach via tea.Exec) ---
Enter
Sleep 2500ms

# --- talk to the agent: type a token and watch it echo back LIVE in the pane ---
Type "PING-TUI-9f3a"
Sleep 900ms
Enter
Sleep 4s

# --- detach back to the session list (Ctrl+Q is agent-deck's detach key) ---
Ctrl+Q
Sleep 2500ms

# --- quit the TUI cleanly ---
Type "q"
Sleep 1500ms
