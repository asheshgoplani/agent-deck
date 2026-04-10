// SessionList.js -- Renders groups + sessions from sessionsSignal
import { html } from 'htm/preact'
import { useEffect } from 'preact/hooks'
import { sessionsSignal, sessionsLoadedSignal, selectedIdSignal, authTokenSignal, sessionCostsSignal, focusedIdSignal, searchQuerySignal } from './state.js'
import { isGroupExpanded, groupExpandedSignal } from './groupState.js'
import { GroupRow } from './GroupRow.js'
import { SessionRow } from './SessionRow.js'
import { useKeyboardNav } from './useKeyboardNav.js'
import { useDebounced } from './useDebounced.js'
import { useVirtualList } from './useVirtualList.js'

// Fetch batch costs once after the session list first loads.
// PERF-I: POST /api/costs/batch with a JSON body {ids:[...]} so long lists
// do not hit the 414 URI Too Long limit that the GET + query-string form
// would trigger once the sidebar grows past ~50 sessions.
let costsFetched = false
async function fetchBatchCosts(items) {
  if (costsFetched) return
  const ids = (items || [])
    .filter(i => i.type === 'session' && i.session)
    .map(i => i.session.id)
  if (ids.length === 0) return
  costsFetched = true

  const headers = {
    Accept: 'application/json',
    'Content-Type': 'application/json',
  }
  const token = authTokenSignal.value
  if (token) headers.Authorization = 'Bearer ' + token

  try {
    const res = await fetch('/api/costs/batch', {
      method: 'POST',
      headers,
      body: JSON.stringify({ ids }),
    })
    if (!res.ok) return
    const data = await res.json()
    sessionCostsSignal.value = data.costs || {}
  } catch (_) {
    // Cost badges unavailable; fail silently
  }
}

// hasCollapsedAncestor walks from the root down to (and including) the given
// path and returns true if any of those nodes is collapsed. Callers pass a
// session's groupPath here, because a session's groupPath IS its direct
// parent group and a collapsed parent must hide the session.
function hasCollapsedAncestor(path) {
  if (!path) return false
  // Read the signal to subscribe
  void groupExpandedSignal.value
  const parts = path.split('/')
  for (let i = 1; i <= parts.length; i++) {
    const ancestor = parts.slice(0, i).join('/')
    if (!isGroupExpanded(ancestor, true)) return true
  }
  return false
}

// hasCollapsedStrictAncestor walks only the STRICT ancestors of the given
// path (i.e. excludes the path itself). A group must never hide itself just
// because its own collapse state is false — its own state governs whether
// its children are shown, not whether IT is shown. This closes BUG #1 /
// CRIT-01: collapsing a top-level group (parts.length === 1) previously made
// hasCollapsedAncestor(group.path) return true and the group vanished from
// the sidebar.
function hasCollapsedStrictAncestor(path) {
  if (!path) return false
  // Read the signal to subscribe
  void groupExpandedSignal.value
  const parts = path.split('/')
  for (let i = 1; i < parts.length; i++) {
    const ancestor = parts.slice(0, i).join('/')
    if (!isGroupExpanded(ancestor, true)) return true
  }
  return false
}

function fuzzyMatch(text, query) {
  if (!query) return true
  const lower = (text || '').toLowerCase()
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean)
  return terms.every(term => lower.includes(term))
}

function matchesSearch(item, query) {
  if (!query) return true
  if (item.type === 'group' && item.group) {
    return fuzzyMatch(item.group.name + ' ' + item.group.path, query)
  }
  if (item.type === 'session' && item.session) {
    const s = item.session
    return fuzzyMatch([s.title, s.id, s.groupPath, s.path, s.tool].join(' '), query)
  }
  return true
}

