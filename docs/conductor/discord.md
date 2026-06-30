# Discord channel setup

Connect a Discord bot to your conductor for server-based monitoring and control.

## What you need

- A Discord server where you have permission to add bots
- A conductor already created (`agent-deck conductor setup <name>`)

## Step-by-step setup

### 1. Create the Discord application

1. Go to [discord.com/developers/applications](https://discord.com/developers/applications) and create a new application.
2. Under the **Bot** tab, create a bot and copy the token.
3. Enable the **MESSAGE CONTENT** intent in the Bot tab.
4. Under **OAuth2** -> **URL Generator**, select scopes `bot` and `applications.commands`, then permissions `Send Messages` and `Read Message History`.
5. Use the generated URL to invite the bot to your server.

### 2. Gather IDs

You need three IDs (enable Developer Mode in Discord settings to copy these):

- **Guild (server) ID** — right-click the server name -> "Copy Server ID"
- **Channel ID** — right-click the channel -> "Copy Channel ID"
- **Your user ID** — right-click your name -> "Copy User ID"

### 3. Run conductor setup (or re-run it)

```bash
agent-deck conductor setup <name>
```

Answer **y** at the Discord prompt and provide:

- Bot token
- Guild (server) ID
- Channel ID
- Your user ID

### 4. Restart the conductor

```bash
agent-deck session restart conductor-<name>
```

### 5. Verify

In the configured channel, send a message to the bot.
The conductor should receive it and respond within a few seconds.

## One bot per conductor

The same constraint applies as with other channels: each conductor needs its own dedicated Discord bot.
Do not share a single bot across multiple conductors.

## Why your user ID matters

The conductor only responds to messages from your user ID, similar to the Telegram user ID constraint.
This prevents other server members from issuing commands to your conductor.

## Configuration

Discord credentials are stored alongside other conductor settings in the conductor's `.env` file.
The bridge daemon handles Discord alongside Telegram and Slack — all three can run simultaneously.

## Listen modes

For a Discord bot (`[conductor.discord]`), `listen_mode` controls which messages
the conductor reacts to. `channel_id` stays the bot's home channel — alerts and
slash commands are scoped to it regardless of mode.

| `listen_mode`             | The bot reacts to                   |
|---------------------------|-------------------------------------|
| `"all"` (default)         | every message in `channel_id`       |
| `"mentions"`              | @mentions in `channel_id`           |
| `"mentions_all_channels"` | @mentions in **any** channel/thread |

`"mentions_all_channels"` reacts only to @mentions (there is no
all-messages-everywhere mode), and the bot needs read access to the channels it
is mentioned in. Authorization is unchanged: only the configured `user_id` is
answered.

```toml
[conductor.discord]
  channel_id  = 456            # home channel: alerts + slash commands
  listen_mode = "mentions_all_channels"
```

## Debugging tips

### Bot is online but does not respond

1. Verify the MESSAGE CONTENT intent is enabled — without it the bot cannot read message text.
2. Confirm the bot has Send Messages and Read Message History permissions in the target channel.
3. Check that the channel ID in the configuration matches the channel you are typing in.
4. Check bridge logs: `tail -f ~/.local/share/agent-deck/conductor/bridge.log`

### Bot goes offline intermittently

Check that only one bridge process is running.
Multiple bridge processes competing for the same bot token can cause disconnections.

### Messages from other users trigger the conductor

Verify that your user ID is correctly configured.
The conductor should ignore messages from any user ID except the one provided during setup.
