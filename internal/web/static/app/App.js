// App.js -- Root Preact component (app shell)
// Phase 3: full-page layout with responsive sidebar.
// Phase 6: adds popstate route sync and URL push on selection change.
// Task 11: /g/{path} gives a selected group the same URL parity as a
// selected session (/s/{id}); applyPath is shared by popstate (here) and
// cold boot (main.js's applyRouteSelection) so the two paths cannot drift.
import { html } from 'htm/preact'
import { useEffect } from 'preact/hooks'
import { AppShell } from './AppShell.js'
import { selectedIdSignal, selectedGroupSignal, selectSession, selectGroup } from './state.js'
import { activeTabSignal, hadStoredTab } from './uiState.js'

// Map a pathname onto the selection. encodeURIComponent turns a nested group
// path ("work/innotrade") into "work%2Finnotrade", which keeps the no-slash
// guard below meaningful for both routes.
// A URL-driven selection must SHOW what it selected without overruling a pane
// the viewer chose. Groups need no tab handling — Panes renders the group panel
// over whatever tab is active. Sessions live in the terminal pane, which is
// hidden unless the tab is 'terminal', so a COLD /s/{id} link must steer; one
// from a viewer with a stored pane choice must not (PR #2047 review item 1:
// forcing it broke Skills). Not done in selectSession/selectGroup — j/k call
// those and must not switch tabs, or xterm eats the next key (issue #780).
export function applyPath(path) {
  if (path.startsWith('/s/')) {
    const raw = path.slice(3)
    if (raw && !raw.includes('/')) {
      try {
        selectSession(decodeURIComponent(raw))
        if (!hadStoredTab) activeTabSignal.value = 'terminal'
      } catch (_) { selectSession(null) }
      return
    }
  }
  if (path.startsWith('/g/')) {
    const raw = path.slice(3)
    if (raw && !raw.includes('/')) {
      try { selectGroup(decodeURIComponent(raw)) } catch (_) { selectSession(null) }
      return
    }
  }
  // Clearing on popstate to / lets the empty dashboard render when the user
  // navigates back.
  if (path === '/') selectSession(null)
}

export function App() {
  // Route sync: update the selection when the browser navigates back/forward.
  useEffect(() => {
    function onPopState() {
      applyPath(window.location.pathname || '/')
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  // URL push: write the URL when the selection changes.
  useEffect(() => {
    const id = selectedIdSignal.value
    const groupPath = selectedGroupSignal.value
    let targetPath = '/'
    if (id) targetPath = '/s/' + encodeURIComponent(id)
    else if (groupPath) targetPath = '/g/' + encodeURIComponent(groupPath)
    if (window.location.pathname !== targetPath) {
      window.history.pushState(null, '', targetPath)
    }
  }, [selectedIdSignal.value, selectedGroupSignal.value])

  return html`
    <${AppShell} />
  `
}
