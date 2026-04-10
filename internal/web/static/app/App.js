// App.js -- Root Preact component (app shell)
// Phase 3: full-page layout with responsive sidebar.
// Phase 6: adds popstate route sync and URL push on selection change.
import { html } from 'htm/preact'
import { useEffect } from 'preact/hooks'
import { AppShell } from './AppShell.js'
import { selectedIdSignal } from './state.js'

export function App() {
  // Route sync: update selectedIdSignal when browser navigates back/forward
  useEffect(() => {
    function onPopState() {
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
      // Don't clear selection on popstate to root: user may still want it
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  // URL push: write URL when selected session changes
  useEffect(() => {
    const id = selectedIdSignal.value
    const currentPath = window.location.pathname
    const targetPath = id ? '/s/' + encodeURIComponent(id) : '/'
    if (currentPath !== targetPath) {
      window.history.pushState(null, '', targetPath)
    }
  }, [selectedIdSignal.value])

  return html`<${AppShell} />`
}
