// panes/FleetPane.js -- At-a-glance overview built from the live menu.
// Renders four stat tiles + a single "Groups" grid of GroupCards. The bundle
// has additional sections (conductor graph, watcher strip) that depend on
// fields the API does not expose; those render as informative empty hints.
import { html } from 'htm/preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { apiFetch } from '../api.js'
import { menuModelSignal } from '../dataModel.js'
import { selectSession } from '../state.js'
import { activeTabSignal, fleetViewSignal, conductorBannerOpenSignal } from '../uiState.js'
import { renderMarkdown } from '../miniMarkdown.js'
import { AnnotationLine, KANBAN_COLUMNS, cardFields, kanbanColumn, noteExcerpt, sessionAnnotation } from '../annotations.js'

const EMPTY_REMOTE_COUNTS = {
  remotesOnline: 0, remotesOffline: 0, sessions: 0,
  running: 0, waiting: 0, idle: 0, error: 0, stopped: 0,
}

function remoteSessions(remote) {
  return Array.isArray(remote?.sessions) ? remote.sessions : []
}

function remoteHealth(remote) {
  if (!remote.online) return 'error'
  const sessions = remoteSessions(remote)
  if (sessions.some(s => s.status === 'error')) return 'error'
  if (sessions.some(s => s.status === 'waiting')) return 'waiting'
  if (sessions.some(s => s.status === 'running' || s.status === 'starting')) return 'running'
  return 'idle'
}

function remoteAge(remote) {
  const seconds = Math.max(0, Number(remote?.ageSeconds) || 0)
  if (seconds < 60) return `${seconds}s ago`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  return `${Math.floor(seconds / 3600)}h ago`
}

function RemoteCard({ remote }) {
  const sessions = remoteSessions(remote)
  const health = remoteHealth(remote)
  const running = sessions.filter(s => s.status === 'running' || s.status === 'starting').length
  const waiting = sessions.filter(s => s.status === 'waiting').length
  const errors = sessions.filter(s => s.status === 'error').length
  return html`
    <article class=${`group-card fleet-remote-card ${health}`}
      data-testid="fleet-remote-card" data-remote-name=${remote.name}>
      <div class="gc-head">
        <span class="t">${remote.name}</span>
        <span class="health"><span class=${`d ${health}`}/></span>
        <span class=${`fleet-remote-state ${remote.online ? 'online' : 'offline'}`}>
          ${remote.stale ? 'stale' : remote.online ? (remote.latencyMs ? `${remote.latencyMs}ms` : 'online') : 'offline'}
        </span>
      </div>
      ${!remote.online && sessions.length === 0
        ? html`<div class="fleet-remote-empty">Remote unavailable</div>`
        : sessions.length === 0
          ? html`<div class="fleet-remote-empty">Online · no sessions</div>`
          : html`<div class="gc-tiles">
              ${sessions.map(s => html`
                <div key=${s.id} class="tile fleet-remote-session-tile"
                  data-testid="fleet-remote-session-tile" data-session-id=${s.id}
                  title=${s.path || s.id}>
                  <span class=${`tdot ${s.status}`}/>
                  <span class="tn">${s.title || s.id}</span>
                  ${s.tool && html`<span class="ttool">${s.tool}</span>`}
                </div>
              `)}
            </div>`}
      <div class="gc-foot">
        <span class="cn"><span class="d running"/>${running}</span>
        <span class="cn"><span class="d waiting"/>${waiting}</span>
        <span class="cn"><span class="d error"/>${errors}</span>
        <span class="path" data-testid="fleet-remote-session-count">
          ${sessions.length} session${sessions.length === 1 ? '' : 's'}
        </span>
      </div>
      ${remote.stale && html`<div class="fleet-remote-age" data-testid="fleet-remote-age">
        Last known state · ${remoteAge(remote)}
      </div>`}
    </article>
  `
}

