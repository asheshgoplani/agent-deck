# Design: Tool-Agnostic Orchestration on omp (oh-my-pi)

Date: 2026-07-16
Status: Approved pending user review
Author: conductor-ops session (Claude) with riplash

## Motivation

The orchestration layer (agent-deck conductor sessions + conductor bridge) currently
assumes Claude Code as the agent runtime. Every time a better model ships — from any
provider — swapping it in means touching orchestration plumbing. omp (oh-my-pi,
https://github.com/can1357/oh-my-pi, a fork of Mario Zechner's Pi) supports 40+
providers with role-based model routing (`default`/`smol`/`slow`/`plan`) and fallback
chains configured in `~/.omp/agent/models.yml`. Running the conductor on omp makes
model choice a config edit instead of an orchestration change.

## Goals

- Conductor sessions run on omp instead of Claude Code.
- bridge.py has no hardcoded agent tool anywhere; the tool is config-driven per
  conductor.
- agent-deck gains first-class omp session support (status detection, resume).
- All agent-deck/bridge changes are self-contained and submitted as PRs to
  `asheshgoplani/agent-deck` (upstream is not ours; we run a fork locally until
  merged).
- Model/provider changes after this migration require editing only omp's
  `models.yml`.

## Non-Goals (day one)

- Porting Claude Code-specific conductor machinery: Stop-hook child-waiting nudges,
  PreCompact state persistence, auto-memory, superpowers skills, the PreToolUse
  credential safety shield. Explicitly deferred ("core loop only" decision).
- Moving worker/teammate sessions to omp by default. Workers keep whatever tool
  fits the task; this migration covers orchestration. (Adapter work makes omp
  workers *possible* later for free.)
- omp RPC-mode (NDJSON) integration with agent-deck. Named as the fallback if
  pane-scraping proves unworkable, but out of scope now.
- Multi-provider model config. Day one is Anthropic via existing subscription
  OAuth; other providers come later via `models.yml` edits (that being the point).

## Decisions Made (with user, 2026-07-16)

- Scope: everything — conductor on omp, bridge fully tool-agnostic, agent-deck
  adapter upstreamed.
- Adapter shape: first-class built-in tool in agent-deck (Approach A), built in a
  local fork, PR'd upstream. Not `compatible_with="pi"` inheritance (omp's Rust
  TUI differs from Pi's; inherited patterns could silently misread status — fatal
  for a conductor). Not config-only patterns (brittle, no resume/fork).
- Parity bar: core loop only (heartbeats, [EVENT] triage, agent-deck CLI via bash,
  state.json/task-log.md discipline, POLICY.md tiers, caveman replies).
- Cutover: parallel observe-only canary first, then in-place swap of conductor-ops.
- Models/auth: Anthropic via existing Claude subscription OAuth. `default` role =
  big Anthropic model; `smol` = Haiku-class for routine turns.

## Verified Facts (2026-07-16)

