# DeepSeek Harness (`dsh`)

agent-deck's `deepseek` tool launches **DeepSeek Harness**, the coding-agent CLI
published by DeepSeek itself.

| | |
|---|---|
| Repository | <https://github.com/deepseek-ai/deepseek-harness> (MIT) |
| Package | `@deepseek-ai/dsh` — `npm install -g @deepseek-ai/dsh` |
| Binary | `dsh` (note: **not** `deepseek`) |
| Version verified | `0.1.0-rc.6`, published 2026-08-13 |
| Status upstream | developer preview — "rapid iteration and potential breaking changes" |
| Credentials | `DEEPSEEK_API_KEY`, or `$DSH_HOME/.credentials.yaml` |

Everything on this page was captured from the real binary in a sandboxed `HOME`.
Where agent-deck declines to support something, the reason is a property of
`dsh` 0.1.0-rc.6, not an omission.

## Which DeepSeek CLI this is

Several unrelated CLIs carry the DeepSeek name. agent-deck integrates the one
published by the vendor's own GitHub organisation and npm scope:

| Project | Publisher | Why not this one |
|---|---|---|
| **`@deepseek-ai/dsh`** | `deepseek-ai` org, maintainer `@deepseek.com` | **this is the one agent-deck supports** |
| `@vegamo/deepcode-cli` (Deep Code) | `lessweb`, third party | Listed as an integration in DeepSeek's API docs, but community-maintained; DeepSeek support does not troubleshoot it |
| `holasoymalva/deepseek-cli`, `PierrunoYT/deepseek-cli`, `@kavienw/deepseek-cli`, `reasonix` | individuals | Unaffiliated wrappers over the DeepSeek API |

Separately, agent-deck already ships a `codewhale` **pattern preset** for a
third-party TUI that runs DeepSeek *models*. That is a status-detection preset
for someone else's binary; this page is about the vendor's own harness.

### The collision that actually matters: `dsh`

Those are all collisions on the *package* name, which nothing executes. The one
on the **command** name is more serious, and it predates DeepSeek Harness by two
decades:

| | |
|---|---|
| Package | `dsh` — *"dancer's shell, or distributed shell"* |
| Ships in | Debian and Ubuntu (`universe/net`), e.g. `0.25.10-1.6build1` |
| Does | executes a command on a **group of machines** over remote shell |

A `dsh` on `PATH` is therefore not evidence of DeepSeek Harness. Without a check,
a host carrying that package and no harness would report DeepSeek as installed
and then run `dsh --profile web …` against a remote-execution tool.

agent-deck verifies identity before claiming DeepSeek is available: when the
configured command is the bare default `dsh`, the resolved binary must identify
itself as DeepSeek Harness (it prints *"boot a DeepSeek Harness profile"* in its
own `--help`). A command you configure explicitly is taken at its word — a
wrapper is under no obligation to reproduce upstream's help text.

`agent-deck deepseek status` names the situation directly:

```
Resolved:  /usr/bin/dsh
           ⚠  this is NOT DeepSeek Harness — it did not identify itself.
           Debian/Ubuntu ship an unrelated `dsh` ("dancer's shell", a
           distributed shell that runs commands on remote machines).
           agent-deck will not launch it.
```

If you need both, install the harness and point `[deepseek].command` at its
absolute path.

## Command grammar

```
dsh --profile <name> [--patch <path>]... [app args...]
dsh web [app args...]                       # hardcoded alias of --profile web
dsh --profile headless "<task>"             # answer once, print it, exit
dsh plugin --profile <name> <pnpm args...>  # forwards to pnpm
dsh --dump-config | --dump-default-config   # print the composed tree, boot nothing
```

The launcher owns **only** `--profile`, `--patch`, and the dumps. The first
token it does not recognise starts the booted app's own argument list. agent-deck
therefore always emits launcher flags first; a `--patch` placed after an app flag
would be handed to the app, which rejects it.

### Shipped profiles

| Profile | Arguments | Shape |
|---|---|---|
| `web` | `--host`, `--port`, repeatable `--trusted-host` | Long-lived HTTP server. Prints one line — `dsh web: http://127.0.0.1:3080` — then serves. |
| `headless` | the task text, positionally | One-shot. Prints the final assistant message and exits 0 (turn completed) or 1. |

Both auto-initialise from shipped templates on first use. Any other profile must
be created with `dsh plugin --profile <name> add <package>`, and the launcher's
own help documents that shape as `dsh --profile tui --resume <session>`.

## Config home

`$DSH_HOME` (default `~/.dsh`) is the single user-data root:

```
$DSH_HOME/profiles/<name>/{package.json,cordis.yml,cordis.patch.yml}
$DSH_HOME/cordis.patch.yml                  machine-local patch layer
$DSH_HOME/.credentials.yaml, $DSH_HOME/.env
$DSH_HOME/storages/workspace.json           workspace path -> ordered sessionIds
$DSH_HOME/sessions/<slug>/<session-id>/session.jsonl.zstd
```

