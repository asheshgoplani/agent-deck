// panes/FleetPane.js -- At-a-glance overview built from the live menu.
// Renders four stat tiles + a single "Groups" grid of GroupCards. The bundle
// has additional sections (conductor graph, watcher strip) that depend on
// fields the API does not expose; those render as informative empty hints.
import { html } from 'htm/preact'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { apiFetch } from '../api.js'
import { menuModelSignal } from '../dataModel.js'
import { selectedIdSignal } from '../state.js'
import { activeTabSignal } from '../uiState.js'

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
    selectedIdSignal.value = id
    activeTabSignal.value = 'terminal'
  }

  return html`
    <div class="fleet" data-testid="fleet-pane">
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
          <span class="kicker">GROUPS</span>
          <span class="sub-kicker">${groups.length} group${groups.length === 1 ? '' : 's'} · ${sessions.length} ${remoteTotal > 0 ? 'local ' : ''}session${sessions.length === 1 ? '' : 's'}</span>
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
