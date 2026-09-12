# Harness identity injection

Every session agent-deck launches is told, at runtime and in a form the model
can read, that it runs inside agent-deck, what its own session identity is,
and how to use the `agent-deck` CLI from inside. This works in every harness
(`claude`, `codex`, `pi`, `gemini`) and, through an environment variable, in
custom `--cmd` sessions too.

## What the session learns

A short block (well under 40 lines) is generated from the session's database
record on every start and restart:

```
# agent-deck session context
You are running inside agent-deck, a terminal session manager for AI coding agents. ...

## This session (snapshot from the agent-deck database at launch)
- session id: 8c211446-1789219548
- title: after-claude
- tool: claude
- group: after
- profile: default
- account: (none)
- parent session id: (none: this is a root session)
- project path: /path/to/project

## agent-deck CLI (flags go BEFORE positional arguments)
- `agent-deck session current --json` — this session's full, current metadata ...
- `agent-deck session send <id-or-title> "message"` — ...
- `agent-deck session output <id-or-title>` — ...
- `agent-deck launch <path> -t "Title" -c claude --message "prompt"` — ...
- `agent-deck session children --json` — ...
- `agent-deck inbox drain --json <id>` — ...
- `agent-deck list --json` — ...

## Completion sentinel
When a task you were given by a parent is fully done, end your final message with exactly one line:
===AGENTDECK_DONE=== status=<ok|fail> summary=<one line>
...
This block is at $AGENTDECK_IDENTITY_FILE and is regenerated on every start/restart.
```

The block is written to `<data-dir>/agent-deck/runtime/identity/<session-id>/identity.md`
(plus a `GEMINI.md` copy for gemini). Its path is exported to the session as
`AGENTDECK_IDENTITY_FILE`. It is a launch-time snapshot; the existing
`AGENTDECK_*` environment variables and `agent-deck session current --json`
remain the machine-readable source of truth and always reflect the database.

Nothing is ever written into the project directory: no `AGENTS.md`,
`CLAUDE.md` or `GEMINI.md` is created or edited there.

## Mechanism per harness

| Tool | How the block reaches the model | Restart / resume | Not covered |
|------|-------------------------------|------------------|-------------|
| `claude` | `--append-system-prompt-file <file>` (Claude's own prompt is kept) | yes, incl. `--resume` and forks | custom `[claude].command` wrappers, `claude <subcommand>` passthrough: env var only |
| `codex` | `-c developer_instructions="<block>"`, the block inlined as a TOML basic string (lossless for any title/path); a `developer_instructions` you configured in that launch's `CODEX_HOME/config.toml` is placed first so it is never replaced. `model_instructions_file` would replace the base prompt and is not used | yes, incl. `resume` / `fork` | custom codex commands (`-c "codex --flag"`): env var only |
| `pi` | `--append-system-prompt <file>` | yes, incl. `session fork` | |
| `gemini` | `--include-directories <dir>` with `GEMINI.md` inside, loaded as project memory next to the project's own | yes, incl. `--resume` | see folder trust below |
| custom `--cmd`, opencode, cursor, copilot, crush, hermes, deepseek | `AGENTDECK_IDENTITY_FILE` in the environment | yes | no prompt injection; the command decides whether to read the file |

SSH (`--ssh`) and Docker-sandboxed sessions are skipped entirely: the file
lives on the controller host and would not be visible where the harness runs.

A session switched to another harness (`session switch-harness`, #2237) is a
new instance in the new harness; its launch command carries the new
session's own identity block through that harness's flag, while the journaled
launch plan stays unchanged.

The block ends with a deference clause: instructions from the operator, from
project or conductor files (`CLAUDE.md`, `AGENTS.md`, `GEMINI.md`) and from
the task prompt take precedence. Nothing existing is replaced: claude gets an
appended prompt (never `--system-prompt`), codex keeps its built-in
instructions and any configured `developer_instructions`, pi appends, and
gemini loads the block next to the project's own context files. Record
fields are rendered on one line each (control characters collapsed), so the
block stays under 40 lines whatever a title contains.

### Gemini and folder trust

Gemini CLI's folder trust (on by default) asks "Do you trust the following
folders being added to this workspace?" for any `--include-directories` path
that no rule in `~/.gemini/trustedFolders.json` covers. That dialog swallows
the initial message `agent-deck launch -m` types, so agent-deck only passes
the flag when it can establish that no dialog will appear:

- folder trust is disabled (`security.folderTrust.enabled = false` in gemini's
  `settings.json`), or
- a `TRUST_FOLDER` (or covering `TRUST_PARENT`) rule exists for the identity
  root, `<data-dir>/agent-deck/runtime/identity`.

Otherwise the gemini session starts without the flag, gets only
`AGENTDECK_IDENTITY_FILE`, and the pane prints a one-line hint naming the rule
to add. To enable it once for all gemini sessions, add the identity root to
`~/.gemini/trustedFolders.json` (`GEMINI_CLI_HOME` and
`GEMINI_CLI_TRUSTED_FOLDERS_PATH` are honoured the way gemini honours them):

```json
{
  "/Users/you/.local/share/agent-deck/runtime/identity": "TRUST_FOLDER"
}
```

The directory only ever contains the identity block for that one session.

## Turning it off

```toml
# config.toml (global)
[launch]
inject_identity = false
```

```bash
# per session, persisted so restarts honour it
agent-deck add --no-identity -t "Plain" -c codex .
agent-deck launch --no-identity . -c claude -m "..."
```

The TUI New Session dialog has no row for this in this release; use the CLI
flag or the config key.

## Verifying

Inside any session: `echo $AGENTDECK_IDENTITY_FILE`, `cat` it, or run
`agent-deck session current --json`. From outside, the flag is visible on the
pane's start command (`tmux display-message -p '#{pane_start_command}'`).
