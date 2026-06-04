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

// Show-archived toggle (web↔TUI parity, Task 8). Default false = archived
// sessions hidden, matching the TUI's default. When true, archived rows render
// dimmed with an [archived] badge. Persisted so the choice survives reload.
export const showArchivedSignal = signal(loadJSON('agentdeck.showArchived', false))
persist(showArchivedSignal, 'agentdeck.showArchived')

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
