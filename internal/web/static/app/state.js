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
// LAYT-05: explicit localStorage value wins; otherwise default based on viewport
// (open on tablet/desktop >= 768px, closed on phone < 768px). Prevents the
// mobile sidebar overlay from covering the terminal on cold load.
function initialSidebarOpen() {
  try {
    const stored = localStorage.getItem('agentdeck.sidebarOpen')
    if (stored === 'true') return true
    if (stored === 'false') return false
  } catch (_) {
    // localStorage may throw in incognito/privacy modes; fall through to viewport default.
  }
  return typeof window !== 'undefined' && window.innerWidth >= 768
}
export const sidebarOpenSignal = signal(initialSidebarOpen())

// Sidebar width in pixels, persisted to localStorage. LAYT-01 (BUG #4 + #10).
// Clamped to [200, 480]; default 280. Mobile overlay ignores this (keeps w-72 = 288px).
const SIDEBAR_WIDTH_MIN = 200
const SIDEBAR_WIDTH_MAX = 480
const SIDEBAR_WIDTH_DEFAULT = 280
function clampSidebarWidth(n) {
  if (!Number.isFinite(n)) return SIDEBAR_WIDTH_DEFAULT
  if (n < SIDEBAR_WIDTH_MIN) return SIDEBAR_WIDTH_MIN
  if (n > SIDEBAR_WIDTH_MAX) return SIDEBAR_WIDTH_MAX
  return Math.round(n)
}
function initialSidebarWidth() {
  try {
    const stored = localStorage.getItem('sidebar-width')
    if (stored != null) {
      const n = parseInt(stored, 10)
      return clampSidebarWidth(n)
    }
  } catch (_) {
    // localStorage may throw in incognito/privacy modes; fall through.
  }
  return SIDEBAR_WIDTH_DEFAULT
}
export const sidebarWidthSignal = signal(initialSidebarWidth())
export { SIDEBAR_WIDTH_MIN, SIDEBAR_WIDTH_MAX, SIDEBAR_WIDTH_DEFAULT, clampSidebarWidth }

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

// Sidebar search query (Issue A: search/filter)
export const searchQuerySignal = signal('')
export const searchVisibleSignal = signal(false)

// Global error toasts (Issue F)
export const toastsSignal = signal([])

// Keyboard shortcuts overlay open/close (BUG #14 / UX-03)
export const shortcutsOverlaySignal = signal(false)

// Toast history (WEB-P0-4 + POL-7): capped at 50 dismissed toasts.
// Persisted to localStorage key `agentdeck_toast_history`.
// Schema is localStorage-only per milestone rule: NO SQLite schema changes.
function initialToastHistory() {
  try {
    const stored = localStorage.getItem('agentdeck_toast_history')
    if (stored) {
      const parsed = JSON.parse(stored)
      if (Array.isArray(parsed)) return parsed.slice(-50)
    }
  } catch (_) {
    // localStorage may throw in incognito/privacy modes; start empty.
  }
  return []
}
export const toastHistorySignal = signal(initialToastHistory())

// Toast history drawer open/close (WEB-P0-4 + POL-7)
export const toastHistoryOpenSignal = signal(false)

// Mutations gate (WEB-P0-4 prevention layer): when /api/settings returns
// webMutations=false, the UI hides write buttons so users cannot generate
// 403 error spam. Defaults to true (optimistic) until AppShell mount fetches
// /api/settings and assigns the real value.
export const mutationsEnabledSignal = signal(true)