export function SessionList() {
  const items = sessionsSignal.value
  const focusedId = focusedIdSignal.value
  // PERF-F: debounce the search term by 250 ms so the filter closure does
  // not rerun on every keystroke. The raw signal still updates immediately
  // so the search input stays responsive; only the downstream filter is
  // delayed. useDebounced returns the raw value on the first render so
  // the filter has something to match against before the timer fires.
  const rawQuery = searchQuerySignal.value
  const query = useDebounced(rawQuery, 250)

  useKeyboardNav()

  // Trigger batch cost fetch on first non-empty items
  useEffect(() => {
    if (items && items.length > 0) fetchBatchCosts(items)
  }, [items && items.length])

  // Signal Preact has taken over session list rendering
  useEffect(() => {
    window.__preactSessionListActive = true
    return () => { window.__preactSessionListActive = false }
  }, [])

  // POL-1 (Phase 9 plan 01): skeleton gate. Show a layout-matched placeholder
  // stack until the first /api/menu response OR SSE `menu` snapshot flips
  // sessionsLoadedSignal to true. The skeleton uses Tailwind's built-in
  // `animate-pulse` and respects `prefers-reduced-motion` via
  // `motion-reduce:animate-none` — no library, no CSS additions beyond what
  // Tailwind v4 already emits. 8 placeholder rows is a sensible first-screen
  // guess. When the user is searching we bypass the skeleton: a visible
  // pulse stack while they type would feel broken.
  if (!sessionsLoadedSignal.value && !query) {
    return html`
      <ul
        data-testid="sidebar-skeleton"
        class="flex flex-col gap-0.5 py-sp-4 min-w-0"
        role="list"
        aria-label="Loading sessions"
        aria-busy="true"
      >
        ${[0, 1, 2, 3, 4, 5, 6, 7].map(i => html`
          <li
            key=${i}
            class="flex items-center gap-sp-8 px-sp-12 py-1.5 min-h-[40px] animate-pulse motion-reduce:animate-none"
          >
            <span class="w-2.5 h-2.5 rounded-full flex-shrink-0 dark:bg-tn-muted/30 bg-gray-300"></span>
            <span class="flex-1 h-3 rounded dark:bg-tn-muted/30 bg-gray-300"></span>
            <span class="w-10 h-3 rounded dark:bg-tn-muted/30 bg-gray-300"></span>
          </li>
        `)}
      </ul>
    `
  }

  // When searching, show all matching sessions (ignore group collapse state)
  const filtered = query
    ? items.filter(item => matchesSearch(item, query))
    : items

  if (!filtered || filtered.length === 0) {
    return html`<div class="px-sp-12 py-sp-16 dark:text-tn-muted text-gray-600 text-sm">
      ${query ? 'No matching sessions' : 'No sessions'}
    </div>`
  }

  // Apply the group-collapse visibility filter BEFORE handing the list to
  // the virtualizer. Otherwise the virtual window would budget space for
  // hidden rows and the scroll surface would be larger than the rendered
  // content. The small cost of two filter passes (one here, one in the
  // non-virtualized branch) is acceptable — for lists > 50 the cost is
  // dwarfed by the DOM node savings.
  const visible = []
  for (const item of filtered) {
    if (item.type === 'group' && item.group) {
      if (!query && hasCollapsedStrictAncestor(item.group.path)) continue
      visible.push(item)
    } else if (item.type === 'session' && item.session) {
      if (!query && hasCollapsedAncestor(item.session.groupPath)) continue
      visible.push(item)
    }
  }

  // PERF-K: opt-in virtualization. Gated at visible.length > 50 AND the
  // localStorage feature flag `agentdeck_virtualize === '1'`. Below either
  // threshold the original non-virtualized render path runs unchanged, so
  // the default experience is untouched. Power users with many sessions
  // can flip the flag to get a windowed render. Rules-of-hooks are
  // satisfied by always routing through VirtualizedList (which always
  // calls useVirtualList) when virtualization is on — extracting to a
  // dedicated component means the non-virtualized path never calls
  // useVirtualList at all, so the hook order stays stable per branch.
  const virtualizationEnabled =
    visible.length > 50 &&
    typeof localStorage !== 'undefined' &&
    localStorage.getItem('agentdeck_virtualize') === '1'

  if (virtualizationEnabled) {
    return html`<${VirtualizedList} items=${visible} focusedId=${focusedId} />`
  }

  return html`<ul class="flex flex-col gap-0.5 py-sp-4 min-w-0" role="list" id="preact-session-list">
    ${visible.map(item => {
      if (item.type === 'group' && item.group) {
        return html`<${GroupRow} key=${item.group.path} item=${item} />`
      }
      if (item.type === 'session' && item.session) {
        const isFocused = focusedId === item.session.id
        return html`<${SessionRow} key=${item.session.id} item=${item} focused=${isFocused} />`
      }
      return null
    })}
  </ul>`
}

// VirtualizedList is the PERF-K windowed render path. Lives in the same
// file as SessionList so the rules-of-hooks argument is local: this
// component always calls useVirtualList, the parent decides whether to
// mount it at all. Per ROADMAP.md Open Question 5, the > 50 gate is
// enforced by the caller; this component assumes items is already the
// visible (post collapse-filter) slice.
function VirtualizedList({ items, focusedId }) {
  const { virtualItems, totalHeight, containerProps } = useVirtualList({
    items,
    estimateSize: (item) => (item && item.type === 'group' ? 44 : 40),
    overscan: 6,
  })

  return html`
    <div
      ...${containerProps}
      id="preact-session-list"
      class="flex-1 min-h-0"
    >
      <div style=${{ height: totalHeight + 'px', position: 'relative' }}>
        ${virtualItems.map(({ index, item, offset, size }) => {
          const key = item && item.type === 'group'
            ? item.group.path
            : (item && item.session ? item.session.id : String(index))
          return html`
            <div
              key=${key}
              role="listitem"
              aria-rowindex=${index + 1}
              style=${{
                position: 'absolute',
                top: offset + 'px',
                left: 0,
                right: 0,
                height: size + 'px',
              }}
            >
              ${item && item.type === 'group' && item.group
                ? html`<${GroupRow} item=${item} />`
                : item && item.session
                  ? html`<${SessionRow} item=${item} focused=${focusedId === item.session.id} />`
                  : null}
            </div>
          `
        })}
      </div>
    </div>
  `
}
