# Watchers: lean triggers from the outside world into your conductors

A watcher is the thing that rings the doorbell. It does not carry the package inside; it just rings. When a GitHub issue gets filed, when a Slack message arrives, when your phone fires an ntfy push, when your gmail-watcher script notices a new label — a watcher's only job is to drop a short, normalized trigger onto a conductor session so the conductor can decide what to do next.

This doc explains the watcher framework that ships in `internal/watcher/`, the philosophy behind it, and how to extend it with your own custom sources.

## The doorbell model

A conductor is an expensive, context-heavy Claude session. Every token you hand it at wake-up time is a token it cannot spend on actual work. So the rule is:

**Forward the bell, not the package.**

- ❌ Forwarding the full email body, the entire meeting transcript, the complete webhook payload. Floods context. Costs tokens. Makes the conductor's log unreadable.
- ✅ Forwarding `[github:pr_opened:asheshgoplani/agent-deck#740]` and letting the conductor call `gh pr view 740` itself if (and only if) it decides the event is worth acting on.

A good trigger is:

- **Short.** Convention: ≤200 characters. If you need more, you are smuggling data that the conductor should fetch live.
- **Structured.** `[source:type:identifier] optional hint`. The conductor's prompt can pattern-match on it. `[gmail:new:label=agent-deck] 3 unread` is a good trigger; `New email from bob@example.com titled "hey"` is not.
- **Self-contained enough to ignore.** The conductor reads the trigger, and if it does not care, it moves on. It should never need to fetch state just to decide whether a trigger is relevant.

Why this matters: conductors run for weeks. A conductor that auto-fetches every trigger's full payload will hit its context limit and start compacting away real work to make room for email bodies. A conductor that treats triggers as doorbells and fetches live state on demand stays lean indefinitely.

## What ships in the repo

The framework lives at `internal/watcher/`:

```
internal/watcher/
├── adapter.go         # WatcherAdapter interface, AdapterConfig, normalized Event struct
├── engine.go          # Runs adapters, dispatches events to router + event_log
├── event_log.go       # SQLite-backed dedupe (INSERT OR IGNORE on (name, event_id))
├── health_bridge.go   # Cross-links watcher health with the session reviver
├── layout.go          # ~/.agent-deck/watcher/<name>/ folder layout (REQ-WF-6)
├── router.go          # Maps events → target conductor/group via clients.json
├── state.go           # Per-watcher state.json (last seen, failure counts)
├── webhook.go         # Generic HTTP webhook adapter
├── github.go          # GitHub webhook adapter (HMAC-SHA256 verified)
├── ntfy.go            # ntfy.sh push-notification topic adapter
├── slack.go           # Slack-over-ntfy bridge adapter
├── gmail.go           # Gmail IMAP adapter (scaffold; see gmail_test.go)
├── triage.go          # Triage subprocess lifecycle (reaper, zombie gate)
└── assets/            # Skills + templates embedded into the binary
```

Subcommands live in `cmd/agent-deck/watcher_cmd.go`; the TUI watcher panel is at `internal/ui/watcher_panel.go` (press `w`).

Every adapter implements the same small interface (`internal/watcher/adapter.go`):

```go
type WatcherAdapter interface {
    Setup(ctx context.Context, config AdapterConfig) error
    Listen(ctx context.Context, events chan<- Event) error
    Teardown() error
    HealthCheck() error
}
```

`Listen` is the only method that does real work: it blocks, normalizes whatever raw payload the source sends, and pushes `Event` structs onto the channel. The engine handles dedupe, routing, event log writes, and health accounting. Adapters stay tiny.

## Built-in adapter types

Four adapters are wired into the CLI today. All four embody the doorbell model — the event fields they forward are small and structured.

