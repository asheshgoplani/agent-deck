// RightRail.js -- Configurable session detail rail (right side).
//
// Cards: Overview, Usage, MCPs, Skills, Children, Events. User toggles which
// are visible in the rail-add picker at the bottom.
//
// MCPs / Skills / Children / Events have no API today; they render an
// informative "TUI-only feature" hint instead of fake data.
import { html } from 'htm/preact'
import { menuModelSignal } from './dataModel.js'
import { selectedIdSignal } from './state.js'
import { rightRailPanelsSignal } from './uiState.js'

const AVAIL_PANELS = [
  { id: 'overview', label: 'Overview' },
  { id: 'usage',    label: 'Usage & activity' },
  { id: 'mcps',     label: 'MCPs' },
  { id: 'skills',   label: 'Skills' },
  { id: 'children', label: 'Children (conductor)' },
  { id: 'events',   label: 'Events (watcher)' },
]

function Card({ title, badge, children }) {
  return html`
    <div class="card">
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

export function RightRail() {
  const { sessions } = menuModelSignal.value
  const selected = selectedIdSignal.value
  const session = sessions.find(s => s.id === selected) || sessions[0]
  const panels = rightRailPanelsSignal.value

  if (!session) {
    return html`
      <div class="rightrail">
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
    <div class="rightrail">
      <div class="rail-head">
        <span class="t">SESSION</span>
        <div class="spacer"/>
        <span class="t" style="color: var(--text-hi);">${session.title}</span>
      </div>
      <div class="rail-body">
        ${panels.overview && html`
          <${Card} title="OVERVIEW" badge=${session.status}>
            <div class="kv"><span class="k">kind</span><span class="v">${session.kind}</span></div>
            <div class="kv"><span class="k">tool</span><span class="v">${session.tool || '—'}</span></div>
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
          <${Card} title="USAGE">
            ${session.cost > 0
              ? html`<div class="kv"><span class="k">cost</span><span class="v ok">$${session.cost.toFixed(2)}</span></div>`
              : html`<${NoData} msg="cost data not available for this session"/>`}
            ${session.tokens > 0 && html`<div class="kv"><span class="k">tokens</span><span class="v">${(session.tokens/1000).toFixed(1)}k</span></div>`}
          </${Card}>
        `}
        ${panels.mcps && html`
          <${Card} title="MCPS">
            <${NoData} msg="MCP attachments not exposed via web API. Use TUI (m key)."/>
          </${Card}>
        `}
        ${panels.skills && html`
          <${Card} title="SKILLS">
            <${NoData} msg="Skill attachments not exposed via web API. Use TUI (s key)."/>
          </${Card}>
        `}
        ${panels.children && session.kind === 'conductor' && html`
          <${Card} title="CHILDREN">
            <${NoData} msg="Conductor child topology not exposed via web API."/>
          </${Card}>
        `}
        ${panels.events && session.kind === 'watcher' && html`
          <${Card} title="EVENTS">
            <${NoData} msg="Watcher event stream not exposed via web API."/>
          </${Card}>
        `}
        <div class="rail-add">
          <div>Right-rail panels</div>
          <div class="opts">
            ${AVAIL_PANELS.map(p => html`
              <span key=${p.id}
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
