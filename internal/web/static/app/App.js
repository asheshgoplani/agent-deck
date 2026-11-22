// App.js -- Root Preact component (app shell)
// Phase 3: full-page layout with responsive sidebar.
// Phase 6: adds popstate route sync and URL push on selection change.
// Task 11: /g/{path} gives a selected group the same URL parity as a
// selected session (/s/{id}); applyPath is shared by popstate (here) and
// cold boot (main.js's applyRouteSelection) so the two paths cannot drift.
import { html } from 'htm/preact'
import { useEffect, useState } from 'preact/hooks'
import { AppShell } from './AppShell.js'
import { selectedIdSignal, selectedGroupSignal, selectSession, selectGroup, authRequiredSignal } from './state.js'
import { activeTabSignal } from './uiState.js'
import { PANES } from './paneRegistry.js'

// Map a pathname onto the selection. encodeURIComponent turns a nested group
// path ("work/innotrade") into "work%2Finnotrade", which keeps the no-slash
// guard below meaningful for both routes.
// A URL-driven selection must SHOW what it selected without overruling a pane
// the viewer chose. Groups need no tab handling — Panes renders the group panel
// over whatever tab is active. Sessions live in the terminal pane, which is
// hidden unless the tab is 'terminal'. A /s/{id} link must leave global panes
// such as Fleet, but preserve session-scoped panes such as Skills (PR #2047).
// Inspect the current pane for both cold loads and browser history navigation.
// Not done in selectSession/selectGroup — j/k call
// those and must not switch tabs, or xterm eats the next key (issue #780).
export function applyPath(path) {
  if (path.startsWith('/s/')) {
    const raw = path.slice(3)
    if (raw && !raw.includes('/')) {
      try {
        selectSession(decodeURIComponent(raw))
        const tab = activeTabSignal.value
        if (typeof tab !== 'string' || !PANES[tab]?.sessionScoped) {
          activeTabSignal.value = 'terminal'
        }
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

  if (authRequiredSignal.value) return html`<${AuthRequired} reason=${authRequiredSignal.value}/>`
  return html`
    <${AppShell} />
  `
}

// AuthRequired replaces the shell when the server answered 401: it says the
// server needs an access token and takes one. Signing in reloads the page
// with ?token=, the same link `agent-deck web` prints; main.js reads the
// token into memory and strips it from the address bar.
function AuthRequired({ reason }) {
  const [token, setToken] = useState('')
  const submit = (e) => {
    e.preventDefault()
    const t = token.trim()
    if (!t) return
    const params = new URLSearchParams(window.location.search)
    params.set('token', t)
    window.location.replace(window.location.pathname + '?' + params.toString() + window.location.hash)
  }
  return html`
    <div class="overlay" data-testid="auth-required">
      <form class="dialog" style="max-width: 460px;" onSubmit=${submit}>
        <div class="dh">
          <span class="kicker">SIGN IN</span>
          <div class="t">This server needs an access token</div>
        </div>
        <div class="db">
          <p style="margin: 0 0 12px; color: var(--text-dim); font-size: 12.5px;">
            ${reason === 'rejected'
              ? 'The token in this link was rejected. Paste the current token to sign in.'
              : 'It was started with --token or --token-file. Open the link it printed, or paste the token here.'}
          </p>
          <div class="field">
            <label for="auth-token-input">Access token</label>
            <input id="auth-token-input" type="password" autocomplete="off" autofocus required
                   value=${token} onInput=${e => setToken(e.target.value)}/>
          </div>
        </div>
        <div class="df">
          <button type="submit" class="btn primary" disabled=${!token.trim()}>Sign in</button>
        </div>
      </form>
    </div>
  `
}
