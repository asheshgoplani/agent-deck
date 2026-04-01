# Group-Scoped TUI

## Goal

Allow launching the AgentDeck TUI locked to a specific group, showing only that group's sessions and its children. Intended for automation workflows that open dedicated windows per group.

## CLI Interface

```
agent-deck --group work        # scoped TUI
agent-deck -g work             # short form
agent-deck --profile dev -g work  # combined with profile
```

The `--group` / `-g` flag only applies to the TUI launch path. It does not affect `add`, `launch`, or other subcommands (which already have their own `-g` flag for group assignment).

If the specified group does not exist, print an error to stderr and exit with code 2 (matching existing `ErrCodeNotFound` pattern).

## Approach: Setter Method on Home

Follow the existing setter pattern used for costs and web (`SetCostStore`, `SetCostPricer`, `SetWebMenuData`):

1. `main.go` extracts `--group` / `-g` flag alongside existing `--profile` / `-p` extraction
2. After constructing `homeModel`, call `homeModel.SetGroupScope(groupPath)` before `tea.NewProgram`
3. No changes to `NewHomeWithProfileAndMode` signature

## Home Struct Changes

New field on `Home`:

```go
groupScope string // When set, TUI is locked to this group + children
```

`SetGroupScope(path string)` stores the normalized path. Validation that the group exists happens at first load in the reload path.

## Scope Enforcement in rebuildFlatItems

After the existing `statusFilter` logic, apply group scope filter when `groupScope` is set:

- Keep items where `item.Path == groupScope` or `strings.HasPrefix(item.Path, groupScope+"/")`
- The scoped root group header is still shown for context
- Status filter composes with group scope (both apply independently)

## Scoped Hotkey Behavior

When `groupScope` is set, navigation and mutation hotkeys are restricted:

| Hotkey | Normal Behavior | Scoped Behavior |
|--------|----------------|-----------------|
| `n` (new session) | Group picker shows all groups | Group picker restricted to scoped root + children. If no children, auto-assign to scoped root. |
| `g` (create group) | Root/Subgroup toggle, any parent | Only scoped root as root, scoped children as subgroup parents |
| `M` (move session) | All groups as targets | Target list filtered to scoped root + children |
| `d` (delete group) | Any group | Only children within scope, not the scope root itself |

## New Session Dialog Scoping

- Group picker restricted to `groupScope` and its children
- If `groupScope` has no children, skip group picker and auto-assign to `groupScope`
- Default path for new sessions uses scoped group's `DefaultPath` if set

## Visual Indicator

- Title bar appends scoped group name in brackets: `Agent Deck [work]`
- Help overlay mentions scope is active and view is locked

No "exit scope" hotkey -- this is a locked mode. Close the window to exit.

## Edge Cases

- **Empty scope**: Show empty state within group context (e.g., "No sessions in work. Press n to create one.")
- **Session removed externally**: Disappears from view on next reload (consistent with existing external change handling)
- **Subgroup scope**: `agent-deck -g work/frontend` scopes to that subgroup and its children
- **Invalid group**: Exit with error code 2 and message to stderr
- **Group deleted while scoped**: Show empty state with message on next reload rather than crashing

## Testing

- **Unit**: `rebuildFlatItems` with `groupScope` set -- verify only scoped items appear, verify status filter composes with group scope
- **CLI**: `agent-deck -g nonexistent` exits with error code 2
- **Integration**: Create group, add sessions in and outside scope, launch scoped TUI, verify only scoped sessions visible

## Scope

- Upstream PR to `asheshgoplani/agent-deck`
- No new dependencies
- No config file changes
- Purely additive feature
