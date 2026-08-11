# Orchestrate Tool Strategy Design

## Motivation

Agent Deck already exposes `default_tool` for ordinary session creation, but
the orchestrate workflow chooses tools through hardcoded launch commands. A
user should be able to make Codex their ordinary default while allowing an
orchestrator to mix the locally available tools when a task or role benefits
from a different one.

The configuration must reuse Agent Deck's existing tool inventory. Users
should not have to maintain a second list of tools for orchestration.

## Decisions

Add one orchestration policy setting with two accepted values:

```toml
default_tool = "codex"

[orchestrate]
tool_strategy = "auto" # "default" or "auto"
```

- `default` launches every orchestrated child with the global `default_tool`.
- `auto` lets the active orchestrator select a tool for each child according
  to the role, task, required capabilities, and locally available tools.
- When `auto` has no meaningful reason to prefer another tool, it uses
  `default_tool`.
- An explicit tool supplied for an individual launch remains authoritative.
- Omitting `[orchestrate]` preserves the workflow's current behavior so an
  upgrade does not silently change existing orchestration runs.

The requested initial user configuration is `default_tool = "codex"` with
`tool_strategy = "auto"`.

## Available Tool Discovery

Automatic selection uses the existing Agent Deck tool registry and its
installed-command detection:

- Built-in and locally configured custom tools are candidates when their
  commands resolve on the host.
- Tools hidden by the existing UI tool configuration are not candidates.
- No orchestration-specific allowlist or duplicate tool catalog is added.
- If only one suitable tool is available, automatic selection uses it for all
  children.
- If the configured default is unavailable, automatic selection may choose a
  different detected tool and must make the fallback visible in its launch
  record or status output.

Command detection proves local installation, not provider authentication.
Provider-specific authentication probes are outside this design.

## Architecture

### Configuration model

Add `OrchestrateSettings` to `UserConfig` with a `tool_strategy` field. The
accepted values are `default` and `auto`. Invalid non-empty values produce a
clear configuration error rather than silently selecting a policy.

Expose a small resolver that returns the strategy, the global fallback tool,
and the locally available candidate tool names. The resolver reuses the tool
registry's installed filtering rather than duplicating command lookup logic.

### Workflow integration

Update the orchestrate skill and its helper scripts so launch instructions no
longer assume Claude unconditionally:

- Under `default`, generated launch commands use the resolved global default.
- Under `auto`, the conductor receives the detected candidates and fallback,
  selects a tool before each launch, and records the selection.
- Conductor rotation follows the same policy and preserves the run's resolved
  strategy rather than reverting to a hardcoded tool.
- Existing explicit cross-provider reviewer guidance remains an explicit
  choice and therefore continues to override the strategy.

The workflow must fail clearly when `default` selects a tool that is not
locally available. It must not silently switch tools in this mode.

### Interfaces and documentation

Document `[orchestrate].tool_strategy` in the configuration reference and add
an example showing Codex as the global default with automatic orchestration.
The orchestration workflow should display or persist enough resolved-policy
information for a user to understand why a child used a given tool.

## Verification

Automated tests cover:

- TOML parsing and serialization of both strategy values.
- Rejection of invalid strategy values.
- Backward-compatible behavior when the setting is absent.
- `default` resolution to `default_tool`.
- `auto` candidate discovery for installed built-ins and configured custom
  tools, including hidden-tool exclusion.
- Auto fallback when the preferred default is unavailable.
- Explicit per-launch tool precedence.
- Rotation and workflow launch instructions using the resolved strategy
  instead of a hardcoded tool.

Run the focused Go tests for configuration and tool-registry resolution, the
skill/helper shell tests, and the repository's standard formatting and test
gates appropriate to the touched packages.

## Out of Scope

- A separate orchestration tool allowlist.
- Per-role tool mappings in configuration.
- Model selection or model-tier policy.
- Provider quota, pricing, benchmark, or authentication checks.
- Changing ordinary session tool precedence.
- Migrating existing user configuration automatically.

## Principles Review

The design adds one settings structure and one resolver. It reuses the
existing registry to avoid duplicating installed-tool knowledge. A separate
mode field, per-role mappings, authentication adapters, and an orchestration
allowlist were excluded because no stated requirement needs them.
