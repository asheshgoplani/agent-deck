# Slack channel setup

Connect a Slack bot to your conductor for channel-based monitoring and control.
The bot listens in a dedicated channel and replies in threads to keep the channel clean.

## What you need

- A Slack workspace where you can install apps
- A conductor already created (`agent-deck conductor setup <name>`)

## Step-by-step setup

### 1. Create the Slack app

1. Go to [api.slack.com/apps](https://api.slack.com/apps) and create a new app.
2. Enable **Socket Mode** -> generate an app-level token (`xapp-...`).
3. Under **OAuth & Permissions**, add bot scopes: `chat:write`, `channels:history`, `channels:read`, `app_mentions:read`.
4. Under **Event Subscriptions**, subscribe to bot events: `message.channels`, `app_mention`.
5. (Optional) If using slash commands, create: `/ad-status`, `/ad-sessions`, `/ad-restart`, `/ad-help`.
6. Install the app to your workspace.
7. Invite the bot to your channel (`/invite @botname`).

### 2. Run conductor setup (or re-run it)

```bash
agent-deck conductor setup <name>
```

Answer **y** at the Slack prompt and provide:

- **Bot token** (`xoxb-...`) — from OAuth & Permissions page
- **App token** (`xapp-...`) — from Socket Mode settings
- **Channel ID** (`C01234...`) — right-click channel name -> "Copy link", the ID is in the URL
- **Allowed member IDs** (`U01234567`, comma separated) — the Slack users who may use the bot.
  Find yours in Slack: your profile -> **...** (More) -> **Copy member ID**.
  Leaving it empty asks you to confirm letting every workspace member in (`allow_all_users = true`).

### 3. Restart the conductor

```bash
agent-deck session restart conductor-<name>
```

### 4. Verify

In your Slack channel, type:

```
/ad-status
```

The bot should reply with an aggregated status across all conductors.

## Available commands

The Slack bot supports both prefix routing and slash commands:

| Command | What it does |
|---------|-------------|
| `<name>: <message>` | Routes message to a specific conductor |
| `/ad-status` | Aggregated status across all profiles |
| `/ad-sessions` | List all sessions |
| `/ad-restart [name]` | Restart a conductor |
| `/ad-help` | List available commands |

Responses come in threads to keep the channel clean.

## One bot per conductor

The same constraint applies as with Telegram: each conductor needs its own dedicated Slack bot.
Do not share a single bot across multiple conductors.

## Configuration

Slack credentials are stored alongside other conductor settings.
The bridge daemon handles Slack and Telegram concurrently — both can run simultaneously.

### Who can use the bot

Only the Slack users listed in `allowed_user_ids` can message the bot or run its slash commands:

```toml
[conductor.slack]
allowed_user_ids = ["U01234567", "U07654321"]
# allow_all_users = true   # instead: every member of the bot's workspace may use it
```

- With no `allowed_user_ids` (missing or empty) and no `allow_all_users = true`, the bridge refuses every Slack user and logs a warning at startup.
- A refused user gets one reply (per bridge run) with their member ID, so the owner can copy it into `allowed_user_ids`. Slash commands get the same text as a reply only they can see.
- A listed user can mention the bot in any channel it is in. With `allow_all_users = true`, mentions and slash commands are accepted only in `channel_id`.
- Events whose workspace ID (from Slack's event envelope or the event itself) names another workspace than the bot's own, for example from a shared channel, are ignored. When Slack sends no workspace ID, this check does not block.
- Messages and mentions posted by bots or integrations (incoming webhooks, workflows, apps), and any event without a user ID, are ignored even with `allow_all_users = true`.

Restart the bridge after changing these keys.

#### Upgrading from an older version

Older versions let every workspace member use the bot when `allowed_user_ids` was empty or missing.
Now such a bridge refuses everyone and logs:

```
Slack bridge: no allowed users are set, so every Slack message and command is refused. Add your Slack member ID under [conductor.slack] in ~/.agent-deck/config.toml: allowed_user_ids = ["U01234567"] (or set allow_all_users = true to let every workspace member in), then restart the bridge.
```

Add your member ID to `allowed_user_ids` (or set `allow_all_users = true` to keep the old open behavior, limited to `channel_id`), then restart the bridge.

### Routing messages without a prefix

A Slack message is routed in this order:

1. A `name: message` prefix always wins.
2. A reply inside a thread the bridge already routed for a conductor (started by a message the bridge routed there, or by an alert, digest or outbox message it posted for that conductor) goes back to that conductor.
3. Otherwise `default_conductor` decides:

```toml
[conductor.slack]
default_conductor = "ops"   # unprefixed messages go to the "ops" conductor
# default_conductor = ""    # no default: the bot replies with the conductor list and the prefix syntax
```

When the key is absent, unprefixed messages go to the alphabetically first conductor, as before.
A name that matches no conductor routes nothing and logs a warning, so a typo never silently picks a conductor.
Thread routing is kept in memory (the most recent 500 threads), so it starts empty after the bridge restarts.
This applies to Slack only for now; Telegram and Discord still use the first conductor for unprefixed messages.

## Debugging tips

### Bot does not respond

1. Check that your member ID is in `allowed_user_ids` (the bridge log names every refused user).
2. Verify Socket Mode is enabled (not HTTP webhooks).
3. Confirm the bot is invited to the channel.
4. Check that event subscriptions include `message.channels` and `app_mention`.
5. Check bridge logs: `tail -f ~/.local/share/agent-deck/conductor/bridge.log`

### Slash commands return "dispatch_failed"

This means Slack cannot reach your app.
With Socket Mode enabled, Slack should deliver slash commands over the WebSocket — verify the app-level token is correct and the bridge is connected.

### Messages appear but conductor does not reply

Verify the conductor session is running:

```bash
agent-deck conductor status <name>
```

Check that routing is correct — the Slack bot uses the same `name: message` prefix convention as Telegram.
