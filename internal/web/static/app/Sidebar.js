// Sidebar.js -- REWRITE. Status filters + groups + sessions list.
//
// Drops the old Tailwind Sidebar (still present in SessionList.js / SessionRow.js
// / GroupRow.js but no longer mounted). New design: bundle's `.sidebar` class
// stack with side-head / side-filter / side-list / sess rows.
//
// Action handlers route through apiFetch; mutations gated by mutationsEnabledSignal.
import { html } from 'htm/preact'
import { useState, useMemo } from 'preact/hooks'
import { Icon, ICONS, Dot, kindSigil } from './icons.js'
import { menuModelSignal, sidebarRowsSignal, isGroupOpen, openCreateSessionForGroup, currentGroupPath } from './dataModel.js'
import {
  selectedIdSignal, selectedGroupSignal, selectSession, selectGroup,
  mutationsEnabledSignal, confirmDialogSignal,
  editSessionDialogSignal,
} from './state.js'
import {
  statusFiltersSignal, showColsSignal, activeTabSignal,
  sidebarFilterSignal, groupExpandedSignal,
} from './uiState.js'
import { apiFetch } from './api.js'
import { addToast } from './Toast.js'
import { formatRelativeTime } from './timeFmt.js'

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
  if (action === 'archive') {
    confirmDialogSignal.value = {
      message: `Archive session "${s.title}"? The process will be stopped and hidden from the active list.`,
      onConfirm: () => apiFetch('POST', `/api/sessions/${id}/archive`)
        .then(() => {
          if (selectedIdSignal.value === id) {
            selectSession(null)
            if (window.location.pathname.startsWith('/s/')) {
              history.replaceState(null, '', '/')
            }
          }
        })
        .catch(() => {}),
    }
  }
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

