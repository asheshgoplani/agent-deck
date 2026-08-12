# Managed agent configuration projection

## Motivation

Agent Deck's root configuration is the operator-facing source of truth for
session defaults, while the directories selected as `CODEX_HOME` and
`CLAUDE_CONFIG_DIR` are runtime homes. Editing a group runtime home by hand
creates drift and can be overwritten by reconciliation. The system needs one
explicit, safe path for shared Claude and Codex defaults.

## Decisions

- Add Codex `default_model` and `default_reasoning_effort` to Agent Deck's
  root `[codex]` configuration.
- Resolve model and reasoning effort from root defaults plus explicit
  per-session or group overrides, then apply them on every create, start, and
  restart.
- Keep Claude's existing root `default_model` and group model resolution as
  launch-time configuration. Do not rewrite `CLAUDE_CONFIG_DIR/settings.json`
  to set a model.
- Reconcile only Agent Deck-owned Codex keys in a group `CODEX_HOME/config.toml`:
  `model`, `model_reasoning_effort`, and the pre-existing managed `[tui]`
  fields. Preserve all other configuration and runtime state.
- Track the exact Codex keys Agent Deck owns in a home-local manifest. When a
  root default is removed, remove only the corresponding key that the manifest
  proves Agent Deck wrote; never delete a user-owned key.

## Architecture

`UserConfig` gains the two Codex defaults. A single resolver returns the
effective launch model and reasoning effort for a session. Codex command
construction consumes that resolver, so group and session overrides keep their
existing precedence over root defaults.

Before a Codex session starts, the existing group-home reconciliation path
projects the effective defaults into its selected `CODEX_HOME/config.toml`. The
projector is a narrow, atomic TOML merge: it updates or removes only keys
listed in its Agent Deck ownership manifest. Plugin, marketplace, MCP, trust,
session history, authentication, and unrelated TOML values remain owned by
Codex or the operator.

Claude does not receive a generated home file because its config directory can
contain credentials and settings used outside Agent Deck. Its effective model
continues to be applied with the existing CLI launch mechanism. This provides
the same centralized default without risking a rewrite of a credential-bearing
home.

## Interfaces

```toml
[codex]
default_model = "gpt-5.6"
default_reasoning_effort = "high"
```

Existing explicit per-session choices and group-specific overrides remain
authoritative. Unset values produce no model/reasoning flag and no managed
Codex key.

## Verification

- Unit-test resolution precedence for Codex defaults, group overrides, and
  explicit session values.
- Unit-test command construction for the resolved Codex flags.
- Unit-test projection into a populated Codex config, proving unrelated keys
  survive and stale Agent Deck-owned keys are removed when their root source is
  unset.
- Unit-test that Claude configuration is passed at launch and no
  `settings.json` write is attempted.

## Out of scope

- Copying or regenerating complete Codex or Claude homes.
- Managing credentials, session history, trust records, marketplaces, plugins,
  or arbitrary third-party configuration.
- Changing the semantics of manually selected per-session models.
