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

// Auth token for API calls (set by app.js after reading from URL)
export const authTokenSignal = signal('')

// Per-session costs from GET /api/costs/batch (map of sessionId -> costUSD)
export const sessionCostsSignal = signal({})

// Sidebar open state (for tablet/phone responsive toggle)
export const sidebarOpenSignal = signal(
  localStorage.getItem('agentdeck.sidebarOpen') !== 'false'
)

// Focused session ID for keyboard navigation (NOT array index, stable across SSE updates)
// Lives in state.js (not SessionList.js) so useKeyboardNav.js can import it without a circular dependency.
export const focusedIdSignal = signal(null)
