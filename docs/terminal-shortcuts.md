# Terminal shortcuts and gotchas

This page documents keyboard shortcuts that interact with agent-deck's
tmux-backed session model — and the small set of platform / terminal
quirks that can surprise users.

## Detach from an attached session

| Keystroke | What happens |
| --------- | ------------ |
| `Ctrl-Q`  | Detach from the currently attached agent-deck session and return to the agent-deck TUI. |

`Ctrl-Q` is agent-deck's wrapper for tmux's session-detach binding. It
works in every terminal emulator we ship support for (iTerm2, Terminal.app,
Alacritty, Ghostty, gnome-terminal, kitty, WezTerm, the Linux console).

## Known terminal gotchas

### iTerm2 tabs disconnect on `Ctrl-Q` (expected)

When agent-deck is attached to a session inside an iTerm2 tab and you
press `Ctrl-Q`, iTerm2 itself receives the keystroke first. iTerm2's
default key map binds `Ctrl-Q` to "soft-quit / close window," which
tears down the SSH tunnel and the visible agent-deck panes along with
it. This is intentional iTerm2 behavior, not an agent-deck bug — the
keystroke is consumed by the terminal before reaching tmux.

Workarounds, in order of preference:

1. **Use the agent-deck TUI's own back-out keys** (`q` from a session
   list view, `Esc` from most overlays) instead of `Ctrl-Q` when inside
   iTerm2. They go through the TUI's bubbletea event loop, never
   through tmux's detach binding, and never reach iTerm2's hotkey
   handler.
2. **Remap iTerm2's `Ctrl-Q`**: open iTerm2 → Preferences → Keys → Key
   Bindings, find `^Q`, and either delete the binding or change it to
   "Send Escape Sequence" with no payload. After that `Ctrl-Q` flows
   through to tmux exactly like every other terminal.
3. **Attach via the macOS Terminal.app or a different terminal** for
   workflows that rely heavily on `Ctrl-Q`. Terminal.app does not bind
   the keystroke by default.

This is the same class of conflict as macOS's system `Cmd-Q` (force-
quit) — a terminal-level binding wins against any program running
inside the terminal. Tracked at GitHub #1112 (bug 4). No code change
ships in agent-deck for this case; the documentation here is the fix.

### `Ctrl-Q` inside an outer tmux

If you've launched `agent-deck` inside an outer tmux instance and that
outer tmux's prefix is `Ctrl-Q`, the detach is consumed by the outer
tmux instead of the agent-deck-owned tmux. Pick a non-default prefix
for your outer tmux (`Ctrl-A` and `Ctrl-B` are the conventional
choices) to keep `Ctrl-Q` reserved for agent-deck's detach.

## Related references

- `internal/tmux/pty.go` — the agent-deck-side intercept for `Ctrl-Q`
  across keyboard-encoding modes (raw bytes, xterm, kitty).
- GitHub #356, #357 — earlier hardening of `Ctrl-Q` detection across
  encodings.
- GitHub #1112 — the cluster of remote / direct-type bugs that
  motivated this page; this entry covers sub-issue 4 (Ctrl-Q in iTerm
  tabs).
