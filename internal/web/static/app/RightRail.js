// RightRail.js -- Configurable session detail rail (right side).
//
// Cards: Overview, Usage, MCPs, Skills, Children, Events. User toggles which
// are visible in the rail-add picker at the bottom.
//
// The Children card renders the conductor child-session topology. The
// tree is built client-side from the same menuModelSignal that drives
// the session list, so it stays in sync with SSE menu updates for free.
// The Go endpoint at GET /api/sessions/{id}/children exposes the same
// shape for direct API consumers and lives in handlers_children.go.
//
// MCPs / Skills / Events still render an informative "TUI-only" hint
// because their underlying APIs are not yet wired through the right rail.
import { html } from 'htm/preact'
import { signal } from '@preact/signals'
import { menuModelSignal } from './dataModel.js'
import { selectedIdSignal } from './state.js'
import { rightRailPanelsSignal } from './uiState.js'

// Module-scope signal so collapsed state survives RightRail re-mounts
// (e.g. when the user switches between sessions and back). Keyed by
// session id so each conductor remembers its own expanded set.
const collapsedNodesSignal = signal({})

const AVAIL_PANELS = [
  { id: 'overview', label: 'Overview' },
  { id: 'usage',    label: 'Usage & activity' },
  { id: 'mcps',     label: 'MCPs' },
  { id: 'skills',   label: 'Skills' },
  { id: 'children', label: 'Children (conductor)' },
  { id: 'events',   label: 'Events (watcher)' },
]

function Card({ title, badge, testid, children }) {
  return html`
    <div class="card" data-testid=${testid}>
      <div class="card-head">
        <span class="name">${title}</span>
        ${badge && html`<span class="pill">${badge}</span>`}
      </div>
      <div class="card-body">${children}</div>
    </div>
  `
}

function NoData({ msg }) {
  return html`<div style="font-family: var(--mono); font-size: 11px; color: var(--muted);">${msg}</div>`
}

// Build the conductor → children adjacency map once per render. Cycles
// (corrupt parent pointers) are broken by a visited set so the tree
// builder cannot loop forever — mirrors the Go handler's defense.
function buildChildrenTree(rootId, sessions) {
  const byParent = new Map()
  for (const s of sessions) {
    const p = s.raw && s.raw.parentSessionId
    if (!p) continue
    if (!byParent.has(p)) byParent.set(p, [])
    byParent.get(p).push(s)
  }
  const visited = new Set([rootId])
  const walk = (id) => {
    const kids = byParent.get(id) || []
    return kids
      .filter((k) => {
        if (visited.has(k.id)) return false
        visited.add(k.id)
        return true
      })
      .map((k) => ({ session: k, children: walk(k.id) }))
  }
  return walk(rootId)
}

// Renders one node + its descendants. Leaf nodes hide the disclosure
// triangle; non-leaf nodes show ▾/▸ and toggle via collapsedNodesSignal.
function ChildNode({ node, depth, rootId }) {
  const collapsed = collapsedNodesSignal.value
  const key = rootId + ':' + node.session.id
  const isOpen = !collapsed[key]
  const hasKids = node.children.length > 0
  const toggle = () => {
    collapsedNodesSignal.value = { ...collapsed, [key]: isOpen }
  }
  return html`
    <div class="child-node" data-session-id=${node.session.id} data-depth=${depth}
         style="font-family: var(--mono); font-size: 11px; line-height: 1.7; padding-left: ${depth * 12}px;">
      <span class="child-row" style="display: inline-flex; align-items: center; gap: 4px;">
        <span class="child-toggle"
              onClick=${hasKids ? toggle : null}
              style=${`width: 10px; display: inline-block; cursor: ${hasKids ? 'pointer' : 'default'}; color: var(--muted);`}>
          ${hasKids ? (isOpen ? '▾' : '▸') : ' '}
        </span>
        <span class="child-status pill" data-status=${node.session.status}
              style="font-size: 9px; padding: 0 4px;">${node.session.status}</span>
        <span class="child-title" style="color: var(--text-hi);">${node.session.title}</span>
        ${node.session.tool && html`<span class="child-tool" style="color: var(--muted);">· ${node.session.tool}</span>`}
      </span>
      ${hasKids && isOpen && node.children.map((kid) => html`
        <${ChildNode} key=${kid.session.id} node=${kid} depth=${depth + 1} rootId=${rootId}/>
      `)}
    </div>
  `
}

