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
import { sessionsSignal, sessionCostsSignal } from './state.js'
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
