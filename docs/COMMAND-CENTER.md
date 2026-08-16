# Command Center

Command Center is the fleet overview in `agent-deck web`. It shows conductors,
their active child sessions, current work, waiting decisions, and recent
completions in one live view. Status updates arrive automatically; the page does
not need to be refreshed.

Start the web UI and open the **Command Center** tab:

```bash
agent-deck web
```

Each conductor row includes its health, active-session counts, and current work.
Waiting decisions have a comment action, and the instruction box can send a
message to Maestro or a selected conductor. Instructions use the same supported
delivery path as `agent-deck session send`.

The web server listens on `127.0.0.1:8420` by default. A non-loopback bind
requires authentication; `--token` also protects API and WebSocket access on a
loopback bind. Use `agent-deck web --read-only` to hide write controls and reject
mutating requests. The [CLI reference](../skills/agent-deck/references/cli-reference.md#web-command) lists the current flags.

For persisted `[web]` settings such as `mutations_enabled`, `trusted_domains`,
and `confirm_link_open`, see the [configuration reference](../skills/agent-deck/references/config-reference.md#web-section).
