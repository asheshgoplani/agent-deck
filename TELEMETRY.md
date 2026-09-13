# Usage telemetry: opt-in and off by default

Nothing is sent or counted until you explicitly consent. Missing, unreadable or corrupt state is off. A timeout, a script, an environment variable or --yes cannot enable it. This feature does not deploy a receiver or enable this installation.

## The consent question

At the first suitable interactive TUI launch, the full question appears:

```
Help improve agent-deck? (optional, off by default)

agent-deck can send one small anonymous usage report per day so the
maintainer can see which features are used. Nothing is sent unless you
say yes here. A random id links reports until you reset it.

What is sent:      a random install id, agent-deck version, OS and CPU
                   type, and counts of features used (for example how
                   many sessions were started per tool, whether remote
                   or conductor commands were used, TUI vs CLI).
What is never sent: session titles, prompts, file paths, commands,
                   hostnames, usernames, or timestamps finer than the day.
                   Receiver operators must disable IP/access logging.
Where:             one HTTPS POST, at most once per day, to
                   {{endpoint}}
Turn off any time: agent-deck telemetry disable
                   or set AGENTDECK_TELEMETRY=0 / DO_NOT_TRACK=1
Preview the current payload: agent-deck telemetry preview
Last acknowledged payload: agent-deck telemetry show-last
Details: https://github.com/asheshgoplani/agent-deck/blob/main/TELEMETRY.md

[y] Yes, send anonymous usage reports    [n] No (remembered, you will not be asked again)
```

Only a deliberate `y` enables. Every other key declines and is remembered. There is no timeout answer. Input in the first 750 ms is ignored. The prompt never interrupts insert mode or another dialog and never reappears on a storage reload. If the terminal cannot display the full question, `y` does nothing; you can decline or run `agent-deck telemetry enable` in your own interactive shell. The CLI asks the same question. EOF is never yes. `--yes` is deliberately refused.

Consent applies to the exact endpoint and schema shown. Changing either suspends sending until you explicitly enable again, creating a fresh ID and empty counters. A declined installation is not prompted again after an upgrade. No prompting, counting or sending in non-interactive contexts, CI, or inside an agent-deck session.

## What the report contains

At most 2 KiB of JSON with exactly these keys:

| Key | Value |
|---|---|
| `schema_version` | `1` |
| `install_id` | 32 random hex characters, generated only on consent |
| `version` | A bounded release version, or `dev` for custom build strings |
| `os`, `arch` | Operating system and CPU architecture |
| `day` | UTC date, YYYY-MM-DD; no finer timestamp |
| `counters` | Best-effort positive counts, each capped at 1000 |

Counter allowlist:

| Counter | Meaning |
|---|---|
| `tui_launches`, `cli_invocations` | TUI launches and human CLI commands; hooks/daemons excluded |
| `sessions_started.<tool>` | Started sessions by built-in tool: claude, codex, gemini, opencode, pi, copilot, crush, cursor, hermes, deepseek, aider, shell; all other names become `other` |
| `remote_used`, `conductor_used` | Uses of remote and conductor CLI features |

Example:

```json
{"schema_version":1,"install_id":"8f1c2a9b4d6e7f0011223344556677aa","version":"1.15.0","os":"darwin","arch":"arm64","day":"2026-09-04","counters":{"tui_launches":3,"sessions_started.codex":2}}
```

No paths, titles, prompts, commands, hostnames, usernames, account identifiers, custom tool names, model/MCP names, environment values or session content. The random ID is pseudonymous: it links daily reports until you rotate it. Rare usage patterns and network metadata can still permit correlation. Rotation removes the explicit ID link; it does not guarantee reports cannot be correlated.

## Controls

All commands support `--help` and `--json`:

```bash
agent-deck telemetry status     # consent, effective endpoint, counts and last send day
agent-deck telemetry enable     # requires a visible interactive disclosure and y/N answer
agent-deck telemetry disable    # remembers no, removes ID, counts and local last payload
agent-deck telemetry preview    # exact current candidate JSON, without sending
agent-deck telemetry show-last  # last acknowledged payload, or sent:false in JSON mode
agent-deck telemetry reset-id   # new ID; discards pending counts and local last payload
```

`preview` does not create an ID when off. After enabling, it serializes the exact current body; counters and UTC day may change before the next send. `show-last` retains the last body acknowledged with 2xx. A failed request might still have reached the server; absence of acknowledgement does not prove non-delivery. In JSON mode, enable writes the question to terminal stderr and the result to stdout. Redirected question output cannot grant consent.

| Hard-disable switch | Effect |
|---|---|
| `AGENTDECK_TELEMETRY=0` | Any value except 1/true/yes/on forces off; those values never grant consent |
| `DO_NOT_TRACK=1` | Any truthy value forces off |
| `[telemetry] disabled = true` | Forces off; an unreadable config fails closed |

Config is loaded at process startup. Restart the TUI after editing config.toml, or use `telemetry disable` to persist an immediate refusal. It may wait up to five seconds for an already-started request, which cannot be recalled. Once disable returns successfully, no subsequent request is permitted.

## Destination and receiver deployment

Only the interactive TUI sends, at most once per UTC day across concurrent processes. It records the attempt before HTTP. Failure is silent, without another attempt that day. CLI controls never send. The client follows no redirects, cookies or proxy environment settings and ignores the bounded response.

The default is the undeployed placeholder `https://telemetry.agent-deck.invalid/v1/ping`. It triggers no network request or DNS lookup. No real receiver or retention policy is claimed by this change. Self-hosters can configure `[telemetry] endpoint = "https://your.host/v1/ping"`; HTTP is accepted only on loopback for local testing. Credentials, queries and fragments are rejected. Changing the destination requires fresh explicit consent.

A receiver must validate the fixed schema, cap input at 2 KiB, and store only the validated fields and receipt day. Disable IP, header and body access logging at the application, reverse proxy, CDN and hosting layers, and publish retention and deletion policies before collecting. The client cannot enforce these server obligations. Every receiver can see the source IP while handling a connection.

## Local storage and design

`telemetry-state.json` lives in the effective agent-deck data directory, shared across profiles, mode 0600. It holds consent, endpoint, schema, revision, ID, counters, attempt/acknowledgement days and the last acknowledged JSON body. A stable sibling lock and durable atomic saves serialize writers across processes; stale positive choices cannot override a newer refusal. Counter writes skip contention, including an in-flight request, to avoid stalling interactive use. Deleting state resets to off. Manual edits, filesystem rollback and deleting files bypass daily-budget history. Disabling cannot delete reports already held by a receiver.

[Design and threat model](docs/TELEMETRY-DESIGN.md) describe the consent boundary, transport contract and required independent verification.