Pointing `DSH_HOME` at different directories is how one machine runs several
DeepSeek accounts, exactly as `CODEX_HOME` does for Codex.

## Configuration

```toml
[deepseek]
command = "dsh"                 # or a wrapper / absolute path (group/conductor overridable)
config_dir = "~/.dsh"           # exported as DSH_HOME
profile = "web"                 # web | headless | any installed profile
env_file = "~/.config/deepseek.env"
patches = ["~/.dsh/extra.cordis.yml"]

# web profile only
host = "127.0.0.1"
port = 3080
trusted_hosts = ["deck.local:8080"]

# See "Restart and resume" — leave unset on a default install.
resume_flag = ""

extra_args = []

# One DSH_HOME per account slot.
[profiles.work.deepseek]
config_dir = "~/.dsh-work"

# Per-group and per-conductor overrides (command, env_file, config_dir, profile).
# All four are resolved conductor -> group (ancestor-walking) -> global.
[groups."clients/acme".deepseek]
profile = "headless"
command = "/opt/acme/bin/dsh-wrapper"

[conductors.boss.deepseek]
config_dir = "~/.dsh-boss"
```

`command`, `profile`, `config_dir`, and `env_file` all resolve the same way:
conductor override, then group override (walking ancestors), then the global
`[deepseek]` value.

`DSH_HOME` resolution, most specific first:

1. `[profiles.<account>.deepseek].config_dir` — the session's account slot
2. `[conductors.<name>.deepseek].config_dir`
3. `[groups."<path>".deepseek].config_dir` (walks ancestors)
4. `$DSH_HOME` from the launching environment
5. `[deepseek].config_dir`

When none is set, agent-deck exports nothing and lets `dsh` resolve `~/.dsh`
itself.

## CLI

```
agent-deck deepseek status [--json]      # binary, version, DSH_HOME, profile, resume, key
agent-deck deepseek profiles [--json]    # profiles under $DSH_HOME/profiles, with bundles
agent-deck deepseek sessions [path] [--json]   # dsh sessions recorded for a workspace
agent-deck launch -c deepseek            # launch a session
```

`deepseek status --json` is the machine-readable answer to "what would agent-deck
actually run", including `resume_supported` and `fork_supported` as explicit
booleans so an agent can tell *unsupported* from *unknown*.

## Status detection

`dsh` enables no hooks by default (the bundled `dsh-hooks-claude-code` /
`dsh-hooks-codex` bridges are plugins a user mounts in a patch layer), so status
comes from pane content plus process liveness:

* `dsh web: http://…` — served and idle, i.e. **waiting**. It is a prompt
  pattern, never a busy one: a server that is up must not spin forever.
* `ctrl+c to interrupt` / `esc to interrupt` — **busy**. Busy is checked before
  prompt, so a working turn is never masked by the ready banner.
* `dsh: MISSING_CREDENTIAL: …` or `dsh: INVALID_CREDENTIAL: …` — a **credential
  failure**, which holds the session out of automatic restart paths. Restarting
  cannot fix a key that is absent or unusable, and `dsh` exits 1 immediately, so
  an unheld session would restart-loop.

  Detection keys on the **error code**, not the message wording. `dsh` renders a
  terminal error as `dsh: <CODE>: <message>`, and the codes are declared
  constants upstream, so a reworded message cannot silently disable the hold.
  The match is anchored at line start, so a line that merely mentions a code —
  an agent discussing this failure, a conductor quoting a child's pane — does
  not qualify.

  The other declared codes (`CONTEXT_WINDOW_EXCEEDED`, `QUOTA`,
  `EMPTY_RESPONSE`) are deliberately **not** held: those are worth re-running,
  and parking them would be the same mistake as sweeping a dropped socket into
  the auth hold.

  | Situation | What `dsh` prints |
  |---|---|
  | `DEEPSEEK_API_KEY` unset | `dsh: MISSING_CREDENTIAL: llm-deepseek: no API key for provider route "deepseek-official"; …` |
  | `DEEPSEEK_API_KEY=""` | same as above |
  | key present but malformed | `dsh: INVALID_CREDENTIAL: llm-deepseek: the API key resolved from DEEPSEEK_API_KEY contains characters no HTTP header can carry; …` |

A custom profile that installs a terminal app brings its own vocabulary; extend
detection through `[tools.<name>]` `busy_patterns` / `prompt_patterns`.

Note that `Usage: dsh …` is the **help screen** (printed on `--help`, exit 0),
not a failure — a real usage error prints `error: a task is required, …` or
`error: unknown option '…'`. It is deliberately not treated as a waiting state.

## Prompt delivery — the profiles are not interchangeable

Each profile has a different answer to "how does a prompt reach this session",
and agent-deck refuses rather than guessing:

