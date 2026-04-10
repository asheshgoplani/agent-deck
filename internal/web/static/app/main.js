// main.js -- Preact app entry point and full boot sequence
// Handles: auth token extraction, SSE connection, route sync, service worker registration
import { render, html } from 'htm/preact'
import { App } from './App.js'
import { apiFetch } from './api.js'
import {
  sessionsSignal,
  selectedIdSignal,
  connectionSignal,
  authTokenSignal,
} from './state.js'

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
}

// ---------- Initial menu load + SSE kick-off ----------

export async function loadMenu() {
  try {
    const data = await apiFetch('GET', '/api/menu')
    sessionsSignal.value = data.items || []
    startSSE()
  } catch (_) {
    connectionSignal.value = 'disconnected'
    // Still start SSE so it can reconnect when server comes back
    startSSE()
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
