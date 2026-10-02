# Session Workflows

Context inspection, worktrees, scratch sessions, runtime health, recall, session sharing, and account switching.

## Context Inspection (what is in a session's context window)

**Use when:** anyone asks "what is in my/this session's context", "why is context so full",
"what can I clean up", or you want to audit a child session's overhead before dispatching
heavy work. Same data as the TUI `C` overlay — full CLI parity by design, so agents can
use every feature themselves.

```bash
agent-deck -p <profile> session context <name>                        # overview: gauge + categories
agent-deck -p <profile> session context <name> --tab breakdown --all  # every item, ranked, with ids + levers
agent-deck -p <profile> session context <name> --item <id>            # ONE item: provenance, lever, verbatim text
agent-deck -p <profile> session context <name> --tab verify           # the arithmetic, measured anchor to the digit
agent-deck -p <profile> session context <name> --json                 # machine-readable; provenance on every figure
agent-deck -p <profile> session context <name> --strict               # exit 3 if the report breaks its own invariants
agent-deck -p <profile> session context <name> --capabilities         # what this harness can report at all
```

Reading the output:
- Every figure carries provenance: **measured** (harness-reported), **~est** (estimated,
  with an error band), or **—/ABSENT** (unknown). An unknown is never printed as zero.
- **POTENTIAL** column = what a skill would cost if invoked; skills cost only their
  name+description until then, so deleting skills saves almost nothing.
- Figures are as-of-now-on-disk: a running session keeps its boot-time copy until restart.
- `--verify` types /context into the LIVE session to compare against the harness's own
  accounting. It mutates the session: confirmation required, use sparingly, never on a busy session.
- Exit codes: 0 ok · 1 could not run (bad args, no pane, unreadable panel) · 2 not found · 3 invariant/reconciliation failure · 4 verify drift · 5 verify indeterminate. `--verify` always asks first; `--yes` skips the prompt for CI.

Agents may run read-only sweeps freely (`list --json` → context per session) to find
bloated sessions; report findings, never edit another session's files without its owner.

## Worktree Workflows

### Create Session in Git Worktree

When working on a feature that needs isolation from main branch:

```bash
# Create session with new worktree and branch
agent-deck add /path/to/repo -t "Feature Work" -c claude --worktree feature/my-feature --new-branch

# Create session in existing branch's worktree
agent-deck add . --worktree develop -c claude
```

### List and Manage Worktrees

```bash
# List all worktrees and their associated sessions
agent-deck worktree list

# Show detailed info for a session's worktree
agent-deck worktree info "My Session"

# Find orphaned worktrees/sessions (dry-run)
agent-deck worktree cleanup

# Actually clean up orphans
agent-deck worktree cleanup --force
```

### When to Use Worktrees

| Use Case | Benefit |
|----------|---------|
| **Parallel agent work** | Multiple agents on same repo, different branches |
| **Feature isolation** | Keep main branch clean while agent experiments |
| **Code review** | Agent reviews PR in worktree while main work continues |
| **Hotfix work** | Quick branch off main without disrupting feature work |

## Scratch Sessions (`agent-deck try`)

**Use when:** the user wants a throwaway playground, a quick experiment, or a scratch repo to dry-run something — "spin up a scratch session", "try this out somewhere disposable", "make a playground".

```bash
# Find-or-create a dated experiment folder and start a session in it
agent-deck try redis-cache            # → <experiments-dir>/2026-07-29-redis-cache/
agent-deck try rds                    # Fuzzy-matches an existing experiment (e.g. redis-cache)
agent-deck try myproject -c gemini    # Non-default tool
agent-deck try myproject --no-session # Create/find the folder only
agent-deck try scratch --sandbox      # Run the session in a Docker sandbox
agent-deck try --list [query]         # List (or fuzzy-search) existing experiments
```

The argument is an **experiment name**, not a prompt. `try` finds or creates `<experiments-dir>/<YYYY-MM-DD>-<name>/`, reuses an existing session for that path if one exists, and otherwise creates one in the `experiments` group and starts it.

