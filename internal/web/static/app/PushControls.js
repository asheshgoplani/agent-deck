// PushControls.js -- Push notification subscribe/unsubscribe and presence tracking (Preact component)
// Migrates push lifecycle from vanilla app.js into a self-contained Preact component.
import { html } from 'htm/preact'
import { useEffect } from 'preact/hooks'
import { pushConfigSignal, pushSubscribedSignal, pushBusySignal, pushEndpointSignal } from './state.js'
import { apiFetch } from './api.js'

// Converts a URL-safe base64 VAPID key to a Uint8Array for the Push API.
function urlBase64ToUint8Array(base64) {
  const padding = '='.repeat((4 - (base64.length % 4)) % 4)
  const normalized = (base64 + padding).replace(/-/g, '+').replace(/_/g, '/')
  const raw = window.atob(normalized)
  const output = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) {
    output[i] = raw.charCodeAt(i)
  }
  return output
}

async function syncPresence(endpoint, focused) {
  if (!endpoint) return
  const body = JSON.stringify({ endpoint, focused })
  try {
    await apiFetch('POST', '/api/push/presence', { endpoint, focused })
  } catch (_err) {
    // Presence sync is best-effort
  }
}

function sendPresenceBeacon(endpoint, focused) {
  if (!endpoint) return
  const body = JSON.stringify({ endpoint, focused })
  const url = '/api/push/presence'
  if (navigator.sendBeacon) {
    try {
      const blob = new Blob([body], { type: 'application/json' })
      navigator.sendBeacon(url, blob)
      return
    } catch (_err) {}
  }
  fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body, keepalive: true }).catch(() => {})
}

async function subscribePush() {
  const cfg = pushConfigSignal.value
  if (!cfg || !cfg.enabled || !cfg.vapidPublicKey) return

  pushBusySignal.value = true
  try {
    const registration = await navigator.serviceWorker.ready
    const subscription = await registration.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(cfg.vapidPublicKey),
    })
    await apiFetch('POST', '/api/push/subscribe', subscription.toJSON())
    pushSubscribedSignal.value = true
    pushEndpointSignal.value = subscription.endpoint || ''
    await syncPresence(subscription.endpoint, document.hasFocus() && document.visibilityState === 'visible')
  } catch (_err) {
    // Subscribe failure is non-fatal; UI remains in unsubscribed state
  } finally {
    pushBusySignal.value = false
  }
}

async function unsubscribePush() {
  pushBusySignal.value = true
  try {
    const registration = await navigator.serviceWorker.ready
    const subscription = await registration.pushManager.getSubscription()
    if (subscription) {
      await apiFetch('POST', '/api/push/unsubscribe', { endpoint: subscription.endpoint })
      await subscription.unsubscribe()
    }
    pushSubscribedSignal.value = false
    pushEndpointSignal.value = ''
  } catch (_err) {
    // Unsubscribe failure is non-fatal
  } finally {
    pushBusySignal.value = false
  }
}

export function PushControls() {
  useEffect(() => {
    let cleanedUp = false
    const listeners = []

    async function initPush() {
      try {
        const config = await apiFetch('GET', '/api/push/config')
        if (cleanedUp) return
        pushConfigSignal.value = config

        if (!config.enabled) return
        if (!('serviceWorker' in navigator) || !('PushManager' in window)) return

        const registration = await navigator.serviceWorker.ready
        if (cleanedUp) return

        const existing = await registration.pushManager.getSubscription()
        if (cleanedUp) return

        if (existing) {
          pushSubscribedSignal.value = true
          pushEndpointSignal.value = existing.endpoint || ''
          // Re-register existing subscription with server in case it expired
          try {
            await apiFetch('POST', '/api/push/subscribe', existing.toJSON())
          } catch (_err) {}
          if (!cleanedUp) {
            await syncPresence(existing.endpoint, document.hasFocus() && document.visibilityState === 'visible')
          }
        }

        // Set up presence event listeners
        function onFocus() {
          syncPresence(pushEndpointSignal.value, true).catch(() => {})
        }
        function onBlur() {
          syncPresence(pushEndpointSignal.value, false).catch(() => {})
        }
        function onVisibility() {
          const focused = document.visibilityState === 'visible' && document.hasFocus()
          syncPresence(pushEndpointSignal.value, focused).catch(() => {})
        }
        function onPagehide() {
          sendPresenceBeacon(pushEndpointSignal.value, false)
        }
        function onBeforeunload() {
          sendPresenceBeacon(pushEndpointSignal.value, false)
        }

        window.addEventListener('focus', onFocus)
        window.addEventListener('blur', onBlur)
        document.addEventListener('visibilitychange', onVisibility)
        window.addEventListener('pagehide', onPagehide)
        window.addEventListener('beforeunload', onBeforeunload)

        listeners.push(
          () => window.removeEventListener('focus', onFocus),
          () => window.removeEventListener('blur', onBlur),
          () => document.removeEventListener('visibilitychange', onVisibility),
          () => window.removeEventListener('pagehide', onPagehide),
          () => window.removeEventListener('beforeunload', onBeforeunload),
        )
      } catch (_err) {
        // Push init failure is non-fatal; component renders nothing
      }
    }

    initPush()

    return () => {
      cleanedUp = true
      listeners.forEach(remove => remove())
    }
  }, [])

  const config = pushConfigSignal.value
  if (!config || !config.enabled) return null

  function toggle() {
    if (pushBusySignal.value) return
    if (pushSubscribedSignal.value) {
      unsubscribePush()
    } else {
      subscribePush()
    }
  }

  return html`
    <div class="flex items-center gap-sp-8">
      <button
        disabled=${pushBusySignal.value}
        onClick=${toggle}
        class="text-xs px-2 py-1 rounded dark:bg-tn-panel bg-gray-100 dark:text-tn-fg text-gray-700 hover:dark:bg-tn-muted/20 hover:bg-gray-200 transition-colors disabled:opacity-50"
      >
        ${pushSubscribedSignal.value ? 'Disable notifications' : 'Enable notifications'}
      </button>
      <span class="text-xs dark:text-tn-muted text-gray-500">
        ${pushBusySignal.value ? 'Updating...' : pushSubscribedSignal.value ? 'Enabled (unfocused only)' : 'Disabled'}
      </span>
    </div>
  `
}
