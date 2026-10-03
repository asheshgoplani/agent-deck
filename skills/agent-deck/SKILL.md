---
name: agent-deck
description: agent-deck, the terminal session manager for AI coding agents. Use when the user mentions "agent-deck", "session", "sub-agent", "MCP attach", or "git worktree", or needs to create, start, message, read, fork, restart, or supervise agent-deck sessions; launch child sessions or consult another CLI through agent-deck; attach MCPs to a session; manage groups, profiles, accounts, or worktree sessions; set up conductors or watchers; configure config.toml; or troubleshoot agent-deck. Not for in-conversation subagents, plain git worktrees, or plain tmux.
metadata:
  compatibility: "claude, codex, opencode"
---

# Agent Deck

> Run `agent-deck --version` for your installed version. This skill targets v1.16.11+; most patterns work back to v1.7. See [Backward Compatibility](references/gotchas.md#backward-compatibility) for what needs ≥1.16.11.

## Scripts

Helper scripts live in this skill's `scripts/` folder. Resolve them from the base directory shown when the skill loaded, not from the user's project:

```bash
SKILL_DIR="/path/shown/in/base-directory-line"
$SKILL_DIR/scripts/launch-subagent.sh "Title" "Prompt" --wait
```

Plugin installs resolve to `~/.claude/plugins/cache/agent-deck/agent-deck/<hash>/skills/agent-deck/`; a repo checkout resolves to `<repo>/skills/agent-deck/`.

## Essential Commands

| Command | Purpose |
|---------|---------|
| `agent-deck` | Launch interactive TUI |
| `agent-deck add -t "Name" -c claude /path` | Create session |
| `agent-deck launch . -c claude --account <name>` | Create and start a session under a named account slot |
| `agent-deck session start/stop/restart <name>` | Control session |
| `agent-deck session send <name> "message"` | Send message |
| `agent-deck session send <name> --message-file <file>` | Send message from file (`-` = stdin); no shell quoting. Also on `launch`/`session start` |
| `agent-deck session output <name>` | Get bounded, ANSI-clean last response (JSON/quiet/copy preserve full source) |
| `agent-deck session children --json` | Child sessions' live status + asserted completions (non-blocking, read-only) |
| `agent-deck session current [-q\|--json]` | Auto-detect current session |
| `agent-deck session fork <name>` | Fork Claude/OpenCode/Pi/Codex/Oh My Pi conversation |
| `agent-deck mcp list` | List available MCPs |
| `agent-deck mcp attach <name> <mcp>` | Attach MCP (then restart) |
| `agent-deck status` | Quick status summary |
| `agent-deck add --worktree <branch>` | Create session in git worktree |

**Status:** `●` running | `◐` waiting | `○` idle | `✕` error

Every other command and flag is in [cli-reference.md](references/cli-reference.md).

## Rules

1. **Flags before arguments:** `session start -m "Hello" name`, not `name -m "Hello"`. A flag that seems ignored is almost always in the wrong position.
2. **MCPs on request only:** attach one when the user asks, then `session restart` it; attaching alone does not load it. `--global` writes Claude's config for every project; the default writes the session's `.mcp.json`.
3. **Leave other sessions to work:** read another session with a single `session output` or `session children` call rather than a polling loop, which can interfere with the target.
4. **State only the task in a child's prompt.** Every session agent-deck launches already carries its identity (session id, title, parent, core commands, completion sentinel; `$AGENTDECK_IDENTITY_FILE` for custom `--cmd`), so a prompt need not explain who the child is or how to reach its parent. Opt out with `--no-identity` or `[launch] inject_identity = false`; gemini also needs its identity folder trusted (`documentation/HARNESS_IDENTITY.md`).

## Configuration

`$XDG_CONFIG_HOME/agent-deck/config.toml` (default `~/.config/agent-deck/config.toml`; legacy `~/.agent-deck/config.toml` still honored). Every option is in [config-reference.md](references/config-reference.md).

## Where to Look Next

Read the matching reference before acting on anything beyond the commands above.

| When the task involves | Read |
|---|---|
| What agent-deck and each session's CLI (claude, codex, gemini) can do; choosing the `-c` tool for a child | [capabilities.md](references/capabilities.md) |
| Sessions messaging each other, `send` delivery guarantees, `output`, `children`, `inbox drain`, `handoff`, send pitfalls | [session-communication.md](references/session-communication.md) |
| Launching a sub-agent (`launch-subagent.sh`), retrieval modes, worker prompt conventions, the `===AGENTDECK_DONE===` completion sentinel, consulting Codex or Gemini, root-level peers (`-no-parent`) | [sub-agents.md](references/sub-agents.md) |
| Fanning out several children and supervising them non-blockingly | [fleet skill](../fleet/SKILL.md) |
| Conductors (`conductor setup`), Telegram/Slack channels, watchers (webhook, GitHub, ntfy, Slack) | [conductors.md](references/conductors.md), then [documentation/WATCHERS.md](https://github.com/asheshgoplani/agent-deck/blob/main/documentation/WATCHERS.md) for custom watchers |
| Context inspection (`session context`), worktrees, scratch sessions (`try`), runtime health (`health`, dead letters, `remote update --from-build`), recall hints and search, session sharing, switching a session to another Claude account | [session-workflows.md](references/session-workflows.md) |
| Self-improvement (transcript mining), goals (goal-driven worker autonomy), trust-but-verify for completion claims | [autonomy.md](references/autonomy.md), then [self-improvement.md](references/self-improvement.md) or [goal.md](references/goal.md) for the deep dive |
| Using agent-deck as a daemon supervisor (it is not one), known gotchas and workarounds (`--no-wait` Enter fallback, `text file busy`, `-c "claude <subcommand>"`, config drift, channel subscription, Telegram conductor topology, v1.9.x findings), and what needs a deck newer than 1.16.11 | [gotchas.md](references/gotchas.md) |
| TUI keys, dialogs, search, and layout | [tui-reference.md](references/tui-reference.md) |
| A session in error, MCPs not loading, diagnostics, getting help, or filing a bug | [troubleshooting.md](references/troubleshooting.md) |
| Docker sandboxed sessions | [sandbox.md](references/sandbox.md) |
| Durable session hints and tags, the cross-harness transcript index, `recall context --into current`, federated search, the recall MCP server | [recall skill](recall/SKILL.md) |
| Exporting or importing a session for another developer | [session-share skill](../session-share/SKILL.md) |
| User-level vs pool skills, attaching and detaching skills | [documentation/SKILLS.md](https://github.com/asheshgoplani/agent-deck/blob/main/documentation/SKILLS.md) |
| Fixing agent-deck itself and opening a PR | `.github/skills/agent-deck-contributor/SKILL.md` in an agent-deck checkout; it mirrors the PR intake gate and runs tests sandboxed |