**The base directory is configurable** — important when your machine only trusts certain roots for agent workspaces:

```toml
[experiments]
directory = "~/code/tries"    # Default: ~/src/tries
date_prefix = true            # YYYY-MM-DD- prefix on folder names
default_tool = "claude"       # Tool when -c is omitted
```

Note: `try` creates a plain folder, not a git repo — run `git init` in it first if the experiment needs one.

## Runtime Health & Fleet Maintenance (v1.16.11+)

**Use when:** anyone asks "is agent-deck healthy", "why is the deck slow", "did that completion event get lost", or you're deploying a local build to a remote ahead of a release.

```bash
agent-deck health --json --since 1h          # per-process CPU/RSS/FDs/goroutines vs performance budgets, no data leaves the host
agent-deck inbox dead-letter list --json      # inspect records that failed to route (list/show only — no retry/purge)
agent-deck remote update dev --from-build /path/to/local/dist   # push a verified local build to a remote, no release needed
```

- `health` reports against fixed budgets (`status_pass_ms_exclusive`, `open_fds_exclusive`, `tmux_calls_per_session`, `remote_poll_ms_exclusive`); use `--since` to widen the history window when a regression is intermittent.
- `inbox dead-letter list|show` is diagnostic-only in this release — there is no `retry` or `purge` subcommand (both are explicitly rejected). Recovering a dead-lettered record means fixing the underlying routing issue and re-draining, not resubmitting the record itself.
- `remote update --from-build <dir>` is for shipping a verified local three-platform build (darwin/arm64, linux/amd64, linux/arm64) to a remote before it's published as a release — same checksum/version verification and downgrade guard as a normal `remote update`.
- `[ui.remote_preview]`/`[ui.header]` share one field vocabulary: `version`, `sessions_by_status`, `harnesses`, `load`, `memory`, `disk`, `last_poll`, and the opt-in `accounts` (named Claude account slots with live 5h/7d usage, one aligned row per slot in the preview, read from each slot's local quota cache — `agent-deck hooks install` wires the feed) and `ssh` (who is connected to the host over SSH right now, per user) — see [config-reference.md](config-reference.md).

## Recall (hints, and the cross-harness transcript index)

**Use when:** you want a session to remember what it was for, you are finishing a task and want the outcome findable later, or you need to find what an earlier conversation (Claude, Codex, pi, Gemini, OpenCode or Hermes) did. Details: [recall skill](../recall/SKILL.md), `docs/recall.md`.

```bash
agent-deck add . -c claude --hint purpose="fix flaky auth test" --ticket SB-412 --tag auth   # also on launch
agent-deck session annotate <id> --decision "clock skew" --outcome worked --tag clock-skew
agent-deck session annotate --self --note-stdin < summary.md    # an agent, on its own session
agent-deck recall search "clock skew" --since 30d --json        # needs [recall] enabled = true
agent-deck recall search "retry budget" --harness codex --json  # one harness; --profile work narrows Claude
agent-deck recall show <session> --turns 20 && agent-deck recall open <session>
agent-deck recall context <session> --tier brief --into current # hand a past conversation to THIS session (any harness)
agent-deck recall search "retry budget" --all-remotes --json     # federated: every remote searches its own index
agent-deck remote <host> session annotate <id> --outcome worked  # hints on a remote session
agent-deck mcp attach <session> recall                           # the built-in MCP server (recall mcp)
```

Hints are single-valued per key (setting again replaces), tags are a set; all of it lives in the profile's state.db and survives any index rebuild. The index (`recall backfill|sweep|status|sessions|search|show|open|gc|rebuild`) is off by default, covers Claude transcripts of every profile plus Codex, pi, Gemini, OpenCode and Hermes, never runs a daemon or watcher, refuses batch work while a session is busy, and stays fresh through the Claude Stop hook, `session stop`, `worker_done` and the daemon's turn-end edge (each appends one line to `recall/queue.jsonl`; the Stop hook also indexes its own file within 150 ms). The TUI `G` key is the same search. `recall open` resumes Claude conversations and starts any bound session; other harnesses are searchable, not resumable. Phase 4: `recall context <session> --into current` delivers a past conversation (card, brief or excerpt under a token budget) to the calling session through `session send`; every sweep derives `lost_time`, `session_kind` and `outcome` lines (`recall enrich` drains the rest; stale ones are marked); `recall search --remote <host>`/`--all-remotes` federates over SSH and stores nothing (an older remote is one line + exit 1); card sync (`export`/`pull`/`import`) is off unless `[recall] remote_cards = true`; `recall mcp` is an MCP server `mcp list` offers as `recall`. `session search` is unchanged.

## Session Sharing

Share Claude sessions between developers for collaboration or handoff.

**Use when:** User says "share session", "export session", "send to colleague", "import session"

```bash
# Export current session to file (session-share is a sibling skill)
$SKILL_DIR/../session-share/scripts/export.sh
# Output: ~/session-shares/session-<date>-<title>.json

# Import received session
$SKILL_DIR/../session-share/scripts/import.sh ~/Downloads/session-file.json
```

**See:** [session-share skill](../../session-share/SKILL.md) for full documentation.

## Switch a Session to Another Claude Account

Move a session — conversation included — to a different Claude account (work/personal/client) and continue exactly where it left off.

**Use when:** User says "switch account", "move this conversation to my other account", "continue this session on account X", "this session should use the <name> account".

**One-time setup** — name each account in `$XDG_CONFIG_HOME/agent-deck/config.toml` (default `~/.config/agent-deck/config.toml`; the target profile must already be logged in: `CLAUDE_CONFIG_DIR=<dir> claude` → `/login`):

```toml
[profiles.personal.claude]
  config_dir = "~/.claude"
[profiles.work.claude]
  config_dir = "~/.claude-team"
```

**In the TUI:** the New Session dialog's Claude options carry an `Account` row
(`←`/`→` or `Space` to cycle; `inherit` keeps the conductor/group/env chain), and
the Edit Session dialog (`Shift+P`) carries an account row that runs the full
switch — conversation migration and `--resume` restart included — on save, after a
"Switch Account?" confirmation. Both rows are hidden when no accounts are configured.

**Commands:**

```bash
# Inspect the account names available to add/launch/switch-account
agent-deck accounts

# Create and start a new session directly under one named account
agent-deck launch . -c claude --account <account>

# Full flow: stop → copy conversation into the target account → set account → restart with --resume
agent-deck session switch-account <session> <account>

# Skip the restart (e.g. switch several sessions, restart later)
agent-deck session switch-account <session> <account> --no-restart

# Equivalent low-level form — also migrates the conversation; restart required
agent-deck session set <session> account <account>
```

**How it works / guarantees:**

- The conversation `.jsonl` is **copied** into `<target-config-dir>/projects/<encoded-path>/` for every project key Claude Code may use for the working directory (typed path, macOS `/private` form, realpath) — the old account keeps its copy. Every existing copy in both accounts is compared; the newest by last event wins (tie: longest), a newer target copy is kept rather than overwritten, and each replaced copy is backed up next to it (`.bak-<timestamp>` or `.pre-switch-<timestamp>`). The receipt says which copy was chosen (`--json`: `transcript`). The session id does not change.
- `claude --resume` is a pure file lookup, so the restarted session continues with full history under the new account's auth.
- Tools/MCPs/plugins and usage limits follow the **new** account; enable any needed plugins in the target profile (e.g. `CLAUDE_CONFIG_DIR=<dir> claude plugin enable telegram@claude-plugins-official` for channel owners).
- A fresh session with no conversation yet switches cleanly (nothing to migrate).
- Unknown account names error and list the configured ones.

**Make a whole group/conductor use the account going forward:** set `[groups.<name>.claude].config_dir` / `[conductors.<name>.claude].config_dir` to the same dir in config.toml — new sessions there spawn on that account; `switch-account` is what carries existing conversations over.
