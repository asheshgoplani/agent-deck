# conductor/

Runtime assets and helper scripts for the conductor feature (Telegram / Slack /
Discord ↔ agent-deck conductor sessions).

## Where is `bridge.py`?

The conductor bridge script has **exactly one canonical source** in the repo:

```
internal/session/conductor_bridge.py
```

It is embedded into the agent-deck binary (`//go:embed`, see
`internal/session/conductor_bridge_embed.go`). `agent-deck conductor setup`
(`InstallBridgeScript`) and `agent-deck update` (`update.UpdateBridgePy`)
materialize the user-facing runtime copy at:

```
<data-dir>/conductor/bridge.py        # e.g. ~/.local/share/agent-deck/conductor/bridge.py
```

So there is **no `conductor/bridge.py` checked into the repo** — editing the
embedded canonical file is the only place to change the bridge. Tests under
`conductor/tests/` load that same canonical file (see
`conductor/tests/conftest.py`), so the tested bytes are exactly the deployed
bytes.

## Stage events

Per-PR pipeline timing is recorded as JSONL, one line per stage transition,
instead of per-merge receipts and metrics prose. The schema, the
`scripts/stage-event.sh` recorder and the `scripts/stage-report.py` summary
(median and p90 per transition, maintainer versus external) are documented in
[STAGE-EVENTS.md](STAGE-EVENTS.md).
