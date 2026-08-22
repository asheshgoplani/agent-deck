# Web Group Selection Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make groups selectable in the web sidebar, show a group stats panel when one is selected, and prefill the new-session dialog from that group's context — matching the TUI.

**Architecture:** A new `selectedGroupSignal` sits beside the existing `selectedIdSignal` with a mutual-exclusion invariant enforced by two exported setters. The sidebar's filter text and collapse map move out of component-local `useState` into signals, which lets a single `sidebarRowsSignal` computed produce the exact rendered row order — the sidebar renders from it and keyboard navigation walks it. A new `GroupStatsPanel` takes over the main work area when a group is selected. The create dialog's open-signal changes from a boolean to a context object carrying group defaults, and its POST body finally includes `groupPath`.

**Tech Stack:** Go 1.x (`internal/web`), Preact 10 + `htm/preact` + `@preact/signals` (no build step, vendored ESM), Vitest + jsdom (unit), Playwright (e2e).

**Spec:** `docs/specs/WEB-GROUP-SELECTION-PARITY-SPEC.md`

## Global Constraints

- **No build step.** Frontend is vanilla ESM served from `internal/web/static/app/`. Use `html` tagged templates from `htm/preact`, `class` not `className`, `<${Comp} p=${x}/>` for components, `.map` with `key=` for lists.
- **All network calls go through `apiFetch`** from `./api.js`. A raw `fetch(` in `internal/web/static/app/**` fails `internal/web/static_files_test.go:188`.
- **Test hooks are `data-testid`** attributes.
- **Existing `data-testid` values must not change.** `group-head-{path}` is used by `tests/web/e2e/sidebar-chrome.spec.js`.
- **`sw.js` `CACHE_VERSION` must be bumped when any file under `internal/web/static/` changes**, together with the pinned literal at `internal/web/server_test.go:165`. Done once in Task 12.
- **Never call `session.ExplicitDefaultPathForGroup` or `DefaultPathForGroup` from `BuildMenuSnapshot`.** They run the uncached `resolveGroupDefaultPath` — an `os.Stat` plus up to three git subprocesses per group (~21ms each). See `internal/session/groups.go:1711-1731`.
- **Group collapse state is client-local.** There is no API to persist it; `PATCH /api/groups/{path}` accepts only `{name}` (`internal/web/handlers_groups.go:88-107`).
- Go tests run with `-race -count=1`.

---

## File Structure

**Created:**
- `internal/web/menu_snapshot_builder_test.go` — coverage for the new `MenuGroup.DefaultPath` field.
- `internal/web/static/app/GroupStatsPanel.js` — the group stats view. One responsibility: render stats for one group path.
- `tests/web/unit/groupSelection.test.js`
- `tests/web/unit/sidebarRows.test.js`
- `tests/web/unit/groupStats.test.js`
- `tests/web/unit/createSessionPrefill.test.js`
- `tests/web/e2e/group-selection.spec.js`

**Modified:**
- `internal/web/session_data_service.go` — one field on `MenuGroup`.
- `internal/web/menu_snapshot_builder.go` — populate it.
- `internal/web/server.go`, `internal/web/static_files.go` — `/g/` SPA route.
- `internal/web/server_test.go` — sw version pin.
- `tests/web/fixtures/cmd/web-fixture/main.go` — seed `DefaultPath`.
- `internal/web/static/app/state.js` — `selectedGroupSignal`, `selectSession`, `selectGroup`, dialog signal shape.
- `internal/web/static/app/uiState.js` — `sidebarFilterSignal`, `groupExpandedSignal`.
- `internal/web/static/app/dataModel.js` — group projection, `sidebarRowsSignal`, `groupStats`, `groupCreateDefaults`, `openCreateSessionForGroup`, `currentGroupPath`.
- `internal/web/static/app/Sidebar.js` — split hit target, selection, render from `sidebarRowsSignal`.
- `internal/web/static/app/TerminalPanel.js` — group branch.
- `internal/web/static/app/AppShell.js` — `WorkHead` group branch, keyboard, dialog signal.
- `internal/web/static/app/RightRail.js` — group branch.
- `internal/web/static/app/CreateSessionDialog.js` — seeding + `groupPath` in payload.
- `internal/web/static/app/CommandPalette.js`, `EmptyStateDashboard.js` — dialog signal shape.
- `internal/web/static/app/App.js`, `main.js` — `/g/` route sync.
- `internal/web/static/app/app.css` — `.side-group-head.sel`, `.group-stats`.
- `internal/web/static/app/KeyboardShortcuts.js` — BINDINGS doc table.
- `internal/web/static/sw.js` — `CACHE_VERSION`.
- `tests/web/e2e/sidebar-chrome.spec.js` — rewrite the group-collapse test.
- `tests/web/unit/dataModel.test.js` — new field passthrough.
- `tests/web/PARITY_MATRIX.md`, `tests/web/e2e/parity-actions.spec.js` — pinned counts.

---

### Task 1: Ship the group's configured folder to the browser

**Files:**
- Modify: `internal/web/session_data_service.go:49-56`
- Modify: `internal/web/menu_snapshot_builder.go:47-53`
- Modify: `tests/web/fixtures/cmd/web-fixture/main.go:203-207`
- Test: `internal/web/menu_snapshot_builder_test.go` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: JSON key `defaultPath` on every group object in `/api/menu`, `/api/groups` and the SSE `menu` payload. Absent when the group has no explicitly configured default path.

- [ ] **Step 1: Write the failing test**

Create `internal/web/menu_snapshot_builder_test.go`:

```go
package web

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestBuildMenuSnapshotGroupDefaultPath pins the wire contract for
// MenuGroup.DefaultPath: it carries the group's EXPLICITLY configured
// default_path and is omitted entirely when none is set.
//
// The "no explicit default" case matters: session.Group also supports a
// derived most-recent-session fallback (DefaultPathForGroup), and shipping
// that would make the web unable to tell "configured" from "guessed". The
// client derives its own fallback from the group's newest session.
func TestBuildMenuSnapshotGroupDefaultPath(t *testing.T) {
	dir := t.TempDir()

	inst := session.NewInstanceWithGroupAndTool("api", "/srv/api", "work", "claude")
	inst.ID = "sess-api"
	inst.GroupPath = "work"

	groups := []*session.GroupData{
		{Name: "work", Path: "work", Expanded: true, Order: 0, DefaultPath: dir},
		{Name: "personal", Path: "personal", Expanded: true, Order: 1},
	}

	snap := BuildMenuSnapshot("default", []*session.Instance{inst}, groups, time.Now())

	byPath := map[string]*MenuGroup{}
	for _, item := range snap.Items {
		if item.Type == MenuItemTypeGroup && item.Group != nil {
			byPath[item.Group.Path] = item.Group
		}
	}

	work := byPath["work"]
	if work == nil {
		t.Fatalf("group %q missing from snapshot", "work")
	}
	if work.DefaultPath != dir {
		t.Errorf("work.DefaultPath = %q, want %q", work.DefaultPath, dir)
	}

	personal := byPath["personal"]
	if personal == nil {
		t.Fatalf("group %q missing from snapshot", "personal")
	}
	if personal.DefaultPath != "" {
		t.Errorf("personal.DefaultPath = %q, want empty (no explicit default configured)", personal.DefaultPath)
	}

	// omitempty: the key must not appear at all for an unconfigured group,
	// so clients can distinguish "unset" from "empty string".
	blob, err := json.Marshal(personal)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(blob); contains(got, "defaultPath") {
		t.Errorf("unconfigured group serialized defaultPath key: %s", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	}()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run TestBuildMenuSnapshotGroupDefaultPath -race -count=1`
Expected: FAIL — `work.DefaultPath undefined (type *MenuGroup has no field or method DefaultPath)`

- [ ] **Step 3: Add the field**

In `internal/web/session_data_service.go`, add to `MenuGroup` (after `SessionCount`):

```go
// DefaultPath is the group's EXPLICITLY configured default_path — the
// folder new sessions in this group start in. Empty (and omitted) when
// the user has not configured one; the client then derives its own
// fallback from the group's newest session.
//
// Read straight off the field. BuildMenuSnapshot's tree comes from
// NewGroupTreeWithGroups, which already normalizes this through the
// CACHED resolver (internal/session/groups.go:316-318 ->
// updateGroupDefaultPath). Do NOT call ExplicitDefaultPathForGroup or
// DefaultPathForGroup here: they run the uncached resolveGroupDefaultPath
// (os.Stat + up to 3 git subprocesses, ~21ms per group) and this builder
// runs on every /api/menu request AND every SSE menu event. See the
// header comment at internal/session/groups.go:1711-1731.
DefaultPath string `json:"defaultPath,omitempty"`
```

- [ ] **Step 4: Populate it**

In `internal/web/menu_snapshot_builder.go`, inside the `&MenuGroup{...}` literal, add after `SessionCount`:

```go
					DefaultPath:  item.Group.DefaultPath,
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/web/ -run TestBuildMenuSnapshotGroupDefaultPath -race -count=1`
Expected: PASS

- [ ] **Step 6: Seed the e2e fixture**

In `tests/web/fixtures/cmd/web-fixture/main.go`, replace the `s.groups` literal:

```go
	s.groups = map[string]*web.MenuGroup{
		"work":           {Name: "work", Path: "work", Expanded: true, Order: 0, SessionCount: 2, DefaultPath: "/srv/work"},
		"work/innotrade": {Name: "innotrade", Path: "work/innotrade", Expanded: true, Order: 1, SessionCount: 1, DefaultPath: "/srv/innotrade"},
		"personal":       {Name: "personal", Path: "personal", Expanded: false, Order: 2, SessionCount: 1},
	}
```

`personal` deliberately has no `DefaultPath` so the client-side newest-session fallback is exercised by e2e.

- [ ] **Step 7: Run the package suite**

Run: `go test ./internal/web/ -race -count=1`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/web/session_data_service.go internal/web/menu_snapshot_builder.go \
        internal/web/menu_snapshot_builder_test.go tests/web/fixtures/cmd/web-fixture/main.go
