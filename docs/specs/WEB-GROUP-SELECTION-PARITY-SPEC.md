# Web UI: selectable groups, group stats panel, group-prefilled create

**Date:** 2026-08-22
**Status:** Approved
**Scope:** `internal/web` (one Go field + two routes), `internal/web/static/app` (frontend), `tests/web`

## Problem

The TUI lets the cursor rest on a **group** row. The web UI does not — clicking a
group only collapses it (`Sidebar.js:224-240`). Three things follow from that
gap, in ascending order of how much they hurt:

1. **No group stats.** In the TUI, putting the cursor on a group swaps the
   preview pane for a group summary (`📁 ws1`, `11 sessions`, a status
   breakdown). The web has no equivalent view at any altitude.
2. **No group context for "new session".** In the TUI, `n` on a group
   preselects that group and its configured folder. In the web, the dialog
   opens blank every time, so every new session means retyping the working
   directory.
3. **Every web-created session lands in `my-sessions`.**
   `CreateSessionDialog.js:126` omits `groupPath` from the POST body even
   though `CreateSessionRequest.GroupPath` (`api_types.go:24`), the handler
   (`handlers_sessions.go:79`) and the mutator (`web_mutator.go:109-113`) all
   support it. This is silent: the session is created, just in the wrong place.

(3) is a bug that exists independently of the UI work and is fixed here because
the same payload change fixes it.

## What the TUI actually does

Verified against source; two points differ from the intuitive reading.

### Selection is `(flatItems, cursor)`

There is no selection object. `Home.flatItems []session.Item` (`home.go:231`)
and `Home.cursor int` (`home.go:297`); "a group is selected" means
`flatItems[cursor].Type == session.ItemTypeGroup`. Group headers and session
rows share one flat list, and `j`/`k` (`home.go:8486`, `:8499`) step one index
without skipping headers.

**Selection and collapse are independent gestures.** `Tab`/`l`/`right`
(`home.go:8786`) and `Enter` on a group (`home.go:8723`) toggle
`Group.Expanded`; `h`/`left` collapses (`home.go:999`). Moving the cursor onto
a header toggles nothing. With a mouse, single click sets the cursor and
**double**-click toggles (`home.go:8345-8356`).

Collapse persists to SQLite (`saveGroupState`, `home.go:11655` →
`statedb.SaveGroups`, column `groups.expanded`), as does cursor position
(`uiState.CursorGroupPath`, `home.go:11719`).

### The stats pane is `renderGroupPreview` (`home.go:19382`)

`renderPreviewPane` dispatches at `home.go:17955-17957` when the cursor item is
a group. Body, in order:

| # | Content | Source |
|---|---|---|
| 1 | `📁 ` + `group.Name`, `ColorCyan` bold | `:19385-19390` |
| 2 | blank | |
| 3 | `fmt.Sprintf("%d sessions", len(group.Sessions))`, `ColorText` bold — never singularized | `:19393-19396` |
| 4 | blank | |
| 5 | status fragments joined by **two spaces**, zero counts omitted entirely | `:19418-19444` |
| 6 | optional `── Repository ──` worktree block | `:19447-19476` |
| 7 | `── Sessions ──` then rows `  {glyph} {title} {tool}` | `:19482-19521` |
| 8 | italic hint line joined by `" • "` | `:19523-19536` |

Status fragments, fixed order:

```
"● %d running"   U+25CF  ColorGreen
"◐ %d waiting"   U+25D0  ColorYellow
"○ %d idle"      U+25CB  ColorText
"■ %d stopped"   U+25A0  ColorTextDim
"✕ %d error"     U+2715  ColorRed
```

Three counting caveats, all of which the web port deliberately does not
reproduce (see "Divergences"):

- `StatusStarting` and `StatusQueued` fall through the switch **uncounted**
  (`home.go:19399-19416`), so fragments can sum to less than the headline.
- `len(group.Sessions)` is **direct members only** — no subgroup rollup. The
  TUI's own sidebar badge *does* roll up (`buildGroupRenderStats`,
  `home.go:16601`), so TUI sidebar and TUI preview already disagree.
- Archived sessions are counted; pruning happens only in `rebuildFlatItems`
  (`home.go:2470-2493`), never on `group.Sessions`.

### Prefill: `n` and `N` are different