- agent-deck v1.9.73 installed; Pi is a built-in tool (upstream #674, #1197, #1287,
  #1565) but omp is not; `compatible_with` accepts only "claude" and "codex".
- Tool registry: `internal/session/builtins.go` (`builtinTools()` slice, precedence
  order is load-bearing). Custom tools live in `[tools.<name>]` config with
  busy/prompt/detect pattern overrides — this is the local escape hatch if
  upstream stalls.
- Status detection: per-tool `RawPatterns` (BusyPatterns / PromptPatterns /
  SpinnerChars) in `internal/tmux/patterns.go`; Pi's arm matches
  `pi>` prompts, `ctrl+c to interrupt`, subagent markers, spinner glyphs.
- bridge.py is upstream code (`internal/session/conductor_bridge.py`), deployed to
  `~/.agent-deck/conductor/bridge.py`. Local-only patches would be clobbered by
  agent-deck updates — changes must land upstream.
- bridge.py has exactly one hardcoded tool: `"-c", "claude"` inside
  `ensure_conductor_running()` (~line 706), the conductor auto-(re)create path.
- `discover_conductors()` already parses each conductor's `meta.json` and
  normalizes `name`/`profile`. `meta.json` already carries an `"agent"` field
  (ops has `"agent": "claude"`) that the bridge currently ignores.
- omp verified from upstream README: Anthropic OAuth (subscription) supported;
  inherits rules/skills/MCP from `.claude`, `.cursor`, etc. on first run; AGENTS.md
  project instructions; `--resume` flag; headless `omp -p` one-shot mode and
  `omp --mode rpc` (NDJSON); model roles in `~/.omp/agent/models.yml`.
- omp is NOT currently installed on this host. No local clone of agent-deck exists
  yet; fork + clone is part of the work.

## Architecture

Four components, four phases of change:

### Component 1: agent-deck omp adapter (fork → PR #1)

- Fork `asheshgoplani/agent-deck` under the procrypto account; clone to
  `~/projects/agent-deck`.
- `internal/session/builtins.go`: add `{Name: "omp", Icon: "⌥", detectTokens:
  []string{"omp"}}`. Token match (like Pi's) so short-name substring collisions
  ("compose", "stomp") cannot false-match. Insert position: adjacent to "pi" in
  the precedence slice; order is load-bearing, so the new entry must not sit in
  front of a tool whose command strings could contain "omp" as a token (none do).
- `internal/tmux/patterns.go`: new `case "omp":` returning RawPatterns derived
  empirically from phase-0 tmux probes of the real omp TUI (idle prompt, busy
  spinner, streaming, permission prompt, subagent activity). Patterns are NOT
  copied from Pi.
- Resume support: wire omp's `--resume` the same way Pi resume landed in #1197.
- Tests: `patterns_test.go`-style cases using pane-content fixtures captured
  during phase 0; registry tests for detect-token matching (3+ true matches,
  3+ false-positive candidates per global boundary-testing rules).
- Keep the diff additive (new registry entry, new case arms, new fixtures) so the
  PR is reviewable and self-contained.
- Until the PR merges: build from the fork (`make`/`go build`) and run the fork
  binary locally. Rebase the fork on upstream releases as needed.

### Component 2: bridge tool-agnostic conductor (PR #2, small)

- `ensure_conductor_running()`: accept the conductor's meta dict (or an `agent`
  parameter threaded from callers) and use `meta.get("agent") or "claude"` for the
  `-c` argument when creating a missing conductor session. Default preserves
  existing behavior for every current conductor.
- `discover_conductors()` gains the same normalization for `agent` it already does
  for `name`/`profile`: `meta["agent"] = meta.get("agent") or "claude"`.
- Extend upstream conductor tests (`conductor/tests/`) with an agent-field case.
- Deploy: upstream PR to `internal/session/conductor_bridge.py`; locally, apply the
  same change to `~/.agent-deck/conductor/bridge.py` (with a dated backup copy,
  matching existing convention: bridge.py.backup) so the canary can run before the
  PR merges. Restarting the bridge service to load it is a user-approved action
  (Tier 1 infra). A bridge restart is separately pending for the rotated Telegram
  token; if that restart has not happened by deploy time, one restart covers both.

### Component 3: omp conductor runtime

- Install omp (npm or shell-script installer, pinned version recorded in the plan).
- User performs `omp` Anthropic OAuth login interactively (subscription billing; no
  new API keys; no secrets handled by the agent).
- `~/.omp/agent/models.yml`: `default` = the strongest Anthropic model exposed via
  subscription OAuth at setup time (Fable 5 / Opus 4.8-class), `smol` =
  Haiku 4.5-class. Fallback chains left empty day one (single provider).
- Write an explicit `AGENTS.md` for the canary conductor directory porting the
  core loop from the shared conductor CLAUDE.md + ops CLAUDE.md: heartbeat
  protocol and response format, NEED/OPEN dedupe, POLICY.md tier rules (referenced,
  not duplicated), state.json/task-log.md discipline, caveman-mode reply rules,
  agent-deck CLI reference. omp's auto-inheritance of `.claude` rules is treated
  as a bonus, not relied upon.
- Explicitly dropped day one (per parity decision): Stop-hook nudges, PreCompact
  state save (omp compaction behavior observed during canary instead), auto-memory,
  superpowers skills, PreToolUse safety shield. The conductor's credential-hygiene
  obligations remain in force at the prompt level via AGENTS.md text.

### Component 4: canary rollout and cutover

- Canary: new directory `~/.agent-deck/conductor/omp-canary/` containing
  `meta.json` (`{"name": "omp-canary", "agent": "omp", "profile": "default",
  "heartbeat_enabled": true, ...}`), `AGENTS.md`, and an observe-only `POLICY.md`.
  The bridge auto-discovers it and creates session `conductor-omp-canary` with
  `-c omp`.
- Double-conductor conflict: conductors monitor a profile, and ops already owns
  `default`. The canary's POLICY.md therefore forbids ALL interventions: no
  `session send` to any session, no NEED lines (no user pings), no restarts. It
  only reads statuses/output and emits `[STATUS]`-style observation reports.
  conductor-ops remains the sole actor during the canary period.
- Validation criteria (all must hold over ≥3 days of live heartbeats):
  - agent-deck status detection for the omp session is correct across
    running/waiting/idle (spot-checked against tmux reality, not self-reported).
  - Bridge `session send --wait` round-trips reliably (heartbeat prompt in,
    parseable reply out) with no stuck sends.
  - `agent-deck session output -q` returns the canary's replies intact.
  - Canary's triage reports substantively agree with conductor-ops's decisions on
    the same heartbeats (tier classification, NEED-vs-OPEN, auto-response calls).
  - No omp crashes/hangs requiring manual restart.
- Cutover (user-approved, Tier 1): set `"agent": "omp"` in ops/meta.json, port
  AGENTS.md/POLICY into `~/.agent-deck/conductor/ops/`, stop `conductor-ops`; the
  bridge auto-recreates it on omp. state.json + task-log.md are the context
  handoff (they already serve this role across restarts). The Claude conductor
  session is retired, not deleted, until the omp conductor has survived its first
  week.
- Rollback: revert `"agent"` to `"claude"` in ops/meta.json, stop the omp session;
  bridge recreates on Claude Code. One-field, one-restart rollback at every stage.
- Cleanup: after cutover has held for a week, remove the canary conductor dir and
  its session.

## Error Handling

- Pattern misdetection (conductor stuck "running" or falsely "waiting"): caught by
  canary validation before cutover; post-cutover, the bridge's existing error
  handling (status probe + restart path) still applies. Escape hatch: a
  `[tools.omp-patched]` custom tool entry in config.toml with corrected patterns
  can override without waiting on a new binary.
- omp version drift breaking the TUI patterns: pin the omp version; record it in
  the canary AGENTS.md; upgrades to omp are deliberate, tested actions.
- Upstream PR rejected/stalled: fork binary keeps running locally indefinitely;
  periodic rebases. No functional dependency on merge timing.
- Bridge deploy vs upstream update race: local bridge.py edit is identical to the
  PR content, so an agent-deck update that ships the merged PR is a no-op; an
  update that ships WITHOUT the PR (clobbering the local edit) is detected because
  the canary/omp conductor fails to auto-recreate — re-apply the patch from the
  fork.

## Testing

- Phase 0 probe (before any Go code): run omp inside a bare tmux session; capture
  pane content for idle, busy/streaming, tool-permission prompt, subagent
  activity, and post-compaction states; save as fixtures. This gates the whole
  design: if omp's TUI proves hostile to pane-scraping, stop and re-plan around
  RPC mode instead of building a bad adapter.
- Go unit tests: patterns (fixtures, true/false-positive cases), registry
  detect-token matching, resume flag wiring.
- Bridge tests: agent-field selection incl. missing-field default ("claude"),
  extending the existing upstream conductor test suite.
- Live validation: the canary period itself (criteria above).

## Sequencing (small, reviewable increments)

1. Phase 0: install omp, OAuth login (user), tmux probe, capture fixtures.
   Go/no-go on pane-scraping.
2. PR #1 branch: agent-deck omp adapter + tests; build fork binary; verify
   `agent-deck add -c omp` + status detection locally against a scratch session.
3. PR #2 branch: bridge agent-field support + tests; deploy patched bridge.py
   locally alongside the pending Telegram-token bridge restart (one restart).
4. Canary: create omp-canary conductor dir; observe ≥3 days; compare triage.
5. Cutover: flip ops meta.json to omp (user-approved); retire Claude conductor
   after a stable week; remove canary.

## Open Questions

- omp's exact TUI strings/spinner glyphs — deliberately unresolved until the
  phase 0 probe; the design treats them as a deliverable, not an assumption.
- Whether upstream prefers one combined PR or two — ask in the PR description;
  the branches are structured to work either way.
