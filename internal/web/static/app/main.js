// main.js -- Preact app entry point and full boot sequence
// Handles: auth token extraction, SSE connection, route sync, service worker registration
import { render, html } from 'htm/preact'
import { App } from './App.js'
import { apiFetch, authHeaders } from './api.js'
import {
  sessionsSignal,
  sessionsLoadedSignal,
  selectedIdSignal,
  connectionSignal,
  authTokenSignal,
  commandCenterSignal,
} from './state.js'
import { addToast } from './Toast.js'

// ---------- Auth token extraction ----------

;(function extractAuthToken() {
  const params = new URLSearchParams(window.location.search)
  const token = params.get('token')
  if (!token) return

  authTokenSignal.value = token

  // Strip token from URL so it isn't logged by the server or leaked via Referer header
  params.delete('token')
  const cleanSearch = params.toString()
  const cleanPath = window.location.pathname + (cleanSearch ? '?' + cleanSearch : '') + window.location.hash
  history.replaceState(null, '', cleanPath)

  // Prevent token from appearing in Referer headers on any subsequent navigation
  let meta = document.querySelector('meta[name="referrer"]')
  if (!meta) {
    meta = document.createElement('meta')
    meta.name = 'referrer'
    document.head.appendChild(meta)
  }
  meta.content = 'no-referrer'
})()

// ---------- SSE connection ----------

let _menuSource = null
let _ccSource = null

const SSE_RECOVERY_DELAYS_MS = [1000, 2000, 4000, 8000, 16000, 30000]
const SSE_PROBE_TIMEOUT_MS = 5000
let _sseRecoveryTimer = null
let _sseRecoveryAbort = null
let _sseRecoveryGeneration = 0
let _sseRecoveryAttempt = 0
let _sseTerminalFailures = new WeakSet()

function closedSSESources(includeTerminal = false) {
  const closed = []
  if (
    _menuSource &&
    _menuSource.readyState === EventSource.CLOSED &&
    (includeTerminal || !_sseTerminalFailures.has(_menuSource))
  ) {
    closed.push({ kind: 'menu', source: _menuSource })
  }
  if (
    _ccSource &&
    _ccSource.readyState === EventSource.CLOSED &&
    (includeTerminal || !_sseTerminalFailures.has(_ccSource))
  ) {
    closed.push({ kind: 'command-center', source: _ccSource })
  }
  return closed
}

async function probeSSEEndpoint(source, signal) {
  let response
  try {
    response = await fetch(source.url, {
      method: 'GET',
      headers: authHeaders({ Accept: 'text/event-stream' }),
      cache: 'no-store',
      signal,
    })
  } catch (_) {
    return 'transient'
  }

  const status = response.status
  const contentType = (response.headers.get('Content-Type') || '').split(';', 1)[0].trim().toLowerCase()
  try {
    await response.body?.cancel()
  } catch (_) {
    // The status and headers are sufficient for this readiness probe.
  }

  if (status === 200 && contentType === 'text/event-stream') return 'ready'
  if (status === 408 || status === 425 || status === 429 || status >= 500) return 'transient'
  return 'terminal'
}

function scheduleSSERecovery(delay) {
  if (_sseRecoveryTimer || _sseRecoveryAbort || closedSSESources().length === 0) return

  const wait = delay ?? SSE_RECOVERY_DELAYS_MS[Math.min(_sseRecoveryAttempt, SSE_RECOVERY_DELAYS_MS.length - 1)]
  const generation = _sseRecoveryGeneration
  _sseRecoveryTimer = setTimeout(async () => {
    _sseRecoveryTimer = null
    const closed = closedSSESources()
    if (closed.length === 0 || generation !== _sseRecoveryGeneration) return

    const controller = new AbortController()
    _sseRecoveryAbort = controller
    const probeTimeout = setTimeout(() => controller.abort(), SSE_PROBE_TIMEOUT_MS)
    const outcomes = await Promise.all(closed.map(({ source }) => probeSSEEndpoint(source, controller.signal)))
    clearTimeout(probeTimeout)
    if (_sseRecoveryAbort === controller) _sseRecoveryAbort = null
    if (generation !== _sseRecoveryGeneration) return

    let retryTransient = false
    _sseRecoveryAttempt = Math.min(_sseRecoveryAttempt + 1, SSE_RECOVERY_DELAYS_MS.length - 1)
    for (let i = 0; i < closed.length; i += 1) {
      const { kind, source } = closed[i]
      if (outcomes[i] === 'transient') {
        retryTransient = true
        continue
      }
      if (outcomes[i] !== 'ready') {
        _sseTerminalFailures.add(source)
        continue
      }

      if (kind === 'menu' && _menuSource === source) {
        source.close()
        _menuSource = null
        startSSE()
      } else if (kind === 'command-center' && _ccSource === source) {
        source.close()
        _ccSource = null
        startCommandCenterSSE()
      }
    }

    if (retryTransient) scheduleSSERecovery()
  }, wait)
}

function markSSEHealthy() {
  if (closedSSESources().length === 0) _sseRecoveryAttempt = 0
}

function wakeSSERecovery() {
  if (closedSSESources(true).length === 0) return
  _sseRecoveryGeneration += 1
  if (_sseRecoveryTimer) clearTimeout(_sseRecoveryTimer)
  _sseRecoveryTimer = null
  _sseRecoveryAbort?.abort()
  _sseRecoveryAbort = null
  _sseRecoveryAttempt = 0
  _sseTerminalFailures = new WeakSet()
  scheduleSSERecovery(0)
}