**`n`** (`home.go:9271`) passes exactly five values into the dialog via
`ShowInGroup` (`newdialog.go:395`). Group path and name come from the cursor
item (`home.go:9354-9377`); the working directory comes from
`getDefaultPathForGroup` (`:9378`). The **tool does not come from the group** —
`SetDefaultTool(resolveInitialTool(session.GetDefaultTool(), rememberedTool(...)))`
at `home.go:9352` reads global `[default_tool]` then the StateDB
`last_used_tool`.

**`N`** (quick create, `home.go:12325-12350`) is where group-derived tool
selection actually lives. With the cursor on a group header it uses the group's
default path, falling back to the most recent path in the group, and inherits
`Tool`, `Command`, `ToolOptionsJSON` and `GeminiYoloMode` from **the most
recently created session in that group**.

This spec adopts `N`'s tool inheritance inside `n`'s dialog, which is what
makes the web dialog feel like it "knows" the group.

## Web UI today

Preact 10 + `htm/preact` + `@preact/signals`, vendored ESM, no build step
(import map at `index.html:34-46`). `class` not `className`; components
interpolated `<${Comp} p=${x}/>`; test hooks are `data-testid`; every fetch goes
through `apiFetch` (enforced by `static_files_test.go:188`).

`selectedIdSignal` (`state.js:14`) holds a session id string or null and is
referenced by **13 modules**. Two of them — `RightRail.js:122` and
`AppShell.js:53` — fall back to `sessions[0]` when it is null, which becomes a
correctness problem the moment a group can be "selected" with no session.

`MenuGroup` on the wire (`session_data_service.go:49-56`) carries
`name / path / expanded / order / sessionCount` and nothing else.
`projectGroup` (`dataModel.js:67-77`) mirrors that.

## Design

### A. Server

One field on `MenuGroup` (`session_data_service.go:49`):

```go
DefaultPath string `json:"defaultPath,omitempty"`
```

Populated in `menu_snapshot_builder.go:47-53` from
`groupTree.ExplicitDefaultPathForGroup(item.Group.Path)` (`groups.go:1795`),
ignoring the `bool` and letting `omitempty` drop the key when unset.

**Why explicit-only, not `DefaultPathForGroup`.** The doc comment at
`groups.go:1784-1794` states the problem directly: `DefaultPathForGroup`
collapses "the user configured this group's default_path" and "nothing is
configured, here's a guess from the group's sessions" into one indistinguishable
string. The client already has to inspect the group's newest session to derive
the tool, so it can derive the path fallback from that same session. One rule,
one place, and the wire field means exactly what its name says.

**No server-side `os.Stat`.** The TUI drops stale paths at `home.go:4318-4322`.
Doing that in `BuildMenuSnapshot` would cost one stat per group on every
`/api/menu` call *and* every SSE `menu` event. The value feeds an editable text
input; a nonexistent directory surfaces as a create error.

`GET /api/groups` (`handlers_groups.go:28-36`) and `GET /api/sessions`
re-serialize the same pointers and need no change.

Routing for §H: `mux.HandleFunc("/g/", s.handleIndex)` next to `server.go:218`,
and `/g/` added to the SPA-prefix check at `static_files.go:60`.

### B. Selection state

New signal in `state.js`, beside the existing one:

```js
// Selected group path, or null. Mutually exclusive with selectedIdSignal:
// exactly one of the two is non-null at any time (or both null at boot).
export const selectedGroupSignal = signal(null)

export function selectSession(id) { selectedGroupSignal.value = null; selectedIdSignal.value = id }
export function selectGroup(path) { selectedIdSignal.value = null; selectedGroupSignal.value = path }
```

**Not** a unified `selectionSignal = {kind, id}`. That is cleaner in the
abstract but rewrites all 13 `selectedIdSignal` modules, `App.js` routing and
`main.js:162-176` for no user-visible difference. Two signals plus two setters
put the invariant in one testable place; the nine existing writers
(`Sidebar.js:166`, `FleetPane.js:163`, `SearchPane.js:26`,
`CommandPalette.js:50`, `AppShell.js:224`, `AppShell.js:284`,
`ArchivedPane.js:41`, `Sidebar.js:52`, `main.js`) switch to `selectSession`.

`createSessionDialogSignal` (`state.js:81`) changes from `signal(false)` to
`signal(null)` carrying:

