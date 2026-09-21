// dataModel.js -- Adapt the GET /api/menu response shape into the bundle's session/group model.
//
// The API menu returns interleaved {type:'group'|'session', ...} items. The
// bundle's design treats sessions and groups as separate flat arrays with
// extra fields (kind, mcps, skills, cost, tokens, branch, worktree, sandbox).
//
// We project the API into that shape, defaulting absent fields to safe zeros
// so the design renders without inventing data. Components that need richer
// data (e.g. RightRail Usage card) fall back to "no data" placeholders.
import { computed, effect } from '@preact/signals'
import { sessionsSignal, sessionCostsSignal, selectedIdSignal, selectedGroupSignal, createSessionDialogSignal, archivedSessionsSignal, mutationsEnabledSignal } from './state.js'
import { sidebarFilterSignal, groupExpandedSignal, statusFiltersSignal } from './uiState.js'
import { apiFetch } from './api.js'

// kind heuristic from session metadata (no API field today).
// `tool` is `claude|codex|gemini|shell|webhook|...`; treat anything not in
// the agent set as a watcher. Conductor is detected by group convention.
function deriveKind(s) {
  if (!s || !s.tool) return 'agent'
  if (s.groupPath === 'conductor' || /conductor/i.test(s.title || '')) return 'conductor'
  if (['webhook', 'ntfy', 'slack-watcher'].includes(s.tool)) return 'watcher'
  return 'agent'
}

export function projectSession(item) {
  const s = item.session || {}
  const id = s.id || ''
  const groupPath = s.groupPath || ''
  return {
    id,
    // Nesting depth straight off the wire (MenuItem.Level): groupLevel+1 for a
    // top-level session, groupLevel+2 for one nested under a parent session.
    //
    // Taken from the server rather than re-derived from parentSessionId on
    // purpose. GroupTree.Flatten nests exactly ONE level: it buckets
    // sub-sessions by parent id, but only parents that are themselves
    // top-level IN THIS GROUP get children attached. A grandchild — or a child
    // whose parent lives in another group — falls through to the orphan branch
    // and is emitted at groupLevel+1 with IsSubSession still true
    // (internal/session/groups.go:749-773). Rebuilding the tree client-side
    // the way RightRail does would recurse past that and disagree with the TUI.
    // Level already encodes the answer, orphans included.
    level: item.level,
    // Exposed for consumers that care about the distinction itself. The
    // sidebar deliberately does NOT use it to decide indentation — the server
    // flags orphans as sub-sessions while still emitting them at top level, so
    // depth is the only faithful signal (see sessionDepth below).
    isSubSession: !!item.isSubSession,
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
    // Menu-snapshot sessions are active by definition (the server
    // archive-filters it); archivedByGroupSignal flips this for the archived
    // feed. Always present so consumers need no guard.
    archived: false,
    // Parent session id, for the sub-session nesting in the sidebar and the
    // conductor tree in RightRail. null for a top-level session.
    parent: s.parentSessionId || null,
    pendingNeeds: 0,
    watcherType: null,
    routes: '',
    events1h: 0,
    meta: '',
    raw: s,
  }
}

// A session row's nesting depth, on the same scale as a group row's.
//
// The server's Level is groupLevel+1 (top-level) or groupLevel+2 (nested), so
// it lines up with group depth directly. Anything missing or nonsensical —
// the archived feed projects sessions with no MenuItem around them — falls
// back to "directly under its group", which is what every row did before
// sub-sessions nested at all.
function sessionDepth(session, groupDepthValue) {
  const level = session.level
  if (Number.isInteger(level) && level > groupDepthValue) return level
  return groupDepthValue + 1
}

// Group paths are '/'-separated, exactly as on the Go side — these mirror
// getParentPath / extractGroupName / GetGroupLevel in internal/session/groups.go.
export function parentGroupPath(path) {
  const i = (path || '').lastIndexOf('/')
  return i === -1 ? '' : path.slice(0, i)
}

function leafGroupName(path) {
  const i = (path || '').lastIndexOf('/')
  return i === -1 ? path : path.slice(i + 1)
}

