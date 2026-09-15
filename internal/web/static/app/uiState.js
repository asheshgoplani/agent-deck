// uiState.js -- UI-only signals for the redesigned shell.
// Keeps state.js (data signals) clean from view-state churn.
//
// Persistence: tab/accent/density/rail/rightRailPanels persist to localStorage
// so a reload restores the user's chosen layout. Status filters and palette
// open/close are session-scoped (don't persist).
import { signal, effect } from '@preact/signals'

function loadJSON(key, fallback) {
  try {
    const raw = localStorage.getItem(key)
    if (raw == null) return fallback
    return JSON.parse(raw)
  } catch (_) {
    return fallback
  }
}
function persist(sig, key) {
  effect(() => {
    try { localStorage.setItem(key, JSON.stringify(sig.value)) } catch (_) { /* private mode */ }
  })
}

// Active tab in main work surface.
// Bundle ships 8 tabs: fleet, terminal, mcp, skills, conductor, watchers, costs, search.
// Only `fleet | terminal | costs | search` have data (search filters local sessions only).
// MCP/Skills/Conductor/Watchers render informative stubs because the API doesn't expose them.
// Whether the VIEWER had already chosen a pane. Captured BEFORE persist()
// below, which writes the default back immediately — after that, "is the key
// set?" cannot tell a real choice from our own write. App.js reads this to
// decide whether a /s/{id} link may steer the pane (only on a cold visit).
export const hadStoredTab = (() => {
  try {
    return localStorage.getItem('agentdeck.tab') != null
  } catch (_) {
    return false   // private mode: treat as cold rather than trapping the link
  }
})()

export const activeTabSignal = signal(loadJSON('agentdeck.tab', 'fleet'))
persist(activeTabSignal, 'agentdeck.tab')

// Command palette + tweaks panel open/close.
export const paletteOpenSignal = signal(false)
export const tweaksOpenSignal = signal(false)

// Accent color (drives body[data-accent]).
export const accentSignal = signal(loadJSON('agentdeck.accent', 'blue'))
persist(accentSignal, 'agentdeck.accent')

// Density (drives body[data-density]).
export const densitySignal = signal(loadJSON('agentdeck.density', 'balanced'))
persist(densitySignal, 'agentdeck.density')

// Right rail visible/hidden (drives body[data-rail] and grid-template-columns).
export const railSignal = signal(loadJSON('agentdeck.rail', 'visible'))
persist(railSignal, 'agentdeck.rail')

// Right rail panel toggles (which cards are shown).
export const rightRailPanelsSignal = signal(loadJSON('agentdeck.rightRailPanels', {
  overview: true, usage: true, mcps: true, skills: true, children: true, events: true,
}))
persist(rightRailPanelsSignal, 'agentdeck.rightRailPanels')

// Sidebar status filter chips (running/waiting/error/idle).
export const statusFiltersSignal = signal([])

// Mobile bottom tab (mirror of activeTab on phones).
export const mobileTabSignal = signal('fleet')

// Sidebar column show/hide menu state.
export const showColsSignal = signal(loadJSON('agentdeck.showCols', {
  tool: true, cost: true, branch: false, attach: false, sandbox: false, lastSeen: false,
}))
persist(showColsSignal, 'agentdeck.showCols')

// Profile selector. Initialized to empty so cold loads don't flash a
// hardcoded default before /api/profiles resolves. AppShell seeds this
// from `current` on the first /api/profiles response; consumers
// (Topbar, Footer, AppShell.WorkHead) treat empty as "not yet known"
// and render a neutral placeholder.
export const profileSignal = signal('')

// Apply accent/density/rail dataset attributes for CSS variable swap.
// design-tokens.css uses `:root[data-*]` selectors; we also mirror to
// <body> so bundle-derived rules using `body[data-rail="hidden"]` still match.
effect(() => {
  if (typeof document === 'undefined') return
  document.documentElement.dataset.accent = accentSignal.value
  document.documentElement.dataset.density = densitySignal.value
  document.documentElement.dataset.rail = railSignal.value
  document.body.dataset.accent = accentSignal.value
  document.body.dataset.density = densitySignal.value
  document.body.dataset.rail = railSignal.value
})

// Sidebar `/ filter` text. Lifted out of Sidebar.js useState so
// sidebarRowsSignal — and therefore keyboard nav — sees the same filter the
// user does. Session-scoped, as before.
export const sidebarFilterSignal = signal('')

// Group collapse map: { [groupPath]: boolean }. Only explicitly-toggled groups
// appear; an absent entry means open (the predicate Sidebar.js already used).
// Persisted because the TUI persists collapse and no web API can write it
// server-side. The server's own `expanded` is deliberately NOT honored —
// nothing can write it back, so it would leak TUI collapse in one-way.
export const groupExpandedSignal = signal(loadJSON('agentdeck.groupExpanded', {}))
persist(groupExpandedSignal, 'agentdeck.groupExpanded')
