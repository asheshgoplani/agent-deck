// main.js -- Preact app entry point and full boot sequence
// Handles: auth token extraction, SSE connection, route sync, service worker registration
import { render, html } from 'htm/preact'
import { App } from './App.js'
import { apiFetch } from './api.js'
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
      connectionSignal.value = 'connected'
    } catch (_) {
      // malformed JSON; keep current connection state
    }
  })

  source.addEventListener('error', () => {
    connectionSignal.value = 'disconnected'
    // EventSource auto-reconnects; we'll update to 'connected' on next successful "menu" event
  })
}

export function stopSSE() {
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

let _ccSource = null
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
    } catch (_) {
      // malformed JSON; ignore
    }
  })

  // The command-center stream shares the connection-state signal via the menu
  // stream; we don't flip it here to avoid fighting the menu reconnect logic.
}

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