function projectGroup(item) {
  const g = item.group || {}
  const path = g.path || ''
  const name = g.name || leafGroupName(path)
  return {
    path,
    // label is the uppercased sidebar form; name is the raw display form
    // used by the stats panel header and the create dialog's GROUP row.
    // Both are the LEAF segment ("ws1"), matching MenuGroup.Name and the TUI
    // (home.go:10793 renders item.Group.Name) — the parent is conveyed by
    // indentation, not by repeating the path on every row.
    label: name.toUpperCase(),
    name,
    // Explicitly configured folder for new sessions in this group. Empty
    // when unset — callers fall back to the group's newest session path.
    defaultPath: g.defaultPath || '',
    expanded: !!g.expanded,
    sessionCount: g.sessionCount || 0,
    // Passthrough of MenuGroup.Order. Nothing reads it since the flat sort by
    // it was removed — kept only so the projection stays faithful to the wire.
    order: g.order || 0,
    kind: path === 'conductor' ? 'conductor' : path === 'watchers' ? 'watcher' : null,
  }
}

// A stand-in row for a group referenced by a session (or implied by a nested
// path) that the snapshot never sent. Mirrors GroupTree.ensureParentGroupsExist:
// without it a subgroup whose parent is missing would render as a root.
function synthesizeGroup(path) {
  const name = leafGroupName(path)
  return {
    path,
    label: name.toUpperCase(),
    name,
    defaultPath: '',
    expanded: true,
    sessionCount: 0,
    order: 0,
    kind: path === 'conductor' ? 'conductor' : path === 'watchers' ? 'watcher' : null,
    derived: true,
  }
}

// Re-derive the group tree and emit it depth-first, parents immediately
// followed by their descendants.
//
// The snapshot already arrives in this order — BuildMenuSnapshot walks
// GroupTree.Flatten(), whose GroupList is sorted parents-before-children with
// each subtree kept contiguous (internal/session/groups.go:430-508). What this
// adds is (a) a home for groups the wire never sent, and (b) immunity to the
// flat `sort((a, b) => a.order - b.order)` that used to run here.
//
// That sort was the bug: MenuGroup.Order is a SIBLING ordinal, not a global
// rank. The server only ever compares Order between groups sharing a parent
// (and MoveGroupUp/Down only swaps siblings), so ordering the flat list by it
// interleaves unrelated subtrees — with stride{ws1,ws2,ws3} and redwood{ws1},
// redwood's ws1 sorted into the middle of stride's children and redwood itself
// fell below them.
//
// A DFS that keeps siblings in encounter order reproduces the server's
// ordering exactly, because encounter order IS the server's sibling order.
function orderGroupsHierarchically(groups) {
  const byPath = new Map()
  for (const g of groups) if (g.path && !byPath.has(g.path)) byPath.set(g.path, g)

  // Fill in missing ancestors. parentGroupPath strictly shortens, so this
  // terminates; iterating a snapshot of the values keeps the walk stable while
  // the map grows.
  for (const g of [...byPath.values()]) {
    for (let p = parentGroupPath(g.path); p && !byPath.has(p); p = parentGroupPath(p)) {
      byPath.set(p, synthesizeGroup(p))
    }
  }

  // Map insertion order is the snapshot's order, so children lists come out in
  // the server's sibling order for free.
  const children = new Map()
  for (const path of byPath.keys()) {
    const p = parentGroupPath(path)
    if (!children.has(p)) children.set(p, [])
    children.get(p).push(path)
  }

  const out = []
  // The walk is the single source of depth for a group row — neither
  // projectGroup nor synthesizeGroup computes it, precisely so there is no
  // second answer to disagree with.
  const walk = (path, depth) => {
    out.push({ ...byPath.get(path), depth, level: depth })
    for (const child of children.get(path) || []) walk(child, depth + 1)
  }
  for (const root of children.get('') || []) walk(root, 0)
  return out
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
      groups.push(synthesizeGroup(s.group))
      seen.add(s.group)
    }
  }
  const byGroup = {}
  for (const s of sessions) (byGroup[s.group] ||= []).push(s)
  return { groups: orderGroupsHierarchically(groups), sessions, byGroup }
})