| Profile | Channel | What agent-deck does |
|---|---|---|
| `headless` | **command line** — the task *is* the invocation | Embeds the task: `dsh --profile headless "<task>"`. It is not also typed into the pane. Launching with no task is **refused**, because `dsh --profile headless` alone is a usage error. |
| installed interactive | **pane** — the app owns a terminal prompt | Waits for readiness, then types the prompt, as with every other tool. |
| `web` | **none** — it is an HTTP server | **Refused.** There is no terminal prompt; text typed into that pane goes to the server process's stdin and is gone. |

The `web` refusal is deliberate and applies to `agent-deck launch -c deepseek -m
…`, `session send`, and the TUI alike, *before* anything is spawned. Reporting
success while discarding a request is the worst failure class in this codebase;
so is reporting failure while leaving a running server behind. To ask a question,
use the `headless` profile, or open the URL the pane prints.

## One-shot handling

A `headless` session is expected to exit as soon as it has answered. agent-deck
recognises that:

* the fast-death watcher is skipped, so a completed run is not recorded as a
  spawn failure and the preview does not paint "⚠ session failed to start" over
  the answer;
* tmux `remain-on-exit` is set, so the pane — and the answer in it — survives the
  process that printed it.

`agent-deck launch -m "<task>" --no-wait` also embeds the task rather than
starting first and sending afterwards: with no task there would be no process to
send *to*. Nothing is lost by that, since the run is already under way the moment
it exists.

The task is **persisted** (in the `tool_data` extras zone), so restarting a
headless session replays the same one-shot. A session whose task is not recorded
— a row written before this field existed — reports `CanRestart() == false`
rather than promising a restart that could only land on a usage error.

One consequence worth knowing: once the run exits, tmux's `remain-on-exit`
banner (`Pane is dead (status 0, …)`) occupies the last line, and the session
list's short preview shows that rather than the answer. The answer is not lost —
it is in the pane buffer; attach to the session, or use `agent-deck session
output`, to read it. `status 0` there means the task completed.

## Restart and resume

Restart re-boots the same profile, in the same workspace, against the same
`DSH_HOME`. Because `dsh` persists its own sessions there, no conversation is
lost by a restart even though the process is new. For the `headless` profile a
restart replays the recorded task (see above).

Session discovery skips sessions listed in `global.archivedSessionIds`:
archiving leaves the id in its workspace's `sessionIds`, so "newest entry wins"
would otherwise reopen a conversation you deliberately put away.

Reopening a *specific* conversation is a separate matter. **Neither shipped
profile accepts a resume flag in 0.1.0-rc.6** — `dsh --profile headless --help`
lists only `-h`, and the web app only `--host`/`--port`/`--trusted-host`.
Emitting `--resume` at one of them would make `dsh` exit with a usage error
instead of booting, so agent-deck emits nothing by default.

If you install a profile whose app documents a resume flag — the shape the
launcher's own help advertises — turn it on:

```toml
[deepseek]
profile = "tui"
resume_flag = "--resume"
```

agent-deck then discovers the workspace's newest session from
`$DSH_HOME/storages/workspace.json`, verifies its body still exists under
`$DSH_HOME/sessions/`, and appends `--resume <session-id>` as an app argument.
Discovery re-runs on every restart rather than caching: a pruned session yields
the current newest, or nothing — in which case the launch starts fresh instead of
resuming a dead ID forever.

## Not supported (and why)

| Capability | Status |
|---|---|
| Fork | `dsh` 0.1.0-rc.6 has no fork or branch command. `CanFork` is explicitly false. |
| MCP management | `dsh` ships `@deepseek-ai/dsh-mcp-client` but enables no server by default, because each server command is trusted code outside the agent sandbox. agent-deck does not write its config. |
| Hook-based status | The Claude Code / Codex hook bridges exist upstream but are opt-in plugins. agent-deck does not auto-install them; it uses pane detection. |
| Cost tracking | No token accounting is exported by the CLI. |

## Process contract

`SIGTERM` starts a graceful drain and exits 0 (a supervisor's ordinary stop);
`SIGINT` reports 130; a second signal forces immediate exit. The plugin tree gets
up to five seconds to dispose.

## Testing

* `internal/session/deepseek_test.go` — command building, the `DSH_HOME`
  priority chain, on-disk session discovery, capability gates.
* `internal/tmux/deepseek_test.go` — tool detection and status patterns, all
  asserted against pane text captured verbatim from the real binary.
* `internal/session/deepseek_lifecycle_test.go` — a real tmux pane through
  launch → send delivery → prompt round-trip → status transitions →
  restart-with-context, for all three profile shapes.

The lifecycle tests run against `internal/session/testdata/fake-dsh`, an
emulator of the CLI contract, because the real launcher answers nothing without
a `DEEPSEEK_API_KEY` that CI does not have. That file's header records exactly
which behaviours it reproduces and where each was captured from.