```js
// null (closed) or { groupPath, groupName, defaultPath, tool, modelId }
```

mirroring `editSessionDialogSignal`'s shape (`state.js:93`). The five open sites
(`Sidebar.js:201`, `EmptyStateDashboard.js:39`, `CommandPalette.js:40`,
`AppShell.js:89`, `AppShell.js:288`) and `closeAllModals` (`AppShell.js:232`)
update with it. Opening with no group context passes `{}`.

### C. One rendered-rows model

`dataModel.js` gains `sidebarRowsSignal`, a `computed` producing the sidebar's
**rendered order**:

```js
[{ type: 'group', group }, { type: 'session', session }, ...]
```

honoring collapse state, the text filter and the status chips. `Sidebar`
renders from it; keyboard nav walks it.

This is a bug fix, not only plumbing. `moveFocus` (`AppShell.js:212-226`)
currently walks `menuModelSignal.value.sessions` — flat, unfiltered,
group-agnostic — so `j`/`k` today can land on a session hidden inside a
collapsed group or excluded by an active filter.

Lifting it requires moving two pieces of state out of `Sidebar`'s closure into
signals in `uiState.js`:

- `sidebarFilterSignal` — the `/ filter` text, currently `useState`
  (`Sidebar.js:144`).
- `groupExpandedSignal` — the collapse map, currently `useState`
  (`Sidebar.js:146`), **persisted to `localStorage` under
  `agentdeck.groupExpanded`** via the existing `persist()` helper
  (`uiState.js:18`).

Persistence is in scope because we are adding keyboard collapse and because the
TUI persists it. It also fixes a dead code path: the lazy seed at
`Sidebar.js:146` reads `g.expanded` before `/api/menu` resolves, so the server's
value never applies (documented at `Sidebar.js:161-164`, pinned by
`sidebar-chrome.spec.js:110-131`). New resolution order:

```
localStorage entry for path  →  server g.expanded  →  open
```

`dataModel.js` also gains a `groupStats(path)` helper returning
`{ total, fragments: [{ id, glyph, count, label }] }` over **direct members** of
that path, in the TUI's fixed order, omitting zero counts.

`projectGroup` passes `defaultPath` through. `MenuItem.level` is already on the
wire (`menu_snapshot_builder.go:36`) but dropped by `projectGroup`; it is passed
through too, so nested groups can be indented like the TUI tree.

### D. Sidebar

The group head splits into two hit targets:

```js
<div class=${`side-group-head ${g.kind || ''} ${selectedGroup === g.path ? 'sel' : ''}`}
     data-testid=${`group-head-${g.path}`}
     onClick=${() => selectGroup(g.path)}>
  <button class="chev" data-testid=${`group-chev-${g.path}`}
          onClick=${e => { e.stopPropagation(); toggleGroup(g.path) }}>${open ? '▾' : '▸'}</button>
  <span class="name">${g.label}</span>
  <span class="badge">(${members.length})</span>
</div>
```

`data-testid="group-head-{path}"` is preserved so existing selectors keep
resolving. `.side-group-head.sel` in `app.css` mirrors the established
`.sess.sel` treatment at `app.css:201-205` (`--accent-soft` background, 2px
`--accent` left border, `--text-hi` text) — the group head has no `border-left`
today, so it gains one plus matching padding compensation.

Rows get `tabindex="-1"` and `aria-selected`, and the sidebar scrolls the
selection into view on keyboard movement (`scrollIntoView({block:'nearest'})`).

The `+` button (`Sidebar.js:201`) passes the current group's context.

The badge stays `members.length` (filtered, direct members) — unchanged
behavior, and consistent with the stats panel's counting.

### E. Group stats panel

New `internal/web/static/app/GroupStatsPanel.js`, branched in at
`TerminalPanel.js:417`, which is already the post-hooks early-return point:

```js
if (selectedGroupSignal.value) return html`<${GroupStatsPanel} path=${selectedGroupSignal.value}/>`
if (!sessionId) return html`<${EmptyStateDashboard} />`
```

`TerminalPane` stays mounted and CSS-hidden inside `Panes` (`AppShell.js:103`),
so selecting a group and returning to a session does not reconnect the
WebSocket or lose scrollback. No new tab, no `activeTabSignal` change.

