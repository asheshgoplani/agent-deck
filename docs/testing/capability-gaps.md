# Capability E2E: honest gaps

This file records capabilities that the fast gate (Tier F) cannot verify
deterministically and offline, so they are not faked green. They live in Tier N
(nightly, needs creds) and show on the dashboard as greyed "NIGHTLY" cards. See
the strategy doc, `docs/testing/2026-05-26-capability-e2e-strategy.md`.

The principle (Pattern 5): a capability we cannot verify in the fast pass is
shown as a documented gap, never as a passing test that does not actually
exercise the capability.

## Wave 1 gaps

### Fork that inherits Claude context
`session fork` only fully works for a Claude-compatible session that has a live
`ClaudeSessionID` captured from a real claude transcript (see
`internal/session/instance.go` `CanFork`). That id is non-deterministic and
key-gated, and the fork itself runs `claude --resume`, which needs the real CLI
and auth. Wave 1 therefore tests the deterministic half of the capability: the
precondition guard refuses to fork a non-Claude session and creates no orphan
child row (`TestCapability_Lifecycle_Fork`). The context-inheriting happy path
is Tier N.

### Real agent round trips (claude, codex, gemini, opencode)
The backbone send-and-reply round trip is verified offline against a
deterministic echo agent (`TestCapability_Agent_EchoRoundTrip`). Verifying a
real agent actually replies needs that CLI installed plus its auth, and the
reply is non-deterministic. These run nightly on a host that has the CLIs and
secrets. The echo proxy guards the wiring; the nightly real-agent test guards
drift between the proxy and reality.

The deterministic echo agent uses `session send --no-wait`. The default send
path includes a Ctrl+C "full resend" recovery (issues #479 / #876) tuned for
real agents that sit visibly "active" after receiving input. A trivial echo
stand-in returns to its prompt too quickly for that heuristic, and the Ctrl+C
would kill it. `--no-wait` still exercises the real send-keys, the readiness
preflight, and the delivery verifier, so the round trip is genuine. The default
send path against a real agent is covered by the Tier N round trips.

### MCP actually loads in an agent
Tier F (a later wave) proves the `.mcp.json` is written with the correct shape.
Proving a real agent honors the attachment needs a real agent to introspect its
own tool list (for example `/mcp`), so it is Tier N.

### Remote over SSH
`remote add` / `remote sessions` need an SSH endpoint. Loopback SSH
(`ssh localhost` to a second `HOME`) can promote this to Tier F if the CI image
allows it; the default is Tier N against a real host.

## Not yet covered in Wave 1 (planned later waves)

These are not gaps in the "cannot be deterministic" sense; they are simply not
built yet. They are scheduled per the strategy build plan:

- Conductor finished-signal (#1186) and EVENT dedup (#1187) end to end (Wave 2).
- Web mutation endpoints and the terminal bridge via httptest (Wave 2).
- MCP attach / detach `.mcp.json` assertions (Wave 2).
- Worktree create + finish against a throwaway git repo (Wave 2).
- Groups / profiles isolation and the offline multi-tool readiness-detector
  golden-fixture test (Wave 3).