// A group with no explicit entry in the collapse map is open. Mirrors the
// predicate Sidebar.js used before this map was lifted into a signal.
//
// This is the group's OWN flag only. Collapsing a parent does not flip it —
// the TUI's CollapseGroup does not cascade either — so use isGroupVisible to
// ask whether a row is actually reachable on screen.
export function isGroupOpen(expandedMap, path) {
  return (expandedMap || {})[path] !== false
}

// Whether a group is reachable in the rendered tree: every ancestor on its
// chain is expanded. The port of GroupTree.ancestorsExpanded
// (internal/session/groups.go:589-624), including its tolerance — an ancestor
// with no entry counts as expanded, and the walk keeps going up so a genuinely
// collapsed ancestor above a gap still hides the subtree.
//
// The client has to own this because the server deliberately does NOT do it:
// BuildMenuSnapshot force-expands every group before flattening "so
// descendants are always available client-side, even when persisted state is
// collapsed" (internal/web/menu_snapshot_builder.go:22-26) and ships the
// persisted flag alongside. Checking only the group's own flag — which is what
// the sidebar did — left subgroup headers on screen after their parent was
// collapsed, orphaned at the top level. Same shape as issue #1878 in the TUI.
export function isGroupVisible(expandedMap, path) {
  for (let p = parentGroupPath(path); p; p = parentGroupPath(p)) {
    if (!isGroupOpen(expandedMap, p)) return false
  }
  return true
}

// Group paths whose collapse write is in flight.
//
// The server re-checks the snapshot every 2s and emits whenever its
// fingerprint moved (internal/web/handlers_events.go:16,60 — a poll plus a
// change signal), so a snapshot triggered by an UNRELATED change — a session
// going idle, a cost update — can easily be in flight carrying the pre-toggle
// `expanded` for the group the user just clicked. Reconciling that would
// visibly flip the chevron back under the cursor. Suppressing reconciliation
// for exactly the paths we are mid-write on keeps the server authoritative
// everywhere else.
const pendingGroupWrites = new Map()

// Paths the user toggled on a server that cannot persist it (read-only, i.e.
// mutationsEnabledSignal false). Nothing was written, so there is no in-flight
// guard to protect them and the next snapshot would re-adopt the server's flag
// and spring the group back open — within ~2s, since the menu stream polls.
//
// Tracking them individually rather than switching reconciliation off wholesale
// keeps the useful half: a read-only viewer still inherits the TUI's collapse
// state for every group they have not touched themselves.
const localGroupOverrides = new Set()

// Send the group's latest requested state, one request at a time per path.
//
// Collapse is a cheap-looking click on a genuinely expensive write:
// SaveGroupsOnly opens a BEGIN IMMEDIATE transaction and upserts EVERY group
// in the tree (group_storage_snapshot.go prepareGroupSave), and each PATCH
// then fans a full menu snapshot out to every connected browser via
// notifyMenuChanged. Firing one request per keypress would also outrun the
// server's 20/s mutation limiter on a held Tab — and a 429 reverts the
// optimistic flip, so the chevron would visibly flip-flop under the user.
//
// Holding at one in-flight request per path and re-sending only if the user
// moved on caps the traffic at the round-trip rate and still converges on
// whatever they last asked for.
function flushGroupWrite(path, entry) {
  const sending = entry.desired
  // Encode per segment: '/' separates real path segments and the handler
  // splits on it, so it must survive unescaped.
  const url = '/api/groups/' + path.split('/').map(encodeURIComponent).join('/')
  let failed = false
  return apiFetch('PATCH', url, { expanded: sending })
    .catch(() => { failed = true })   // apiFetch already toasted
    .then(() => {
      if (entry.desired !== sending) return flushGroupWrite(path, entry)
      pendingGroupWrites.delete(path)
      // ONLY a failed write is rolled back here. On success the server holds
      // the new value but its snapshot is still in flight — notifyMenuChanged
      // only pokes a channel, and the SSE goroutine then has to rebuild and
      // fingerprint a whole snapshot, so the small PATCH response nearly
      // always wins that race. Reconciling here would therefore adopt the
      // PRE-toggle `expanded` and flip the chevron back under the user until
      // the real snapshot landed. The effect picks it up when it does.
      if (failed) reconcileGroupExpanded(menuModelSignal.value.groups)
    })
}