git commit -m "feat(web): ship group defaultPath in the menu snapshot"
```

---

### Task 2: Pass group fields through the client data model

**Files:**
- Modify: `internal/web/static/app/dataModel.js:67-77` (`projectGroup`), `:98-103` (synthesized-group fallback)
- Test: `tests/web/unit/dataModel.test.js`

**Interfaces:**
- Consumes: `defaultPath` from Task 1.
- Produces: every object in `menuModelSignal.value.groups` now has `{ path, label, name, defaultPath, level, expanded, sessionCount, order, kind }`. `label` stays UPPERCASED for the sidebar; `name` is the raw display name for the stats panel header and the dialog's GROUP row.

- [ ] **Step 1: Write the failing test**

Append to `tests/web/unit/dataModel.test.js`:

```js
describe('menuModelSignal group projection', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal } = await import(stateModulePath)
    sessionsSignal.value = []
    sessionCostsSignal.value = {}
  })

  it('carries defaultPath, raw name and level through to the group model', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { menuModelSignal } = await import(dataModelModulePath)

    sessionsSignal.value = [
      { type: 'group', level: 0, group: { name: 'work', path: 'work', expanded: true, order: 0, sessionCount: 2, defaultPath: '/srv/work' } },
      { type: 'group', level: 1, group: { name: 'innotrade', path: 'work/innotrade', expanded: true, order: 1, sessionCount: 0 } },
    ]

    const byPath = new Map(menuModelSignal.value.groups.map((g) => [g.path, g]))

    expect(byPath.get('work').defaultPath).toBe('/srv/work')
    expect(byPath.get('work').name).toBe('work')
    expect(byPath.get('work').label).toBe('WORK')
    expect(byPath.get('work').level).toBe(0)

    // Unconfigured group: absent key becomes empty string, never undefined.
    expect(byPath.get('work/innotrade').defaultPath).toBe('')
    expect(byPath.get('work/innotrade').level).toBe(1)
  })

  it('gives synthesized groups the same shape as API-provided ones', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { menuModelSignal } = await import(dataModelModulePath)

    // A session whose group never appeared as a group item.
    sessionsSignal.value = [
      { type: 'session', session: { id: 's1', title: 'orphan', groupPath: 'ghost', tool: 'claude' } },
    ]

    const ghost = menuModelSignal.value.groups.find((g) => g.path === 'ghost')
    expect(ghost).toBeDefined()
    expect(ghost.name).toBe('ghost')
    expect(ghost.defaultPath).toBe('')
    expect(ghost.level).toBe(0)
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tests/web && npx vitest run unit/dataModel.test.js`
Expected: FAIL — `expected undefined to be '/srv/work'`

- [ ] **Step 3: Extend the projection**

In `internal/web/static/app/dataModel.js`, replace `projectGroup`:

```js
function projectGroup(item) {
  const g = item.group || {}
  const path = g.path || ''
  const name = g.name || path
  return {
    path,
    // label is the uppercased sidebar form; name is the raw display form
    // used by the stats panel header and the create dialog's GROUP row.
    label: name.toUpperCase(),
    name,
    // Explicitly configured folder for new sessions in this group. Empty
    // when unset — callers fall back to the group's newest session path.
    defaultPath: g.defaultPath || '',
    // Nesting depth from MenuItem.level ("work/innotrade" => 1).
    level: item.level || 0,
    expanded: !!g.expanded,
    sessionCount: g.sessionCount || 0,
    order: g.order || 0,
    kind: path === 'conductor' ? 'conductor' : path === 'watchers' ? 'watcher' : null,
  }
}
```

And in the synthesized-group fallback loop, replace the pushed literal:

```js
    if (s.group && !seen.has(s.group)) {
      groups.push({
        path: s.group,
        label: s.group.toUpperCase(),
        name: s.group,
        defaultPath: '',
        level: 0,
        expanded: true,
        sessionCount: 0,
        order: 999,
        kind: null,
      })
      seen.add(s.group)
    }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tests/web && npx vitest run unit/dataModel.test.js`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/web/static/app/dataModel.js tests/web/unit/dataModel.test.js
git commit -m "feat(web): project group defaultPath, name and level into the client model"
```

---

### Task 3: Selection state — a group can be selected

**Files:**
- Modify: `internal/web/static/app/state.js:14`
- Modify: `internal/web/static/app/Sidebar.js:52-57,166-169`, `AppShell.js:224,284`, `CommandPalette.js:50`, `panes/FleetPane.js:163-166`, `panes/SearchPane.js:26-29`, `panes/ArchivedPane.js:41-42`
- Test: `tests/web/unit/groupSelection.test.js` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: `selectedGroupSignal` (signal of group-path string or `null`), `selectSession(id)`, `selectGroup(path)` — all exported from `state.js`. Invariant: at most one of `selectedIdSignal` / `selectedGroupSignal` is non-null.

- [ ] **Step 1: Write the failing test**

Create `tests/web/unit/groupSelection.test.js`:

```js
import { beforeEach, describe, expect, it } from 'vitest'

const stateModulePath = '../../../internal/web/static/app/state.js'

describe('selection mutual exclusion', () => {
  beforeEach(async () => {
    const { selectedIdSignal, selectedGroupSignal } = await import(stateModulePath)
    selectedIdSignal.value = null
    selectedGroupSignal.value = null
  })

  it('selectGroup clears any selected session', async () => {
    const { selectedIdSignal, selectedGroupSignal, selectGroup } = await import(stateModulePath)

    selectedIdSignal.value = 'sess-001'
    selectGroup('work')

    expect(selectedGroupSignal.value).toBe('work')
    expect(selectedIdSignal.value).toBe(null)
  })

  it('selectSession clears any selected group', async () => {
    const { selectedIdSignal, selectedGroupSignal, selectGroup, selectSession } = await import(stateModulePath)

    selectGroup('work')
    selectSession('sess-002')

    expect(selectedIdSignal.value).toBe('sess-002')
    expect(selectedGroupSignal.value).toBe(null)
  })

  it('selectSession(null) clears both', async () => {
    const { selectedIdSignal, selectedGroupSignal, selectGroup, selectSession } = await import(stateModulePath)

    selectGroup('work')
    selectSession(null)

    expect(selectedIdSignal.value).toBe(null)
    expect(selectedGroupSignal.value).toBe(null)
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tests/web && npx vitest run unit/groupSelection.test.js`
Expected: FAIL — `selectGroup is not a function`

- [ ] **Step 3: Add the signal and setters**

In `internal/web/static/app/state.js`, immediately after the `selectedIdSignal` declaration:

```js
// Currently selected group path, or null. MUTUALLY EXCLUSIVE with
// selectedIdSignal: at most one of the two is non-null at any moment.
//
// Two signals rather than one { kind, id } union because selectedIdSignal is
// referenced by 13 modules; a union rewrites all of them plus routing for no
// user-visible gain. The invariant lives in the two setters below — always go
// through them, never assign the raw signals from a component.
export const selectedGroupSignal = signal(null)

export function selectSession(id) {
  selectedGroupSignal.value = null
  selectedIdSignal.value = id
}

export function selectGroup(path) {
  selectedIdSignal.value = null
  selectedGroupSignal.value = path
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tests/web && npx vitest run unit/groupSelection.test.js`
Expected: PASS

- [ ] **Step 5: Route every existing writer through the setters**

Each of these currently assigns `selectedIdSignal.value` directly. Add `selectSession` to the module's `state.js` import and replace the assignment.

`internal/web/static/app/Sidebar.js` — the archive handler:

```js
        .then(() => {
          if (selectedIdSignal.value === id) {
            selectSession(null)
            if (window.location.pathname.startsWith('/s/')) {
              history.replaceState(null, '', '/')
            }
          }
        })
```

`internal/web/static/app/Sidebar.js` — `onSelect`:

```js
  const onSelect = (id) => {
    selectSession(id)
    activeTabSignal.value = 'terminal'
  }
```

`internal/web/static/app/AppShell.js` — inside `moveFocus`, replace `selectedIdSignal.value = next.id` with `selectSession(next.id)`.

`internal/web/static/app/AppShell.js` — the `Enter` branch, replace `selectedIdSignal.value = s.id` with `selectSession(s.id)`.

`internal/web/static/app/CommandPalette.js:50` — replace `selectedIdSignal.value = s.id` with `selectSession(s.id)`.

`internal/web/static/app/panes/FleetPane.js:163-166`, `panes/SearchPane.js:26-29` — replace `selectedIdSignal.value = id` with `selectSession(id)`.

`internal/web/static/app/panes/ArchivedPane.js:41-42` — replace `selectedIdSignal.value = null` with `selectSession(null)`.

- [ ] **Step 6: Verify nothing assigns the signal outside state.js**

Run:

```bash
cd /Users/dbeaudoin/workspace/tools/agent-deck && \
  grep -rn "selectedIdSignal.value = \|selectedGroupSignal.value = " internal/web/static/app --include=*.js \
  | grep -v "app/state.js"
```

Expected: only `internal/web/static/app/main.js` and `internal/web/static/app/App.js` (route sync, rewritten in Task 11). No other file.

- [ ] **Step 7: Run the unit suite**

Run: `cd tests/web && npm run test:unit`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/web/static/app tests/web/unit/groupSelection.test.js
git commit -m "feat(web): add selectedGroupSignal with mutually exclusive setters"
```

---

### Task 4: One rendered-rows model for the sidebar

Today the filter text and collapse map live inside `Sidebar`'s render closure, so keyboard navigation cannot see them — `moveFocus` walks the raw unfiltered session array and can land on a row that is hidden by a collapsed group or excluded by an active filter. Lifting both to signals fixes that and is a prerequisite for navigating group headers.

**Files:**
- Modify: `internal/web/static/app/uiState.js`
- Modify: `internal/web/static/app/dataModel.js`
- Test: `tests/web/unit/sidebarRows.test.js` (create)

**Interfaces:**
- Consumes: `menuModelSignal` (Task 2), `statusFiltersSignal` (existing, `uiState.js:63`).
- Produces:
  - `sidebarFilterSignal` (signal of string) and `groupExpandedSignal` (signal of `{ [path]: boolean }`, persisted to `localStorage` key `agentdeck.groupExpanded`) from `uiState.js`.
  - `isGroupOpen(expandedMap, path) -> boolean` and `sessionMatches(session, filter, statuses) -> boolean` from `dataModel.js`.
  - `sidebarRowsSignal` from `dataModel.js`: an array of `{ type: 'group', key, path, group, memberCount }` and `{ type: 'session', key, id, session }` in exact rendered order. `key` is `g:<path>` / `s:<id>`.

- [ ] **Step 1: Write the failing test**

Create `tests/web/unit/sidebarRows.test.js`:

```js
import { beforeEach, describe, expect, it } from 'vitest'

const stateModulePath = '../../../internal/web/static/app/state.js'
const uiStateModulePath = '../../../internal/web/static/app/uiState.js'
const dataModelModulePath = '../../../internal/web/static/app/dataModel.js'

const MENU = [
  { type: 'group', level: 0, group: { name: 'work', path: 'work', expanded: true, order: 0 } },
  { type: 'session', session: { id: 's1', title: 'api', groupPath: 'work', tool: 'claude', status: 'running' } },
  { type: 'session', session: { id: 's2', title: 'web', groupPath: 'work', tool: 'codex', status: 'idle' } },
  { type: 'group', level: 0, group: { name: 'personal', path: 'personal', expanded: true, order: 1 } },
  { type: 'session', session: { id: 's3', title: 'scratch', groupPath: 'personal', tool: 'shell', status: 'idle' } },
]

describe('sidebarRowsSignal', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal } = await import(stateModulePath)
    const { sidebarFilterSignal, groupExpandedSignal, statusFiltersSignal } = await import(uiStateModulePath)
    sessionsSignal.value = MENU
    sessionCostsSignal.value = {}
    sidebarFilterSignal.value = ''
    groupExpandedSignal.value = {}
    statusFiltersSignal.value = []
  })

  it('interleaves group headers with their members in render order', async () => {
    const { sidebarRowsSignal } = await import(dataModelModulePath)
    expect(sidebarRowsSignal.value.map((r) => r.key)).toEqual([
      'g:work', 's:s1', 's:s2', 'g:personal', 's:s3',
    ])
  })

  it('omits members of a collapsed group but keeps its header', async () => {
    const { groupExpandedSignal } = await import(uiStateModulePath)
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    groupExpandedSignal.value = { work: false }

    expect(sidebarRowsSignal.value.map((r) => r.key)).toEqual([
      'g:work', 'g:personal', 's:s3',
    ])
    // The header still reports how many members it is hiding.
    expect(sidebarRowsSignal.value[0].memberCount).toBe(2)
  })

  it('drops groups with no matching members when a text filter is active', async () => {
    const { sidebarFilterSignal } = await import(uiStateModulePath)
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    sidebarFilterSignal.value = 'scratch'

    expect(sidebarRowsSignal.value.map((r) => r.key)).toEqual(['g:personal', 's:s3'])
  })

  it('honors status filter chips', async () => {
    const { statusFiltersSignal } = await import(uiStateModulePath)
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    statusFiltersSignal.value = ['running']

    expect(sidebarRowsSignal.value.map((r) => r.key)).toEqual(['g:work', 's:s1', 'g:personal'])
  })
})

describe('groupExpandedSignal persistence', () => {
  it('writes collapse state to localStorage', async () => {
    const { groupExpandedSignal } = await import(uiStateModulePath)
    groupExpandedSignal.value = { work: false }
    expect(JSON.parse(localStorage.getItem('agentdeck.groupExpanded'))).toEqual({ work: false })
  })
})

describe('isGroupOpen', () => {
  it('treats an absent entry as open, matching the pre-existing predicate', async () => {
    const { isGroupOpen } = await import(dataModelModulePath)
    expect(isGroupOpen({}, 'work')).toBe(true)
    expect(isGroupOpen({ work: true }, 'work')).toBe(true)
    expect(isGroupOpen({ work: false }, 'work')).toBe(false)
  })
})
```

Note the status-filter case: `personal` keeps its header because a status chip is not a text filter — only a text filter hides empty groups, matching `Sidebar.js:229` today (`if (filter && members.length === 0) return null`).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tests/web && npx vitest run unit/sidebarRows.test.js`
Expected: FAIL — `sidebarFilterSignal is not exported` / `sidebarRowsSignal is not exported`

- [ ] **Step 3: Add the two UI signals**