function GroupCard({ name, items, onSelect }) {
  const running = items.filter(s => s.status === 'running').length
  const waiting = items.filter(s => s.status === 'waiting').length
  const errors  = items.filter(s => s.status === 'error').length
  const dominant = errors ? 'error' : waiting ? 'waiting' : running ? 'running' : ''
  return html`
    <div class=${`group-card ${dominant}`} data-testid="fleet-group-card" data-group-name=${name}>
      <div class="gc-head">
        <span class="t">${name}</span>
        <span class="health"><span class=${`d ${dominant || 'idle'}`}/></span>
        <span class="cost"></span>
      </div>
      <div class="gc-tiles">
        ${items.slice(0, 6).map(s => html`
          <button key=${s.id} class="tile" data-testid="fleet-session-tile" data-session-id=${s.id} onClick=${() => onSelect(s.id)}>
            <span class=${`tdot ${s.status}`}/>
            <span class="tn">${s.title}</span>
            ${s.tool && html`<span class="ttool">${s.tool}</span>`}
            <${AnnotationLine} s=${s} class="tile-annot"/>
          </button>
        `)}
      </div>
      <div class="gc-foot">
        <span class="cn"><span class="d running"/>${running}</span>
        <span class="cn"><span class="d waiting"/>${waiting}</span>
        <span class="cn"><span class="d error"/>${errors}</span>
        <span class="path" data-testid="fleet-group-session-count">${items.length} session${items.length === 1 ? '' : 's'}</span>
      </div>
    </div>
  `
}

// Within a column, sessions that want a human (process waiting) float up,
// then running ones, then the rest; ties break on title.
const PROCESS_RANK = { waiting: 0, running: 1, starting: 1, idle: 2, stopped: 3, error: 4 }
const byAttention = (a, b) =>
  (PROCESS_RANK[a.status] ?? 5) - (PROCESS_RANK[b.status] ?? 5) || a.title.localeCompare(b.title)

// One kanban card, written to be read at normal zoom: name, then status
// chip · ticket · group, then the conductor's labeled Goal / Current state /
// Decision needed. Sessions without those hints fall back to the full
// headline plus a few lines of their note.
function KanbanCard({ s, groupLabel, onSelect }) {
  const ann = sessionAnnotation(s)
  const fields = cardFields(s)
  const note = fields.length ? '' : noteExcerpt((s.hints || {}).note, 3)
  return html`
    <button class=${`kb-card ${s.status}`} data-testid="kanban-card" data-session-id=${s.id} onClick=${() => onSelect(s.id)}>
      <div class="kb-top">
        <span class=${`tdot ${s.status}`} title=${'process: ' + s.status}/>
        <span class="kb-title">${s.title}</span>
        <span class="kb-proc">${s.status}</span>
      </div>
      <div class="kb-meta">
        ${ann.status && html`<span class=${`hint-status ${ann.statusTone}`} data-testid="kanban-status">${ann.status}</span>`}
        ${ann.ticket && html`<span class="hint-ticket" data-testid="kanban-ticket">${ann.ticket}</span>`}
        ${groupLabel && html`<span class="kb-group">${groupLabel}</span>`}
      </div>
      ${fields.length
        ? html`<dl class="kb-fields" data-testid="kanban-fields">
            ${fields.map(f => html`
              <div class=${`kb-field ${f.key}`} key=${f.key} data-testid=${`kanban-field-${f.key}`}>
                <dt>${f.label}</dt><dd>${f.value}</dd>
              </div>`)}
          </dl>`
        : ann.headline && html`<div class="kb-headline" data-testid="kanban-headline">${ann.headline}</div>`}
      ${note && html`<div class="kb-note" data-testid="kanban-note">${note}</div>`}
    </button>
  `
}

// Conductors orchestrate the fleet, so they are pinned above the board rather
// than filed as a card in a status column or group.
export const isConductorSession = (s) => s.kind === 'conductor' || !!(s.raw && s.raw.isConductor)

