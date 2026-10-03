# Conductors and Watchers

Long-lived orchestrator sessions, their remote channels, and the watchers that route inbound events to them.

## Conductors

A conductor is a persistent agent-deck session that orchestrates other sessions. It watches the rest of your sessions, auto-responds when confident, escalates to you when ambiguous, and optionally pairs with a remote channel (Telegram or Slack) so you can talk to it from your phone.

Use this section when the user says **"conductor"**, **"set up a conductor"**, **"monitor sessions"**, **"telegram bot for agent-deck"**, **"slack bot for agent-deck"**, or **"remote control my sessions"**.

```bash
# Create a conductor in the default profile
agent-deck conductor setup ops --description "Ops monitor"

# Create on a specific profile (work/personal/etc.)
agent-deck -p work conductor setup infra --description "Infra watcher"

# Use a non-Claude agent for the conductor itself
agent-deck conductor setup review --agent codex --description "Codex reviewer"

# Provide custom env (e.g., third-party Anthropic-compatible endpoint)
agent-deck conductor setup glm-bot \
  -env ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic \
  -env ANTHROPIC_AUTH_TOKEN=<token>

# Status across all conductors
agent-deck conductor status

# List configured conductors
agent-deck conductor list
```

Each conductor lives at `~/.agent-deck/conductor/<name>/` (new installs: `$XDG_DATA_HOME/agent-deck/conductor/<name>/`, default `~/.local/share/agent-deck/conductor/<name>/`) with its own `CLAUDE.md` (or `AGENTS.md` for Codex), `meta.json`, `state.json`, and `task-log.md`. Multiple conductors per profile are supported and each can pair with its own bot.

### Channels (Telegram / Slack)

Channels are how a conductor talks to you remotely. Each conductor pairs **one-to-one** with its own bot — bots are not shared. `agent-deck conductor setup` interactively walks you through Telegram or Slack pairing during creation.

Key constraints:

- The Telegram plugin must be installed under the conductor's Claude profile, but **never globally enabled** in `settings.json`. Per-session activation happens via the `channels` field on the conductor's session record.
- Bot tokens live at `<channel-state-dir>/.env` (chmod 600). Never committed to git.
- Exactly one bot, one conductor, one chat — the routing is deterministic.
- Watch for the "many competing telegram pollers" gotcha (see [Many competing telegram pollers](gotchas.md#many-competing-telegram-pollers-after-multiple-session-starts)) — child sessions inherit `TELEGRAM_STATE_DIR` and can leak duplicate pollers on the same bot token, causing 409 conflicts.

**See:** [docs/conductor/](https://github.com/asheshgoplani/agent-deck/blob/main/docs/conductor/) for the full quickstart, channel setup, multi-conductor setups, and lifecycle commands.

## Watchers

Watchers listen for inbound events (webhooks, push notifications, GitHub events, Slack messages) and route them into conductor sessions. Use them when the user says **"set up a watcher"**, **"listen for webhooks"**, **"route GitHub events to my conductor"**, **"forward ntfy notifications"**, or similar.

Four adapter types are supported:

| Type | Required flag | Typical use |
|------|---------------|-------------|
| `webhook` | `--port` | Generic HTTP listener |
| `github` | `--secret` | GitHub repo webhooks with HMAC verification |
| `ntfy` | `--topic` | ntfy.sh push notifications |
| `slack` | `--topic` | Slack (via Cloudflare Worker bridge) |

```bash
agent-deck watcher create <type> --name <name> <adapter-flags...>
agent-deck watcher start <name>
agent-deck watcher list                # health + events/hour
agent-deck watcher test <name>         # synthetic event (verify routing)
```

Full conversational setup flow is available as a separate skill:

```bash
agent-deck watcher install-skill watcher-creator
```

After running the install command, read the installed `watcher-creator/SKILL.md` to walk the user through adapter selection, required settings, and configuring the effective watcher data dir's `clients.json` routing (`${XDG_DATA_HOME:-$HOME/.local/share}/agent-deck/watcher/clients.json` for new users; legacy `~/.agent-deck/watcher/clients.json` when existing watcher state is present).

See `agent-deck watcher --help` for the full command surface and per-adapter examples.