Content ports `renderGroupPreview`: `📁 name` header, `N sessions`, the ordered
status fragments, then the session list as `glyph title tool` where clicking a
row calls `selectSession(id)`. The Repository/worktree block (`home.go:19447`)
is **out of scope** — `MenuSession.worktreeRepoRoot` is on the wire but the
per-branch dirty state is not, and a half-populated block is worse than none.
The TUI's hint footer is replaced by a single **New session in this group**
button. The TUI also offers rename / delete / subgroup there; those stay out of
scope (see below) so this panel does not drag in dialog wiring.

`WorkHead` (`AppShell.js:52-88`) gains a group branch rendering the breadcrumb
and a "New" button bound to the group. Both `WorkHead` and `RightRail`
(`RightRail.js:120-123`) must stop falling back to `sessions[0]` when
`selectedGroupSignal` is set — otherwise an unrelated session's Stop/Restart
controls render above the group stats.

#### Divergences from the TUI, deliberate

1. **`starting` folds into running, `queued` into idle.** The TUI's five-bucket
   switch (`home.go:19399-19416`) drops both, so its fragments can sum to less
   than its own `N sessions` headline. The web fragments always sum to the
   headline.
2. **Archived sessions are excluded.** The web snapshot already excludes them
   (`session_data_service.go:264`); the TUI preview counts them.
3. **No subgroup rollup** — direct members only, matching the TUI *preview*.
   Note `MenuGroup.sessionCount` from the server *does* roll up
   (`SessionCountForGroup`, `groups.go:1466`), so the panel must count from
   `byGroup[path]`, not from `g.sessionCount`.
4. **No Repository block**, per above.

### F. Create dialog

`CreateSessionDialog` reads the payload object:

| Field | Source |
|---|---|
| GROUP (read-only row) | `groupName` / `groupPath` |
| WORKING DIR | `defaultPath` → else newest-in-group session's `path` |
| TOOL | newest-in-group session's `tool` → else current `'claude'` default |
| MODEL ID | newest-in-group session's `modelId`, only if present in `MODEL_ID_CATALOG[tool]` |
| TITLE | empty |

"Newest in group" means max `createdAt` among `byGroup[groupPath]`, mirroring
`home.go:12332-12340`. Resolved by a pure exported helper
(`groupCreateDefaults(groupPath, model)`) so it is unit-testable without
mounting the dialog.

Seeding uses a `seededFor` guard modeled on `EditSessionDialog.js:64-78`, keyed
on `groupPath`, so SSE-driven re-renders do not stomp in-progress edits and a
reopen on a different group re-seeds.

The POST body (`CreateSessionDialog.js:126-130`) gains `groupPath` when
non-empty. **This is the fix for sessions silently landing in `my-sessions`.**

Model prefill caveat, worth a code comment: `resolveClaudeLaunchModel`
(`internal/session/claude.go:611`) treats a dialog-supplied model as an explicit
per-session override that outranks group/global config. Prefilling a model from
a sibling session therefore pins it. This is why the model is prefilled only
when it is a recognized catalog id, and why it remains user-clearable to
"Tool default".

### G. Keyboard

In the single `useEffect` at `AppShell.js:208-337`:

| Key | Behavior |
|---|---|
| `j` / `ArrowDown` | next row in `sidebarRowsSignal` (groups included) |
| `k` / `ArrowUp` | previous row |
| `ArrowLeft` / `h` | collapse focused group |
| `ArrowRight` / `Tab` | expand focused group |
| `Enter` | session → open in terminal; group → toggle collapse |
| `n` | new session, prefilled from the current selection |

`n` resolves its group as: selected group → else selected session's group →
else none. All bindings stay behind the existing `inField` guard
(`AppShell.js:243`). `Tab` needs `preventDefault` to avoid moving browser focus.

`KeyboardShortcuts.js:11-26` `BINDINGS` is hand-maintained documentation and is
updated in the same commit.

### H. Routing

`/g/{encodeURIComponent(path)}` alongside `/s/{id}` in `App.js:11-42` and
`main.js:applyRouteSelection`. `encodeURIComponent('work/innotrade')` yields
`work%2Finnotrade`, which survives the existing no-slash guard unchanged and
decodes back correctly.

## Testing

TDD: test first for every unit below.

### Go

