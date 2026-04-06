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

// Dialog open/close signals (Phase 4: mutations)
// createSessionDialogSignal: boolean (true = dialog open)
export const createSessionDialogSignal = signal(false)

// confirmDialogSignal: null or { message: string, onConfirm: function }
export const confirmDialogSignal = signal(null)

// groupNameDialogSignal: null or { mode: 'create'|'rename', groupPath: string, currentName: string, onSubmit: function }
export const groupNameDialogSignal = signal(null)

// WebSocket connection state for terminal: 'disconnected' | 'connecting' | 'connected' | 'error'
export const wsStateSignal = signal('disconnected')

// Read-only mode from WebSocket status:connected payload
export const readOnlySignal = signal(false)

// Tab navigation: 'terminal' | 'costs'
export const activeTabSignal = signal('terminal')

// Push notification state (migrated from app.js state object)
export const pushConfigSignal = signal(null)        // null or { enabled, vapidPublicKey }
export const pushSubscribedSignal = signal(false)
export const pushBusySignal = signal(false)
export const pushEndpointSignal = signal('')

// Info drawer open/close state (Phase 10: replaces showSettings local state in Topbar)
export const infoDrawerOpenSignal = signal(false)
