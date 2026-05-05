// panes/FleetPane.js -- At-a-glance overview built from the live menu.
// Renders four stat tiles + a single "Groups" grid of GroupCards. The bundle
// has additional sections (conductor graph, watcher strip) that depend on
// fields the API does not expose; those render as informative empty hints.
import { html } from 'htm/preact'
import { useMemo } from 'preact/hooks'
import { menuModelSignal } from '../dataModel.js'
import { selectedIdSignal } from '../state.js'
import { activeTabSignal } from '../uiState.js'

function GroupCard({ name, items, onSelect }) {
  const running = items.filter(s => s.status === 'running').length
  const waiting = items.filter(s => s.status === 'waiting').length
  const errors  = items.filter(s => s.status === 'error').length
  const dominant = errors ? 'error' : waiting ? 'waiting' : running ? 'running' : ''
  return html`
    <div class=${`group-card ${dominant}`}>
      <div class="gc-head">
        <span class="t">${name}</span>
        <span class="health"><span class=${`d ${dominant || 'idle'}`}/></span>
        <span class="cost"></span>
      </div>
      <div class="gc-tiles">
        ${items.slice(0, 6).map(s => html`
          <button key=${s.id} class="tile" onClick=${() => onSelect(s.id)}>
            <span class=${`tdot ${s.status}`}/>
            <span class="tn">${s.title}</span>
            ${s.tool && html`<span class="ttool">${s.tool}</span>`}
          </button>
        `)}
      </div>
      <div class="gc-foot">
        <span class="cn"><span class="d running"/>${running}</span>
        <span class="cn"><span class="d waiting"/>${waiting}</span>
        <span class="cn"><span class="d error"/>${errors}</span>
        <span class="path">${items.length} session${items.length === 1 ? '' : 's'}</span>
      </div>
    </div>
  `
}

export function FleetPane() {
  const { groups, byGroup, sessions } = menuModelSignal.value
  const counts = useMemo(() => ({
    running: sessions.filter(s => s.status === 'running').length,
    waiting: sessions.filter(s => s.status === 'waiting').length,
    error:   sessions.filter(s => s.status === 'error').length,
    idle:    sessions.filter(s => s.status === 'idle').length,
  }), [sessions])
  const totalCost = sessions.reduce((n, s) => n + (s.cost || 0), 0)

  const onSelect = (id) => {
    selectedIdSignal.value = id
    activeTabSignal.value = 'terminal'
  }

  return html`
    <div class="fleet">
      <div class="fleet-stats">
        <div class="stat"><div class="lbl">RUNNING</div><div class="num running">${counts.running}</div></div>
        <div class="stat"><div class="lbl">WAITING</div><div class="num waiting">${counts.waiting}</div></div>
        <div class="stat"><div class="lbl">ERROR</div><div class="num error">${counts.error}</div></div>
        <div class="stat"><div class="lbl">IDLE</div><div class="num idle">${counts.idle}</div></div>
        <div class="stat"><div class="lbl">SPEND · TODAY</div><div class="num cost">$${totalCost.toFixed(2)}</div></div>
        <div class="stat"><div class="lbl">SESSIONS</div><div class="num">${sessions.length}</div></div>
      </div>

      <div class="fleet-section">
        <div class="fleet-section-head">
          <span class="kicker">GROUPS</span>
          <span class="sub-kicker">${groups.length} group${groups.length === 1 ? '' : 's'} · ${sessions.length} session${sessions.length === 1 ? '' : 's'}</span>
        </div>
        ${groups.length === 0 || sessions.length === 0
          ? html`<div style="font-family: var(--mono); font-size: 11px; color: var(--muted); padding: 16px;">
              No sessions yet. Use the sidebar to create one.
            </div>`
          : html`<div class="fleet-grid">
              ${groups.map(g => {
                const items = byGroup[g.path] || []
                if (items.length === 0) return null
                return html`<${GroupCard} key=${g.path} name=${g.label} items=${items} onSelect=${onSelect}/>`
              })}
            </div>`}
      </div>
    </div>
  `
}
