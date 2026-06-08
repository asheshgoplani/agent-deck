// Sidebar.js -- Nested groups + sub-sessions render (web↔TUI parity, Task 8).
//
// Renders the server's ordered, interleaved `items` list (menuModelSignal.items)
// in a single pass, indenting each row by its `level`. Groups collapse
// recursively (collapsing a parent hides ALL descendant subgroups + sessions).
// Filters compose: status chips + text filter + archived toggle, all applied to
// SESSION items only (group rows always render).
//
// Per-row kebab menu surfaces the new parity actions (Edit, Archive/Unarchive,
// Restart fresh, Mark unread, Quick approve, Copy info) alongside the existing
// quick inline actions (start/stop/restart/fork/worktree-finish/delete). Group
// headers gain a kebab with New subgroup / Rename / Move / Delete affordances.
//
// All mutating apiFetch calls .catch(()=>{}) (apiFetch already toasts) and are
// gated behind mutationsEnabledSignal (mirroring doAction). Styling reuses
// existing app.css classes (.sess, .side-group-head, .show-menu, .mini, .tag,
// .icon-btn); genuinely-new bits (indentation, nesting guide line, archived
// dimming) use inline styles so no new CSS class is required.
import { html } from 'htm/preact'
import { useState, useMemo, useEffect, useRef } from 'preact/hooks'
import { Icon, ICONS, Dot, kindSigil } from './icons.js'
import { menuModelSignal, displaySessionTitle } from './dataModel.js'
import {
  selectedIdSignal, mutationsEnabledSignal, confirmDialogSignal,
  createSessionDialogSignal, groupNameDialogSignal, editSessionDialogSignal,
  sidebarOpenSignal,
} from './state.js'
import { statusFiltersSignal, showColsSignal, showArchivedSignal, activeTabSignal } from './uiState.js'
import { NAV_TABS } from './tabs.js'
import { apiFetch } from './api.js'
import { addToast } from './Toast.js'

const STATUS_CHIPS = [
  { id: 'running', sym: '●' },
  { id: 'waiting', sym: '◐' },
  { id: 'error',   sym: '✕' },
  { id: 'idle',    sym: '○' },
]

const SHOW_COL_OPTIONS = [
  { id: 'tool',     label: 'Tool badge' },
  { id: 'cost',     label: 'Cost' },
  { id: 'branch',   label: 'Git branch' },
  { id: 'attach',   label: 'MCPs / skills' },
  { id: 'sandbox',  label: 'Docker / worktree' },
  { id: 'lastSeen', label: 'Last activity' },
]

// Indentation geometry. Base matches the existing .sess left padding (16px);
// each nesting level adds a step. The nesting guide line is a left border on
// indented rows (level > 0) drawn inside the indentation gutter.
const INDENT_BASE = 16
const INDENT_STEP = 14

function rowPad(level) {
  return INDENT_BASE + Math.max(0, level) * INDENT_STEP
}

// A mutating action wrapper mirroring the historic doAction(): gate on
// mutationsEnabledSignal, swallow rejected promises (apiFetch already toasts).
function mutate(fn) {
  if (!mutationsEnabledSignal.value) {
    addToast('mutations disabled')
    return
  }
  fn()
}

function doAction(action, s) {
  if (!mutationsEnabledSignal.value) {
    addToast('mutations disabled')
    return
  }
  const id = s.id
  if (action === 'start')   return apiFetch('POST', `/api/sessions/${id}/start`).catch(() => {})
  if (action === 'stop')    return apiFetch('POST', `/api/sessions/${id}/stop`).catch(() => {})
  if (action === 'restart') return apiFetch('POST', `/api/sessions/${id}/restart`).catch(() => {})
  if (action === 'fork')    return apiFetch('POST', `/api/sessions/${id}/fork`, { title: s.title + '-fork' }).catch(() => {})
  if (action === 'delete') {
    confirmDialogSignal.value = {
      message: `Delete session "${s.title}"? This stops the tmux session and removes metadata.`,
      onConfirm: () => apiFetch('DELETE', `/api/sessions/${id}`).catch(() => {}),
    }
  }
  if (action === 'worktreeFinish') {
    // Issue #1126 — POST /api/sessions/{id}/worktree/finish. Mirrors TUI
    // W/shift+w. Body left empty so the backend auto-detects target
    // branch and uses default flags (merge + delete branch).
    const branch = s.worktreeBranch || s.branch
    confirmDialogSignal.value = {
      message: `Finish worktree for "${s.title}"? Merges branch "${branch}" into default branch, removes worktree, deletes branch, and removes session.`,
      onConfirm: () => apiFetch('POST', `/api/sessions/${id}/worktree/finish`).catch(() => {}),
    }
  }
  if (action === 'edit') {
    editSessionDialogSignal.value = { sessionId: id }
  }
}