| Type | What it listens for | Required flag | Transport |
|------|---------------------|---------------|-----------|
| `webhook` | Any service that can fire an HTTP POST | `--port <int>` | Local HTTP listener |
| `github` | GitHub repo events (issues, PRs, pushes) | `--secret <hmac-secret>` | HTTPS webhook + HMAC-SHA256 verify |
| `ntfy` | [ntfy.sh](https://ntfy.sh) topics (phone → conductor) | `--topic <name>` | Persistent SSE subscription |
| `slack` | Slack messages via a Cloudflare Worker bridge into an ntfy topic | `--topic <name>` | Slack → CF Worker → ntfy → watcher |

A fifth adapter (`gmail`) is scaffolded in `gmail.go` but not exposed through `watcher create` at the time of this writing. If you need gmail right now, the supported path is an external polling script (see "Custom external watchers" below) rather than enabling the internal adapter.

## CLI surface

Verified against `agent-deck watcher --help`:

```
agent-deck watcher create <type> --name <name> [options]
agent-deck watcher start <name>
agent-deck watcher stop <name>
agent-deck watcher list [--json]
agent-deck watcher status <name> [--json]
agent-deck watcher test <name>
agent-deck watcher routes [--json]
agent-deck watcher import <path>
agent-deck watcher install-skill <skill-name>
```

The usual loop:

```bash
# Create the watcher (writes ~/.agent-deck/watcher/<name>/ with watcher.toml + state.json).
agent-deck watcher create github --name gh-alerts --secret "$GITHUB_WEBHOOK_SECRET"

# Activate it (picked up by the engine on the next tick).
agent-deck watcher start gh-alerts

# Confirm: list shows status + events/hour; status shows recent events.
agent-deck watcher list
agent-deck watcher status gh-alerts

# Smoke-test without waiting for a real event.
agent-deck watcher test gh-alerts
```

`agent-deck watcher routes` prints the currently-loaded routing rules across every watcher, so you can double-check which conductor or group owns which event types.

Conversational setup is also supported: `agent-deck watcher install-skill watcher-creator` drops a Claude Code skill into `~/.agent-deck/skills/pool/`, and inside an agent-deck Claude session you can then ask *"Use the watcher-creator skill to set up a GitHub watcher"*. The skill walks through adapter choice, required settings, and emits the exact `watcher create` command.

## Routing: how an event finds a conductor

Each watcher gets its own directory at `~/.agent-deck/watcher/<name>/` with (REQ-WF-6):

```
~/.agent-deck/watcher/<name>/
├── watcher.toml     # adapter type + [source] settings
├── clients.json     # routing rules: event type → target conductor/group
├── state.json       # last-seen cursor, consecutive-failure counter, health
└── events.log       # append-only dedupe-resolved event log (mirrored into SQLite)
```

`clients.json` is where you decide *which* conductor wakes up for *which* event. Edit it by hand, or let `watcher create` prompt you for defaults. `agent-deck watcher routes` is the authoritative read-out.

Dedupe is SQL-level: `INSERT OR IGNORE INTO watcher_events (watcher_name, event_id, ...)`. Retries from the sender (GitHub re-fires when its delivery times out, ntfy replays when the subscriber reconnects) cannot double-fire a conductor. Turning this off requires an RFC.

## Security guarantees

- **GitHub adapter verifies HMAC-SHA256** on every webhook. Missing or invalid signature → event dropped, counter incremented, nothing forwarded. Removing this check requires an RFC (`CLAUDE.md` "Watcher structural changes requiring RFC").
- **ntfy adapter** relies on topic secrecy. Use a long random topic name; anyone who knows it can publish to it.
- **webhook adapter** binds to localhost by default; exposing it to the public internet is on you (use a reverse proxy + auth header).
- **slack adapter** rides on top of the ntfy adapter, so it inherits ntfy's topic-secrecy threat model plus whatever you put in front of your Cloudflare Worker.

## Custom external watchers

The in-repo framework covers HTTP-receivable sources. For sources that require polling — IMAP inboxes, calendar APIs, a CI that only exposes a status page, a custom SaaS — the cleanest pattern today is an **external polling script** that forwards triggers via `agent-deck session send`.

The pattern:

1. **Poll.** A small script (Python, bash, Go — whatever fits) queries the source on a schedule. systemd timers or launchd work well; so does a plain `while true; do ...; sleep 60; done` under agent-deck itself.
2. **Dedupe locally.** Track "last seen" in a state file (`~/.agent-deck/events/<name>.last`, a SQLite file, whatever). Do not rely on the conductor to filter duplicates — that is context pressure you can avoid.
3. **Forward lean.** Emit the bell, not the package:
   ```bash
   agent-deck session send <conductor-name> "[gmail:new:label=agent-deck] 3 unread" --no-wait
   ```
   `--no-wait` matters: if the conductor is unreachable, you want the script to log-and-move-on rather than block until timeout.
4. **Test the silent-failure path.** A watcher that cannot reach its conductor is worse than useless, because nobody notices. Emit a metric or log line on every successful send, and page yourself if the send count drops to zero.

Two informal examples of this pattern, both maintainer-side rather than in this repo:

- **gmail-watcher** — polls an IMAP label, fires `[gmail:new:...]` triggers.
- **meeting-watcher** — polls Google Calendar, fires `[calendar:starting:<event>]` triggers ~5 minutes before meetings.

Neither ships in agent-deck because both are tied to a specific identity's credentials. The pattern above is what you are copying, not the scripts themselves.

## Writing a first-class in-repo adapter

If your source *can* be wired through the built-in engine (it exposes webhooks, a push channel, or a long-lived stream), it is worth adding a real adapter. You get dedupe, health, the TUI panel, and the event log for free.

Drop a new file into `internal/watcher/` that implements `WatcherAdapter`, register it in `engine.go`, teach `watcher_cmd.go` about the new `--type`. Then — non-negotiable per `CLAUDE.md`:

```bash
go test ./internal/watcher/... -race -count=1 -timeout 120s
go test ./cmd/agent-deck/... -run "Watcher" -race -count=1
bash scripts/verify-watcher-framework.sh
```

Any change under `internal/watcher/**`, `cmd/agent-deck/watcher_cmd*.go`, `internal/ui/watcher_panel.go`, or `internal/statedb/statedb.go` (watcher rows) is test-gated. `TestSkillDriftCheck_WatcherCreator` additionally enforces that embedded skills + README + CHANGELOG stay in sync with the layout (REQ-WF-7).

## Common gotchas

- **Forwarding verbose content.** Breaks the doorbell model. Every extra kilobyte costs tokens on every conductor turn until the context compacts. If your trigger is >200 chars you are probably smuggling data.
- **No local dedupe in a polling script.** Some sources re-deliver on retry (IMAP on reconnect, cron re-runs, flaky networks). Without a state file you will fire the same trigger again and again, and the conductor will eventually start ignoring you — or, worse, act on stale signals.
- **Forgetting `--no-wait` in a polling script.** `session send` blocks until the session acknowledges. A wedged conductor then wedges every watcher that forwards to it. Use `--no-wait` unless you genuinely need the ack.
- **Leaking host-private topic names into shared configs.** `ntfy` topic secrecy is your only auth. Do not commit topic names into tracked files; keep them in `~/.agent-deck/watcher/<name>/watcher.toml`.
- **Standing up a new adapter without updating embedded skills.** `TestSkillDriftCheck_WatcherCreator` will fail the build. Fix the assets alongside the code.
- **Assuming the event log is for debugging only.** It is the dedupe source of truth. Hand-editing `events.log` or deleting SQLite rows can cause the next real event to fire twice.

## Interaction with conductors

A conductor that receives a trigger is responsible for everything that happens next:

1. Parse the trigger (`[source:type:identifier]` is a stable format).
2. Decide whether it cares. Many triggers are ignored. That is healthy.
3. If relevant, fetch live state (`gh pr view`, `curl`, `slack api`, whatever the source exposes).
4. Act — or delegate to a child session — or escalate to the user via the conductor's channel.

The watcher does not know which of these happens. That is the separation of concerns that makes the whole thing scale.

## Related docs

- [CONDUCTOR.md](CONDUCTOR.md) — what the watcher is ringing the doorbell *for*.
- [WATCHDOG.md](WATCHDOG.md) — keeps the conductor alive so the doorbell gets answered.
- [SKILLS.md](SKILLS.md) — `watcher-creator` is a pool skill; this doc explains the two tiers and how to install skills.
- `internal/watcher/` — the code. Start at `adapter.go` and `engine.go` to understand the data flow.
