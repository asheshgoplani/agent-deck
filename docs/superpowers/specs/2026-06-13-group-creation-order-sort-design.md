# Group Creation-Order Sort — Design

**Date:** 2026-06-13
**Status:** Approved (pending spec review)
**Branch:** `feature/group-creation-order-sort`

## Problem

Sessions displayed inside a group do not preserve their creation order. They
"randomly reorganize." Two distinct mechanisms cause this:

1. **Intentional automatic reordering (issue #857).** Within a group's *normal*
   band, `SortInstancesByActionable` (`internal/session/groups.go:175`) sorts by
   status priority, then `LastAccessedAt` (recency), using `Order` only as a
   final tie-breaker. So a session jumps position whenever its status changes or
   it is touched. To the user this reads as random shuffling.

2. **A genuine non-determinism bug.** In `Flatten()`
   (`internal/session/groups.go:619`), *orphaned* sub-sessions (sub-sessions
   whose parent session lives in a different group) are bucketed into a Go
   `map[string][]*Instance` and emitted in **map iteration order**, which Go
   randomizes on every render. Those rows genuinely shuffle between renders even
   with no state change.

## Goal

Sessions inside a group display in **creation order by default**, with the
existing K/J manual reorder still able to override and persist that order. Make
the automatic actionable sort opt-in via config. Eliminate the orphan
sub-session non-determinism entirely.

## Key facts (verified)

- `Order` **is** creation order. New sessions get `inst.Order = len(group.Sessions)`
  at insertion (`groups.go:923`, `groups.go:1345`) — a monotonic position.
- K/J manual reorder rewrites `Order` (`groups.go:766,813,831,891`) and it
  persists through the `sort_order` column (`statedb` `LoadInstances ... ORDER BY
  sort_order`). So **sorting the normal band by `Order` alone yields creation
  order by default and the user's manual order after K/J.**
- `SortInstancesByActionable` is called only at tree construction, in two places:
  `NewGroupTree` (`groups.go:242`) and `NewGroupTreeWithGroups` (`groups.go:312`).
- Pin zones (pin-top / pin-bottom) and Maestro are an outer sort band
  (`pinZone`, `groups.go:119`) applied in both the load-time sort and the live
  `stablePinPartition` in `Flatten()`. These are explicit user actions and are
  **out of scope** — they keep surfacing in both modes.

## Decisions

- **Config toggle, default creation order.** Keep the actionable sort available
  for users who prefer it (preserves the #857 feature). Default is creation order.
- **Cached package-level mode** for wiring (not an explicit constructor
  parameter), to avoid churning the ~6 constructor call sites and their tests.
- **Pins / Maestro keep surfacing** in both modes.

## Design

### 1. Config field

Add to `UserConfig` (`internal/session/userconfig.go`):

```go
// GroupSort controls the order of sessions within a group.
//   "creation"   (default) — fixed creation order; honors K/J manual reorder.
//   "actionable"           — issue #857 status→recency→Order surfacing.
// Empty or unrecognized values normalize to "creation".
GroupSort string `toml:"group_sort"`
```

Accessor:

```go
func (c *UserConfig) GetGroupSort() string {
    switch c.GroupSort {
    case "actionable":
        return "actionable"
    default:
        return "creation"
    }
}
```

`config.toml` example documentation: `group_sort = "creation"  # or "actionable"`.

### 2. Cached mode in the `session` package

A package-level, concurrency-safe cache so the sort can read the mode without a
disk hit and without changing constructor signatures:

```go
// groupSortMode caches the active group-sort mode ("creation"|"actionable").
// Defaults to "creation" until SetGroupSortMode is called.
var groupSortMode atomic.Value // string

func SetGroupSortMode(mode string) {
    if mode != "actionable" {
        mode = "creation"
    }
    groupSortMode.Store(mode)
}

func currentGroupSortMode() string {
    if v, ok := groupSortMode.Load().(string); ok && v != "" {
        return v
    }
    return "creation"
}
```

Callers of `SetGroupSortMode(cfg.GetGroupSort())`:
- UI on config load **and** on reload (`internal/ui/home.go`, where the tree is
  rebuilt at `~4223`/`4236` and wherever config is (re)loaded).
- Web snapshot builder before it builds the tree
  (`internal/web/menu_snapshot_builder.go:15`).

Tests set the mode directly via `SetGroupSortMode`.

### 3. Normal-band sort change

Refactor the normal-band branch of `SortInstancesByActionable`
(`groups.go:189-198`). Outer keys (pin zone, and Order within
maestro/pin-top/pin-bottom bands) are unchanged. Only the *normal* band differs:

```go
// Normal (1) band:
if currentGroupSortMode() == "actionable" {
    pi, pj := actionablePriority(insts[i].Status), actionablePriority(insts[j].Status)
    if pi != pj {
        return pi < pj
    }
    ai, aj := insts[i].LastAccessedAt, insts[j].LastAccessedAt
    if !ai.Equal(aj) {
        return ai.After(aj)
    }
}
// creation mode (default) and actionable tie-breaker: Order only.
return insts[i].Order < insts[j].Order
```

`stablePinPartition` (used live in `Flatten`) is unchanged — it already leaves
the normal band in its load-time order, which is now creation order.

### 4. Orphan sub-session determinism fix (both modes)

In `Flatten()`, replace the random map-iteration tail
(`for _, subs := range subSessionsByParent { ... }`, `groups.go:618-630`) with a
deterministic pass: collect the remaining orphan sub-sessions into a single
slice and `sort.SliceStable` by `Order` before appending them as top-level
items. This is a strict bug fix and applies regardless of `group_sort` mode.

The `topLevelCount` pre-count loop earlier in `Flatten` already counts these
orphans, so `IsLastInGroup` math is unaffected.

## Components touched

| File | Change |
|------|--------|
| `internal/session/userconfig.go` | `GroupSort` field + `GetGroupSort()` |
| `internal/session/groups.go` | cached mode (`SetGroupSortMode`/`currentGroupSortMode`); normal-band sort branch; deterministic orphan emission in `Flatten` |
| `internal/ui/home.go` | call `SetGroupSortMode` on config load/reload |
| `internal/web/menu_snapshot_builder.go` | call `SetGroupSortMode` before building tree |

## Testing

- **Default → creation order:** with no config (mode unset/`creation`), a group
  of sessions with shuffled statuses and `LastAccessedAt` renders strictly by
  `Order`.
- **Orphan determinism:** a group containing orphaned sub-sessions returns the
  same `Flatten()` order across many repeated calls (guards against map-order
  regression); assert in both modes.
- **Actionable preserved:** existing `internal/ui/issue857_sort_actionable_test.go`
  and related tests set `SetGroupSortMode("actionable")` in setup and continue to
  pass unchanged.
- **K/J survives:** after a manual reorder (which rewrites `Order`), creation
  mode renders in the new manual order, and it persists across a tree rebuild.
- **Pins/Maestro:** pin-top/pin-bottom/maestro still surface in creation mode.

## Out of scope

- The K/J manual reorder mechanism itself (already implemented; only relied upon).
- Pin and Maestro behavior (unchanged).
- Group-level ordering (`GroupList`) — only *within-group* session ordering changes.