// Build a short, human-readable info block for the "Copy info" action.
function sessionInfoText(s) {
  const lines = [
    `id: ${s.id}`,
    `title: ${displaySessionTitle(s) || s.title}`,
    s.tool && `tool: ${s.tool}`,
    s.path && `path: ${s.path}`,
    s.branch && s.branch !== '—' && `branch: ${s.branch}`,
    s.group && `group: ${s.group}`,
  ].filter(Boolean)
  return lines.join('\n')
}

function copySessionInfo(s) {
  const text = sessionInfoText(s)
  if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
    navigator.clipboard.writeText(text).then(
      () => addToast('Copied session info', 'success'),
      () => addToast('Clipboard unavailable'),
    )
  } else {
    addToast('Clipboard unavailable')
  }
}

// SessionRow renders one session item. `menuOpen` / `onMenuToggle` lift the
// kebab open-state into Sidebar (single menu open at a time, queryable in tests).
function SessionRow({ s, level, sel, onSelect, showCols, mutable, menuOpen, onMenuToggle }) {
  const mcpCount = (s.mcps || []).length
  const skillCount = (s.skills || []).length
  const hasSubline =
    (showCols.branch && s.branch && s.branch !== '—') ||
    (showCols.attach && (mcpCount > 0 || skillCount > 0)) ||
    (showCols.sandbox && (s.sandbox || s.worktree))
  const label = displaySessionTitle(s) || s.title

  const closeMenu = () => onMenuToggle(null)
  const runFromMenu = (fn) => { closeMenu(); mutate(fn) }

  // Inline indentation + nesting guide line. Sub-sessions / nested rows carry a
  // subtle left border inside the indentation gutter (web-native, not ASCII).
  const style = {
    paddingLeft: rowPad(level) + 'px',
    ...(level > 0 ? { borderLeft: '2px solid var(--border)' } : {}),
    ...(s.archived ? { opacity: 0.5 } : {}),
  }

  return html`
    <div class=${`sess ${sel ? 'sel' : ''} ${s.kind} ${s.isSubSession ? 'sub' : ''}`}
         style=${style}
         data-level=${level}
         data-session-id=${s.id}
         data-archived=${s.archived ? 'true' : 'false'}
         onClick=${() => onSelect(s.id)}>
      <span class="sig">${kindSigil(s.kind)}</span>
      <div class="titleline">
        <${Dot} status=${s.status}/>
        <span class="tt" title=${label}>${label}</span>
        ${s.archived && html`<span class="tag" data-archived-badge="true">[archived]</span>`}
      </div>
      <div class="meta">
        ${showCols.tool && s.tool && html`<span class="tag">${s.tool}</span>`}
        ${showCols.cost && s.cost > 0 && html`<span class="cost">$${s.cost.toFixed(2)}</span>`}
        <button class="row-chev" title="Actions" aria-label="Session actions" data-kebab="true"
                onClick=${e => { e.stopPropagation(); onMenuToggle(menuOpen ? null : s.id) }}>⋯</button>
      </div>
      ${hasSubline && html`
        <div class="subline">
          ${showCols.branch && s.branch && s.branch !== '—' && html`<span class="trunc"><span class="b">git</span> ${s.branch}</span>`}
          ${showCols.attach && mcpCount > 0 && html`<span class="att-count">${mcpCount} mcp${mcpCount > 1 ? 's' : ''}</span>`}
          ${showCols.attach && skillCount > 0 && html`<span class="att-count skill">${skillCount} skill${skillCount > 1 ? 's' : ''}</span>`}
          ${showCols.sandbox && s.sandbox && html`<span class="att-count warn">docker</span>`}
          ${showCols.sandbox && s.worktree && html`<span class="att-count">worktree</span>`}
        </div>
      `}
      <div class="actions" onClick=${e => e.stopPropagation()}>
        ${(s.status === 'running' || s.status === 'waiting')
          ? html`<button class="mini" title="Stop" onClick=${() => doAction('stop', s)}><${Icon} d=${ICONS.stop} size=${12}/></button>`
          : html`<button class="mini good" title="Start" onClick=${() => doAction('start', s)}><${Icon} d=${ICONS.play} size=${12}/></button>`}
        <button class="mini good" title="Restart" onClick=${() => doAction('restart', s)}><${Icon} d=${ICONS.restart} size=${12}/></button>
        <button class="mini" title="Edit" data-testid="edit-session-btn" onClick=${() => doAction('edit', s)}><${Icon} d=${ICONS.edit} size=${12}/></button>
        ${s.canFork && html`<button class="mini fork" title="Fork" onClick=${() => doAction('fork', s)}><${Icon} d=${ICONS.fork} size=${12}/></button>`}
        ${s.worktree && html`<button class="mini" title="Finish worktree (merge + cleanup)" onClick=${() => doAction('worktreeFinish', s)} data-action="worktree-finish">⎇✓</button>`}
        <button class="mini danger" title="Delete" onClick=${() => doAction('delete', s)}><${Icon} d=${ICONS.trash} size=${12}/></button>
      </div>
      ${menuOpen && html`
        <div class="show-menu" data-row-menu="true" style="width: 200px;"
             onClick=${e => e.stopPropagation()}>
          <div class="sm-head">SESSION</div>
          ${mutable && html`
            <div class="sm-row" data-act="edit" onClick=${() => { closeMenu(); editSessionDialogSignal.value = { sessionId: s.id } }}>
              <span>Edit…</span>
            </div>
            <div class="sm-row" data-act="move" onClick=${() => { closeMenu(); editSessionDialogSignal.value = { sessionId: s.id } }}>
              <span>Move to group…</span>
            </div>
            ${s.archived
              ? html`<div class="sm-row" data-act="unarchive" onClick=${() => runFromMenu(() => apiFetch('DELETE', `/api/sessions/${s.id}/archive`).catch(() => {}))}><span>Unarchive</span></div>`
              : html`<div class="sm-row" data-act="archive" onClick=${() => runFromMenu(() => apiFetch('POST', `/api/sessions/${s.id}/archive`).catch(() => {}))}><span>Archive</span></div>`}
            <div class="sm-row" data-act="restart-fresh" onClick=${() => runFromMenu(() => apiFetch('POST', `/api/sessions/${s.id}/restart-fresh`).catch(() => {}))}>
              <span>Restart fresh</span>
            </div>
            <div class="sm-row" data-act="unread" onClick=${() => runFromMenu(() => apiFetch('POST', `/api/sessions/${s.id}/unread`).catch(() => {}))}>
              <span>Mark unread</span>
            </div>
            <div class="sm-row" data-act="approve" onClick=${() => runFromMenu(() => apiFetch('POST', `/api/sessions/${s.id}/approve`).catch(() => {}))}>
              <span>Quick approve</span>
            </div>
          `}
          <div class="sm-row" data-act="copy" onClick=${() => { closeMenu(); copySessionInfo(s) }}>
            <span>Copy info</span>
          </div>
          <div class="sm-foot" onClick=${closeMenu}>close</div>
        </div>
      `}
    </div>
  `
}

