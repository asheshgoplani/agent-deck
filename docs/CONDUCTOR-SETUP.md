# Conductor setup — get a fleet running in five minutes

> **TL;DR** — A *conductor* is one persistent Claude/Codex/Gemini session that
> watches all your other agent-deck sessions and talks back to you on
> Telegram (or Slack, or Discord). One CLI command sets it up end-to-end.

This is the **onboarding** guide. Once you have a conductor running and
talking to you, the deeper reference lives in
[`documentation/CONDUCTOR.md`](../documentation/CONDUCTOR.md).

![Fleet topology: user phone → conductor → child sessions, with watchers ringing the conductor from the side](images/fleet-topology.png)

---

## What is a conductor?

Think of agent-deck as a **fleet of AI agents** — each project, each task, each
fix on its own session. When the fleet is small, eyeballing the TUI is enough.
When it grows past three or four active sessions, you stop being able to keep
track. Things stall in `waiting`, errors pile up unnoticed, you forget which
agent is mid-experiment.

A **conductor** is the one session in your fleet whose job is *not* to write
code. Instead it:

- Watches the status of every other session (running / waiting / idle / error)
- Auto-answers routine questions ("yes use the existing component", "yes run
  the tests", "no don't merge yet")
- **Escalates the interesting decisions to your phone via Telegram** (or Slack,
  or Discord)
- Spawns new child sessions when you ask it to ("kick off a fix for issue
  #742", "review the diff on branch X")

The conductor is the **brain** of the fleet. The other sessions are the
**hands**. [Watchers](WATCHER-SETUP.md) are the **senses** — they ring the
doorbell when something in the outside world deserves attention.

You stay in the loop without staring at a terminal.

---

## Before you start (2 min)

1. **agent-deck installed** — `agent-deck --version` should print `v1.9.x`
   or newer. If not, see [Installation](../README.md#installation).
2. **A Claude / Codex / Gemini profile already authed** — `claude` should be
   able to start a session without an auth prompt. The conductor runs as a
   normal session of whichever agent you pick.
3. **Decide a name.** It's a short slug — `work`, `personal`, `ryan`,
   `infra`. You can run as many conductors as you want; one name per scope of
   work. Multiple conductors do not share state.
4. **(Optional) Decide a profile.** If you keep work + personal Claude logins
   separate (e.g. `~/.claude` vs `~/.claude-work`), pass `-p <profile>` to
   every command. Otherwise omit it.

---

## The five-minute path

### Step 1 — Create the Telegram bot

Telegram is the easiest channel — no app to install, no workspace permissions
to negotiate. You can skip this and pair Slack or Discord later instead, but
most users start here.

On Telegram:

1. Message **@BotFather** → `/newbot` → answer the prompts.
2. Copy the **HTTP API token** it gives you (looks like
   `123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11`).
3. Message **@userinfobot** → it replies with your numeric Telegram user ID.
   Copy that too — the bot will only respond to messages from this ID.

That's the entire Telegram side. The bot has no commands yet; the conductor
will register its own.

> **Why your user ID matters.** Anyone who finds your bot's username can
> message it. The conductor refuses to act on messages from any user ID
> except yours. Treat the token as a secret anyway — anyone with it can
> *impersonate* the bot.

### Step 2 — Run conductor setup

```bash
# Personal profile (default)
agent-deck conductor setup <name> --description "<short description>"

# Or in a different profile
agent-deck -p work conductor setup <name> --description "Work fleet"
```

The first time you run this, `setup` walks you through a one-time wizard:

```
Conductor Setup
===============

The conductor system lets you create named persistent claude conductor sessions that
monitor and orchestrate all your agent-deck sessions.

Connect Telegram bot for mobile control? (y/N): y

  1. Message @BotFather on Telegram -> /newbot -> copy the token
  2. Message @userinfobot on Telegram -> copy your user ID

Telegram bot token: 123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11
Your Telegram user ID: 123456789

Connect Slack bot for channel-based control? (y/N): N
Connect Discord bot for channel-based control? (y/N): N

[ok] Conductor config saved to config.toml
[ok] Shared CLAUDE.md installed/updated
[ok] Shared POLICY.md installed/updated
[ok] Shared LEARNINGS.md installed

Setting up conductor: work (profile: personal)
  [ok] Directory, CLAUDE.md, and meta.json created
  [ok] Session 'conductor-work' registered as claude (ID: 4a5bb1e0)
  [ok] Heartbeat timer installed (every 15 min)

Installing bridge...
[ok] bridge.py installed
[ok] Bridge daemon loaded
[ok] Transition notifier daemon installed

Conductor setup complete!

  Name:      work
  Agent:     claude
  Profile:   personal
  Heartbeat: true

Next steps:
  agent-deck -p personal session start conductor-work
  Test from Telegram: send /status to your bot
  View bridge logs:   tail -f ~/.agent-deck/conductor/bridge.log
```

That single command:

- Created `~/.agent-deck/conductor/<name>/` with `CLAUDE.md`, `POLICY.md`,
  `LEARNINGS.md`, `meta.json`, `state.json`, `task-log.md`.
- Stored the Telegram token in `~/.agent-deck/conductor/<name>/.env`
  (chmod 600). **Never commit this file.**
- Registered a session called `conductor-<name>` in the profile's
  agent-deck database.
- Installed a heartbeat daemon (launchd on macOS, systemd on Linux) that
  pings the conductor every 15 min with a status summary.
- Installed `bridge.py` — the Python daemon that proxies messages from
  Telegram into the conductor's tmux pane and routes replies back.
- **Auto-disabled `enabledPlugins."telegram@claude-plugins-official"`** in
  `~/.claude/settings.json`. This is intentional. See the gotcha below.

### Step 3 — Start the conductor

```bash
agent-deck -p personal session start conductor-work
```

This launches `claude` in a tmux pane, inside the conductor directory, with
the Telegram plugin loaded *only for this session* via `--channels
plugin:telegram@claude-plugins-official`. The conductor reads its
`CLAUDE.md` on first turn and learns its job.

### Step 4 — Verify

From your phone, message your Telegram bot:

```
/status
```

Within a few seconds the conductor should reply with the current fleet
state — something like:

```
[STATUS] Fleet summary

Running: 2 (frontend-app, api-fix)
Waiting: 1 (docs-pr — needs your call on the API rename)
Idle:    3
Error:   0
```

You're now talking to your fleet from your phone.

### Step 5 — Try a real round trip

In any agent-deck session, intentionally get stuck — e.g. ask Claude
something ambiguous and let it sit in `waiting`. Within 15 minutes the
heartbeat fires, the conductor notices the new `waiting` session, and either
auto-responds (if `POLICY.md` matches) or pings your Telegram with the
question.

Reply on Telegram. Conductor unblocks the worker. You never opened the TUI.

---

## How channels actually work (one thing to internalise)

```
                            ┌──────────────────────────────┐
   Telegram bot   ──────►   │ bridge.py daemon             │
   (1 bot = 1 conductor)    │  - polls Telegram getUpdates │
                            │  - matches sender to user ID │
                            │  - writes to conductor pane  │
                            └────────┬─────────────────────┘
                                     ▼
                            ┌──────────────────────────────┐
                            │ conductor-<name> tmux pane   │
                            │   running `claude` with      │
                            │   --channels plugin:telegram │
                            │   loaded for THIS session    │
                            │   only                       │
                            └──────────────────────────────┘
```

Two pieces are doing the work:

1. **`bridge.py`** — a small Python daemon installed by `setup`. It runs as
   a launchd / systemd service and is responsible for polling Telegram and
   feeding messages into the right tmux pane.
2. **The `telegram@claude-plugins-official` plugin**, loaded *per-session*
   via the session's `channels = [...]` field — visible in
   `agent-deck list --json`. This is what lets Claude *send* messages back.

Each conductor must have its own dedicated bot. Bots cannot be shared
between conductors — Telegram's `getUpdates` is single-consumer. Running two
conductors against the same token produces 409 Conflict errors on every
poll.

---

## Common gotchas (read these once)

### 1. "Why is the Telegram plugin disabled globally?"

`setup` runs this line:

```text
✓ Auto-disabled enabledPlugins."telegram@claude-plugins-official"
  in ~/.claude/settings.json (issue #666 remediation)
```

That is intentional. The plugin must only load in the **specific
conductor session**, not in every Claude Code subprocess globally. If it
loads globally, every child session that `claude` spawns also tries to
poll Telegram → N pollers fighting for one token → 409 errors → the
conductor stops receiving messages.

The conductor session loads the plugin from its `channels` array, not from
global `enabledPlugins`. Leave the global setting `false`.

Verify exactly one poller is running:

```bash
pgrep -af "bun.*telegram" | wc -l
# Expected: 1 per active conductor
```

### 2. "My token disappears across session restarts"

The token is in `<conductor-dir>/.env`. agent-deck loads it via `env_file`
in `~/.agent-deck/config.toml`:

```toml
[conductors.work.claude]
config_dir = "~/.claude"
env_file = "~/.agent-deck/conductor/work/.env"
```

If you renamed or moved the conductor dir, this block points at the wrong
path and the bot silently never gets its token. Check the block matches.

Do **not** try to wire the token via `session set wrapper "env FOO=bar
{command}"` — it looks like it works but doesn't survive `claude --resume`.
Always use `env_file`.

### 3. "Channels field is empty in `agent-deck list --json`"

Some upgrade paths drop the `channels` field on the conductor session. Fix:

```bash
agent-deck -p personal session set conductor-work \
  channels plugin:telegram@claude-plugins-official
agent-deck -p personal session restart conductor-work
```

### 4. "Conductor profile and Claude profile don't match"

If you ran `agent-deck -p work conductor setup ...` but `claude` itself
authenticates against `~/.claude` (your personal account), the conductor
will start under the wrong identity. Pin the profile explicitly:

```toml
[conductors.work.claude]
config_dir = "~/.claude-work"
```

Lookup precedence (highest wins):
`CLAUDE_CONFIG_DIR` env var → `[conductors.<name>.claude]` →
`[groups."conductor".claude]` → `[profiles.<profile>.claude]` →
`[claude]` global → `~/.claude` default.

### 5. "I want a Slack or Discord conductor instead"

Re-run `agent-deck conductor setup <name>` and answer **y** at the Slack
or Discord prompt. The wizard walks you through bot scopes and tokens. The
same bridge daemon handles all three.

### 6. "How do I tear it down?"

```bash
agent-deck conductor teardown <name>            # stop, keep state
agent-deck conductor teardown <name> --remove   # stop and delete dir + session
agent-deck conductor teardown --all --remove    # nuke every conductor in this profile
```

---

## Running several conductors

You will almost certainly want more than one — typical pairing:

| Conductor | Profile | Channel |
|-----------|--------------|------------------------|
| `personal` | `~/.claude` | `@my_personal_bot` |
| `work` | `~/.claude-work` | `@my_work_bot` |
| `oncall` | `~/.claude-work` | `@my_oncall_bot` |

Each gets its **own** Telegram bot (one bot = one conductor — see gotcha
#1). They run side by side, never share state, and each escalates to a
different chat on your phone so you can mentally compartmentalise.

The bridge daemon handles all of them — one bridge process per machine
multiplexes across N bots.

---

## After setup: making the conductor smarter

Two files in `~/.agent-deck/conductor/<name>/` are worth knowing:

- **`POLICY.md`** — rules for what the conductor should auto-answer vs
  escalate. The starting template includes "if a worker asks whether to
  run tests, say yes". Edit freely; the conductor reads it every turn.
- **`LEARNINGS.md`** — append-only patterns the conductor accumulates over
  time. After every correction you make on Telegram ("no, don't do X
  again"), the conductor should add a learning. You can also hand-edit.

For the deeper reference (heartbeat tuning, multi-conductor topologies,
state machine details), see
[`documentation/CONDUCTOR.md`](../documentation/CONDUCTOR.md).

To add **watchers** that ring the conductor when something happens in the
outside world (new Gmail, GitHub event, calendar reminder, ntfy push), see
[`WATCHER-SETUP.md`](WATCHER-SETUP.md).

---

## Cheatsheet

```bash
# Setup
agent-deck conductor setup <name> --description "..."
agent-deck -p work conductor setup <name> --agent codex     # use Codex instead

# Lifecycle
agent-deck conductor list                   # all conductors
agent-deck conductor status                 # health of all
agent-deck conductor status <name>          # health of one
agent-deck conductor teardown <name>        # stop (keep state)
agent-deck conductor teardown <name> --remove  # stop and delete

# Move between profiles
agent-deck conductor move <name> --to-profile <other>

# Daily ops
agent-deck session start conductor-<name>
agent-deck session restart conductor-<name>
agent-deck session output conductor-<name> -q
tail -f ~/.agent-deck/conductor/bridge.log
```

If anything in this guide didn't work for you, please open an issue — the
docs themselves are part of the fleet.