function ChildrenTree({ rootId, sessions }) {
  const tree = buildChildrenTree(rootId, sessions)
  if (tree.length === 0) {
    return html`<${NoData} msg="No child sessions yet."/>`
  }
  return html`
    <div class="children-tree" data-children-count=${tree.length}>
      ${tree.map((node) => html`
        <${ChildNode} key=${node.session.id} node=${node} depth=${0} rootId=${rootId}/>
      `)}
    </div>
  `
}

export function RightRail() {
  const { sessions } = menuModelSignal.value
  const selected = selectedIdSignal.value
  const session = sessions.find(s => s.id === selected) || sessions[0]
  const panels = rightRailPanelsSignal.value

  if (!session) {
    return html`
      <div class="rightrail" data-testid="right-rail">
        <div class="rail-head"><span class="t">SESSION</span></div>
        <div class="rail-body">
          <div style="padding: 18px; font-family: var(--mono); font-size: 11px; color: var(--muted);">
            no session selected
          </div>
        </div>
      </div>
    `
  }

  const togglePanel = (id) => {
    rightRailPanelsSignal.value = { ...panels, [id]: !panels[id] }
  }

  return html`
    <div class="rightrail" data-testid="right-rail">
      <div class="rail-head">
        <span class="t">SESSION</span>
        <div class="spacer"/>
        <span class="t" style="color: var(--text-hi);">${session.title}</span>
      </div>
      <div class="rail-body">
        ${panels.overview && html`
          <${Card} title="OVERVIEW" badge=${session.status} testid="rail-card-overview">
            <div class="kv"><span class="k">kind</span><span class="v">${session.kind}</span></div>
            <div class="kv"><span class="k">tool</span><span class="v">${session.tool || '—'}</span></div>
            ${session.model && html`
              <div class="kv"><span class="k">model</span><span class="v">${session.model}</span></div>`}
            ${session.modelVersion && html`
              <div class="kv"><span class="k">version</span><span class="v">${session.modelVersion}</span></div>`}
            ${session.modelId && html`
              <div class="kv"><span class="k">model id</span><span class="v" title=${session.modelId}>${session.modelId}</span></div>`}
            <div class="kv"><span class="k">group</span><span class="v">${session.group || '—'}</span></div>
            ${session.branch && session.branch !== '—' && html`
              <div class="kv"><span class="k">branch</span><span class="v">${session.branch}</span></div>`}
            ${session.path && html`
              <div class="kv"><span class="k">path</span><span class="v" title=${session.path}>${session.path}</span></div>`}
            ${session.sandbox && html`<div class="kv"><span class="k">sandbox</span><span class="v warn">docker</span></div>`}
            ${session.worktree && html`<div class="kv"><span class="k">worktree</span><span class="v ok">yes</span></div>`}
          </${Card}>
        `}
        ${panels.usage && html`
          <${Card} title="USAGE" testid="rail-card-usage">
            ${session.cost > 0
              ? html`<div class="kv"><span class="k">cost</span><span class="v ok">$${session.cost.toFixed(2)}</span></div>`
              : html`<${NoData} msg="cost data not available for this session"/>`}
            ${session.tokens > 0 && html`<div class="kv"><span class="k">tokens</span><span class="v">${(session.tokens/1000).toFixed(1)}k</span></div>`}
          </${Card}>
        `}
        ${panels.mcps && html`
          <${Card} title="MCPS" testid="rail-card-mcps">
            <${NoData} msg="MCP attachments not exposed via web API. Use TUI (m key)."/>
          </${Card}>
        `}
        ${panels.skills && html`
          <${Card} title="SKILLS" testid="rail-card-skills">
            <${NoData} msg="Skill attachments not exposed via web API. Use TUI (s key)."/>
          </${Card}>
        `}
        ${panels.children && session.kind === 'conductor' && html`
          <${Card} title="CHILDREN" badge="conductor" testid="rail-card-children">
            <${ChildrenTree} rootId=${session.id} sessions=${sessions}/>
          </${Card}>
        `}
        ${panels.events && session.kind === 'watcher' && html`
          <${Card} title="EVENTS" testid="rail-card-events">
            <${NoData} msg="Watcher event stream not exposed via web API."/>
          </${Card}>
        `}
        <div class="rail-add">
          <div>Right-rail panels</div>
          <div class="opts">
            ${AVAIL_PANELS.map(p => html`
              <span key=${p.id}
                    data-testid=${`rail-panel-toggle-${p.id}`}
                    class=${`opt ${panels[p.id] ? 'on' : ''}`}
                    onClick=${() => togglePanel(p.id)}>
                ${p.label}
              </span>
            `)}
          </div>
        </div>
      </div>
    </div>
  `
}