// The pinned orchestrator banner: name + process status, its headline, and
// (expanded by default) the markdown fleet summary it keeps in its `note`
// (or `summary`) hint. Collapse state persists per browser.
function ConductorBanner({ s, onSelect }) {
  const open = conductorBannerOpenSignal.value
  const h = s.hints || {}
  const summary = h.summary || h.note || ''
  const ann = sessionAnnotation(s)
  return html`
    <section class=${`conductor-banner ${s.status}`} data-testid="conductor-banner" data-session-id=${s.id}>
      <div class="cb-head">
        <span class="cb-kicker">CONDUCTOR</span>
        <span class=${`tdot ${s.status}`} title=${'process: ' + s.status}/>
        <button class="cb-name" title="Open conductor terminal" onClick=${() => onSelect(s.id)}>${s.title}</button>
        <span class=${`cb-proc ${s.status}`} data-testid="conductor-banner-status">${s.status}</span>
        ${ann.headline && html`<span class="cb-headline" title=${ann.headline}>${ann.headline}</span>`}
        ${summary && html`
          <button class="cb-toggle" aria-expanded=${open} data-testid="conductor-banner-toggle"
                  onClick=${() => { conductorBannerOpenSignal.value = !open }}>
            ${open ? 'Hide summary ▴' : 'Show summary ▾'}
          </button>`}
      </div>
      ${summary && open && html`<div class="cb-body cc-md" data-testid="conductor-banner-summary">${renderMarkdown(summary)}</div>`}
      ${!summary && html`<div class="cb-empty">No fleet summary yet: the conductor writes one with <code>agent-deck session annotate ${s.title} --note-stdin</code>.</div>`}
    </section>
  `
}

function StatusKanban({ sessions, groupLabels, onSelect }) {
  const buckets = {}
  for (const s of sessions) (buckets[kanbanColumn(s)] ||= []).push(s)
  const cols = KANBAN_COLUMNS.filter(c => c.always || (buckets[c.id] || []).length)
  return html`
    <div class="kanban" data-testid="fleet-kanban" style=${`--kb-cols:${cols.length}`}>
      ${cols.map(c => {
        const items = (buckets[c.id] || []).slice().sort(byAttention)
        return html`
          <div class=${`kb-col ${c.tone}`} key=${c.id} data-testid=${`kanban-col-${c.id}`}>
            <div class="kb-col-head">
              <span class="kb-col-name">${c.label}</span>
              <span class="kb-col-count">${items.length}</span>
            </div>
            <div class="kb-stack">
              ${items.length
                ? items.map(s => html`<${KanbanCard} key=${s.id} s=${s} groupLabel=${groupLabels[s.group] || s.group} onSelect=${onSelect}/>`)
                : html`<div class="kb-empty">—</div>`}
            </div>
          </div>
        `
      })}
    </div>
  `
}

