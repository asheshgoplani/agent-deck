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

## Switch sessions without detaching

Cycle between sessions while staying attached — no detach-then-reattach
round trip through the list.

> **Opt-in — unbound by default.** Switching *while attached* requires
> intercepting a control byte in the attach loop **before the attached
> program sees it**, so the chord is taken from whatever runs inside the
> session. There is no control byte that's safe to steal from every tool:
> the previously-suggested `Ctrl-S` is Claude Code's "stash prompt" key and
> the terminal XOFF flow-control freeze. The switcher therefore ships
> **disabled**. Enable it by binding a `ctrl+<letter>` chord your attached
> tools don't use:
>
> ```toml
> [hotkeys]
> switch_session = "ctrl+s"   # pick a key free in your inner tools
> ```
>
> The examples below assume you've bound `Ctrl-S`.

| Keystroke | What happens |
| --------- | ------------ |
| `Ctrl-S` | Open the session switcher, pre-highlighted on the session you're currently in. |

With the switcher open:

- **`Ctrl-S`** again — cycle **forward** (the first step lands on the
  most-recently-used *other* session); **`Ctrl-A`** — cycle **backward**.
  Once you've cycled at least once this way, the switcher **auto-attaches
  ~1 second after you stop** (the closest we can get to "switch when you
  let go of the key"; see below). Holding the key down advances a step and
  then stops — it does not spin through the list.
- **`Up` / `Down`** — browse without auto-committing. Touching an arrow
  cancels the pending auto-commit, so you stay put until you press Enter.
- **`Enter`** — attach to the highlight immediately.
- **`Esc`** — when you opened the switcher *while attached*, re-attach to
  the session you came from (you meant to switch, not to leave). When you
  opened it from the overview, it just closes.
- **`Ctrl-Q`** (the detach key) — leave the switcher *and* the session,
  dropping you in the overview.

The same `Ctrl-S` also works **from the overview list** — it opens the
switcher pre-highlighted on the session under the cursor, so you can hop
to a recent session without scrolling the grouped list.

> The switcher currently lists **local sessions only**. Remote (SSH) sessions
> use a separate attach path and aren't yet included in the picker.

A single `Ctrl-S` just opens the switcher (highlighting the session you're
already in, so an immediate `Enter` is a no-op) and waits — it only starts
the auto-attach countdown once you actually cycle inside it, so an
accidental press never yanks you away.

**Why a `ctrl+<letter>` chord and not `Ctrl-Tab` / `Ctrl-Shift-Tab`?**
Those chords only produce a distinct keystroke on terminals running an
enhanced keyboard protocol (kitty / Ghostty / WezTerm / foot), and not
reliably through an attach — everywhere else `Ctrl-Tab` is indistinguishable
from a plain `Tab`. A `ctrl+<letter>` byte is the only portable trigger.

**Why is it opt-in, and why not a built-in default key?** Because the
trigger is only useful if the attach loop grabs it *before* forwarding to
the attached program — which means that program never receives the byte.
Every `ctrl+<letter>` already means something to some tool: `Ctrl-S` is
Claude Code's "stash prompt" (and XON/XOFF flow-control), readline binds
`Ctrl-A`/`Ctrl-E`/`Ctrl-W`, and so on. There is no globally-safe choice, so
the switcher ships unbound and you pick a key that's free in the tools you
actually attach to.

**Why does it auto-commit instead of switching on key release?**
Terminals don't deliver key-*release* events without an enhanced
keyboard protocol that isn't available here, so "switch the moment you
release Ctrl" can't be detected. The idle auto-commit (~1s) approximates
it: tap to cycle, stop, and it lands. Press `Enter` to commit instantly
or `Esc` to back out.

The trigger is configured under `[hotkeys]` as `switch_session` (must be a
`ctrl+<letter>` chord); it is unbound by default and never overrides the
detach key.

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
  and the session-switch keys across keyboard-encoding modes (raw bytes,
  xterm, kitty).
- `internal/ui/session_switcher.go` — the in-attach switcher overlay.
- GitHub #356, #357 — earlier hardening of `Ctrl-Q` detection across
  encodings.
- GitHub #1112 — the cluster of remote / direct-type bugs that
  motivated this page; this entry covers sub-issue 4 (Ctrl-Q in iTerm
  tabs).