In `internal/web/static/app/uiState.js`, append at the tail (new signals go at the tail per the file's own convention):

```js
// Sidebar `/ filter` text. Lifted out of Sidebar.js local useState so
// dataModel's sidebarRowsSignal — and therefore keyboard navigation — sees
// the same filter the user is looking at. Session-scoped (not persisted),
// matching the previous useState behavior.
export const sidebarFilterSignal = signal('')

// Group collapse map: { [groupPath]: boolean }. Only groups the user has
// explicitly toggled appear here; an absent entry means open, which is
// exactly the predicate Sidebar.js used before (`expanded[p] !== false`).
//
// Persisted so collapse survives a reload — the TUI persists it to SQLite,
// and there is no web API to write it server-side (PATCH /api/groups/{path}
// accepts only {name}). The server's own `expanded` field is deliberately
// NOT honored: nothing can write it back from the browser, so respecting it
// would make TUI collapse leak into the web one-way.
export const groupExpandedSignal = signal(loadJSON('agentdeck.groupExpanded', {}))
persist(groupExpandedSignal, 'agentdeck.groupExpanded')
```

- [ ] **Step 4: Add the derived rows**

In `internal/web/static/app/dataModel.js`, add `sidebarFilterSignal`, `groupExpandedSignal` and `statusFiltersSignal` to the imports:

```js
import { sidebarFilterSignal, groupExpandedSignal, statusFiltersSignal } from './uiState.js'
```

Then append:

```js
// A group with no explicit entry in the collapse map is open. Mirrors the
// predicate Sidebar.js used before this map was lifted into a signal.
export function isGroupOpen(expandedMap, path) {
  return (expandedMap || {})[path] !== false
}

// Row-level filter predicate shared by the sidebar and keyboard nav.
// `filter` must already be lowercased and trimmed.
export function sessionMatches(s, filter, statuses) {
  if (statuses && statuses.length && !statuses.includes(s.status)) return false
  if (!filter) return true
  const hay = (s.title || '') + ' ' + (s.group || '') + ' ' + (s.path || '') +
              ' ' + (s.tool || '') + ' ' + (s.branch || '')
  return hay.toLowerCase().includes(filter)
}

// The sidebar's rendered row order, group headers included. Single source of
// truth: the Sidebar renders from it and keyboard navigation walks it, so the
// two can never disagree about what is on screen.
export const sidebarRowsSignal = computed(() => {
  const { groups, byGroup } = menuModelSignal.value
  const filter = (sidebarFilterSignal.value || '').trim().toLowerCase()
  const statuses = statusFiltersSignal.value
  const expandedMap = groupExpandedSignal.value

  const rows = []
  for (const g of groups) {
    const members = (byGroup[g.path] || []).filter((s) => sessionMatches(s, filter, statuses))
    // A text filter hides groups with nothing to show; a status chip does not.
    if (filter && members.length === 0) continue
    rows.push({ type: 'group', key: 'g:' + g.path, path: g.path, group: g, memberCount: members.length })
    if (isGroupOpen(expandedMap, g.path)) {
      for (const s of members) rows.push({ type: 'session', key: 's:' + s.id, id: s.id, session: s })
    }
  }
  return rows
})
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd tests/web && npx vitest run unit/sidebarRows.test.js`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/web/static/app/uiState.js internal/web/static/app/dataModel.js tests/web/unit/sidebarRows.test.js
git commit -m "feat(web): derive sidebar rows from lifted filter and collapse signals"
```

---

### Task 5: Selectable group rows in the sidebar

**Files:**
- Modify: `internal/web/static/app/Sidebar.js:139-249`
- Modify: `internal/web/static/app/app.css:174-187`
- Modify: `tests/web/e2e/sidebar-chrome.spec.js:1-28,110-131`
- Test: `tests/web/e2e/group-selection.spec.js` (create)

**Interfaces:**
- Consumes: `selectGroup` / `selectedGroupSignal` (Task 3), `sidebarRowsSignal` / `isGroupOpen` (Task 4).
- Produces: DOM contract — `[data-testid="group-head-{path}"]` selects the group on click and carries class `sel` when selected; `[data-testid="group-chev-{path}"]` toggles collapse without selecting; every row carries `data-row-key` matching the `key` from `sidebarRowsSignal` (used by Task 10's scroll-into-view).

- [ ] **Step 1: Write the failing e2e test**

Create `tests/web/e2e/group-selection.spec.js`:

```js
// e2e/group-selection.spec.js -- Group rows are selectable, and selecting one
// swaps the main work area for the group stats panel.
//
// Fixture seed (tests/web/fixtures/cmd/web-fixture/main.go seed()):
//   sess-001 "agent-deck"    tool=claude status=idle    group=work
//   sess-002 "frontend"      tool=claude status=running group=work
//   sess-003 "innotrade-api" tool=codex  status=idle    group=work/innotrade
//   sess-004 "scratch"       tool=shell  status=idle    group=personal
//
// Phone (<768px) skips: the sidebar is desktop/tablet-only.
import { test, expect } from '@playwright/test'

test.describe('group selection', () => {
  test.beforeEach(async ({ page, viewport }) => {
    test.skip(!!viewport && viewport.width < 768, 'sidebar is desktop/tablet-only')
    await page.goto('/')
    await expect(page.locator('.sess')).toHaveCount(4, { timeout: 5000 })
  })

  test('clicking the group name selects it without collapsing', async ({ page }) => {
    const head = page.locator('[data-testid="group-head-work"]')

    await head.locator('.name').click()

    await expect(head).toHaveClass(/\bsel\b/)
    await expect(head.locator('.chev')).toHaveText('▾')
    await expect(page.locator('.sess')).toHaveCount(4)
  })

  test('clicking the chevron collapses without selecting', async ({ page }) => {
    const head = page.locator('[data-testid="group-head-work"]')

    await page.locator('[data-testid="group-chev-work"]').click()

    await expect(head.locator('.chev')).toHaveText('▸')
    await expect(page.locator('.sess')).toHaveCount(2)
    await expect(head).not.toHaveClass(/\bsel\b/)
  })

  test('selecting a session clears the group selection', async ({ page }) => {
    const head = page.locator('[data-testid="group-head-work"]')

    await head.locator('.name').click()
    await expect(head).toHaveClass(/\bsel\b/)

    await page.locator('.sess', { hasText: 'agent-deck' }).first().click()

    await expect(head).not.toHaveClass(/\bsel\b/)
  })

  test('collapse survives a reload', async ({ page }) => {
    await page.locator('[data-testid="group-chev-work"]').click()
    await expect(page.locator('.sess')).toHaveCount(2)

    await page.goto('/')
    await expect(page.locator('[data-testid="group-head-work"] .chev')).toHaveText('▸', { timeout: 5000 })
    await expect(page.locator('.sess')).toHaveCount(2)
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tests/web && npx playwright test e2e/group-selection.spec.js --project=chromium-desktop 2>&1 | tail -20`
Expected: FAIL — no `group-chev-work` test id; `sel` class never applied.

The three configured projects are `chromium-desktop`, `chromium-tablet` and
`chromium-phone` (`tests/web/playwright.config.js:59-67`). Iterate against
`chromium-desktop` for speed; run all three before committing.

- [ ] **Step 3: Rewrite the Sidebar list**

In `internal/web/static/app/Sidebar.js`, update the imports:

```js
import { menuModelSignal, sidebarRowsSignal, isGroupOpen } from './dataModel.js'
import {
  selectedIdSignal, selectedGroupSignal, selectSession, selectGroup,
  mutationsEnabledSignal, confirmDialogSignal,
  createSessionDialogSignal, editSessionDialogSignal,
} from './state.js'
import {
  statusFiltersSignal, showColsSignal, activeTabSignal,
  sidebarFilterSignal, groupExpandedSignal,
} from './uiState.js'
```

Replace the body of `Sidebar()` from `const { groups, byGroup, sessions } = ...` down to the end of the `side-list` div. The local `filter` and `expanded` `useState` calls are deleted; `showMenu` stays:

```js
export function Sidebar() {
  const { sessions } = menuModelSignal.value
  const rows = sidebarRowsSignal.value
  const selected = selectedIdSignal.value
  const selectedGroup = selectedGroupSignal.value
  const statusFilters = statusFiltersSignal.value
  const showCols = showColsSignal.value
  const filter = sidebarFilterSignal.value
  const expandedMap = groupExpandedSignal.value
  const [showMenu, setShowMenu] = useState(false)

  const totalVisible = useMemo(
    () => rows.reduce((n, r) => n + (r.type === 'session' ? 1 : 0), 0),
    [rows],
  )

  const toggleStatus = (id) => {
    const cur = statusFiltersSignal.value
    statusFiltersSignal.value = cur.includes(id) ? cur.filter(x => x !== id) : [...cur, id]
  }
  const toggleGroup = (p) => {
    groupExpandedSignal.value = { ...groupExpandedSignal.value, [p]: !isGroupOpen(groupExpandedSignal.value, p) }
  }
  const onSelect = (id) => {
    selectSession(id)
    activeTabSignal.value = 'terminal'
  }
  const setShowCol = (id) => {
    showColsSignal.value = { ...showCols, [id]: !showCols[id] }
  }
  ...
```

`totalVisible` now counts only the session rows actually on screen — previously it counted every matching session regardless of collapse, which disagreed with the list below it.

The filter input becomes controlled by the signal:

```js
        <input
          placeholder="/ filter"
          data-testid="sidebar-filter-input"
          value=${filter}
          onInput=${e => (sidebarFilterSignal.value = e.target.value)}
        />
```

And the list renders from rows:

```js
      <div class="side-list">
        ${rows.map(r => r.type === 'group'
          ? html`
            <div key=${r.key}
                 class=${`side-group-head ${r.group.kind || ''} ${selectedGroup === r.path ? 'sel' : ''}`}
                 data-testid=${`group-head-${r.path}`}
                 data-row-key=${r.key}
                 aria-selected=${selectedGroup === r.path}
                 onClick=${() => selectGroup(r.path)}>
              <button type="button" class="chev"
                      data-testid=${`group-chev-${r.path}`}
                      title=${isGroupOpen(expandedMap, r.path) ? 'Collapse group' : 'Expand group'}
                      onClick=${e => { e.stopPropagation(); toggleGroup(r.path) }}>
                ${isGroupOpen(expandedMap, r.path) ? '▾' : '▸'}
              </button>
              <span class="name">${r.group.label}</span>
              <span class="badge">(${r.memberCount})</span>
            </div>
          `
          : html`
            <${SessionItem} key=${r.key} s=${r.session} sel=${selected === r.id}
                            rowKey=${r.key} onSelect=${onSelect} showCols=${showCols}/>
          `,
        )}
        ${sessions.length === 0 && html`
          <div style="padding: 16px; font-family: var(--mono); font-size: 11px; color: var(--muted); text-align: center;">
            No sessions yet. Press <span class="kbd" style="border:1px solid var(--border); padding: 0 4px; border-radius: 3px;">n</span> to create one.
          </div>
        `}
      </div>
```

In `SessionItem`, accept and apply `rowKey` so keyboard scroll-into-view can find the element:

```js
function SessionItem({ s, sel, rowKey, onSelect, showCols }) {
```

```js
    <div class=${`sess ${sel ? 'sel' : ''} ${s.kind} ${exp ? 'exp' : ''}`}
         data-row-key=${rowKey}
         aria-selected=${!!sel}
         onClick=${() => onSelect(s.id)}>
```

- [ ] **Step 4: Style the selected group head**

In `internal/web/static/app/app.css`, replace the `.side-group-head` block:

```css
.side-group-head {
  display: flex; align-items: center; gap: 6px;
  padding: 6px 12px 6px 10px;
  font-family: var(--mono); font-size: 10.5px; letter-spacing: 0.08em;
  color: var(--muted); cursor: pointer; user-select: none;
  text-transform: uppercase;
  border-left: 2px solid transparent;
}
.side-group-head:hover { color: var(--text); }
.side-group-head.sel {
  background: var(--accent-soft);
  border-left-color: var(--accent);
  color: var(--text-hi);
}
.side-group-head .chev {
  font-size: 10px; width: 14px; height: 14px;
  display: inline-flex; align-items: center; justify-content: center;
  padding: 0; margin: 0;
  background: none; border: 0; border-radius: 2px;
  color: inherit; font-family: inherit; cursor: pointer;
}
.side-group-head .chev:hover { background: rgba(255,255,255,0.08); }
.side-group-head .name { flex: 1; }
.side-group-head .badge { color: var(--muted); }
.side-group-head.sel .badge { color: var(--accent); }
.side-group-head.conductor .name { color: var(--tn-purple); }
.side-group-head.watcher .name { color: var(--tn-cyan); }
```

The `padding-left` drops from 12px to 10px to absorb the new 2px border, so the row text does not shift when selected.

- [ ] **Step 5: Rewrite the stale sidebar-chrome assertions**

In `tests/web/e2e/sidebar-chrome.spec.js`, replace the group-collapse test:

```js
  test('group collapse hides member sessions; expand restores; persists across reload', async ({ page }) => {
    const workHead = page.locator('[data-testid="group-head-work"]')
    const workChev = page.locator('[data-testid="group-chev-work"]')

    // Collapse "work" → its 2 members (agent-deck, frontend) disappear.
    // work/innotrade is a distinct group path, so innotrade-api stays.
    await workChev.click()
    await expect(workHead.locator('.chev')).toHaveText('▸')
    await expect(page.locator('.sess')).toHaveCount(2)
    await expect(page.locator('.sess .tt')).toHaveText(['innotrade-api', 'scratch'])

    // Expand restores the members.
    await workChev.click()
    await expect(workHead.locator('.chev')).toHaveText('▾')
    await expect(page.locator('.sess')).toHaveCount(ALL_TITLES.length)

    // Collapse again, then reload: groupExpandedSignal persists to
    // localStorage `agentdeck.groupExpanded`, so it stays collapsed.
    await workChev.click()
    await expect(page.locator('.sess')).toHaveCount(2)
    const stored = await page.evaluate(() => JSON.parse(localStorage.getItem('agentdeck.groupExpanded')))
    expect(stored.work).toBe(false)

    await page.goto('/')
    await expect(page.locator('[data-testid="group-head-work"] .chev')).toHaveText('▸', { timeout: 5000 })
    await expect(page.locator('.sess')).toHaveCount(2)
  })
```

`gotoSidebar()` waits for 4 `.sess` rows, which no longer holds once `work` is collapsed — that is why the reload assertions above use `page.goto('/')` directly.

Then update the header comment block. Replace these two bullets:

```
//   - Group expand/collapse is plain useState — it does NOT persist across
//     reload (no localStorage key). The reload assertion pins that.
//   - The seeded `personal` group has Expanded:false, but the Sidebar's
//     `expanded` useState initializer runs on first render BEFORE the async
//     /api/menu fetch resolves (groups=[] at that point), so every group
//     defaults open (`expanded[path] !== false` with an empty map). All 4
//     seeded sessions are therefore visible on load.
```

with:

```
//   - Group expand/collapse lives in uiState.groupExpandedSignal and DOES
//     persist across reload (localStorage key `agentdeck.groupExpanded`).
//   - The seeded `personal` group has Expanded:false, but the web never
//     honors the server's `expanded` field — there is no API to write it
//     back from the browser, so respecting it would leak TUI collapse
//     one-way. Absent a stored entry every group renders open, so all 4
//     seeded sessions are visible on a fresh load.
```

- [ ] **Step 6: Run both e2e specs**

Run: `cd tests/web && npx playwright test e2e/group-selection.spec.js e2e/sidebar-chrome.spec.js 2>&1 | tail -20`
Expected: PASS

- [ ] **Step 7: Run the unit suite (Sidebar changes can break dataModel consumers)**

Run: `cd tests/web && npm run test:unit`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/web/static/app/Sidebar.js internal/web/static/app/app.css \
        tests/web/e2e/group-selection.spec.js tests/web/e2e/sidebar-chrome.spec.js
git commit -m "feat(web): select groups from the sidebar, chevron owns collapse"
```

---

### Task 6: Group status statistics

**Files:**
- Modify: `internal/web/static/app/dataModel.js`
- Test: `tests/web/unit/groupStats.test.js` (create)

**Interfaces:**
- Consumes: `menuModelSignal` (Task 2).
- Produces: `statusBucket(status) -> 'running'|'waiting'|'idle'|'stopped'|'error'`, `GROUP_STATUS_BUCKETS` (ordered array of `{ id, glyph, label }`), and `groupStats(groupPath) -> { total, fragments: [{ id, glyph, count, label }] }` from `dataModel.js`.

- [ ] **Step 1: Write the failing test**

Create `tests/web/unit/groupStats.test.js`:

```js
import { beforeEach, describe, expect, it } from 'vitest'

const stateModulePath = '../../../internal/web/static/app/state.js'
const dataModelModulePath = '../../../internal/web/static/app/dataModel.js'

function session(id, status, groupPath = 'work') {
  return { type: 'session', session: { id, title: id, groupPath, tool: 'claude', status } }
}

describe('groupStats', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal } = await import(stateModulePath)
    sessionsSignal.value = []
    sessionCostsSignal.value = {}
  })

  it('emits non-zero buckets in the TUI fixed order with the TUI glyphs', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { groupStats } = await import(dataModelModulePath)

    sessionsSignal.value = [
      { type: 'group', group: { name: 'work', path: 'work' } },
      session('e1', 'error'), session('e2', 'error'),
      session('r1', 'running'),
      session('w1', 'waiting'), session('w2', 'waiting'),
      session('st1', 'stopped'),
    ]

    const stats = groupStats('work')
    expect(stats.total).toBe(6)
    expect(stats.fragments.map((f) => [f.glyph, f.count, f.label])).toEqual([
      ['●', 1, 'running'],
      ['◐', 2, 'waiting'],
      ['■', 1, 'stopped'],
      ['✕', 2, 'error'],
    ])
  })

  it('omits zero buckets entirely', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { groupStats } = await import(dataModelModulePath)

    sessionsSignal.value = [
      { type: 'group', group: { name: 'work', path: 'work' } },
      session('r1', 'running'),
    ]

    expect(groupStats('work').fragments.map((f) => f.id)).toEqual(['running'])
  })

  it('folds starting into running and queued into idle so counts sum to the total', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { groupStats } = await import(dataModelModulePath)

    sessionsSignal.value = [
      { type: 'group', group: { name: 'work', path: 'work' } },
      session('a', 'running'), session('b', 'starting'),
      session('c', 'idle'), session('d', 'queued'),
      session('e', 'totally-unknown-status'),
    ]

    const stats = groupStats('work')
    const sum = stats.fragments.reduce((n, f) => n + f.count, 0)
    expect(sum).toBe(stats.total)
    expect(stats.fragments.find((f) => f.id === 'running').count).toBe(2)
    // idle absorbs queued and any unrecognized status.
    expect(stats.fragments.find((f) => f.id === 'idle').count).toBe(3)
  })

  it('counts direct members only, never subgroups', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { groupStats } = await import(dataModelModulePath)

    sessionsSignal.value = [
      { type: 'group', group: { name: 'work', path: 'work' } },
      session('a', 'running', 'work'),
      { type: 'group', group: { name: 'innotrade', path: 'work/innotrade' } },
      session('b', 'running', 'work/innotrade'),
    ]

    expect(groupStats('work').total).toBe(1)
    expect(groupStats('work/innotrade').total).toBe(1)
  })

  it('returns an empty result for an unknown group', async () => {
    const { groupStats } = await import(dataModelModulePath)
    expect(groupStats('nope')).toEqual({ total: 0, fragments: [] })
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tests/web && npx vitest run unit/groupStats.test.js`
Expected: FAIL — `groupStats is not a function`

- [ ] **Step 3: Implement**

Append to `internal/web/static/app/dataModel.js`:

```js
// Status buckets for the group stats panel, in the TUI's fixed display order
// with the TUI's glyphs (internal/ui/home.go:19418-19444).
export const GROUP_STATUS_BUCKETS = [
  { id: 'running', glyph: '●', label: 'running' },
  { id: 'waiting', glyph: '◐', label: 'waiting' },
  { id: 'idle',    glyph: '○', label: 'idle' },
  { id: 'stopped', glyph: '■', label: 'stopped' },
  { id: 'error',   glyph: '✕', label: 'error' },
]

// Map a session status onto one of the five display buckets.
//
// Deliberate divergence from the TUI: its five-case switch lets `starting`
// and `queued` fall through UNCOUNTED, so its fragments can sum to less than
// its own "N sessions" headline. We fold them in — and default anything
// unrecognized to idle — so the breakdown always adds up.
export function statusBucket(status) {
  switch (status) {
    case 'running':
    case 'starting':
      return 'running'
    case 'waiting':
      return 'waiting'
    case 'stopped':
      return 'stopped'
    case 'error':
      return 'error'
    default:
      return 'idle'
  }
}

// Status breakdown for one group. Direct members only — no subgroup rollup,
// matching the TUI preview pane (note MenuGroup.sessionCount from the server
// DOES roll up, so do not use it here).
export function groupStats(groupPath) {
  const members = (menuModelSignal.value.byGroup[groupPath] || [])
  const counts = { running: 0, waiting: 0, idle: 0, stopped: 0, error: 0 }
  for (const s of members) counts[statusBucket(s.status)]++
  return {
    total: members.length,
    fragments: GROUP_STATUS_BUCKETS
      .filter((b) => counts[b.id] > 0)
      .map((b) => ({ id: b.id, glyph: b.glyph, count: counts[b.id], label: b.label })),
  }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tests/web && npx vitest run unit/groupStats.test.js`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/web/static/app/dataModel.js tests/web/unit/groupStats.test.js
git commit -m "feat(web): compute group status breakdown"
```

---

### Task 7: The group stats panel

**Files:**
- Create: `internal/web/static/app/GroupStatsPanel.js`
- Modify: `internal/web/static/app/TerminalPanel.js:417-419`
- Modify: `internal/web/static/app/AppShell.js:52-88` (`WorkHead`)
- Modify: `internal/web/static/app/RightRail.js:120-123`
- Modify: `internal/web/static/app/app.css` (append)
- Test: `tests/web/e2e/group-selection.spec.js` (extend)

**Interfaces:**
- Consumes: `groupStats` (Task 6), `selectedGroupSignal` / `selectSession` (Task 3), `openCreateSessionForGroup` (Task 8 — **this task's button is wired in Task 9**; until then the button is omitted).
- Produces: `GroupStatsPanel({ path })` default export-free named export. DOM contract: `[data-testid="group-stats-panel"]` with `[data-testid="group-stats-total"]`, `[data-testid="group-stats-fragments"]`, and one `.gs-row` per member session.

- [ ] **Step 1: Write the failing e2e test**

Append to `tests/web/e2e/group-selection.spec.js` inside the `describe`:

```js
  test('selecting a group shows its stats panel', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()

    const panel = page.locator('[data-testid="group-stats-panel"]')
    await expect(panel).toBeVisible()
    await expect(panel).toContainText('📁')
    await expect(panel).toContainText('work')

    // "work" has sess-001 (idle) and sess-002 (running) as direct members.
    // work/innotrade is a separate group path and must NOT roll up.
    await expect(page.locator('[data-testid="group-stats-total"]')).toHaveText('2 sessions')

    const fragments = page.locator('[data-testid="group-stats-fragments"]')
    await expect(fragments).toContainText('● 1 running')
    await expect(fragments).toContainText('○ 1 idle')

    // Both members are listed with their tool.
    await expect(panel.locator('.gs-row')).toHaveCount(2)
    await expect(panel.locator('.gs-row')).toContainText(['agent-deck', 'frontend'])
  })

  test('clicking a session in the stats panel opens it', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await page.locator('[data-testid="group-stats-panel"] .gs-row', { hasText: 'agent-deck' }).click()

    await expect(page.locator('[data-testid="group-stats-panel"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="group-head-work"]')).not.toHaveClass(/\bsel\b/)
  })

  test('the right rail does not show an unrelated session while a group is selected', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await expect(page.locator('[data-testid="right-rail"]')).toContainText('group selected')
  })
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tests/web && npx playwright test e2e/group-selection.spec.js 2>&1 | tail -20`
Expected: FAIL — `group-stats-panel` never appears.

- [ ] **Step 3: Create the panel**

Create `internal/web/static/app/GroupStatsPanel.js`:

```js
// GroupStatsPanel.js -- Main-area view shown when a GROUP (not a session) is
// selected in the sidebar. Web port of the TUI's renderGroupPreview
// (internal/ui/home.go:19382).
//
// Deliberate divergences from the TUI, all documented in
// docs/specs/WEB-GROUP-SELECTION-PARITY-SPEC.md:
//   - `starting` counts as running, `queued` as idle, so the fragments always
//     sum to the headline (the TUI drops both).
//   - Archived sessions are excluded (the web menu snapshot never carries
//     them; the TUI preview counts them).
//   - Direct members only — no subgroup rollup, matching the TUI preview.
//   - No Repository/worktree block: per-branch dirty state is not on the wire.
import { html } from 'htm/preact'
import { menuModelSignal, groupStats } from './dataModel.js'
import { selectSession } from './state.js'
import { activeTabSignal } from './uiState.js'
import { Dot } from './icons.js'

export function GroupStatsPanel({ path }) {
  const { groups, byGroup } = menuModelSignal.value
  const group = groups.find(g => g.path === path)
  const members = byGroup[path] || []
  const stats = groupStats(path)

  const openSession = (id) => {
    selectSession(id)
    activeTabSignal.value = 'terminal'
  }

  return html`
    <div class="group-stats" data-testid="group-stats-panel" data-group-path=${path}>
      <div class="gs-head">
        <span class="gs-folder" aria-hidden="true">📁</span>
        <span class="gs-name">${group ? group.name : path}</span>
      </div>

      <div class="gs-total" data-testid="group-stats-total">${stats.total} sessions</div>

      ${stats.fragments.length > 0 && html`
        <div class="gs-fragments" data-testid="group-stats-fragments">
          ${stats.fragments.map(f => html`
            <span key=${f.id} class=${`gs-frag ${f.id}`}>
              <span class="gs-glyph">${f.glyph}</span> ${f.count} ${f.label}
            </span>
          `)}
        </div>
      `}

      ${group && group.defaultPath && html`
        <div class="gs-path" title=${group.defaultPath}>
          <span class="gs-k">default path</span>
          <span class="gs-v">${group.defaultPath}</span>
        </div>
      `}

      <div class="gs-divider"><span>SESSIONS</span></div>

      ${members.length === 0
        ? html`<div class="gs-empty">No sessions in this group</div>`
        : members.map(s => html`
            <div key=${s.id} class="gs-row" onClick=${() => openSession(s.id)}>
              <${Dot} status=${s.status}/>
              <span class="gs-title">${s.title}</span>
              <span class="gs-tool">${s.tool}</span>
            </div>
          `)}
    </div>
  `
}
```

- [ ] **Step 4: Branch TerminalPanel onto it**

In `internal/web/static/app/TerminalPanel.js`, add to the imports:

```js
import { GroupStatsPanel } from './GroupStatsPanel.js'
```

and add `selectedGroupSignal` to the existing `./state.js` import. Then replace the `if (!sessionId)` early return:

```js
  // A selected GROUP takes over the main area, mirroring the TUI where the
  // group preview replaces the session preview in the same pane. Placed after
  // every hook so hook order stays stable across renders.
  if (selectedGroupSignal.value) {
    return html`<${GroupStatsPanel} path=${selectedGroupSignal.value}/>`
  }

  if (!sessionId) {
    return html`<${EmptyStateDashboard} />`
  }
```

Read `selectedGroupSignal.value` at the top of the component body alongside the other signal reads so the component re-renders when it changes:

```js
  const sessionId = selectedIdSignal.value
  const selectedGroup = selectedGroupSignal.value
```

and use `if (selectedGroup) {` in the branch above.

- [ ] **Step 5: Stop WorkHead and RightRail falling back to sessions[0]**

In `internal/web/static/app/AppShell.js`, add `selectedGroupSignal` to the `./state.js` import, then replace the head of `WorkHead`:

```js
function WorkHead() {
  const { sessions, groups } = menuModelSignal.value
  const selected = selectedIdSignal.value
  const selectedGroup = selectedGroupSignal.value
  const profile = profileSignal.value || ''
  const canMutate = mutationsEnabledSignal.value

  // A selected group owns the head: never fall through to sessions[0], which
  // would render an unrelated session's controls above the group stats.
  if (selectedGroup) {
    const group = groups.find(g => g.path === selectedGroup)
    return html`
      <div class="work-head" data-testid="work-head-group">
        <div class="path">
          <span class="kind">GROUP</span>
          ${profile && html`<span class="seg">${profile} /</span>`}
          <span class="cur">${group ? group.name : selectedGroup}</span>
        </div>
        <span class="spacer"/>
      </div>
    `
  }

  const session = sessions.find(s => s.id === selected) || sessions[0]
  if (!session) return null
  const kindLabel = (session.kind || 'agent').toUpperCase()
  const modelLabel = session.model
    ? `${session.model}${session.modelVersion ? ` ${session.modelVersion}` : ''}`
    : ''
  ...
```

Leave the rest of `WorkHead` unchanged (it already declared `profile` and `canMutate`; delete the now-duplicated declarations further down).

In `internal/web/static/app/RightRail.js`, add `selectedGroupSignal` to the `./state.js` import and insert before the existing `if (!session)` guard:

```js
export function RightRail() {
  const { sessions } = menuModelSignal.value
  const selected = selectedIdSignal.value
  const selectedGroup = selectedGroupSignal.value
  const panels = rightRailPanelsSignal.value

  // The rail is session-scoped; a selected group has no session to describe.
  // Say so instead of falling back to sessions[0].
  if (selectedGroup) {
    return html`
      <div class="rightrail" data-testid="right-rail">
        <div class="rail-head"><span class="t">SESSION</span></div>
        <div class="rail-body">
          <div style="padding: 18px; font-family: var(--mono); font-size: 11px; color: var(--muted);">
            group selected — pick a session to see its details
          </div>
        </div>
      </div>
    `
  }

  const session = sessions.find(s => s.id === selected) || sessions[0]
```

- [ ] **Step 6: Style the panel**

Append to `internal/web/static/app/app.css`:

```css
/* Group stats panel (GroupStatsPanel.js) — shown in the main work area when
   a group rather than a session is selected. */
.group-stats {
  flex: 1; min-height: 0; overflow: auto;
  padding: 22px 26px;
  font-family: var(--mono); font-size: 12px; color: var(--text);
}
.group-stats .gs-head {
  display: flex; align-items: center; gap: 8px;
  font-size: 16px; font-weight: 600; color: var(--tn-cyan);
  margin-bottom: 14px;
}
.group-stats .gs-total {
  font-size: 13px; font-weight: 600; color: var(--text-hi);
  margin-bottom: 10px;
}
.group-stats .gs-fragments { display: flex; flex-wrap: wrap; gap: 14px; margin-bottom: 16px; }
.group-stats .gs-frag { white-space: nowrap; color: var(--muted); }
.group-stats .gs-frag.running .gs-glyph { color: var(--tn-green); }
.group-stats .gs-frag.waiting .gs-glyph { color: var(--tn-yellow); }
.group-stats .gs-frag.idle    .gs-glyph { color: var(--text); }
.group-stats .gs-frag.stopped .gs-glyph { color: var(--muted); }
.group-stats .gs-frag.error   .gs-glyph { color: var(--tn-red); }
.group-stats .gs-path { display: flex; gap: 10px; margin-bottom: 16px; min-width: 0; }
.group-stats .gs-path .gs-k { color: var(--muted); }
.group-stats .gs-path .gs-v { color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.group-stats .gs-divider {
  display: flex; align-items: center; gap: 10px;
  font-size: 10px; letter-spacing: 0.1em; color: var(--muted);
  margin: 18px 0 8px;
}
.group-stats .gs-divider::after {
  content: ""; flex: 1; height: 1px; background: var(--border);
}
.group-stats .gs-empty { color: var(--muted); font-style: italic; padding: 4px 0; }
.group-stats .gs-row {
  display: flex; align-items: center; gap: 8px;
  padding: 5px 6px; border-radius: 3px; cursor: pointer;
}
.group-stats .gs-row:hover { background: rgba(255,255,255,0.03); }
.group-stats .gs-title { flex: 1; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.group-stats .gs-tool { color: var(--tn-purple); opacity: 0.75; }
```

- [ ] **Step 7: Run the e2e spec**

Run: `cd tests/web && npx playwright test e2e/group-selection.spec.js 2>&1 | tail -20`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/web/static/app/GroupStatsPanel.js internal/web/static/app/TerminalPanel.js \
        internal/web/static/app/AppShell.js internal/web/static/app/RightRail.js \
        internal/web/static/app/app.css tests/web/e2e/group-selection.spec.js
git commit -m "feat(web): show group stats in the main area when a group is selected"
```

---

### Task 8: Derive create-session defaults from a group

**Files:**
- Modify: `internal/web/static/app/dataModel.js`
- Test: `tests/web/unit/createSessionPrefill.test.js` (create)

**Interfaces:**
- Consumes: `menuModelSignal` (Task 2), `selectedGroupSignal` / `selectedIdSignal` (Task 3).
- Produces from `dataModel.js`:
  - `groupCreateDefaults(groupPath) -> { groupPath, groupName, defaultPath, tool, modelId }`
  - `currentGroupPath() -> string` — the group implied by the current selection (selected group, else the selected session's group, else `''`).

- [ ] **Step 1: Write the failing test**

Create `tests/web/unit/createSessionPrefill.test.js`:

```js
import { beforeEach, describe, expect, it } from 'vitest'

const stateModulePath = '../../../internal/web/static/app/state.js'
const dataModelModulePath = '../../../internal/web/static/app/dataModel.js'

const MENU = [
  { type: 'group', group: { name: 'work', path: 'work', defaultPath: '/srv/work' } },
  { type: 'session', session: { id: 'old', title: 'old', groupPath: 'work', tool: 'gemini', modelId: 'gemini-2.5-pro', projectPath: '/srv/old', createdAt: '2026-01-01T00:00:00Z' } },
  { type: 'session', session: { id: 'new', title: 'new', groupPath: 'work', tool: 'codex', modelId: 'gpt-5.5', projectPath: '/srv/new', createdAt: '2026-06-01T00:00:00Z' } },
  { type: 'group', group: { name: 'personal', path: 'personal' } },
  { type: 'session', session: { id: 'p1', title: 'scratch', groupPath: 'personal', tool: 'shell', projectPath: '/home/me/scratch', createdAt: '2026-03-01T00:00:00Z' } },
  { type: 'group', group: { name: 'empty', path: 'empty' } },
]

describe('groupCreateDefaults', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal, selectedIdSignal, selectedGroupSignal } = await import(stateModulePath)
    sessionsSignal.value = MENU
    sessionCostsSignal.value = {}
    selectedIdSignal.value = null
    selectedGroupSignal.value = null
  })

  it('takes the folder from group config and the tool from the newest session', async () => {
    const { groupCreateDefaults } = await import(dataModelModulePath)

    expect(groupCreateDefaults('work')).toEqual({
      groupPath: 'work',
      groupName: 'work',
      defaultPath: '/srv/work',
      tool: 'codex',
      modelId: 'gpt-5.5',
    })
  })

  it('falls back to the newest session path when the group has no configured folder', async () => {
    const { groupCreateDefaults } = await import(dataModelModulePath)

    expect(groupCreateDefaults('personal')).toEqual({
      groupPath: 'personal',
      groupName: 'personal',
      defaultPath: '/home/me/scratch',
      tool: 'shell',
      modelId: '',
    })
  })

  it('returns empty defaults for a group with no sessions and no config', async () => {
    const { groupCreateDefaults } = await import(dataModelModulePath)

    expect(groupCreateDefaults('empty')).toEqual({
      groupPath: 'empty',
      groupName: 'empty',
      defaultPath: '',
      tool: '',
      modelId: '',
    })
  })

  it('returns a blank context for an unknown or empty group path', async () => {
    const { groupCreateDefaults } = await import(dataModelModulePath)

    expect(groupCreateDefaults('')).toEqual({
      groupPath: '', groupName: '', defaultPath: '', tool: '', modelId: '',
    })
    expect(groupCreateDefaults('nope').groupPath).toBe('')
  })
})

describe('currentGroupPath', () => {
  beforeEach(async () => {
    const { sessionsSignal, selectedIdSignal, selectedGroupSignal } = await import(stateModulePath)
    sessionsSignal.value = MENU
    selectedIdSignal.value = null
    selectedGroupSignal.value = null
  })

  it('prefers an explicitly selected group', async () => {
    const { selectGroup } = await import(stateModulePath)
    const { currentGroupPath } = await import(dataModelModulePath)

    selectGroup('personal')
    expect(currentGroupPath()).toBe('personal')
  })

  it('falls back to the selected session group', async () => {
    const { selectSession } = await import(stateModulePath)
    const { currentGroupPath } = await import(dataModelModulePath)

    selectSession('new')
    expect(currentGroupPath()).toBe('work')
  })

  it('returns empty when nothing is selected', async () => {
    const { currentGroupPath } = await import(dataModelModulePath)
    expect(currentGroupPath()).toBe('')
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tests/web && npx vitest run unit/createSessionPrefill.test.js`
Expected: FAIL — `groupCreateDefaults is not a function`

- [ ] **Step 3: Implement**

In `internal/web/static/app/dataModel.js`, add `selectedGroupSignal` to the `./state.js` import, then append:

```js
// Epoch millis for a session's createdAt; -Infinity when absent or unparsable
// so a session with no timestamp never wins "newest".
function createdAtMillis(s) {
  const t = Date.parse(s.createdAt || '')
  return Number.isNaN(t) ? -Infinity : t
}

// Defaults for a new session created in `groupPath`.
//
// Mirrors the TUI's quick-create (internal/ui/home.go:12325-12350): the folder
// comes from the group's configured default_path and falls back to the group's
// newest session path; the tool and model are inherited from the most recently
// CREATED session in the group. Everything is derived client-side, so no
// per-group tool/model schema is needed.
export function groupCreateDefaults(groupPath) {
  const blank = { groupPath: '', groupName: '', defaultPath: '', tool: '', modelId: '' }
  if (!groupPath) return blank

  const { groups, byGroup } = menuModelSignal.value
  const group = groups.find(g => g.path === groupPath)
  if (!group) return blank

  let newest = null
  for (const s of (byGroup[groupPath] || [])) {
    if (!newest || createdAtMillis(s) > createdAtMillis(newest)) newest = s
  }

  return {
    groupPath: group.path,
    groupName: group.name,
    defaultPath: group.defaultPath || (newest ? newest.path : '') || '',
    tool: (newest && newest.tool) || '',
    modelId: (newest && newest.modelId) || '',
  }
}

// The group implied by the current selection: an explicitly selected group,
// else the selected session's group, else none.
export function currentGroupPath() {
  if (selectedGroupSignal.value) return selectedGroupSignal.value
  const id = selectedIdSignal.value
  if (!id) return ''
  const s = (menuModelSignal.value.sessions || []).find(x => x.id === id)
  return s ? s.group : ''
}
```

`dataModel.js` already imports `sessionsSignal` and `sessionCostsSignal` from `./state.js`; add `selectedIdSignal` and `selectedGroupSignal` to that same import statement.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tests/web && npx vitest run unit/createSessionPrefill.test.js`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/web/static/app/dataModel.js tests/web/unit/createSessionPrefill.test.js
git commit -m "feat(web): derive new-session defaults from group context"
```

---

### Task 9: Create the session in the selected group

This task carries the standalone bug fix: today the web never sends `groupPath`, so every browser-created session lands in the default group.

**Files:**
- Modify: `internal/web/static/app/state.js:81`
- Modify: `internal/web/static/app/dataModel.js`
- Modify: `internal/web/static/app/CreateSessionDialog.js`
- Modify: `internal/web/static/app/Sidebar.js:201`, `EmptyStateDashboard.js:39`, `CommandPalette.js:40`, `AppShell.js:89,232-240,287-288`, `GroupStatsPanel.js`
- Test: `internal/web/handlers_sessions_test.go`, `tests/web/unit/createSessionPrefill.test.js`, `tests/web/e2e/group-selection.spec.js`

**Interfaces:**
- Consumes: `groupCreateDefaults` / `currentGroupPath` (Task 8).
- Produces: `createSessionDialogSignal` now holds `null` (closed) or `{ groupPath, groupName, defaultPath, tool, modelId }`; `openCreateSessionForGroup(groupPath)` from `dataModel.js` is the single way to open it.

- [ ] **Step 1: Write the failing Go test**

Append to `internal/web/handlers_sessions_test.go`:

```go
// TestSessionsCollectionPOSTForwardsGroupPath pins that a create request
// naming a group actually creates the session there. The handler has always
// threaded GroupPath, but the web client never sent it — so every
// browser-created session silently landed in the default group.
func TestSessionsCollectionPOSTForwardsGroupPath(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var gotGroup string
	srv.mutator = &fakeMutator{
		createSessionFn: func(title, tool, projectPath, groupPath, modelID, reasoningEffort string) (string, error) {
			gotGroup = groupPath
			return "new-id", nil
		},
	}

	body := strings.NewReader(`{"title":"Test","tool":"claude","projectPath":"/tmp","groupPath":"work/innotrade"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d: %s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	if gotGroup != "work/innotrade" {
		t.Fatalf("groupPath = %q, want %q", gotGroup, "work/innotrade")
	}
}
```

- [ ] **Step 2: Run it — this one should already PASS**

Run: `go test ./internal/web/ -run TestSessionsCollectionPOSTForwardsGroupPath -race -count=1`
Expected: PASS. The server side was never broken; this test locks the contract the client is about to start relying on. Commit it as regression cover.

- [ ] **Step 3: Write the failing frontend test**

Append to `tests/web/unit/createSessionPrefill.test.js`:

```js
describe('openCreateSessionForGroup', () => {
  beforeEach(async () => {
    const { sessionsSignal, createSessionDialogSignal } = await import(stateModulePath)
    sessionsSignal.value = MENU
    createSessionDialogSignal.value = null
  })

  it('opens the dialog carrying the group context', async () => {
    const { createSessionDialogSignal } = await import(stateModulePath)
    const { openCreateSessionForGroup } = await import(dataModelModulePath)

    openCreateSessionForGroup('work')

    expect(createSessionDialogSignal.value).toEqual({
      groupPath: 'work',
      groupName: 'work',
      defaultPath: '/srv/work',
      tool: 'codex',
      modelId: 'gpt-5.5',
    })
  })

  it('opens with a blank context when no group is implied', async () => {
    const { createSessionDialogSignal } = await import(stateModulePath)
    const { openCreateSessionForGroup } = await import(dataModelModulePath)

    openCreateSessionForGroup('')

    expect(createSessionDialogSignal.value).toEqual({
      groupPath: '', groupName: '', defaultPath: '', tool: '', modelId: '',
    })
  })
})
```

- [ ] **Step 4: Run test to verify it fails**

Run: `cd tests/web && npx vitest run unit/createSessionPrefill.test.js`
Expected: FAIL — `openCreateSessionForGroup is not a function`

- [ ] **Step 5: Change the signal shape and add the opener**

In `internal/web/static/app/state.js`, replace the `createSessionDialogSignal` declaration:

```js
// Create-session dialog. null = closed; otherwise the group context the
// dialog seeds itself from:
//   { groupPath, groupName, defaultPath, tool, modelId }
// Open it through dataModel.openCreateSessionForGroup(), never by assigning
// here — that helper is what fills the context.
export const createSessionDialogSignal = signal(null)
```

In `internal/web/static/app/dataModel.js`, add `createSessionDialogSignal` to the `./state.js` import and append:

```js
// The single entry point for opening the create-session dialog. Pass '' for
// no group context (dialog opens blank, as it always did).
export function openCreateSessionForGroup(groupPath) {
  createSessionDialogSignal.value = groupCreateDefaults(groupPath)
}
```

- [ ] **Step 6: Update every open site**

`Sidebar.js:201` — import `openCreateSessionForGroup` and `currentGroupPath` from `./dataModel.js`:

```js
          <button class="icon-btn" title="New session (n)" aria-label="New session"
                  onClick=${() => openCreateSessionForGroup(currentGroupPath())}>
```

`EmptyStateDashboard.js:39`:

```js
            <button class="btn primary" onClick=${() => openCreateSessionForGroup(currentGroupPath())}>
```

`CommandPalette.js:40`:

```js
      list.unshift({ id: 'cmd-new', sec: 'COMMANDS', label: 'New session', tool: 'n', run: () => { openCreateSessionForGroup(currentGroupPath()); close() } })
```

`AppShell.js:89` (the WorkHead "New" button — session branch):

```js
          <button class="btn primary" onClick=${() => openCreateSessionForGroup(session.group || '')}>
```

`AppShell.js` `closeAllModals`:

```js
      createSessionDialogSignal.value = null
```

`AppShell.js` render — the boolean guard becomes a null check:

```js
  const showCreateSession = createSessionDialogSignal.value
```

(unchanged line; `null` is falsy so `${showCreateSession && html\`<${CreateSessionDialog}/>\`}` still works.)

`GroupStatsPanel.js` — add the action button below the header, importing `openCreateSessionForGroup` from `./dataModel.js` and `mutationsEnabledSignal` from `./state.js`:

```js
      ${mutationsEnabledSignal.value && html`
        <div class="gs-actions">
          <button class="btn primary" data-testid="group-new-session-btn"
                  onClick=${() => openCreateSessionForGroup(path)}>
            New session in this group <span class="kbd">n</span>
          </button>
        </div>
      `}
```

and the matching style appended to `app.css`:

```css
.group-stats .gs-actions { margin: 4px 0 18px; }
```

- [ ] **Step 7: Seed the dialog and send groupPath**

In `internal/web/static/app/CreateSessionDialog.js`, replace the top of the component:

```js
export function CreateSessionDialog() {
  const open = createSessionDialogSignal.value
  const ctx = open || { groupPath: '', groupName: '', defaultPath: '', tool: '', modelId: '' }

  const [title, setTitle] = useState('')
  const [tool, setTool] = useState('claude')
  const [modelId, setModelId] = useState('')
  const [customModel, setCustomModel] = useState('')
  const [reasoningEffort, setReasoningEffort] = useState('')
  const [path, setPath] = useState('')
  const [error, setError] = useState(null)
  const [submitting, setSubmitting] = useState(false)
  const [seededFor, setSeededFor] = useState(null)

  // Re-seed when the dialog opens for a different group. Keyed on groupPath so
  // SSE-driven re-renders never stomp edits the user is in the middle of, and
  // reopening on another group does not inherit the previous group's values.
  if (open && seededFor !== ctx.groupPath) {
    const seedTool = ctx.tool || 'claude'
    setTool(seedTool)
    setPath(ctx.defaultPath || '')
    // Only prefill a model the catalog recognizes: an unknown id would render
    // as a blank <select> and, on submit, become an explicit per-session
    // override (see resolveClaudeLaunchModel, internal/session/claude.go:611).
    const known = (MODEL_ID_CATALOG[seedTool] || []).some(m => m.value === ctx.modelId)
    setModelId(known ? ctx.modelId : '')
    setCustomModel('')
    setReasoningEffort('')
    setTitle('')
    setError(null)
    setSeededFor(ctx.groupPath)
  }

  // WEB-P0-4 prevention layer: when mutations are disabled (server
  // webMutations=false), do not render the dialog at all. Hooks order is
  // preserved by placing this guard AFTER all useState calls.
  if (!mutationsEnabledSignal.value) return null
```

Update the payload in `handleSubmit`:

```js
      const payload = { title, tool, projectPath: path }
      if (ctx.groupPath) payload.groupPath = ctx.groupPath
      const modelId = selectedModelId()
      if (modelId) payload.modelId = modelId
      if (reasoningEffort) payload.reasoningEffort = reasoningEffort
```

Update `close`:

```js
  const close = () => (createSessionDialogSignal.value = null)
```

Add the read-only GROUP row as the first field in `.db`:

```js
          ${ctx.groupName && html`
            <div class="field">
              <label>GROUP</label>
              <div class="ro-value" data-testid="create-session-group">${ctx.groupName}</div>
            </div>
          `}
```

with the style appended to `app.css`:

```css
.dialog .ro-value {
  font-family: var(--mono); font-size: 12px; color: var(--text-hi);
  padding: 7px 9px; border: 1px solid var(--border); border-radius: 4px;
  background: rgba(255,255,255,0.02);
}
```

- [ ] **Step 8: Add the e2e assertion**

Append to `tests/web/e2e/group-selection.spec.js` inside the `describe`:

```js
  test('new session from a group prefills the group folder and tool', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await page.locator('[data-testid="group-new-session-btn"]').click()

    // Fixture: group "work" has DefaultPath "/srv/work"; its newest session
    // (sess-002 "frontend") uses claude.
    await expect(page.locator('[data-testid="create-session-group"]')).toHaveText('work')
    await expect(page.locator('.dialog input').nth(1)).toHaveValue('/srv/work')
    await expect(page.locator('.dialog .seg-btn.on')).toHaveText('Claude')
  })

  test('a group with no configured folder falls back to its newest session path', async ({ page }) => {
    await page.locator('[data-testid="group-head-personal"] .name').click()
    await page.locator('[data-testid="group-new-session-btn"]').click()

    // Fixture: "personal" has no DefaultPath; sess-004 "scratch" is its only
    // session (ProjectPath "/home/dev/scratch", tool=shell), so the newest-
    // session fallback supplies both the folder and the tool.
    await expect(page.locator('[data-testid="create-session-group"]')).toHaveText('personal')
    await expect(page.locator('.dialog input').nth(1)).toHaveValue('/home/dev/scratch')
  })
```

The two `.dialog input` positions are TITLE (0) and WORKING DIR (1) — the GROUP
row is a read-only `div.ro-value`, not an input, so it does not shift the index.

- [ ] **Step 9: Run everything**

Run:

```bash
go test ./internal/web/ -race -count=1
cd tests/web && npm run test:unit && npx playwright test e2e/group-selection.spec.js 2>&1 | tail -20
```

Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/web/static/app internal/web/handlers_sessions_test.go tests/web/unit tests/web/e2e/group-selection.spec.js
git commit -m "fix(web): create sessions in the selected group, prefilled from its context"
```

---

### Task 10: Keyboard navigation over group headers

**Files:**
- Modify: `internal/web/static/app/AppShell.js:208-337`
- Modify: `internal/web/static/app/KeyboardShortcuts.js:11-26`
- Test: `tests/web/e2e/group-selection.spec.js` (extend)

**Interfaces:**
- Consumes: `sidebarRowsSignal` (Task 4), `selectGroup` / `selectSession` (Task 3), `groupExpandedSignal` / `isGroupOpen` (Task 4), `openCreateSessionForGroup` / `currentGroupPath` (Task 8/9).
- Produces: no new exports. Behavior contract only.

- [ ] **Step 1: Write the failing e2e test**

Append to `tests/web/e2e/group-selection.spec.js` inside the `describe`:

```js
  test('j walks group headers as well as sessions', async ({ page }) => {
    await page.locator('body').click()

    // Rendered order: g:work, s:sess-001, s:sess-002, g:work/innotrade,
    // s:sess-003, g:personal, s:sess-004.
    await page.keyboard.press('j')
    await expect(page.locator('[data-testid="group-head-work"]')).toHaveClass(/\bsel\b/)

    await page.keyboard.press('j')
    await expect(page.locator('[data-testid="group-head-work"]')).not.toHaveClass(/\bsel\b/)
    await expect(page.locator('.sess.sel .tt')).toHaveText('agent-deck')

    await page.keyboard.press('k')
    await expect(page.locator('[data-testid="group-head-work"]')).toHaveClass(/\bsel\b/)
  })

  test('arrow keys collapse and expand the focused group', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()

    await page.keyboard.press('ArrowLeft')
    await expect(page.locator('[data-testid="group-head-work"] .chev')).toHaveText('▸')
    await expect(page.locator('.sess')).toHaveCount(2)

    await page.keyboard.press('ArrowRight')
    await expect(page.locator('[data-testid="group-head-work"] .chev')).toHaveText('▾')
    await expect(page.locator('.sess')).toHaveCount(4)
  })

  test('n opens the dialog prefilled from the focused group', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await page.keyboard.press('n')

    await expect(page.locator('[data-testid="create-session-group"]')).toHaveText('work')
  })
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tests/web && npx playwright test e2e/group-selection.spec.js -g "walks group headers" 2>&1 | tail -20`
Expected: FAIL — `j` selects a session, never the group header.

- [ ] **Step 3: Rewrite moveFocus and add the collapse keys**

In `internal/web/static/app/AppShell.js`, extend the `./dataModel.js` import:

```js
import {
  menuModelSignal, sidebarRowsSignal, isGroupOpen,
  openCreateSessionForGroup, currentGroupPath,
} from './dataModel.js'
```

add `selectSession, selectGroup, selectedGroupSignal` to the `./state.js` import and `groupExpandedSignal` to the `./uiState.js` import.

Replace `moveFocus` and `focusedSession` inside the keyboard `useEffect`:

```js
    // Scroll the newly selected row into view. Rows carry data-row-key
    // matching sidebarRowsSignal keys (Sidebar.js).
    const revealRow = (key) => {
      const el = document.querySelector(`[data-row-key="${CSS.escape(key)}"]`)
      if (el && typeof el.scrollIntoView === 'function') {
        el.scrollIntoView({ block: 'nearest' })
      }
    }

    // Move the selection by `delta` through the sidebar's RENDERED rows —
    // group headers included, collapse state and filters honored. Walking
    // the raw session array (the previous behavior) could land on a row
    // hidden inside a collapsed group or filtered off screen.
    const moveFocus = (delta) => {
      const rows = sidebarRowsSignal.value
      if (rows.length === 0) return

      const groupPath = selectedGroupSignal.value
      const sessionId = selectedIdSignal.value
      let idx = -1
      if (groupPath) idx = rows.findIndex(r => r.type === 'group' && r.path === groupPath)
      else if (sessionId) idx = rows.findIndex(r => r.type === 'session' && r.id === sessionId)
      if (idx === -1) idx = delta > 0 ? -1 : rows.length

      const next = rows[Math.max(0, Math.min(rows.length - 1, idx + delta))]
      if (!next) return
      // Only move the selection; do NOT switch to the terminal tab. Activating
      // the terminal hands focus to xterm.js, which swallows later keypresses
      // (issue #780 review). Enter is what opens.
      if (next.type === 'group') selectGroup(next.path)
      else selectSession(next.id)
      revealRow(next.key)
    }

    // Collapse/expand the focused group. Mirrors the TUI's h/left and
    // l/right/tab (internal/ui/home.go:999, :8786). No-op unless a group is
    // actually selected.
    const setGroupOpen = (open) => {
      const p = selectedGroupSignal.value
      if (!p) return false
      if (isGroupOpen(groupExpandedSignal.value, p) === open) return true
      groupExpandedSignal.value = { ...groupExpandedSignal.value, [p]: open }
      return true
    }

    const focusedSession = () => {
      const sessions = (menuModelSignal.value?.sessions) || []
      const id = selectedIdSignal.value
      if (!id) return null
      return sessions.find(s => s.id === id) || null
    }
```

`focusedSession` no longer falls back to `sessions[0]`: with a group selected there is no focused session, and the old fallback would let `Enter`, `r` and `Shift+D` act on an arbitrary session.

Replace the key branches. `j`/`k` gain arrow aliases, and the new keys slot in before the `Enter` branch:

```js
      } else if (e.key === 'j' || e.key === 'ArrowDown') {
        e.preventDefault(); moveFocus(+1)
      } else if (e.key === 'k' || e.key === 'ArrowUp') {
        e.preventDefault(); moveFocus(-1)
      } else if (e.key === 'ArrowLeft' || e.key === 'h') {
        if (setGroupOpen(false)) e.preventDefault()
      } else if (e.key === 'ArrowRight' || e.key === 'l') {
        if (setGroupOpen(true)) e.preventDefault()
      } else if (e.key === 'Tab' && selectedGroupSignal.value) {
        // Tab toggles the focused group, matching the TUI. Guarded on an
        // actual group selection so normal focus traversal is untouched.
        e.preventDefault()
        const p = selectedGroupSignal.value
        groupExpandedSignal.value = {
          ...groupExpandedSignal.value,
          [p]: !isGroupOpen(groupExpandedSignal.value, p),
        }
      } else if (e.key === 'Enter') {
        if (selectedGroupSignal.value) {
          e.preventDefault()
          const p = selectedGroupSignal.value
          groupExpandedSignal.value = {
            ...groupExpandedSignal.value,
            [p]: !isGroupOpen(groupExpandedSignal.value, p),
          }
          return
        }
        const s = focusedSession()
        if (s) {
          e.preventDefault()
          selectSession(s.id)
          activeTabSignal.value = 'terminal'
        }
      } else if (e.key === 'n' && mutationsEnabledSignal.value) {
        e.preventDefault()
        openCreateSessionForGroup(currentGroupPath())
      }
```

Leave the `r`, `D`, `q`, `]`, `?`, `/`, `Shift+Enter` and Ctrl/Cmd branches as they are.

- [ ] **Step 4: Update the shortcuts overlay**

In `internal/web/static/app/KeyboardShortcuts.js`, each entry is
`{ keys: string[], label: string }` and every element of `keys` renders as its
own `<kbd>`. **Replace** the existing `j`, `k`, `Enter` and `n` rows (do not
duplicate them) and add the new ones, keeping the array's current order:

```js
  { keys: ['j'],               label: 'Move focus down (groups and sessions)' },
  { keys: ['k'],               label: 'Move focus up (groups and sessions)' },
  { keys: ['↑'],               label: 'Move focus up' },
  { keys: ['↓'],               label: 'Move focus down' },
  { keys: ['←'],               label: 'Collapse focused group' },
  { keys: ['→'],               label: 'Expand focused group' },
  { keys: ['Tab'],             label: 'Toggle focused group' },
  { keys: ['Enter'],           label: 'Open focused session / toggle focused group' },
  { keys: ['n'],               label: 'New session in the focused group' },
```

- [ ] **Step 5: Fix the two specs that assume the old key semantics**

Both breakages are real behavior changes, not flakes. With the fixture's rows in
render order — `g:work`, `sess-001`, `sess-002`, `g:work/innotrade`, `sess-003`,
`g:personal`, `sess-004` — the **first** `j` now lands on the `work` group
header, so reaching the first session takes two presses.

In `tests/web/e2e/keyboard-parity.spec.js`, the `j` test currently does
`press('j')` then `waitForSelector('.sess.sel')`, which will now time out.
Replace both navigation tests:

```js
  test('j moves focus through group headers and sessions', async ({ page }) => {
    const titles = await page.locator('.sess .tt').allTextContents()
    test.skip(titles.length < 2, 'need at least two sessions for j to be observable')

    // Nothing selected initially. Rows are interleaved group headers and
    // sessions, so the first `j` lands on the first GROUP header.
    await page.keyboard.press('j')
    await page.waitForSelector('.side-group-head.sel', { timeout: 2000 })

    // The next `j` steps into that group's first session.
    await page.keyboard.press('j')
    await page.waitForSelector('.sess.sel', { timeout: 2000 })
    const first = await page.locator('.sess.sel .tt').textContent()
    expect(first).toBeTruthy()

    // And again to the second session in the same group.
    await page.keyboard.press('j')
    await page.waitForFunction((prev) => {
      const sel = document.querySelector('.sess.sel .tt')
      return sel && sel.textContent && sel.textContent !== prev
    }, first, { timeout: 2000 })
    expect(await page.locator('.sess.sel .tt').textContent()).not.toBe(first)
  })

  test('k moves focus back through the rendered rows', async ({ page }) => {
    const titles = await page.locator('.sess .tt').allTextContents()
    test.skip(titles.length < 2, 'need at least two sessions for k to be observable')

    // Bootstrap: j×3 → group header, first session, second session.
    await page.keyboard.press('j')
    await page.keyboard.press('j')
    await page.keyboard.press('j')
    await page.waitForSelector('.sess.sel', { timeout: 2000 })
    const before = await page.locator('.sess.sel .tt').textContent()

    await page.keyboard.press('k')
    await page.waitForFunction((prev) => {
      const sel = document.querySelector('.sess.sel .tt')
      return sel && sel.textContent && sel.textContent !== prev
    }, before, { timeout: 2000 })
    expect(await page.locator('.sess.sel .tt').textContent()).not.toBe(before)
  })
```

In `tests/web/e2e/toasts.spec.js`, the history-drawer test presses `r` with
nothing selected and relies on it acting on the implicit first session — the
`sessions[0]` fallback this task removes. Replace the two setup blocks:

```js
    // Toast A: focus the first session explicitly (j×2 — the first j lands on
    // the `work` group header) and fire the rename-gap info toast.
    await page.keyboard.press('j')
    await page.keyboard.press('j')
    await page.keyboard.press('r')
    const toastA = page.locator('[data-testid="toast"]', { hasText: 'Rename "agent-deck"' })
    await expect(toastA).toBeVisible({ timeout: 3000 })
    await toastA.locator('[data-testid="toast-dismiss"]').click()
    await expect(toastA).toHaveCount(0)

    // Toast B: one more j → the second session ("frontend"). Repeat.
    await page.keyboard.press('j')
    await page.keyboard.press('r')
```

Also update the comment at `toasts.spec.js:77-78`, which describes the removed
fallback ("`r` with default focus (first session, \"agent-deck\")").

- [ ] **Step 6: Run the affected specs**

Run: `cd tests/web && npx playwright test e2e/group-selection.spec.js e2e/keyboard-parity.spec.js e2e/toasts.spec.js e2e/keyboard-extras.spec.js e2e/close-undo.spec.js --project=chromium-desktop 2>&1 | tail -20`
Expected: PASS. If `keyboard-extras.spec.js` or `close-undo.spec.js` also press
`r`, `Shift+D` or `Enter` with nothing selected, give them the same explicit
`j`-to-select bootstrap.

- [ ] **Step 7: Commit**

```bash
git add internal/web/static/app/AppShell.js internal/web/static/app/KeyboardShortcuts.js tests/web/e2e
git commit -m "feat(web): navigate group headers and collapse them from the keyboard"
```

---

### Task 11: A URL for the selected group

**Files:**
- Modify: `internal/web/server.go:218`
- Modify: `internal/web/static_files.go:60`
- Modify: `internal/web/static/app/App.js`
- Modify: `internal/web/static/app/main.js:162-176`
- Test: `internal/web/static_files_test.go` (extend), `tests/web/e2e/group-selection.spec.js` (extend)

**Interfaces:**
- Consumes: `selectedGroupSignal` / `selectGroup` / `selectSession` (Task 3).
- Produces: `/g/{encodeURIComponent(path)}` serves the SPA shell and restores the group selection on load; the URL is pushed when a group is selected.

- [ ] **Step 1: Write the failing Go test**

Append to `internal/web/static_files_test.go`:

```go
// TestIndexServesGroupRoute pins that /g/{path} serves the SPA shell rather
// than 404ing, so a selected group is linkable and survives a reload.
func TestIndexServesGroupRoute(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})

	for _, path := range []string{"/g/work", "/g/work%2Finnotrade"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want %d", path, rr.Code, http.StatusOK)
		}
		if !strings.Contains(rr.Body.String(), "<!doctype html") &&
			!strings.Contains(rr.Body.String(), "<!DOCTYPE html") {
			t.Errorf("GET %s did not serve the SPA shell", path)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run TestIndexServesGroupRoute -race -count=1`
Expected: FAIL — `GET /g/work = 404, want 200`

- [ ] **Step 3: Register the route**

In `internal/web/server.go`, after the `/s/` line:

```go
	mux.HandleFunc("/g/", s.handleIndex)
```

In `internal/web/static_files.go`, widen the guard:

```go
	path := r.URL.Path
	if path != "/" && !strings.HasPrefix(path, "/s/") && !strings.HasPrefix(path, "/g/") {
		http.NotFound(w, r)
		return
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/web/ -run TestIndexServesGroupRoute -race -count=1`
Expected: PASS

- [ ] **Step 5: Sync the frontend route**

In `internal/web/static/app/App.js`, replace the whole component:

```js
export function App() {
  // Route sync: update the selection when the browser navigates back/forward.
  useEffect(() => {
    function onPopState() {
      applyPath(window.location.pathname || '/')
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  // URL push: write the URL when the selection changes.
  useEffect(() => {
    const id = selectedIdSignal.value
    const groupPath = selectedGroupSignal.value
    let targetPath = '/'
    if (id) targetPath = '/s/' + encodeURIComponent(id)
    else if (groupPath) targetPath = '/g/' + encodeURIComponent(groupPath)
    if (window.location.pathname !== targetPath) {
      window.history.pushState(null, '', targetPath)
    }
  }, [selectedIdSignal.value, selectedGroupSignal.value])

  return html`
    <${AppShell} />
  `
}
```

with imports and a shared `applyPath` at module scope:

```js
import { selectedIdSignal, selectedGroupSignal, selectSession, selectGroup } from './state.js'

// Map a pathname onto the selection. encodeURIComponent turns a nested group
// path ("work/innotrade") into "work%2Finnotrade", which keeps the no-slash
// guard below meaningful for both routes.
export function applyPath(path) {
  if (path.startsWith('/s/')) {
    const raw = path.slice(3)
    if (raw && !raw.includes('/')) {
      try { selectSession(decodeURIComponent(raw)) } catch (_) { selectSession(null) }
      return
    }
  }
  if (path.startsWith('/g/')) {
    const raw = path.slice(3)
    if (raw && !raw.includes('/')) {
      try { selectGroup(decodeURIComponent(raw)) } catch (_) { selectSession(null) }
      return
    }
  }
  // Clearing on popstate to / lets the empty dashboard render when the user
  // navigates back.
  if (path === '/') selectSession(null)
}
```

In `internal/web/static/app/main.js`, replace `applyRouteSelection` with a delegation so boot and popstate cannot drift:

```js
// ---------- Route sync: URL -> selection ----------

export function applyRouteSelection() {
  const path = window.location.pathname || '/'
  // Don't force-clear at boot when the path is neither /s/ nor /g/.
  if (path === '/') return
  applyPath(path)
}
```

and import `applyPath` from `./App.js` there.

- [ ] **Step 6: Add the e2e assertion**

Append to `tests/web/e2e/group-selection.spec.js` inside the `describe`:

```js
  test('group selection is reflected in the URL and survives a reload', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await expect(page).toHaveURL(/\/g\/work$/)

    await page.reload()
    await expect(page.locator('[data-testid="group-stats-panel"]')).toBeVisible({ timeout: 5000 })
    await expect(page.locator('[data-testid="group-head-work"]')).toHaveClass(/\bsel\b/)
  })

  test('a nested group path round-trips through the URL', async ({ page }) => {
    await page.locator('[data-testid="group-head-work/innotrade"] .name').click()
    await expect(page).toHaveURL(/\/g\/work%2Finnotrade$/)

    await page.reload()
    await expect(page.locator('[data-testid="group-stats-panel"]')).toHaveAttribute('data-group-path', 'work/innotrade', { timeout: 5000 })
  })
```

- [ ] **Step 7: Run everything**

Run:

```bash
go test ./internal/web/ -race -count=1
cd tests/web && npx playwright test e2e/group-selection.spec.js e2e/url-routing.spec.js 2>&1 | tail -20
```

Expected: PASS. `url-routing.spec.js` covers `/s/`; if it asserts that any non-`/s/` path 404s, update that assertion.

- [ ] **Step 8: Commit**

```bash
git add internal/web/server.go internal/web/static_files.go internal/web/static_files_test.go \
        internal/web/static/app/App.js internal/web/static/app/main.js tests/web/e2e/group-selection.spec.js
git commit -m "feat(web): give a selected group a URL"
```

---

### Task 12: Ship it — cache bust, parity bookkeeping, full gate

Without the service-worker bump, returning users keep running the old JS from cache: `handleShellAsset` (`sw.js:182`) is cache-first and `activate` (`sw.js:21`) only evicts when `CACHE_VERSION` changes.

**Files:**
- Modify: `internal/web/static/sw.js:1`
- Modify: `internal/web/server_test.go:165`
- Modify: `tests/web/PARITY_MATRIX.md`
- Modify: `tests/web/e2e/parity-actions.spec.js:21,31`

**Interfaces:**
- Consumes: everything above.
- Produces: a green `make ci`.

- [ ] **Step 1: Bump the cache version**

`internal/web/static/sw.js` line 1:

```js
const CACHE_VERSION = "agentdeck-shell-v9"
```

`internal/web/server_test.go` line 165:

```go
	if !strings.Contains(rr.Body.String(), `agentdeck-shell-v9`) {
```

- [ ] **Step 2: Verify the pin test catches drift**

Run: `go test ./internal/web/ -run TestServiceWorkerServed -race -count=1`
Expected: PASS

- [ ] **Step 3: Update the parity matrix**

In `tests/web/PARITY_MATRIX.md`, under `**GROUP OPERATIONS**`, add:

```markdown
| Select group | `internal/ui/home.go:8486` (`j`/`k` onto a group row) | N/A (client state) | N/A | `tests/web/e2e/group-selection.spec.js` | Sidebar group name selects; chevron collapses |
| Group stats panel | `internal/ui/home.go:19382` (`renderGroupPreview`) | GET `/api/menu` | N/A | `tests/web/e2e/group-selection.spec.js` | Web folds starting→running, queued→idle; no worktree block |
| New session in group (prefilled) | `internal/ui/home.go:9271` (`n`) + `:12325` (`N`) | POST `/api/sessions` `groupPath` | `CreateSession` | `handlers_sessions_test.go`, `group-selection.spec.js` | Folder from group `defaultPath`, tool from newest session in group |
```

- [ ] **Step 4: Re-pin the drift counters**

Run the parity spec to learn the real new counts rather than guessing:

Run: `cd tests/web && npx playwright test e2e/parity-actions.spec.js 2>&1 | tail -20`
Expected: FAIL with a message naming the new count, e.g. `PARITY_MATRIX.md changed action row count from 51 to 54`.

Set `EXPECTED_ACTION_ROWS` at `tests/web/e2e/parity-actions.spec.js:21` to the reported number. If the failure also names a new `EXPECTED_PROBEABLE_MISSING`, update line 31 too.

Re-run: `cd tests/web && npx playwright test e2e/parity-actions.spec.js 2>&1 | tail -20`
Expected: PASS

- [ ] **Step 5: Refresh visual baselines**

The new `.sel` group style and the group panel change screenshots.

Run: `cd tests/web && npm run test:e2e:update-snapshots 2>&1 | tail -20`

Then inspect the regenerated files under `tests/web/screenshots/` with `git diff --stat` and confirm every change is one you intended. Revert and investigate anything unexpected.

- [ ] **Step 6: Full gate**

Run:

```bash
cd /Users/dbeaudoin/workspace/tools/agent-deck
go test -race ./...
make test-web
make ci
```

Expected: all PASS. `make ci` runs css-verify → lint → build → test → yaml-lint serially.

- [ ] **Step 7: Commit**

```bash
git add internal/web/static/sw.js internal/web/server_test.go \
        tests/web/PARITY_MATRIX.md tests/web/e2e/parity-actions.spec.js tests/web/screenshots
git commit -m "chore(web): bump sw cache, record group parity rows, refresh baselines"
```

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| A. Server — `MenuGroup.DefaultPath`, read the field | 1 |
| A. Server — `/g/` route + static-file guard | 11 |
| B. `selectedGroupSignal` + setters | 3 |
| B. dialog signal shape | 9 |
| C. `sidebarFilterSignal`, `groupExpandedSignal`, localStorage | 4 |
| C. `sidebarRowsSignal` | 4 |
| C. `groupStats` | 6 |
| C. `projectGroup` passthrough (`defaultPath`, `level`, `name`) | 2 |
| D. Split hit target, `.sel` styling, `data-row-key` | 5 |
| E. `GroupStatsPanel`, `TerminalPanel` branch | 7 |
| E. `WorkHead` / `RightRail` group branches | 7 |
| E. divergences (starting/queued, archived, no rollup, no worktree block) | 6 (logic), 7 (comment) |
| F. dialog seeding + `groupPath` payload | 8, 9 |
| G. keyboard | 10 |
| H. routing | 11 |
| I. tests | every task |
| Bookkeeping (sw.js, parity pins, matrix) | 12 |

No spec requirement is unassigned.

**Type consistency:** `selectedGroupSignal`, `selectSession`, `selectGroup`, `sidebarFilterSignal`, `groupExpandedSignal`, `sidebarRowsSignal`, `isGroupOpen`, `sessionMatches`, `statusBucket`, `GROUP_STATUS_BUCKETS`, `groupStats`, `groupCreateDefaults`, `currentGroupPath`, `openCreateSessionForGroup`, `GroupStatsPanel`, `applyPath` — each is defined in exactly one task and referenced with the same name and signature everywhere after. Row objects use `key` / `type` / `path` / `group` / `memberCount` / `id` / `session` consistently between Task 4 (producer), Task 5 (renderer) and Task 10 (navigator).

**Ordering:** 3 before 5/10 (setters), 4 before 5/10 (rows), 2 before 6/8 (group fields), 6 before 7 (stats), 8 before 9/10 (defaults helper), 9 before 10's `n` assertion (dialog context). Task 7's "New session in this group" button is deliberately introduced in Task 9, once `openCreateSessionForGroup` exists.

**Known ripples, all handled in Task 10 Step 5:**

- `tests/web/e2e/keyboard-parity.spec.js:63-95` — the `j`/`k` tests wait for
  `.sess.sel` after a single `j`, which now selects a group header instead.
  Concrete replacements are in the plan.
- `tests/web/e2e/toasts.spec.js:76-92` — presses `r` with nothing selected and
  depends on the `sessions[0]` fallback that Task 10 removes. Concrete
  replacement is in the plan.
- `tests/web/e2e/keyboard-extras.spec.js` and `close-undo.spec.js` also press
  keys that route through `focusedSession()`; verified as candidates but not
  read line-by-line. Task 10 Step 6 runs them and gives the fix pattern.
