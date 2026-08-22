// dataModel.js -- Adapt the GET /api/menu response shape into the bundle's session/group model.
//
// The API menu returns interleaved {type:'group'|'session', ...} items. The
// bundle's design treats sessions and groups as separate flat arrays with
// extra fields (kind, mcps, skills, cost, tokens, branch, worktree, sandbox).
//
// We project the API into that shape, defaulting absent fields to safe zeros
// so the design renders without inventing data. Components that need richer
// data (e.g. RightRail Usage card) fall back to "no data" placeholders.
import { computed } from '@preact/signals'
import { sessionsSignal, sessionCostsSignal, selectedIdSignal, selectedGroupSignal, createSessionDialogSignal, archivedSessionsSignal } from './state.js'
import { sidebarFilterSignal, groupExpandedSignal, statusFiltersSignal } from './uiState.js'

// kind heuristic from session metadata (no API field today).
// `tool` is `claude|codex|gemini|shell|webhook|...`; treat anything not in
// the agent set as a watcher. Conductor is detected by group convention.
function deriveKind(s) {
  if (!s || !s.tool) return 'agent'
  if (s.groupPath === 'conductor' || /conductor/i.test(s.title || '')) return 'conductor'
  if (['webhook', 'ntfy', 'slack-watcher'].includes(s.tool)) return 'watcher'
  return 'agent'
}

function projectSession(item) {
  const s = item.session || {}
  const id = s.id || ''
  const groupPath = s.groupPath || ''
  return {
    id,
    kind: deriveKind(s),
    title: s.title || id,
    group: groupPath,
    tool: s.tool || '',
    modelId: s.modelId || '',
    model: s.model || '',
    modelVersion: s.modelVersion || '',
    canFork: !!s.canFork,
    // Server-computed (session.ToolSupportsMCPManager). Default true so a
    // payload predating the field does not hide the MCP pane.
    mcpSupported: s.mcpSupported !== false,
    status: s.status || 'idle',
    branch: s.branch || '—',
    path: s.projectPath || '',
    cost: 0,            // hydrated separately via sessionCostsSignal
    tokens: 0,          // not exposed by API
    mcps: [],           // not exposed by API (TUI-only feature; pane shows stub)
    skills: [],         // not exposed by API (TUI-only feature; pane shows stub)
    children: [],       // not exposed by API
    // worktree: derived from MenuSession.worktreeBranch (issue #1126).
    // When truthy, the UI shows the "Finish worktree" action button so
    // users can merge + clean up from the browser instead of dropping
    // back to the TUI.
    worktree: !!(s.worktreeBranch && s.worktreeRepoRoot),
    worktreeBranch: s.worktreeBranch || '',
    lastAccessedAt: s.lastAccessedAt || '',
    createdAt: s.createdAt || '',
    sandbox: false,     // not exposed by API
    // Sessions from the menu snapshot are active by definition — the server
    // archive-filters it (session_data_service.go:278). archivedByGroupSignal
    // flips this to true for the archived feed. Always present, never
    // undefined, so consumers can branch on it without a guard.
    archived: false,
    parent: null,
    pendingNeeds: 0,
    watcherType: null,
    routes: '',
    events1h: 0,
    meta: '',
    raw: s,
  }
}

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

// Computed derived view: { groups: [...], sessions: [...], byGroup: { path -> sessions[] } }
export const menuModelSignal = computed(() => {
  const items = sessionsSignal.value || []
  const costs = sessionCostsSignal.value || {}
  const groups = []
  const sessions = []
  for (const it of items) {
    if (!it) continue
    if (it.type === 'group') {
      groups.push(projectGroup(it))
    } else if (it.type === 'session') {
      const s = projectSession(it)
      const c = costs[s.id]
      if (typeof c === 'number') s.cost = c
      sessions.push(s)
    }
  }
  // ensure groups encountered via sessionPath also render even if API omitted them
  const seen = new Set(groups.map(g => g.path))
  for (const s of sessions) {
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
  }
  groups.sort((a, b) => a.order - b.order)
  const byGroup = {}
  for (const s of sessions) (byGroup[s.group] ||= []).push(s)
  return { groups, sessions, byGroup }
})

// A group with no explicit entry in the collapse map is open. Mirrors the
// predicate Sidebar.js used before this map was lifted into a signal.
export function isGroupOpen(expandedMap, path) {
  return (expandedMap || {})[path] !== false
}

// Flip a group's collapsed/expanded state. The single implementation behind
// every collapse-toggle call site (Sidebar's chevron click, AppShell's Tab
// and Enter keyboard handlers, and setGroupOpen's force-to-value case) —
// those four used to reimplement this spread three times byte-identically,
// which is how the Tab-key overlay bug (review finding #1) slipped into
// only one of the copies (review finding #7).
export function toggleGroupOpen(path) {
  groupExpandedSignal.value = { ...groupExpandedSignal.value, [path]: !isGroupOpen(groupExpandedSignal.value, path) }
}

// Row-level filter predicate shared by the sidebar and keyboard nav.
// `filter` must already be lowercased and trimmed.
export function sessionMatches(s, filter, statuses) {
  // Compare on the BUCKET, not the raw status, so the chips agree with the
  // group stats panel: `starting` counts as running and `queued` as idle.
  // An exact `statuses.includes(s.status)` made those two match no chip at
  // all, so a starting/queued session vanished from the sidebar as soon as
  // any filter was activated.
  if (statuses && statuses.length && !statuses.includes(statusBucket(s.status))) return false
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
// Archived sessions bucketed by group path, projected into the same shape as
// active ones. GET /api/sessions/archived returns bare MenuSession objects
// (not menu items), so wrap each before reusing projectSession.
export const archivedByGroupSignal = computed(() => {
  const byGroup = {}
  for (const raw of (archivedSessionsSignal.value || [])) {
    if (!raw) continue
    const s = projectSession({ session: raw })
    s.archived = true
    ;(byGroup[s.group] ||= []).push(s)
  }
  return byGroup
})

// Everything the group stats panel shows for a group: active members first,
// then archived ones.
//
// The archived half exists for TUI parity. The TUI builds its group tree from
// the full instance set (internal/ui/home.go:3540), so renderGroupPreview
// lists and counts archived sessions — while its LEFT list partitions them out
// (home.go:2470-2493). The web mirrors that split: the snapshot behind the
// sidebar is archive-filtered server-side (session_data_service.go:278), and
// this function folds the archived feed back in for the panel alone.
export function groupMembers(groupPath) {
  const active = menuModelSignal.value.byGroup[groupPath] || []
  const archived = archivedByGroupSignal.value[groupPath] || []
  return [...active, ...archived]
}

export function groupStats(groupPath) {
  const members = groupMembers(groupPath)
  const counts = { running: 0, waiting: 0, idle: 0, stopped: 0, error: 0 }
  for (const s of members) counts[statusBucket(s.status)]++
  return {
    total: members.length,
    fragments: GROUP_STATUS_BUCKETS
      .filter((b) => counts[b.id] > 0)
      .map((b) => ({ id: b.id, glyph: b.glyph, count: counts[b.id], label: b.label })),
  }
}

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

// The single entry point for opening the create-session dialog. Pass '' for
// no group context (dialog opens blank, as it always did).
export function openCreateSessionForGroup(groupPath) {
  createSessionDialogSignal.value = groupCreateDefaults(groupPath)
}
