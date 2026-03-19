// App.js -- Root Preact component (app shell)
// Phase 3: full-page layout with responsive sidebar.
// Replaced Phase 2 floating overlay with AppShell that owns the entire viewport.
import { html } from 'htm/preact'
import { AppShell } from './AppShell.js'

export function App() {
  return html`<${AppShell} />`
}
