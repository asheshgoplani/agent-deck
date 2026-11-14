# Quick start and keys

[Back to the README](../../README.md) . [Guide index](README.md)

## Quick Start

```bash
agent-deck                        # Launch TUI
agent-deck add . -c claude        # Add current dir with Claude
agent-deck session fork my-proj   # Fork a supported session
agent-deck session send my-proj --message-file task.md # Send a multiline prompt
agent-deck session ref my-proj reviewer # Type a reference to my-proj into reviewer (not sent)
agent-deck session remove my-proj # Remove stopped/errored session from registry (transcripts preserved)
agent-deck mcp attach my-proj exa # Attach MCP to session
agent-deck skill attach my-proj docs --source pool --restart # Attach skill + restart
agent-deck web                    # Start web UI on http://127.0.0.1:8420
```

> **⚠️ Changed again after v1.16.32:** in the new-session dialog (`n`), **Enter creates the session from any row** with what is filled in: this folder, your last-used tool, the selected group, and the folder name when Name is empty. `n` then `Enter` is the whole common case; `N` on the home screen creates without the dialog. Tab/↓ reach the optional rows, `↓`/`Space` open the model list, and a wrong required field takes focus with the reason. To walk the form with Enter down to the **`[ Create session ]`** button instead, set `[ui].new_session_enter_advances = true`; **Ctrl+S** creates from any field in both modes.

**First run:** a fresh install opens a Welcome that shows which agents you have (Claude Code, Codex, Gemini CLI, Pi), whether Claude and Codex are signed in (from their own `claude auth status` / `codex login status`), and the install command for any that are missing. `l` runs the tool's own login (`claude auth login`, `codex login`) in a session for you; agent-deck never touches your tokens. `Enter` opens the New Session dialog with the picked agent and this folder, and `Enter` again starts it. `Esc` skips, once and for good. **`Alt+W` ("Show me around")** shows what you can do any time.

### Key Shortcuts

From the main menu, `o` and `Shift+Q` open a message composer above the footer.
Press `Enter` to submit, `Ctrl+J` for a newline, or `Esc` to cancel.
`Ctrl+X`, then `E`, opens the draft in `$VISUAL` / `$EDITOR` (fallback: `vi`);
returning from the editor restores the draft without sending it.
Queueing uses native Claude/OpenCode 2 queueing where available and waits for
the current turn to finish on other commands.
OpenCode 2 queueing uses its session API and requires a detected session ID;
unconfirmed delivery is reported without retrying or falling back to terminal keys.
The main-menu composer currently supports local sessions only, not remote-session rows.

| Key | Action |
|-----|--------|
| `Enter` | Attach to session |
| `Ctrl+Q` | Detach from session |
| `n` | New session |
| `f` / `F` | Fork (quick / dialog) |
| `A` / `Shift+U` | Archive / unarchive session |
| `^` | Show archived sessions |
| `m` | MCP Manager |
| `o` | Send a message to the selected session (a busy Claude session queues it, no interrupt) |
| `Shift+Q` | Queue a message after the current turn |
| `s` | Skills Manager |
| `$` | Cost Dashboard |
| `M` | Move session to group |
| `S` | Settings |
| `/` / `G` | Search / Global search |
| `r` / `R` | Rename / Restart session |
| `d` | Delete |
| `b` | Re-run worktree setup script |
| `E` | Container shell (sandboxed sessions) |
| `c` / `B` | Copy last AI response / session info (repo, path, branch) |
| `C` | Context inspector (what the session is sent, ranked by cost) |
| `V` / `Y` | Copy visible terminal text / a fenced code block |
| `Shift+drag` | Select text natively (`Option+drag` in iTerm2) — see below |
| `Alt+W` | Show me around: what you can do, your agents and sign-in |
| `Alt+N` | What's new: this version's highlights and your reports it fixed ("Is it fixed for you?"); opens by itself after an update |
| `?` | Full help |

> **Why can't I drag-select text?** The TUI holds the terminal in mouse
> reporting mode so clicks, wheel scrolling and the divider drag work, which
> means your terminal never sees a drag as a selection. Hold `Shift` while
> dragging (`Option` in iTerm2) to bypass it, or use the `c` / `B` / `V` / `Y`
> copy keys above. Full detail in
> [Terminal shortcuts](../../docs/terminal-shortcuts.md#text-selection-and-copying).

See [TUI Reference](../../skills/agent-deck/references/tui-reference.md) for all shortcuts and [CLI Reference](../../skills/agent-deck/references/cli-reference.md) for all commands.