export function FleetPane() {
  const { groups, byGroup, sessions } = menuModelSignal.value
  const [remoteFleet, setRemoteFleet] = useState(null)
  const [remoteError, setRemoteError] = useState('')

  useEffect(() => {
    let cancelled = false
    let requestRunning = false
    const load = async () => {
      if (requestRunning) return
      requestRunning = true
      try {
        const value = await apiFetch('GET', '/api/remotes')
        if (!cancelled) {
          setRemoteFleet(value)
          setRemoteError('')
        }
      } catch (err) {
        if (!cancelled) setRemoteError(err.message || 'Could not scan configured remotes')
      } finally {
        requestRunning = false
      }
    }
    load()
    const interval = setInterval(load, 10000)
    return () => {
      cancelled = true
      clearInterval(interval)
    }
  }, [])

  const localCounts = useMemo(() => ({
    running: sessions.filter(s => s.status === 'running').length,
    waiting: sessions.filter(s => s.status === 'waiting').length,
    error:   sessions.filter(s => s.status === 'error').length,
    idle:    sessions.filter(s => s.status === 'idle').length,
  }), [sessions])
  const remoteCounts = remoteFleet?.counts || EMPTY_REMOTE_COUNTS
  const remotes = Array.isArray(remoteFleet?.remotes) ? remoteFleet.remotes : []
  const remoteTotal = remoteCounts.remotesOnline + remoteCounts.remotesOffline
  const counts = {
    running: localCounts.running + remoteCounts.running,
    waiting: localCounts.waiting + remoteCounts.waiting,
    error: localCounts.error + remoteCounts.error,
    idle: localCounts.idle + remoteCounts.idle,
  }
  const sessionTotal = sessions.length + remoteCounts.sessions
  const totalCost = sessions.reduce((n, s) => n + (s.cost || 0), 0)

  const onSelect = (id) => {
    selectSession(id)
    activeTabSignal.value = 'terminal'
  }
  const view = fleetViewSignal.value === 'groups' ? 'groups' : 'status'
  const conductors = sessions.filter(isConductorSession)
  const workers = sessions.filter(s => !isConductorSession(s))
  // Leaf group name ("stride/ws1" -> "ws1"), as the group cards show it.
  const groupLabels = useMemo(
    () => Object.fromEntries(groups.map(g => [g.path, g.name || g.label])),
    [groups],
  )

  return html`
    <div class="fleet" data-testid="fleet-pane">
      ${conductors.map(s => html`<${ConductorBanner} key=${s.id} s=${s} onSelect=${onSelect}/>`)}
      <div class="fleet-stats">
        <div class="stat" data-testid="fleet-stat-running"><div class="lbl">RUNNING</div><div class="num running">${counts.running}</div></div>
        <div class="stat" data-testid="fleet-stat-waiting"><div class="lbl">WAITING</div><div class="num waiting">${counts.waiting}</div></div>
        <div class="stat" data-testid="fleet-stat-error"><div class="lbl">ERROR</div><div class="num error">${counts.error}</div></div>
        <div class="stat" data-testid="fleet-stat-idle"><div class="lbl">IDLE</div><div class="num idle">${counts.idle}</div></div>
        <div class="stat" data-testid="fleet-stat-cost"><div class="lbl">SPEND · TODAY</div><div class="num cost">$${totalCost.toFixed(2)}</div></div>
        <div class="stat" data-testid="fleet-stat-sessions">
          <div class="lbl">SESSIONS</div>
          <div class="num">${sessionTotal}</div>
          ${remoteTotal > 0 && html`<div class="fleet-remotes-summary" data-testid="fleet-stat-remotes">
            ${remoteCounts.remotesOnline}/${remoteTotal} remotes online
          </div>`}
        </div>
      </div>

      ${(remoteTotal > 0 || remoteError) && html`
        <div class="fleet-section" data-testid="fleet-remotes-section">
          <div class="fleet-section-head">
            <span class="kicker">REMOTES</span>
            <span class="sub-kicker">${remoteCounts.remotesOnline} online · ${remoteCounts.sessions} sessions</span>
          </div>
          ${remoteError
            ? html`<div class="fleet-remote-error">${remoteError}</div>`
            : html`<div class="fleet-grid">
                ${remotes.map(remote => html`<${RemoteCard} key=${remote.name} remote=${remote}/>`)}
              </div>`}
        </div>
      `}

      <div class="fleet-section">
        <div class="fleet-section-head">
          <span class="kicker">${view === 'status' ? 'BY STATUS' : 'GROUPS'}</span>
          <span class="sub-kicker">${groups.length} group${groups.length === 1 ? '' : 's'} · ${sessions.length} ${remoteTotal > 0 ? 'local ' : ''}session${sessions.length === 1 ? '' : 's'}</span>
          <div class="fleet-view-toggle" role="group" aria-label="Board layout">
            ${[['status', 'Status'], ['groups', 'Groups']].map(([id, label]) => html`
              <button key=${id} class=${view === id ? 'on' : ''} aria-pressed=${view === id}
                      data-testid=${`fleet-view-${id}`} onClick=${() => { fleetViewSignal.value = id }}>${label}</button>
            `)}
          </div>
        </div>
        ${sessions.length > 0 && view === 'status'
          ? html`<${StatusKanban} sessions=${workers} groupLabels=${groupLabels} onSelect=${onSelect}/>`
          : groups.length === 0 || sessions.length === 0
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
