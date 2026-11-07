# Agent Deck guide

[Back to the README](../../README.md)

## Documentation

**Onboarding** — five-minute walkthroughs for new users:

| Guide | What's Inside |
|-------|---------------|
| [Conductor](../../docs/conductor/) | Local quickstart, architecture, state files, lifecycle, remote channels, gotchas |
| [Watcher Setup](../../docs/WATCHER-SETUP.md) | Give your fleet ears: GitHub / Gmail / ntfy / Slack / calendar event forwarding |

**User guides** — reference material for going deeper:

| Guide | What's Inside |
|-------|---------------|
| [Skills](../../documentation/SKILLS.md) | User-level vs pool skills, authoring, attach/detach, when to use which tier |
| [Watchdog](../../documentation/WATCHDOG.md) | Optional Python daemon that auto-restarts critical sessions and nudges stuck children |
| [Watchers](../../documentation/WATCHERS.md) | Event-forwarding framework: doorbell model, built-in adapters, custom watchers, gotchas |
| [Plug in any machine](../../docs/PLUG-IN-ANY-MACHINE.md) | Sandbox providers: make a fresh machine (Docker, your VMs) per session, the JSON contract, security |

**References** — drill into specifics:

| Guide | What's Inside |
|-------|---------------|
| [CLI Reference](../../skills/agent-deck/references/cli-reference.md) | Commands, flags, scripting examples |
| [Configuration](../../skills/agent-deck/references/config-reference.md) | config.toml, MCP setup, custom tools, socket pool, skills registry paths, docker |
| [Docker Sandbox](../../skills/agent-deck/references/sandbox.md) | Containers, overlays, custom images, troubleshooting |
| [TUI Reference](../../skills/agent-deck/references/tui-reference.md) | Keyboard shortcuts, status indicators, navigation |
| [Troubleshooting](../../skills/agent-deck/references/troubleshooting.md) | Common issues, debugging, recovery, uninstalling |
| [Capability Checklist](../../docs/verification/README.md) | User-level verification matrix: every capability exercised against the real binary, per surface |
| [Performance](../../docs/performance.md) | How fast each operation is, targets, how it is measured, `agent-deck perf` |
| [Architecture Diagrams](../../docs/conductor/assets/) | D2 sources + rendered SVGs for the conductor, channels and fleet diagrams (session lifecycle: [docs/diagrams/](../../docs/diagrams/)) |

Additional resources:
- [CONTRIBUTING.md](../../CONTRIBUTING.md) — how to contribute; [Architecture](../../docs/architecture.md) — where the code lives; [AGENTS.md](../../AGENTS.md) — contributing with an agent
- [CHANGELOG.md](../../CHANGELOG.md) — release history
- [llms-full.txt](../../llms-full.txt) — full context for LLMs