export function startSSE() {
  if (_menuSource) return

  const token = authTokenSignal.value
  const url = token
    ? '/events/menu?token=' + encodeURIComponent(token)
    : '/events/menu'

  const source = new EventSource(url)
  _menuSource = source

  // CRITICAL: The Go server emits SSE events with event type "menu"
  // (see handlers_events.go: writeSSEEvent(w, flusher, "menu", snapshot))
  source.addEventListener('menu', (event) => {
    try {
      const snapshot = JSON.parse(event.data)
      if (snapshot && Array.isArray(snapshot.items)) {
        sessionsSignal.value = snapshot.items
        // POL-1: first SSE snapshot counts as loaded. Skeleton unmounts
        // even if the snapshot is empty — the server has spoken.
        sessionsLoadedSignal.value = true
      }
      markSSEHealthy()
      connectionSignal.value = 'connected'
    } catch (_) {
      // malformed JSON; keep current connection state
    }
  })

  source.addEventListener('error', () => {
    connectionSignal.value = 'disconnected'
    if (source.readyState === EventSource.CLOSED && _menuSource === source) {
      scheduleSSERecovery()
    }
  })
}

export function stopSSE() {
  _sseRecoveryGeneration += 1
  if (_sseRecoveryTimer) clearTimeout(_sseRecoveryTimer)
  _sseRecoveryTimer = null
  _sseRecoveryAbort?.abort()
  _sseRecoveryAbort = null
  _sseRecoveryAttempt = 0
  _sseTerminalFailures = new WeakSet()
  if (_menuSource) {
    _menuSource.close()
    _menuSource = null
  }
  if (_ccSource) {
    _ccSource.close()
    _ccSource = null
  }
}

// ---------- Command Center SSE ----------
// A second stream alongside the menu SSE, carrying the synthesized cross-
// project god-view snapshot. Live by construction (fingerprint-diffed
// server-side), so the panel never polls. recentlyCompleted entries drive
// "✅ X just finished" notifications.

// Track which completion ids we've already toasted so a steady-state re-emit
// of the same snapshot (or a reconnect) doesn't re-fire notifications.
const _ccSeenCompletions = new Set()

export function startCommandCenterSSE() {
  if (_ccSource) return

  const token = authTokenSignal.value
  const url = token
    ? '/events/command-center?token=' + encodeURIComponent(token)
    : '/events/command-center'

  const source = new EventSource(url)
  _ccSource = source

  // CRITICAL: the Go server emits this event with type "command-center"
  // (handlers_command_center.go: writeSSEEvent(w, flusher, "command-center", snapshot)).
  source.addEventListener('command-center', (event) => {
    try {
      const snapshot = JSON.parse(event.data)
      if (snapshot && typeof snapshot === 'object') {
        commandCenterSignal.value = snapshot
        const done = Array.isArray(snapshot.recentlyCompleted) ? snapshot.recentlyCompleted : []
        for (const c of done) {
          const key = (c && (c.id || '')) + ':' + (c && (c.at || ''))
          if (_ccSeenCompletions.has(key)) continue
          _ccSeenCompletions.add(key)
          if (c && c.title) addToast(`✅ ${c.title} just finished`, 'success')
        }
        // Bound the seen-set so it can't grow unbounded over a long session.
        if (_ccSeenCompletions.size > 200) {
          _ccSeenCompletions.clear()
        }
      }
      markSSEHealthy()
    } catch (_) {
      // malformed JSON; ignore
    }
  })

  source.addEventListener('error', () => {
    if (source.readyState === EventSource.CLOSED && _ccSource === source) {
      scheduleSSERecovery()
    }
  })

  // The command-center stream shares the connection-state signal via the menu
  // stream; we don't flip it here to avoid fighting the menu reconnect logic.
}

window.addEventListener('online', wakeSSERecovery)
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') wakeSSERecovery()
})

// ---------- Initial menu load + SSE kick-off ----------

export async function loadMenu() {
  try {
    const data = await apiFetch('GET', '/api/menu')
    sessionsSignal.value = data.items || []
    // POL-1: first real data arrived — unmount the skeleton. Do NOT set
    // this in the catch branch; the skeleton is the correct state when
    // we're offline.
    sessionsLoadedSignal.value = true
    startSSE()
    startCommandCenterSSE()
  } catch (_) {
    connectionSignal.value = 'disconnected'
    // Still start SSE so it can reconnect when server comes back
    startSSE()
    startCommandCenterSSE()
  }
}

// ---------- Route sync: URL -> selectedIdSignal ----------

export function applyRouteSelection() {
  const path = window.location.pathname || '/'
  if (path.startsWith('/s/')) {
    const raw = path.slice(3)
    if (raw && !raw.includes('/')) {
      try {
        selectedIdSignal.value = decodeURIComponent(raw)
      } catch (_) {
        selectedIdSignal.value = null
      }
      return
    }
  }
  // Don't force-clear selection at boot if no /s/ path; leave it null
}

// ---------- Service worker registration ----------

export function registerServiceWorker() {
  if (!('serviceWorker' in navigator)) return

  function doRegister() {
    navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(() => {
      // SW registration failure is non-fatal; app works without it
    })
  }

  if (document.readyState === 'complete' || document.readyState === 'interactive') {
    doRegister()
  } else {
    window.addEventListener('load', doRegister, { once: true })
  }
}

// ---------- Boot sequence ----------

const root = document.getElementById('app-root')
if (root) {
  root.style.cssText = 'position:fixed;inset:0;z-index:10;'
  applyRouteSelection()
  loadMenu()
  registerServiceWorker()
  render(html`<${App} />`, root)
}