// GroupRow renders one group header with a chevron, count badge, "new session"
// (+) button, and a kebab menu of group affordances (New subgroup / Rename /
// Move / Delete). Default group is detected (path === '' || 'default') to hide
// rename/move/delete (the backend rejects default-group delete anyway).
function GroupRow({ g, level, open, onToggle, mutable, menuOpen, onMenuToggle }) {
  const isDefault = g.path === '' || g.path === 'default'
  const name = g.label || g.path
  const closeMenu = () => onMenuToggle(null)
  // position: relative anchors the absolutely-positioned kebab .show-menu to
  // this header. .side-group-head has no `position` in app.css, so without
  // this the dropdown would escape to the viewport's initial containing block.
  const style = {
    position: 'relative',
    paddingLeft: rowPad(level) + 'px',
    ...(level > 0 ? { borderLeft: '2px solid var(--border)' } : {}),
  }
  return html`
    <div class=${`side-group-head ${g.kind || ''}`}
         style=${style}
         data-level=${level}
         data-group-path=${g.path}
         onClick=${() => onToggle(g.path)}>
      <span class="chev">${open ? '▾' : '▸'}</span>
      <span class="name">${name}</span>
      <span class="badge">(${g.sessionCount})</span>
      ${mutable && html`
        <button class="side-group-new" title="New session in group" aria-label="New session in group"
                onClick=${e => { e.stopPropagation(); createSessionDialogSignal.value = { groupPath: g.path } }}>+</button>
        <button class="side-group-new" title="Group actions" aria-label="Group actions" data-group-kebab="true"
                style="font-size: 13px;"
                onClick=${e => { e.stopPropagation(); onMenuToggle(menuOpen ? null : g.path) }}>⋯</button>
        ${menuOpen && html`
          <div class="show-menu" data-group-menu="true" style="width: 190px;"
               onClick=${e => e.stopPropagation()}>
            <div class="sm-head">GROUP</div>
            <div class="sm-row" data-act="new-subgroup"
                 onClick=${() => { closeMenu(); groupNameDialogSignal.value = { mode: 'create', parentPath: g.path } }}>
              <span>New subgroup…</span>
            </div>
            ${!isDefault && html`
              <div class="sm-row" data-act="rename"
                   onClick=${() => { closeMenu(); groupNameDialogSignal.value = { mode: 'rename', groupPath: g.path, currentName: name } }}>
                <span>Rename…</span>
              </div>
              <div class="sm-row" data-act="reparent"
                   onClick=${() => { closeMenu(); groupNameDialogSignal.value = { mode: 'reparent', groupPath: g.path, currentName: name } }}>
                <span>Move…</span>
              </div>
              <div class="sm-row" data-act="delete"
                   onClick=${() => { closeMenu(); groupNameDialogSignal.value = { mode: 'delete', groupPath: g.path, currentName: name } }}>
                <span>Delete…</span>
              </div>
            `}
            <div class="sm-foot" onClick=${closeMenu}>close</div>
          </div>
        `}
      `}
    </div>
  `
}