// Adopt the server's collapse state for every group we are not mid-write on.
//
// This is what makes TUI and browser agree: MenuGroup.Expanded is the flag the
// TUI persists (home.go saveGroupState), and BuildMenuSnapshot reports it
// per-group even though it force-expands the tree it flattens.
//
// Client-synthesized rows are skipped — their `expanded: true` is a
// placeholder this module invented, not something the server said.
export function reconcileGroupExpanded(groups) {
  const current = groupExpandedSignal.peek()
  let next = null
  for (const g of groups || []) {
    if (!g || !g.path || g.derived) continue
    if (pendingGroupWrites.has(g.path) || localGroupOverrides.has(g.path)) continue
    const open = g.expanded !== false
    if (isGroupOpen(current, g.path) === open) continue
    next ||= { ...current }
    // Preserve the map's "absent means open" shape: only closures are stored,
    // so it stays small and a deleted group cannot linger in localStorage.
    if (open) delete next[g.path]
    else next[g.path] = false
  }
  if (next) groupExpandedSignal.value = next
}

// Flip a group's collapsed/expanded state. The single implementation behind
// every collapse-toggle call site (Sidebar's chevron click, AppShell's Tab
// and Enter keyboard handlers, and setGroupOpen's force-to-value case) —
// those four used to reimplement this spread three times byte-identically,
// which is how the Tab-key overlay bug (review finding #1) slipped into
// only one of the copies (review finding #7).
//
// Applies optimistically, then persists through PATCH /api/groups/{path} so
// the TUI follows. On a read-only server there is nothing to persist to and
// the flip stays local (still remembered via localStorage), which is the
// behavior this had everywhere before the endpoint existed.
export function toggleGroupOpen(path) {
  const open = !isGroupOpen(groupExpandedSignal.value, path)
  groupExpandedSignal.value = { ...groupExpandedSignal.value, [path]: open }

  if (!mutationsEnabledSignal.value) {
    // Read-only server: remember that this path is ours now, or the next
    // snapshot re-adopts the server's flag and undoes the click.
    localGroupOverrides.add(path)
    return
  }
  // Writable again (or always was): the server becomes the authority for this
  // path once more.
  localGroupOverrides.delete(path)

  const existing = pendingGroupWrites.get(path)
  if (existing) {
    // A write for this path is already open; hand it the new target rather
    // than racing a second request against it.
    existing.desired = open
    return existing.promise
  }
  const entry = { desired: open, promise: null }
  pendingGroupWrites.set(path, entry)
  entry.promise = flushGroupWrite(path, entry)
  return entry.promise
}

// Row-level filter predicate shared by the sidebar and keyboard nav.
// `filter` must already be lowercased and trimmed.
export function sessionMatches(s, filter, statuses) {
  // Compare on the BUCKET so chips agree with the panel. An exact
  // `statuses.includes(s.status)` made starting/queued match no chip, so those
  // sessions vanished from the sidebar whenever any filter was active.
  if (statuses && statuses.length && !statuses.includes(statusBucket(s.status))) return false
  if (!filter) return true
  const hay = (s.title || '') + ' ' + (s.group || '') + ' ' + (s.path || '') +
              ' ' + (s.tool || '') + ' ' + (s.branch || '')
  return hay.toLowerCase().includes(filter)
}

