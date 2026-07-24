# `launch --tier` design — config-driven connector+model tiers

Status: follow-up design, not implemented. Companion to the "Model &
connector tiering" section of `skills/orchestrate/SKILL.md`, which today
hand-plumbs models via `--extra-arg --model --extra-arg <model>`.

## Problem

- The orchestrate skill hardcodes connector-specific model flags in every
  launch command; retuning models means editing the skill.
- Tiering only covers connectors the skill happens to document; the skill's
  `compatibility: "claude, opencode"` claim and its claude/codex-only model
  plumbing quietly disagree.
- Cross-provider role assignment (plan on opus, implement on sonnet, review
  with codex) works but takes two flags (`-c` + extra-args) and per-connector
  knowledge at every call site.
- Nothing records which model a session ran, so escalations and cost
  outcomes are invisible to `session children` / `session info`.

## Design

### Config (`config.toml`) — ladders × levels

The structure is two-dimensional: a **ladder** per provider, and a fixed
**level** vocabulary (`cheap` / `mid` / `strong`) shared by every ladder.
This beats flat tier names (`strong-codex`, `review`, …) because the level
vocabulary never grows — the orchestrate skill and the planner's `tier:`
tags only ever speak three words, and they resolve against any ladder
without translation. Cross-provider selection is a ladder prefix, not a
new name, and adding a provider is one table.

```toml
[tiers]
default_ladder = "claude"   # bare levels resolve against this ladder

[tiers.ladders.claude]
connector = "claude"
cheap = "haiku"    # Claude aliases track the latest release automatically
mid = "sonnet"     # (today: haiku->4.5, sonnet->5, opus->4.8)
strong = "opus"    # point at "fable" (claude-fable-5) to go above opus

[tiers.ladders.codex]
connector = "codex"
cheap = "gpt-5.6-luna"
mid = "gpt-5.6-terra"
strong = "gpt-5.6-sol"
```

Model strings are whatever the connector's `--model` accepts. The two
providers drift differently: **Claude aliases are self-updating** (`haiku`
/ `sonnet` / `opus` / `fable` always resolve to the latest release, so the
claude ladder above never needs a version bump; pin full IDs like
`claude-sonnet-5` or `claude-opus-4-8` only when reproducibility matters
more than freshness). **OpenAI's Sol/Terra/Luna are durable tier names but
carry the generation prefix** (`gpt-5.6-*`), so the codex ladder needs one
edit per generation. Either way, config is the single place models are
named — the skill and the CLI never hardcode them.

### CLI

`agent-deck launch <path> --tier <[ladder:]level> ...` (and later `add`):

- `--tier mid` resolves `mid` against `default_ladder`; `--tier codex:mid`
  names the ladder explicitly. Resolution sets the connector as if `-c`
  had been passed and appends that connector's model args.
- `--tier` together with an explicit `-c` that contradicts the ladder's
  connector is an **error**, not a silent preference — silent mismatch is
  how tiering rots. Matching `-c` is redundant but allowed.
- Unknown ladder or level → error listing what's configured.
- No `--tier` → exactly today's behavior; tiers are pure opt-in.

### Connector translation

| Connector | Model args appended |
| --- | --- |
| claude | `--model <model>` |
| codex | `--model <model>` |
| opencode | `--model <model>` (verify `provider/model` format) |
| others | error: "connector X has no model mapping" |

Translation lives next to the existing connector definitions so adding a
connector adds its model flag in one place.

### Surfacing

- Persist the resolved `ladder:level` + connector/model on the instance (instances
  table). Schema bump required — **follow the LOCAL schema numbering**, not
  upstream's (local v11=auto_name, v12=pin; see the divergence note in the
  repo docs/memory).
- Expose `tier`, `model` in `session children --json` and `session info` so
  a conductor can audit tiers and escalations without a manifest.

### Skill follow-up

Once shipped, the orchestrate skill's tiering section drops the
`--extra-arg` plumbing and says only "launch with `--tier [ladder:]<level>`";
the tier table stays, the mechanics collapse. Read-only enforcement
(`--disallowedTools` / `--sandbox read-only`) stays per-connector — it is a
role property, not a tier property, and does not belong in tier config.

## Tests

- Config parsing: valid ladders, unknown connector, missing level,
  missing/unknown `default_ladder`.
- `--tier` parsing: bare level, `ladder:level`, unknown ladder/level errors.
- Arg translation per connector, including the no-mapping error.
- `--tier` + contradicting `-c` errors; matching `-c` passes.
- Tier/model round-trip into `session children --json`.

## Open questions

- Per-profile tier overrides (profiles already isolate state.db) — defer
  until a real need shows up.
- Should escalation (conductor relaunching a session at a higher tier) be
  recorded as a first-class event rather than manifest prose? Defer;
  `session info` showing the tier of each session already covers the audit.