export function Sidebar() {
  // Capture every signal .value up front so useMemo dep arrays are honest.
  const { items, sessions, groups } = menuModelSignal.value
  const selected = selectedIdSignal.value
  const statusFilters = statusFiltersSignal.value
  const showCols = showColsSignal.value
  const showArchived = showArchivedSignal.value
  const mutable = mutationsEnabledSignal.value

  const [filter, setFilter] = useState('')
  const [showMenu, setShowMenu] = useState(false)
  const [openRowMenu, setOpenRowMenu] = useState(null)   // session id whose kebab is open
  const [openGroupMenu, setOpenGroupMenu] = useState(null) // group path whose kebab is open
  // Collapsed group paths (Set). Default: respect each group's `expanded` flag.
  const [collapsed, setCollapsed] = useState(() => {
    const set = new Set()
    for (const g of groups) if (g.expanded === false) set.add(g.path)
    return set
  })

  // One-shot seed of the collapse Set from group `expanded` flags. main.js mounts
  // Sidebar BEFORE the menu snapshot lands (loadMenu() is async + unawaited), so
  // the useState initializer above sees an EMPTY `groups` and never re-runs when
  // the signal later populates. Without this effect, groups the user collapsed in
  // the TUI (expanded:false) would render EXPANDED in the browser — a parity
  // regression. We seed exactly once, the first time `groups` becomes non-empty,
  // MERGING the default-collapsed paths into the current Set (via the functional
  // setter) so any toggle the user made before the snapshot arrived is preserved.
  // After seeding, user toggles fully own the state.
  const seededCollapse = useRef(false)
  useEffect(() => {
    if (seededCollapse.current || groups.length === 0) return
    seededCollapse.current = true
    const defaults = groups.filter(g => g.expanded === false).map(g => g.path)
    if (defaults.length === 0) return
    setCollapsed(prev => {
      const next = new Set(prev)
      for (const p of defaults) next.add(p)
      return next
    })
  }, [groups])

  // A session passes the row filters when it matches status + text + archived.
  const matchesSession = (s) => {
    if (!showArchived && s.archived) return false
    if (statusFilters.length && !statusFilters.includes(s.status)) return false
    if (!filter) return true
    const t = filter.toLowerCase()
    return ((s.title || '') + ' ' + (displaySessionTitle(s) || '') + ' ' + (s.group || '') + ' ' +
            (s.path || '') + ' ' + (s.tool || '') + ' ' + (s.branch || ''))
      .toLowerCase().includes(t)
  }

  // Stage 1: filter session items (group items always pass). Preserves order.
  const filteredItems = useMemo(() => {
    return items.filter(it => it.type !== 'session' || matchesSession(it.session))
  }, [items, filter, statusFilters, showArchived])

  const totalVisible = useMemo(
    () => filteredItems.filter(it => it.type === 'session').length,
    [filteredItems],
  )

  const toggleStatus = (id) => {
    const cur = statusFiltersSignal.value
    statusFiltersSignal.value = cur.includes(id) ? cur.filter(x => x !== id) : [...cur, id]
  }
  const toggleGroup = (path) => {
    setOpenGroupMenu(null)
    setCollapsed(prev => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }
  const onSelect = (id) => {
    selectedIdSignal.value = id
    activeTabSignal.value = 'terminal'
    sidebarOpenSignal.value = false   // close the mobile drawer after picking
    setOpenRowMenu(null)
    setOpenGroupMenu(null)
  }
  const setShowCol = (id) => {
    showColsSignal.value = { ...showCols, [id]: !showCols[id] }
  }

  // Stage 2: collapse-walk over the filtered list. Hide every item whose level
  // is deeper than an enclosing collapsed group — recursively, via collapseLevel.
  const rows = []
  let collapseLevel = Infinity
  for (const it of filteredItems) {
    if (it.level > collapseLevel) continue   // inside a collapsed subtree → skip
    collapseLevel = Infinity                 // exited any collapsed subtree
    if (it.type === 'group') {
      const g = it.group
      const isOpen = !collapsed.has(g.path)
      rows.push(html`
        <${GroupRow} key=${'g:' + g.path} g=${g} level=${it.level} open=${isOpen}
          onToggle=${toggleGroup} mutable=${mutable}
          menuOpen=${openGroupMenu === g.path}
          onMenuToggle=${(v) => { setOpenGroupMenu(v); setOpenRowMenu(null) }}/>
      `)
      if (!isOpen) collapseLevel = it.level
    } else {
      const s = it.session
      rows.push(html`
        <${SessionRow} key=${'s:' + s.id} s=${s} level=${it.level}
          sel=${selected === s.id} onSelect=${onSelect} showCols=${showCols} mutable=${mutable}
          menuOpen=${openRowMenu === s.id}
          onMenuToggle=${(v) => { setOpenRowMenu(v); setOpenGroupMenu(null) }}/>
      `)
    }
  }

  return html`
    <div class="sidebar">
      <div class="side-head">
        <span class="label">SESSIONS</span>
        <span class="count">${totalVisible}</span>
        <div class="spacer"/>
        <button class=${`icon-btn ${showArchived ? 'active' : ''}`}
                title=${showArchived ? 'Hide archived' : 'Show archived'}
                aria-label="Toggle archived" aria-pressed=${showArchived ? 'true' : 'false'}
                data-show-archived=${showArchived ? 'true' : 'false'}
                onClick=${() => (showArchivedSignal.value = !showArchivedSignal.value)}>
          <${Icon} d=${ICONS.book}/>
        </button>
        <div style="position: relative;">
          <button class=${`icon-btn ${showMenu ? 'active' : ''}`} title="Show columns" aria-label="Show columns"
                  onClick=${() => setShowMenu(m => !m)}>
            <${Icon} d=${ICONS.filter}/>
          </button>
          ${showMenu && html`
            <div class="show-menu" onClick=${e => e.stopPropagation()}>
              <div class="sm-head">SHOW IN ROW</div>
              ${SHOW_COL_OPTIONS.map(c => html`
                <label key=${c.id} class="sm-row">
                  <input type="checkbox" checked=${!!showCols[c.id]} onChange=${() => setShowCol(c.id)}/>
                  <span>${c.label}</span>
                </label>
              `)}
              <div class="sm-foot" onClick=${() => setShowMenu(false)}>done</div>
            </div>
          `}
        </div>
        ${mutable && html`
          <button class="icon-btn" title="New session (n)" aria-label="New session"
                  onClick=${() => (createSessionDialogSignal.value = true)}>
            <${Icon} d=${ICONS.plus}/>
          </button>
        `}
      </div>
      <div class="side-filter">
        <input
          placeholder="/ filter"
          value=${filter}
          onInput=${e => setFilter(e.target.value)}
        />
        ${STATUS_CHIPS.map(s => html`
          <span key=${s.id}
                class=${`side-chip ${statusFilters.includes(s.id) ? 'on' : ''}`}
                onClick=${() => toggleStatus(s.id)}
                title=${s.id}>
            ${s.sym}
          </span>
        `)}
      </div>
      <div class="side-list">
        ${rows}
        ${sessions.length === 0 && html`
          <div style="padding: 16px; font-family: var(--mono); font-size: 11px; color: var(--muted); text-align: center;">
            No sessions yet. Press <span class="kbd" style="border:1px solid var(--border); padding: 0 4px; border-radius: 3px;">n</span> to create one.
          </div>
        `}
      </div>
      <div class="side-nav">
        ${NAV_TABS.map(t => html`
          <button key=${t.id}
            class=${`side-nav-btn ${activeTabSignal.value === t.id ? 'on' : ''}`}
            onClick=${() => { activeTabSignal.value = t.id; sidebarOpenSignal.value = false }}>
            ${t.label}
          </button>
        `)}
      </div>
    </div>
  `
}