// The sidebar's rendered row order, group headers included. Single source of
// truth: the Sidebar renders it and keyboard nav walks it, so they cannot
// disagree about what is on screen.
export const sidebarRowsSignal = computed(() => {
  const { groups, byGroup } = menuModelSignal.value
  const filter = (sidebarFilterSignal.value || '').trim().toLowerCase()
  const statuses = statusFiltersSignal.value
  const expandedMap = groupExpandedSignal.value

  // Direct members first, then roll them up the tree. `groups` is depth-first
  // pre-order, so every descendant precedes its ancestor when walked backwards
  // — one reverse pass adds each group's total into its parent.
  const direct = new Map()
  const total = new Map()
  for (const g of groups) {
    const members = (byGroup[g.path] || []).filter((s) => sessionMatches(s, filter, statuses))
    direct.set(g.path, members)
    total.set(g.path, members.length)
  }
  for (let i = groups.length - 1; i >= 0; i--) {
    const p = parentGroupPath(groups[i].path)
    if (p && total.has(p)) total.set(p, total.get(p) + total.get(groups[i].path))
  }

  // While a text filter is active the user is searching, not browsing. Without
  // this, a parent kept on screen because a DESCENDANT matched would render as
  // a header with a non-zero count and nothing beneath it, since collapse is
  // applied independently of the filter. Forcing the tree open for this render
  // pass only — groupExpandedSignal is never written — means clearing the
  // filter restores exactly the collapse state the user had. Matches the TUI,
  // whose search also reveals matches inside collapsed groups.
  const searching = !!filter

  const rows = []
  for (const g of groups) {
    // A text filter hides groups with nothing to show; a status chip does not.
    // Judged on the SUBTREE total so a parent stays on screen when only a
    // descendant matches — otherwise the surviving row has no parent to sit
    // under and the indentation points at nothing.
    if (filter && total.get(g.path) === 0) continue
    // An ancestor is collapsed ⇒ this group and everything under it stay hidden.
    if (!searching && !isGroupVisible(expandedMap, g.path)) continue
    rows.push({
      type: 'group',
      key: 'g:' + g.path,
      path: g.path,
      group: g,
      depth: g.depth,
      // Descendants included, matching the TUI's group row (home.go:21265
      // renders the recursive groupStats count). A collapsed "stride" that
      // reported "(0)" while hiding three subgroups and their sessions would
      // be worse than no badge at all.
      //
      // NB this deliberately differs from the group stats panel, which counts
      // DIRECT members only (groupPanelData.js groupStats). That split is
      // inherited, not accidental: the TUI's own preview counts direct members
      // too (home.go:24227 len(VisibleInstances(group.Sessions))) while its
      // sidebar row counts recursively. Each web surface matches its TUI
      // counterpart, so aligning them here would break parity, not fix it.
      memberCount: total.get(g.path),
    })
    if (searching || isGroupOpen(expandedMap, g.path)) {
      // Snapshot order, untouched: GroupTree.Flatten already emits each parent
      // session immediately followed by its sub-sessions, then the orphans.
      for (const s of direct.get(g.path)) {
        rows.push({
          type: 'session',
          key: 's:' + s.id,
          id: s.id,
          session: s,
          depth: sessionDepth(s, g.depth),
          // The owning group's depth, so a consumer can tell "nested under
          // another session" from "inside a nested group" without re-deriving.
          groupDepth: g.depth,
        })
      }
    }
  }
  return rows
})

// Map a status onto one of the five display buckets. Deliberate divergence:
// the TUI's switch lets `starting`/`queued` fall through uncounted, so its
// fragments can under-sum its own headline. Folding them in (and defaulting
// unknowns to idle) keeps the breakdown adding up.
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

// Status breakdown for one group. Direct members only, matching the TUI
// preview. NB: MenuGroup.sessionCount from the server DOES roll up subgroups —
// do not use it here.

// Epoch millis for a session's createdAt; -Infinity when absent or unparsable
// so a session with no timestamp never wins "newest".
function createdAtMillis(s) {
  const t = Date.parse(s.createdAt || '')
  return Number.isNaN(t) ? -Infinity : t
}

// Defaults for a new session in `groupPath`, mirroring the TUI's quick-create
// (home.go:12325-12350): folder from the group's configured default_path,
// falling back to its newest session's path; tool and model inherited from the
// most recently CREATED session. All derived client-side.
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

// Keep collapse state in step with the server. Reads menuModelSignal (which
// does NOT depend on groupExpandedSignal, so this cannot loop) and peeks the
// collapse map rather than subscribing to it, so a local toggle does not
// re-enter here — only a new snapshot does.
effect(() => {
  reconcileGroupExpanded(menuModelSignal.value.groups)
})
