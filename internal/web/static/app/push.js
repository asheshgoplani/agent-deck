// push.js -- Browser side of `agent-deck web --push` (issue #2413).
//
// The server exposes /api/push/{config,subscribe,unsubscribe,presence} and
// sw.js shows the notifications, but the rewrite of the old app.js (#174)
// dropped the client flow that creates a subscription. This module carries it
// over: read /api/push/config, subscribe the service worker's pushManager with
// the server's VAPID key, POST the subscription, and keep the server told
// whether this tab is focused so it only notifies when you are looking away.
//
// The subscription itself is the remembered state: the browser keeps it across
// reloads, and initPush() re-sends it so a restarted server relearns it. When
// push is off on the server or the browser has no Push API, pushConfigSignal
// stays disabled and the Tweaks toggle is simply not rendered.
import { apiFetch, authHeaders } from './api.js'
import { addToast } from './toasts.js'
import {
  pushConfigSignal, pushSubscribedSignal, pushBusySignal, pushEndpointSignal,
  pushPermissionSignal,
} from './state.js'

let lastFocused = null

function pushSupported() {
  return 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window
}

// VAPID keys arrive base64url-encoded; pushManager.subscribe wants raw bytes.
function urlBase64ToUint8Array(base64) {
  const padded = (base64 + '='.repeat((4 - (base64.length % 4)) % 4)).replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(padded)
  return Uint8Array.from(raw, c => c.charCodeAt(0))
}

function registration() {
  // register() resolves to the existing registration when main.js already
  // registered the worker, so this is idempotent.
  return navigator.serviceWorker.register('/sw.js', { scope: '/' })
}

async function currentSubscription() {
  return (await registration()).pushManager.getSubscription()
}

// POST without apiFetch's own error toast, so the load-time re-sync stays
// silent and the toggle handlers report a failure exactly once.
async function post(path, body) {
  const res = await fetch(path, { method: 'POST', headers: authHeaders(), body: JSON.stringify(body) })
  if (!res.ok) throw new Error(path + ' returned ' + res.status)
}

function isFocused() {
  return document.visibilityState === 'visible' && (typeof document.hasFocus !== 'function' || document.hasFocus())
}

function sendPresence(focused, keepalive) {
  const endpoint = pushEndpointSignal.value
  if (!pushSubscribedSignal.value || !endpoint || focused === lastFocused) return
  lastFocused = focused
  // fetch keepalive rather than sendBeacon: /api/ auth is header-only, and a
  // beacon cannot carry the Authorization header.
  fetch('/api/push/presence', {
    method: 'POST',
    headers: authHeaders(),
    body: JSON.stringify({ endpoint, focused }),
    keepalive,
  }).catch(() => {})
}

function syncPresence() {
  sendPresence(isFocused(), false)
}

async function saveSubscription(sub) {
  await post('/api/push/subscribe', sub.toJSON())
  pushEndpointSignal.value = sub.endpoint || ''
  pushSubscribedSignal.value = true
  lastFocused = null
  syncPresence()
}

export async function initPush() {
  if (!pushSupported()) return
  let cfg
  try {
    cfg = await apiFetch('GET', '/api/push/config')
  } catch (_) {
    return
  }
  if (!cfg || !cfg.enabled || !cfg.vapidPublicKey) return
  pushPermissionSignal.value = Notification.permission
  pushConfigSignal.value = cfg

  document.addEventListener('visibilitychange', syncPresence)
  window.addEventListener('focus', syncPresence)
  window.addEventListener('blur', syncPresence)
  window.addEventListener('pagehide', () => sendPresence(false, true))

  try {
    const existing = await currentSubscription()
    if (existing && Notification.permission === 'granted') await saveSubscription(existing)
  } catch (_) {
    // Re-sync is best effort; the toggle still lets the user subscribe again.
  }
}

export async function enablePush() {
  const cfg = pushConfigSignal.value
  if (!cfg || pushBusySignal.value) return
  pushBusySignal.value = true
  try {
    const permission = await Notification.requestPermission()
    pushPermissionSignal.value = permission
    if (permission !== 'granted') {
      addToast('Notifications are blocked for this site. Allow them in the browser settings to enable push.')
      return
    }
    const reg = await registration()
    const sub = (await reg.pushManager.getSubscription()) || await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(cfg.vapidPublicKey),
    })
    await saveSubscription(sub)
  } catch (err) {
    addToast('Could not enable notifications: ' + (err.message || err))
  } finally {
    pushBusySignal.value = false
  }
}

export async function disablePush() {
  if (pushBusySignal.value) return
  pushBusySignal.value = true
  try {
    const sub = await currentSubscription()
    if (sub) {
      await post('/api/push/unsubscribe', { endpoint: sub.endpoint })
      await sub.unsubscribe()
    }
    pushSubscribedSignal.value = false
    pushEndpointSignal.value = ''
    lastFocused = null
  } catch (err) {
    addToast('Could not disable notifications: ' + (err.message || err))
  } finally {
    pushBusySignal.value = false
  }
}
