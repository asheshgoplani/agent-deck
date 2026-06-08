# Standalone pin-sessions (upstream PR) — design

**Date:** 2026-06-08
**Goal:** Extract the pin-sessions feature (local commit `bbea0b87`) into a self-contained
change that applies onto **upstream `origin/main` (`asheshgoplani/agent-deck`, SchemaVersion 9)**,
which has neither the archive-sessions nor the group-view-mode features. Open as an issue + PR
to upstream.

## Why a rework, not a cherry-pick

`bbea0b87` sits on top of an unmerged local stack (archive, web-parity, mobile, menubar,
view-modes, quick-session auto-name). It depends on three things upstream does not have:

1. **Archive** — `IsArchived()`, the archived sort band, `InstanceData.ArchivedAt`,
   `InstanceRow.ArchivedAt`, the `archived_at` column.
2. **View modes** — `internal/session/group_view_mode.go` does not exist upstream.
3. **Schema chain** — the commit is schema **v12**, layered on unmerged v10 (archive) and
   v11 (auto-name). Upstream is **v9**.

A cherry-pick fails (context lines reference `ArchivedAt`, missing file, wrong version), so the
feature is hand-ported pin-only.

## Scope decision: pure 3-zone pin

Strip the archive band and the view-mode override entirely. Sort zones become:

```
0  pin-top      fixed at top, exempt from status/recency, ordered by Order alone
1  normal       existing actionable sort (status -> recency -> Order)
2  pin-bottom   fixed at bottom, exempt from status/recency, ordered by Order alone
```

When archive / view-modes later land upstream, the pin<->archive<->viewmode interactions
(archived-pinned sinks to the archived band; pin overrides the active/idle section split) get
re-integrated then — they are out of scope here.

## Changes

### Ports verbatim (pin lines only; skip archive neighbors)

| File | Change |
|---|---|
| `internal/session/instance.go` | `PinMode` type + `PinNone`/`PinTop`/`PinBottom` consts; `Instance.Pin PinMode` field with `json:"pin,omitempty"` |
| `internal/session/mutators.go` | `FieldPin = "pin"` const; add to `ValidMutableFields`; `SetField` case validating `""`/`top`/`bottom` with a `MutationError` otherwise |
| `internal/session/storage.go` | `InstanceData.Pin PinMode`; round-trip in `instanceToRow`, `LoadLite`, `LoadWithGroups`, `convertToInstances` (add only the `Pin` lines — upstream structs have no `ArchivedAt` neighbor) |
| `internal/ui/home.go` | `📌 ` prefix on pinned rows in `renderSessionItem`, prepended before the AutoName truncation budget |
| `internal/ui/edit_session_dialog.go` | Pin-position pills (`Off`/`Top`/`Bottom`); `pillLabels` field on `editField`; `renderLabelPills`; `pinCursorFor`; `fieldInitialValue` case |

### Reworked

**`internal/session/groups.go`**

- `pinZone(inst *Instance) int` — **3 zones, no archive branch**:
  `PinTop->0`, `PinBottom->2`, default `->1`.
- `stablePinPartition(insts []*Instance)` — `sort.SliceStable` by `pinZone`, preserving
  in-band order. Run on Flatten's local display slices for live re-order without restart.
- `SortInstancesByActionable` — wrap upstream's existing comparator: outermost key `pinZone`;
  zones 0 and 2 compare by `Order` only; zone 1 keeps the existing
  `actionablePriority -> LastAccessedAt -> Order` tiers.
- `Flatten` — after the existing parent/sub split, call `stablePinPartition(parentSessions)`
  and `stablePinPartition` on each `subSessionsByParent[parentID]`. Upstream `Flatten` already
  has both locals.

**`internal/statedb/statedb.go`**

- Bump `SchemaVersion 9 -> 10`.
- `InstanceRow.Pin string`.
- Add `pin TEXT NOT NULL DEFAULT ''` to the `CREATE TABLE instances` block, following
  upstream's column set (which has **no** `archived_at` / `auto_name` columns).
- Idempotent `ALTER TABLE instances ADD COLUMN pin TEXT NOT NULL DEFAULT ''` in
  `alterMigrations` and a versioned `if oldVer < 10 { ... }` migration case
  (duplicate-column-tolerant).
- Thread `pin` through `SaveInstance`, `saveInstancesOnce` (column list + placeholder + arg),
  and `LoadInstances` (column list + scan target). Placeholder counts adjust to upstream's
  leaner column set, not the commit's v12 set.

### Dropped

- `internal/session/group_view_mode.go` change — file absent upstream.
- Archive band (zone 3) and all `IsArchived()` checks.

## Tests

Adapt `internal/session/pin_test.go` from the commit, **dropping** archive-zone and
view-mode cases. Keep:

- 3-zone `SortInstancesByActionable` ordering (pin-top/normal/pin-bottom; Order-only within
  pin bands; status/recency within normal).
- `stablePinPartition` / live Flatten re-order.
- `SetField` pin validation (valid values + error on garbage).
- statedb round-trip (`SaveInstance` -> `LoadInstances` preserves `pin`; migration adds column).
- UI `pinCursorFor` mapping.

Verify: `go build ./...`, `go vet ./...`, and `-run`-filtered tests for `internal/session`,
`internal/statedb`, `internal/ui`. Per project memory, some `internal/ui` (zoxide) and
`internal/session` (JSONL+python3) tests are environment-flaky in this sandbox — reconcile any
failure against a clean `origin/main` baseline before attributing it to this change.

## Delivery

- **Workspace:** git worktree off `origin/main`, branch `feat/pin-sessions-standalone`.
- **Issue:** on `asheshgoplani/agent-deck`, framed around pinning long-running shell
  app-launcher sessions (e.g. `npm run dev`) that the actionable sort keeps reshuffling.
- **PR:** push branch to `DoozyX` fork; PR against upstream. **Code-only** — this design doc
  stays in the local checkout, off the PR branch.