function SessionItem({ s, sel, rowKey, onSelect, showCols }) {
  const [exp, setExp] = useState(false)
  const mcpCount = (s.mcps || []).length
  const skillCount = (s.skills || []).length
  const hasSubline =
    (showCols.branch && s.branch && s.branch !== '—') ||
    (showCols.attach && (mcpCount > 0 || skillCount > 0)) ||
    (showCols.sandbox && (s.sandbox || s.worktree)) ||
    showCols.lastSeen
  return html`
    <div class=${`sess ${sel ? 'sel' : ''} ${s.kind} ${exp ? 'exp' : ''}`}
         data-row-key=${rowKey}
         aria-selected=${!!sel}
         onClick=${() => onSelect(s.id)}>
      <span class="sig">${kindSigil(s.kind)}</span>
      <div class="titleline">
        <${Dot} status=${s.status}/>
        <span class="tt">${s.title}</span>
      </div>
      <div class="meta">
        ${showCols.tool && s.tool && html`<span class="tag">${s.tool}</span>`}
        ${showCols.cost && s.cost > 0 && html`<span class="cost">$${s.cost.toFixed(2)}</span>`}
        <button class="row-chev" title="Details" onClick=${e => { e.stopPropagation(); setExp(v => !v) }}>
          ${exp ? '▾' : '▸'}
        </button>
      </div>
      ${hasSubline && html`
        <div class="subline">
          ${showCols.branch && s.branch && s.branch !== '—' && html`<span class="trunc"><span class="b">git</span> ${s.branch}</span>`}
          ${showCols.attach && mcpCount > 0 && html`<span class="att-count">${mcpCount} mcp${mcpCount > 1 ? 's' : ''}</span>`}
          ${showCols.attach && skillCount > 0 && html`<span class="att-count skill">${skillCount} skill${skillCount > 1 ? 's' : ''}</span>`}
          ${showCols.sandbox && s.sandbox && html`<span class="att-count warn">docker</span>`}
          ${showCols.sandbox && s.worktree && html`<span class="att-count">worktree</span>`}
          ${showCols.lastSeen && html`<span class="att-count" title="Last active">⏱ ${s.status === 'running' ? 'active now' : formatRelativeTime(s.lastAccessedAt)}</span>`}
        </div>
      `}
      ${exp && html`
        <div class="row-detail" onClick=${e => e.stopPropagation()}>
          <div class="rd-row"><span class="rd-k">tool</span><span class="rd-v">${s.tool || '—'}</span></div>
          ${s.branch && s.branch !== '—' && html`<div class="rd-row"><span class="rd-k">branch</span><span class="rd-v">${s.branch}</span></div>`}
          ${s.path && html`<div class="rd-row"><span class="rd-k">path</span><span class="rd-v" title=${s.path}>${s.path}</span></div>`}
          ${s.cost > 0 && html`<div class="rd-row"><span class="rd-k">cost</span><span class="rd-v ok">$${s.cost.toFixed(2)}</span></div>`}
        </div>
      `}
      <div class="actions" onClick=${e => e.stopPropagation()}>
        ${(s.status === 'running' || s.status === 'waiting')
          ? html`<button class="mini" title="Stop" data-testid="session-stop-btn" onClick=${() => doAction('stop', s)}><${Icon} d=${ICONS.stop} size=${12}/></button>`
          : html`<button class="mini good" title="Start" data-testid="session-start-btn" onClick=${() => doAction('start', s)}><${Icon} d=${ICONS.play} size=${12}/></button>`}
        <button class="mini good" title="Restart" data-testid="session-restart-btn" onClick=${() => doAction('restart', s)}><${Icon} d=${ICONS.restart} size=${12}/></button>
        <button class="mini" title="Edit" data-testid="edit-session-btn" onClick=${() => doAction('edit', s)}><${Icon} d=${ICONS.edit} size=${12}/></button>
        ${s.canFork && html`<button class="mini fork" title="Fork" data-testid="session-fork-btn" onClick=${() => doAction('fork', s)}><${Icon} d=${ICONS.fork} size=${12}/></button>`}
        ${s.worktree && html`<button class="mini" title="Finish worktree (merge + cleanup)" onClick=${() => doAction('worktreeFinish', s)} data-action="worktree-finish" data-testid="session-worktree-finish-btn">⎇✓</button>`}
        <button class="mini" title="Archive" onClick=${() => doAction('archive', s)}>⌂</button>
        <button class="mini danger" title="Delete" data-testid="session-delete-btn" onClick=${() => doAction('delete', s)}><${Icon} d=${ICONS.trash} size=${12}/></button>
      </div>
    </div>
  `
}

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

  return html`
    <div class="sidebar">
      <div class="side-head">
        <span class="label">SESSIONS</span>
        <span class="count">${totalVisible}</span>
        <div class="spacer"/>
        <div style="position: relative;">
          <button class=${`icon-btn ${showMenu ? 'active' : ''}`} title="Show columns" aria-label="Show columns"
                  data-testid="show-cols-btn"
                  onClick=${() => setShowMenu(m => !m)}>
            <${Icon} d=${ICONS.filter}/>
          </button>
          ${showMenu && html`
            <div class="show-menu" data-testid="show-cols-menu" onClick=${e => e.stopPropagation()}>
              <div class="sm-head">SHOW IN ROW</div>
              ${SHOW_COL_OPTIONS.map(c => html`
                <label key=${c.id} class="sm-row" data-testid=${`show-col-${c.id}`}>
                  <input type="checkbox" checked=${!!showCols[c.id]} onChange=${() => setShowCol(c.id)}/>
                  <span>${c.label}</span>
                </label>
              `)}
              <div class="sm-foot" onClick=${() => setShowMenu(false)}>done</div>
            </div>
          `}
        </div>
        ${mutationsEnabledSignal.value && html`
          <button class="icon-btn" title="New session (n)" aria-label="New session"
                  onClick=${() => openCreateSessionForGroup(currentGroupPath())}>
            <${Icon} d=${ICONS.plus}/>
          </button>
        `}
      </div>
      <div class="side-filter">
        <input
          placeholder="/ filter"
          data-testid="sidebar-filter-input"
          value=${filter}
          onInput=${e => (sidebarFilterSignal.value = e.target.value)}
        />
        ${STATUS_CHIPS.map(s => html`
          <span key=${s.id}
                class=${`side-chip ${statusFilters.includes(s.id) ? 'on' : ''}`}
                data-testid=${`status-chip-${s.id}`}
                onClick=${() => toggleStatus(s.id)}
                title=${s.id}>
            ${s.sym}
          </span>
        `)}
      </div>
      <div class="side-list">
        ${rows.map(r => r.type === 'group'
          ? html`
            <div key=${r.key}
                 class=${`side-group-head ${r.group.kind || ''} ${selectedGroup === r.path ? 'sel' : ''}`}
                 data-testid=${`group-head-${r.path}`}
                 data-row-key=${r.key}
                 aria-selected=${selectedGroup === r.path}
                 onClick=${() => { selectGroup(r.path); activeTabSignal.value = 'terminal' }}>
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
    </div>
  `
}
