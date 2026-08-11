# Inferred Alternate Quick-Create Design

**Date:** 2026-08-11
**Status:** Approved

## Motivation

Alternate quick-create should not require users to repeat tool selection in
configuration. Agent Deck already owns the default tool, the picker order,
tool visibility, and installed-command discovery, so it has enough information
to infer the other tool in a primary/alternate pair.

## Decisions

- Keep normal contextual quick-create unchanged.
- Keep `quick_create_alternate` opt-in and local-only.
- Infer the primary tool from `default_tool`, falling back to Claude when it is
  unset.
- Infer the alternate as the first visible, installed, non-shell tool in the
  existing picker order that differs from the primary.
- When the contextual tool equals the inferred alternate, alternate
  quick-create launches the primary. In every other context it launches the
  inferred alternate.
- Respect `[ui].hidden_tools` and include configured custom tools after the
  built-in picker entries.
- Check installation for inference even when
  `[ui].show_only_installed_tools = false`, so the shortcut never chooses an
  unavailable command.
- Do not add a configurable tool order or another tool inventory.

Examples:

| Default | Installed and visible tools | Inferred alternate |
| --- | --- | --- |
| Claude | Claude, Codex | Codex |
| Codex | Claude, Codex | Claude |
| Codex | Claude, Gemini, Codex | Claude |
| Claude | Claude only | Error: no alternate available |

## Configuration

`[quick_create].alternate_tool` is no longer an active setting. The minimal
configuration is:

```toml
default_tool = "codex"

[hotkeys]
quick_create_alternate = "ctrl+n"
```

Legacy `alternate_tool` entries remain parseable for configuration
compatibility but are ignored. Loading one emits a warning explaining that the
alternate is now inferred. Generated configuration and documentation no longer
advertise the field.

## Architecture

Add a session-level resolver that returns the primary and inferred alternate.
It reuses the registry's ordered picker inventory, visibility rules, and
command resolution rather than duplicating built-in lists or executable
lookup. Shell is excluded from candidacy.

Alternate quick-create resolves the existing contextual tool, asks the new
resolver for the pair, replaces only the selected tool, clears inherited
tool-specific command and options, and enters the existing session-creation
pipeline.

An unavailable configured default remains the primary. Alternate inference
does not silently redefine `default_tool`. If no installed and visible
alternate exists, the action returns a clear error and creates nothing.

## Compatibility

- Normal quick-create and the new-session dialog do not change.
- Existing alternate hotkey bindings continue to work.
- Legacy explicit alternate values no longer control selection and produce a
  warning.
- Alternate quick-create remains unsupported for remote sessions.

## Verification

Automated tests cover:

- Claude default with Codex inferred.
- Codex default with Claude inferred.
- Multiple installed tools following picker order.
- Hidden and unavailable tools being skipped.
- Configured custom tools participating after built-ins.
- Shell exclusion and the no-candidate error.
- Legacy `alternate_tool` being ignored with a warning.
- Existing normal contextual quick-create behavior remaining unchanged.

Run focused configuration, registry, hotkey, and quick-create tests, followed
by the repository's sandboxed Go test suite.

## Out of Scope

- User-configurable tool ordering
- More than one alternate or cycling through tools
- Authentication or provider-availability probes
- Remote alternate quick-create
- Changes to normal quick-create or the new-session dialog

## Principles Review

The resolver is the only new behavior-bearing component. It reuses the
registry as the source of truth for order, visibility, and installation. A new
ordering setting, migration command, authentication adapter, and multi-tool
cycler were excluded because no stated requirement needs them.