- `internal/web/menu_snapshot_builder_test.go` — `defaultPath` populated from an
  explicit `GroupData.DefaultPath`; key **absent** (`omitempty`) when the group
  has no explicit default even if it has sessions (guards against someone
  "fixing" it back to `DefaultPathForGroup`).
- `internal/web/handlers_sessions_test.go` — `POST /api/sessions` with
  `groupPath` lands the instance in that group. The handler path exists at
  `handlers_sessions.go:79` but the web client has never exercised it.
- `internal/web/server_test.go:165` — update the pinned `agentdeck-shell-v8`
  literal.
- `tests/web/fixtures/cmd/web-fixture/main.go:203-207` — seed `DefaultPath` on
  `work` and `work/innotrade`, leave `personal` without one so the
  newest-session fallback is exercised. Note the file's own warning: it is the
  visual contract baseline.

### Vitest (`tests/web/unit/`)

- `groupSelection.test.js` — `selectSession`/`selectGroup` mutual exclusion;
  `closeAllModals` clears the new dialog shape.
- `groupStats.test.js` — fixed fragment order; zero buckets omitted;
  `starting`→running and `queued`→idle; fragments sum to the headline;
  `1 sessions` not singularized (TUI-faithful).
- `sidebarRows.test.js` — rendered order under collapse, text filter and status
  chips; a collapsed group's members are absent.
- `createSessionPrefill.test.js` — `groupCreateDefaults` picks the newest by
  `createdAt`; `defaultPath` wins over the session path; unknown `modelId` is
  not prefilled; payload includes `groupPath` and still omits empty
  `modelId`/`reasoningEffort`.
- extend `dataModel.test.js` — `defaultPath` and `level` passthrough.

### Playwright (`tests/web/e2e/`)

- **`sidebar-chrome.spec.js:110-131` asserts the current click-collapses
  contract and will fail.** It is rewritten for the split hit target, not
  deleted.
- new `group-selection.spec.js` — clicking a group name selects it and renders
  the stats pane with `📁`, `N sessions` and the fragments; clicking the chevron
  collapses without selecting; selecting a session clears the group selection;
  reload restores via `/g/`.
- new/extended create coverage — `n` with a group selected opens the dialog with
  GROUP and WORKING DIR prefilled from the fixture; submit posts `groupPath`.
- `keyboard-parity.spec.js` — arrow/Tab/Enter rows.
- Visual baselines change (new `.sel` state, new panel): rerun
  `npm run test:e2e:update-snapshots` deliberately and review the diff.

### Bookkeeping CI enforces

- `tests/web/PARITY_MATRIX.md` — add rows for group selection, group stats and
  group-scoped create.
- `tests/web/e2e/parity-actions.spec.js:21,31` — `EXPECTED_ACTION_ROWS` (51) and
  `EXPECTED_PROBEABLE_MISSING` (5) are pinned and must move in the same commit.
- `internal/web/static/sw.js:1` — `CACHE_VERSION` `agentdeck-shell-v8` →
  `v9`, together with `server_test.go:165`. `handleShellAsset`
  (`sw.js:182-202`) is cache-first and `activate` (`:21`) only evicts on a
  version change, so without this users keep running the old JS.
- `make css` only if a Tailwind class in `styles.src.css`'s `@source` scope
  changes; `app.css` edits do not require it.

### Commands

```bash
go test ./internal/web/ -race -count=1
go test -race ./...
make test-web-install     # once
make test-web-unit
make test-web-e2e
make ci                   # pre-push gate
```

## Out of scope

- Persisting group collapse **server-side**. `PATCH /api/groups/{path}` accepts
  only `{name}` (`handlers_groups.go:88-107`); localStorage is the fix here.
- Real per-group tool/model defaults. Would touch four storage mirrors
  (`groups.go:78`, `storage.go:183`, `statedb.go:270`+`:425`, `migrate.go:64`)
  plus CLI and TUI surfaces to set them.
- The Repository/worktree block in the stats panel (data not on the wire).
- Group rename / delete / create-subgroup from the stats panel. The API
  endpoints exist (`PATCH`/`DELETE /api/groups/{path}`, `POST /api/groups`) and
  `GroupNameDialog.js` is already written and rendered (`AppShell.js:356`), but
  it is unreachable — nothing ever sets `groupNameDialogSignal` non-null.
  Wiring it is a natural follow-up, not part of this change.
- Moving a session between groups (`M` in the TUI) — still web-MISSING.
