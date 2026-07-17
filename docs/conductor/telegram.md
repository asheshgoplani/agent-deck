# Telegram channel setup

Connect a Telegram bot to your conductor so you can talk to it from your phone.
Each conductor pairs one-to-one with its own dedicated bot.

## What you need

- A Telegram account
- A conductor already created (`agent-deck conductor setup <name>`)

## Step-by-step setup

### 1. Create the bot

On Telegram:

1. Message **@BotFather** -> `/newbot` -> answer the prompts.
2. Copy the **HTTP API token** it gives you (looks like `123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11`).
3. Message **@userinfobot** -> it replies with your numeric Telegram user ID. Copy that too.

The bot has no commands yet; the conductor will register its own.

### 2. Run conductor setup (or re-run it)

If you already have a conductor, re-run setup to add Telegram:

```bash
agent-deck conductor setup <name>
```

Answer **y** at the Telegram prompt, paste the bot token and your user ID.

If creating a new conductor, the same wizard asks during initial setup.

### 3. Start the conductor

```bash
agent-deck session start conductor-<name>
```

This launches `claude` in a tmux pane with the Telegram plugin loaded per-session via `--channels plugin:telegram@claude-plugins-official`.

### 4. Verify

From your phone, message the bot:

```
/status
```

Within a few seconds the conductor should reply with the current fleet state:

```
[STATUS] Fleet summary

Running: 2 (frontend-app, api-fix)
Waiting: 1 (docs-pr — needs your call on the API rename)
Idle:    3
Error:   0
```

Verify exactly one poller is running:

```bash
pgrep -af "bun.*telegram" | wc -l
# Expected: 1 per active conductor with Telegram
```

## How it works

```
                            +------------------------------+
   Telegram bot   ------>   | bridge.py daemon             |
   (1 bot = 1 conductor)   |  - polls Telegram getUpdates |
                            |  - matches sender to user ID |
                            |  - writes to conductor pane  |
                            +--------+---------------------+
                                     |
                                     v
                            +------------------------------+
                            | conductor-<name> tmux pane   |
                            |   running `claude` with      |
                            |   --channels plugin:telegram |
                            |   loaded for THIS session    |
                            |   only                       |
                            +------------------------------+
```

Two pieces do the work:

1. **`bridge.py`** — a Python daemon installed by `setup`. It runs as a systemd/launchd service, polls Telegram, and feeds messages into the right tmux pane.
2. **The `telegram@claude-plugins-official` plugin**, loaded per-session via the session's `channels` field. This is what lets Claude send messages back.

## Configuration

The token is stored in `<conductor-dir>/.env` (chmod 600).
agent-deck loads it via `env_file` in `~/.config/agent-deck/config.toml`:

```toml
[conductors.work.claude]
config_dir = "~/.claude"
env_file = "~/.local/share/agent-deck/conductor/work/.env"
```

## Why your user ID matters

Anyone who finds your bot's username can message it.
The conductor refuses to act on messages from any user ID except yours.
Treat the token as a secret anyway — anyone with it can impersonate the bot.

## One bot per conductor

Bots cannot be shared between conductors.
Telegram's `getUpdates` is single-consumer.
Running two conductors against the same token produces 409 Conflict errors on every poll.

## Debugging tips

### Plugin is globally enabled

`setup` auto-disables `enabledPlugins."telegram@claude-plugins-official"` in `~/.claude/settings.json`.
If it loads globally, every child session spawns a poller -> N pollers fighting for one token -> 409 errors.
The conductor session loads the plugin from its `channels` array, not from global `enabledPlugins`.

### Token disappears across restarts

The `env_file` path in `config.toml` must match the conductor directory location.
If you renamed or moved the conductor dir, update the path.

### Bot responds to `/getMe` but not to messages

Check that the bridge daemon is running:

```bash
pgrep -af bridge.py
```

Check bridge logs:

```bash
tail -f ~/.local/share/agent-deck/conductor/bridge.log
```

### Multi-conductor routing

The bridge daemon handles all conductors — one bridge process per machine multiplexes across N bots.
When you message a specific bot, the bridge routes it to the matching conductor.
Use the `name: message` prefix syntax to route explicitly when multiple conductors share a bridge.
