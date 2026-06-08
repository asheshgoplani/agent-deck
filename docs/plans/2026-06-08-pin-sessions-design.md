# Pin Sessions to Fixed Position (Top/Bottom of Group)

**Date:** 2026-06-08
**Status:** Design approved, pending spec review

## Problem

Long-running "app launcher" shell sessions (e.g. `npm run dev`, a local server) get
reshuffled in the TUI list as the actionable sort reorders sessions by status and
recency. Manually positioning them with K/J does not stick, because the manual `Order`
field is only the last tie-breaker in `SortInstancesByActionable()` — status priority and
recency override it the moment the session's state changes.

The user wants to anchor specific sessions to a fixed slot so they stop moving.

## Requirements

1. **Individual sessions** are pinnable (not whole groups, not auto-pin-all-shells).
2. **Per-session choice** of top or bottom.
3. **Fully fixed** — a pinned session is exempt from the status/recency sort; it stays in
   its slot regardless of whether it errors, waits, etc.
4. **Within its group** — the anchor is the top or bottom of the session's own group's
   session list, not the whole flat list.
5. **Respect filters** — pinning affects position only, not visibility. A pinned session
   that is filtered out (e.g. status filter) still hides.

A deliberate consequence of requirement 4: because pinning is *within-group*, it composes
cleanly with the existing view-mode partitioning (`PartitionByViewMode`,
`group_view_mode.go`), which reorders whole groups. The two systems operate at different
levels and do not conflict.

## Approach

Chosen: **explicit `Pin` field** on the session model.

Rejected alternatives:
- **Sentinel `Order` ranges** (negative = pin-top, `1<<30` = pin-bottom, mirroring the
  existing group-pinning trick at `groups.go:25-26`). Rejected: `MoveSessionUp/Down`
  (`groups.go:658-749`) reassigns every sibling's `Order` on each move, which would clobber
  the sentinels, and the "fully fixed" semantics become muddy.
- **Group-only pin** (put app launchers in a pinned subgroup). Rejected: fails requirement
  1 and forces group reorganization. Remains available as a zero-code workaround today.

## Design

### Data model — `internal/session/instance.go`

Add a pin mode to the `Instance` struct (near `Order`/`GroupPath`, ~line 82-83):

```go
type PinMode string

const (
    PinNone   PinMode = ""        // default; empty so existing rows migrate cleanly
    PinTop    PinMode = "top"
    PinBottom PinMode = "bottom"
)

// On Instance:
Pin PinMode
```

### Sort — `SortInstancesByActionable()` (`internal/session/groups.go:133-153`)

Replace the current 4-tier comparison with a zoned sort. The outermost key is the pin zone,
then archived, then (for the normal zone only) the existing actionable tiers:

Zone ordering (top of list → bottom):
1. **Pin-top** sessions — sorted among themselves by `Order` only (fully fixed; status and
   recency ignored).
2. **Normal** (`PinNone`) sessions — current behaviour: status priority
   (error → waiting → running/starting → idle → stopped) → `LastAccessedAt` desc → `Order`.
3. **Pin-bottom** sessions — sorted among themselves by `Order` only.
4. **Archived** sessions — always last, regardless of pin. Archive is the stronger signal;
   a pinned session that is archived loses its anchored slot until unarchived.

Within the pin-top and pin-bottom zones, ordering by `Order` means K/J reordering
(`MoveSessionUp/Down`) continues to work *inside* a pin band, letting the user order
multiple pinned app launchers.

### Persistence — `internal/statedb/statedb.go` + `internal/session/storage.go`

- Add a `pin TEXT NOT NULL DEFAULT ''` column to the `instances` table
  (schema near `statedb.go:333-363`), with a migration for existing databases.
- Add `Pin` to `InstanceRow` (`statedb.go:131-171`) and round-trip it through
  `LoadInstances`/`SaveInstances` and the `InstanceRow` ↔ `Instance` conversions in
  `storage.go` (~`997-1051` save, `1153-1333` load).
- Empty string maps to `PinNone`, so no backfill of existing rows is needed beyond the
  column default.

### UI — `internal/ui/home.go`

- **Keybinding:** `P` on the selected session opens a small submenu with three actions:
  **Pin Top**, **Pin Bottom**, **Unpin** — consistent with the existing action-menu
  pattern. Selecting an action sets `Instance.Pin`, persists, and rebuilds the flat item
  list (`rebuildFlatItems`, `home.go:1690-1839`).
- **Marker:** a 📌 emoji prefix on any pinned session row, rendered in the list item.
  (Position conveys top vs bottom; the emoji conveys "this is pinned".)

### Scope notes

- Pin applies among **siblings**: top-level sessions pin among top-level sessions;
  sub-sessions (`ParentSessionID` set) pin within their parent's child list. Children always
  follow their parent — a parent's pin moves its children with it.
- Pinned sessions still obey status/archive filters (requirement 5).

## Testing

- **Sort zones:** table-driven tests for `SortInstancesByActionable()` covering pin-top /
  normal / pin-bottom / archived ordering, including a pinned session in an `error` state
  staying in its zone (proving "fully fixed"), and multiple pinned sessions ordered by
  `Order`.
- **Persistence round-trip:** pin value survives `SaveWithGroups` → `LoadWithGroups`;
  existing rows without the column default to `PinNone`.
- **View-mode composition:** pin-within-group holds while `GroupViewActiveTop` partitions
  groups (pin and partition do not interfere).
- **Filter interaction:** a pinned session hidden by a status filter does not appear.

## Out of scope

- Pinning whole groups (already exists for `conductor`/`my-sessions` via sentinels).
- Auto-pinning all shell sessions.
- Whole-list (cross-group) pin bands.
