// state.js -- Shared signals for vanilla JS <-> Preact bridge
// Vanilla JS imports these and sets .value on SSE updates.
// Preact components import these and read .value reactively.
import { signal } from '@preact/signals'

// Session data from SSE snapshot
export const sessionsSignal = signal([])

// Currently selected session ID
export const selectedIdSignal = signal(null)

// SSE connection state: 'connecting' | 'connected' | 'disconnected'
export const connectionSignal = signal('connecting')

// Theme preference: 'light' | 'dark' | 'system'
export const themeSignal = signal(
  localStorage.getItem('theme') || 'system'
)

// Settings from GET /api/settings
export const settingsSignal = signal(null)
