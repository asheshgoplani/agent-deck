# Dynamic Alternate Quick-Create Design

**Date:** 2026-08-09
**Status:** Approved

## Motivation

Agent Deck's existing quick-create action is intentionally contextual: it
inherits the highlighted session's tool, or the most recent tool in the
highlighted group, and falls back to `default_tool`. Users who regularly move
between two agents need a second one-keystroke path that launches the other
agent without opening the new-session dialog.

## Decisions

- Keep the existing `quick_create` action and its contextual tool resolution
  unchanged.
- Add an optional `[quick_create].alternate_tool` setting.
- Add an opt-in `quick_create_alternate` hotkey action with no default binding.
- Resolve the normal contextual tool first. The alternate action chooses:
  - `default_tool` when the contextual tool equals `alternate_tool`;
  - `alternate_tool` otherwise.
- Treat an empty `default_tool` as `claude`, matching the existing quick-create
  fallback.
- Reuse the existing contextual project path, group, generated name, and
  session-creation pipeline. Only the selected tool changes.
- Show a clear error when the alternate action is invoked without a configured
  alternate tool.

Example configuration:

```toml
default_tool = "claude"

[quick_create]
alternate_tool = "codex"

[hotkeys]
quick_create_alternate = "ctrl+n"
```

With that configuration, normal quick-create remains contextual. Alternate
quick-create launches Codex from Claude or unrelated-tool contexts and launches
Claude from a Codex context. Neither action opens a dialog.

## Architecture

The session config owns the alternate-tool value. The UI extracts the existing
context resolution in `quickCreateSession` into a shared resolver. Both
quick-create actions use that resolver; the alternate path replaces only the
resolved tool and clears inherited tool-specific command/options before calling
the existing creator.

The hotkey registry, normalization, help overlay, and footer already render
configured actions dynamically. The new action follows those existing paths and
ships unbound so current navigation keys and configurations do not change.

## Interfaces

New configuration field:

```toml
[quick_create]
alternate_tool = "codex"
```

New hotkey action:

```toml
[hotkeys]
quick_create_alternate = "ctrl+n"
```

Resolution examples when `default_tool = "claude"` and the alternate is Codex:

| Contextual tool | Normal quick-create | Alternate quick-create |
| --- | --- | --- |
| Claude | Claude | Codex |
| Codex | Codex | Claude |
| Gemini | Gemini | Codex |
| No prior context | Claude | Codex |

## Compatibility

- Configurations without `[quick_create].alternate_tool` retain current
  quick-create behavior.
- `quick_create_alternate` is unbound by default.
- Binding `ctrl+n` intentionally replaces its overview-list move-down action;
  Down Arrow and `j` remain available.
- Existing `default_tool` behavior remains unchanged outside alternate
  resolution.

## Out of Scope

- More than one alternate tool
- Arbitrary hotkey-to-command mappings
- Tool-specific actions such as `quick_create_codex`
- Changing the new-session dialog
- Alternate quick-create for remote sessions

## Verification

- Configuration parsing tests cover an absent and configured alternate tool.
- Resolver tests cover Claude, Codex, unrelated-tool, and fallback contexts.
- Hotkey tests cover the opt-in action and `ctrl+n` normalization.
- Dispatch tests verify alternate creation bypasses the dialog and selects the
  resolved opposite tool.
- Existing quick-create tests and the broader UI/session suites remain green.
