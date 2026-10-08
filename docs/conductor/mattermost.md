# Mattermost channel setup

> **Experimental.** The Mattermost bridge is new and has had less real-world use than Telegram, Slack and Discord.
> Its configuration keys and commands may still change, and it does not yet support Slack's `default_conductor` routing or thread affinity.
> Please report problems on the issue tracker with the bridge log attached (`bridge.log`, tokens removed).

Connect a Mattermost bot account to your conductor so you can talk to it from the Mattermost desktop or mobile app.
By default the bot talks to you in a direct message; it can use a channel instead.

## What you need

- A Mattermost account on a server that allows bot accounts
- A conductor already created (`agent-deck conductor setup <name>`)

## Step-by-step setup

### 1. Create the bot account

In Mattermost, open **Main Menu -> Integrations -> Bot Accounts -> Add Bot Account**, then copy the access token it shows.
If that menu is missing, your server only lets admins create bots: ask an admin to create one and give you its token.

To have the bot use a channel instead of a DM, add it to that channel and copy the channel ID (**View Info** on the channel).

### 2. Run conductor setup (or re-run it)

```bash
agent-deck conductor setup <name>
```

Answer **y** at the Mattermost prompt and provide:

- **Server URL** — for example `https://mattermost.example.com`.
  Use `https`: the bot token and every message travel on this connection.
  Plain `http` is accepted for `localhost` without asking; for any other host setup asks you to confirm and records `allow_insecure_http = true`.
- **Bot access token** — or `$ENV_VAR` / `keychain:<service>` to keep it out of `config.toml`
- **Your username** — the only person the bot obeys
- **Channel ID** — leave blank to use a DM between you and the bot

Setup installs `aiohttp` for the bridge; the bridge needs nothing else for Mattermost.

### 3. Restart the conductor

```bash
agent-deck session restart conductor-<name>
```

### 4. Verify

In the bot's DM (or the configured channel), send:

```
!status
```

The bot should reply with an aggregated status across all conductors.
Anything that is not a command goes to the conductor, and its reply comes back to you.

## Available commands

Mattermost slash commands need an HTTP endpoint the server can call, so the bot reads text commands from ordinary messages instead:

| Command | What it does |
|---------|-------------|
| `<name>: <message>` | Routes message to a specific conductor |
| `!status` | Aggregated status across all profiles |
| `!sessions` | List all sessions |
| `!restart [name]` | Restart a conductor |
| `!help` | List available commands |

Replies stay in the thread of the message they answer.
In a channel, the bot starts a thread under each message; in a DM it replies inline unless you wrote in a thread.
While the conductor works, the bot shows as typing.
`NEED:` alerts and other heartbeat notifications are posted to the same DM or channel.

## Configuration

```toml
[conductor.mattermost]
server_url  = "https://mattermost.example.com"
bot_token   = "keychain:agent-deck-mattermost"   # or the token, or "$ENV_VAR"
user        = "your-username"                    # or your user ID
channel_id  = ""                                 # blank: a DM with `user`
listen_mode = "all"                              # in a channel: "all" or "mentions"
# allow_insecure_http = true                     # only to use plain http on a host other than localhost
```

The bridge refuses a plain `http` server URL on any host but `localhost`, `127.0.0.1` or `::1` unless `allow_insecure_http` is set; it logs why in the bridge log.

`listen_mode` only matters in a channel: with `"mentions"` the bot acts only on posts that @mention it.
In a DM every message from you is handled.

Messages reach the conductor tagged with where they came from, like the other channels:
`[from:<username> (<user_id>)] [dm]` or `[from:<username> (<user_id>)] [channel:~<name> (<channel_id>)]`.

## Why your username matters

Anyone who can see the bot can message it.
The bridge acts only on posts from the configured user and ignores everyone else (it logs them as unauthorized).
It also ignores posts made by webhooks, bots and plugins: Mattermost attributes an incoming webhook's posts to the person who created the webhook, so without this anyone holding the URL of a webhook you made could instruct your conductor.
Treat the token as a secret anyway: anyone holding it can post as the bot.

## Debugging tips

### Bot does not respond

1. Check the bridge log for `Mattermost bot @<name> ready` and `Mattermost event stream connected`:
   `tail -f ~/.local/share/agent-deck/conductor/bridge.log`
2. A `401` at start means the token is wrong or was revoked; a `404` on the username means `user` is misspelled.
3. With a `channel_id`, confirm the bot is a member of that channel.

### Replies arrive late after a network blip

The bridge reconnects on its own, backing off up to a minute between attempts, and on reconnect it picks up the messages you sent while it was disconnected.
It reads the channel back page by page to a minute before the last post it handled, using the server's timestamps, however long the outage was, so a busy channel or a wrong local clock does not lose messages, and nothing is handled twice.
Each page is anchored on a post it has already read (`before=<post id>`), so posts deleted or added during the read do not shift later pages and hide others.
If that read fails part way, nothing from it is handled and the next connection reads it all again.
If posting a reply fails with a network error or a server error, the bridge retries it twice before logging it as failed.
When the server rate-limits a request (HTTP 429), the bridge waits as long as the server asks (`Retry-After` or `X-Ratelimit-Reset`, at most a minute) and tries again.
