// AppShell.js -- Five-zone layout shell for the redesigned WebUI.
//
// .app grid: [topbar / sidebar . main . rightrail / footer]. Panes switch
// inside .main via activeTabSignal. Overlays (CommandPalette, TweaksPanel,
// CreateSession/Confirm/GroupName dialogs, toasts) mount as siblings.
//
// Preserves existing dialog + toast components (still Tailwind-classed) so
// no functional regression. Restyling those is a follow-up.
import { html } from 'htm/preact'
import { useEffect } from 'preact/hooks'
import { Topbar } from './Topbar.js'
import { Sidebar } from './Sidebar.js'
import { Footer } from './Footer.js'
import { RightRail } from './RightRail.js'
import { MobileTabs } from './MobileTabs.js'
import { CommandPalette } from './CommandPalette.js'
import { TweaksPanel } from './TweaksPanel.js'
import { TerminalPane } from './panes/TerminalPane.js'
import { CostsPane } from './panes/CostsPane.js'
import { FleetPane } from './panes/FleetPane.js'
import { StubPane } from './panes/StubPane.js'
import { SearchPane } from './panes/SearchPane.js'
import { McpPane } from './panes/McpPane.js'
import { Icon, ICONS } from './icons.js'
import { menuModelSignal } from './dataModel.js'
import {
  selectedIdSignal, createSessionDialogSignal, confirmDialogSignal,
  groupNameDialogSignal, mutationsEnabledSignal, infoDrawerOpenSignal,
  profilesSignal, systemStatsSignal,
} from './state.js'
import {
  activeTabSignal, paletteOpenSignal, tweaksOpenSignal,
  railSignal, profileSignal,
} from './uiState.js'
import { CreateSessionDialog } from './CreateSessionDialog.js'
import { ConfirmDialog } from './ConfirmDialog.js'
import { GroupNameDialog } from './GroupNameDialog.js'
import { ToastContainer, addToast } from './Toast.js'
import { ToastHistoryDrawer } from './ToastHistoryDrawer.js'
import { SettingsPanel } from './SettingsPanel.js'
import { KeyboardShortcuts } from './KeyboardShortcuts.js'
import { apiFetch } from './api.js'
import { shortcutsOverlaySignal } from './state.js'

function WorkHead() {
  const { sessions } = menuModelSignal.value
  const selected = selectedIdSignal.value
  const session = sessions.find(s => s.id === selected) || sessions[0]
  if (!session) return null

  const kindLabel = (session.kind || 'agent').toUpperCase()
  const profile = profileSignal.value || ''
  const canMutate = mutationsEnabledSignal.value
  const modelLabel = session.model
    ? `${session.model}${session.modelVersion ? ` ${session.modelVersion}` : ''}`
    : ''

  const action = (verb) => {
    if (!canMutate) return
    if (verb === 'fork') return apiFetch('POST', `/api/sessions/${session.id}/fork`, { title: session.title + '-fork' }).catch(() => {})
    return apiFetch('POST', `/api/sessions/${session.id}/${verb}`).catch(() => {})
  }

  return html`
    <div class="work-head">
      <div class="path">
        <span class=${`kind ${session.kind || ''}`}>${kindLabel}</span>
        ${profile && html`<span class="seg">${profile} /</span>`}
        <span class="seg">${session.group || 'default'} /</span>
        <span class="cur">${session.title}</span>
      </div>
      <span class=${`status-chip ${session.status}`}><span class="d"/>${session.status}</span>
      ${modelLabel && html`<span class="status-chip model" title=${session.modelId || modelLabel}>${modelLabel}</span>`}
      <span class="spacer"/>
      ${canMutate && html`
        <div class="actions">
          ${(session.status === 'running' || session.status === 'waiting')
            ? html`<button class="btn ghost" onClick=${() => action('stop')}><${Icon} d=${ICONS.stop} size=${12}/>Stop</button>`
            : html`<button class="btn ghost" onClick=${() => action('start')}><${Icon} d=${ICONS.play} size=${12}/>Start</button>`}
          <button class="btn ghost" onClick=${() => action('restart')}><${Icon} d=${ICONS.restart} size=${12}/>Restart</button>
          ${session.tool === 'claude' && html`<button class="btn" onClick=${() => action('fork')}><${Icon} d=${ICONS.fork} size=${12}/>Fork</button>`}
          <button class="btn primary" onClick=${() => (createSessionDialogSignal.value = true)}>
            <${Icon} d=${ICONS.plus} size=${12}/>New <span class="kbd">n</span>
          </button>
        </div>
      `}
    </div>
  `
}

// Pane switcher — TerminalPane is ALWAYS rendered and only hidden via CSS
// when another tab is active. This preserves the xterm.js + WebSocket lifecycle
// across tab switches; unmounting would trigger a reconnect storm and lose
// scrollback. Other panes are cheap enough to mount/unmount on demand.
function Panes({ tab }) {
  return html`
    <div style=${{ display: tab === 'terminal' ? 'flex' : 'none', flex: 1, minHeight: 0, flexDirection: 'column' }}>
      <${TerminalPane}/>
    </div>
    ${tab === 'fleet'     && html`<${FleetPane}/>`}
    ${tab === 'costs'     && html`<${CostsPane}/>`}
    ${tab === 'search'    && html`<${SearchPane}/>`}
    ${tab === 'mcp'       && html`<${McpPane}/>`}
    ${tab === 'skills'    && html`<${StubPane} title="Skills"
                              message="Skill attachments are managed in the TUI today. The web API does not expose skill management yet."
                              hotkey="s"/>`}
    ${tab === 'conductor' && html`<${StubPane} title="Conductor"
                              message="Conductor orchestration view is TUI-only. The web API does not expose child topology, bridges, or NEED escalation."/>`}
    ${tab === 'watchers'  && html`<${StubPane} title="Watchers"
                              message="Watcher framework events are routed in the backend; the web API does not surface event streams or routing config."/>`}
  `
}

export function AppShell() {
  const activeTab = activeTabSignal.value
  const showCreateSession = createSessionDialogSignal.value
  const confirmData = confirmDialogSignal.value
  const groupNameData = groupNameDialogSignal.value
  const drawerOpen = infoDrawerOpenSignal.value

  // Hide the vanilla .app div from the legacy boot path (kept for back-compat
  // until we delete it).
  useEffect(() => {
    const vanillaApp = document.querySelector('body > .app')
    if (vanillaApp && vanillaApp.id !== 'app-root-grid') vanillaApp.style.display = 'none'
    return () => { if (vanillaApp) vanillaApp.style.display = '' }
  }, [])

  // WEB-P0-4 prevention layer: hydrate webMutations gate from /api/settings.
  useEffect(() => {
    fetch('/api/settings')
      .then(r => r.ok ? r.json() : null)
      .then(data => {
        if (data && typeof data.webMutations === 'boolean') {
          mutationsEnabledSignal.value = data.webMutations
        }
      })
      .catch(() => {})
  }, [])

  // Hydrate profilesSignal once. The Topbar reads this for the profile
  // dropdown options and uses the `current` field to seed profileSignal
  // (UI-side selection) on first load.
  useEffect(() => {
    fetch('/api/profiles')
      .then(r => r.ok ? r.json() : null)
      .then(data => {
        if (data && Array.isArray(data.profiles)) {
          profilesSignal.value = data
          if (data.current) profileSignal.value = data.current
        }
      })
      .catch(() => {})
  }, [])

  // Poll /api/system/stats every 5s for the Footer indicators. Stops on
  // unmount; the Footer treats absent fields as "unavailable" so the user
  // sees nothing rather than zeros when a collector is offline.
  useEffect(() => {
    let cancelled = false
    const fetchStats = () => {
      fetch('/api/system/stats')
        .then(r => r.ok ? r.json() : null)
        .then(data => { if (!cancelled && data) systemStatsSignal.value = data })
        .catch(() => {})
    }
    fetchStats()
    const id = setInterval(fetchStats, 5000)
    return () => { cancelled = true; clearInterval(id) }
  }, [])

  // Global keyboard shortcuts — TUI parity, issue #780.
  // Top-10 bindings combined with the existing Web-only ones (Ctrl+K, ]).
  // Guard: any key that isn't a modal-bound modifier combo must NOT fire
  // while the user is typing in an input/textarea/select/contenteditable.
  useEffect(() => {
    // Navigate selectedIdSignal by `delta` (+1 or -1) through the flat
    // session list from menuModelSignal. Stable across SSE updates because
    // we resolve by ID, not by array index in a possibly-stale snapshot.
    const moveFocus = (delta) => {
      const sessions = (menuModelSignal.value?.sessions) || []
      if (sessions.length === 0) return
      const curId = selectedIdSignal.value
      let idx = sessions.findIndex(s => s.id === curId)
      if (idx === -1) idx = delta > 0 ? -1 : sessions.length
      const next = sessions[Math.max(0, Math.min(sessions.length - 1, idx + delta))]
      if (next) {
        // Only change the selected id; do NOT switch to the terminal tab on
        // j/k navigation. Activating the terminal hands focus to xterm.js,
        // which swallows subsequent keypresses (issue #780 review).
        // The TUI's `enter` key is what opens; j/k just moves focus.
        selectedIdSignal.value = next.id
      }
    }
    const focusedSession = () => {
      const sessions = (menuModelSignal.value?.sessions) || []
      const id = selectedIdSignal.value
      return sessions.find(s => s.id === id) || sessions[0] || null
    }
    const closeAllModals = () => {
      paletteOpenSignal.value = false
      tweaksOpenSignal.value = false
      shortcutsOverlaySignal.value = false
      createSessionDialogSignal.value = false
      confirmDialogSignal.value = null
      groupNameDialogSignal.value = null
      infoDrawerOpenSignal.value = false
    }
    const onKey = (e) => {
      const t = e.target
      const inField = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)
      // Cmd+K / Ctrl+K opens palette anywhere (also works inside inputs).
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        paletteOpenSignal.value = true
        return
      }
      // Esc unfocuses inputs and closes overlays — fires even while typing.
      if (e.key === 'Escape') {
        if (inField && typeof t.blur === 'function') t.blur()
        closeAllModals()
        return
      }
      if (inField) return

      // Shift+Enter: open focused session in new browser tab (web equivalent
      // of the TUI's iTerm "new tab" affordance, issue #1077). Check this
      // BEFORE bare Enter so the shift modifier is honored.
      if (e.key === 'Enter' && e.shiftKey) {
        const s = focusedSession()
        if (s) {
          e.preventDefault()
          const url = `${window.location.pathname}#session=${encodeURIComponent(s.id)}`
          window.open(url, '_blank', 'noopener')
        }
        return
      }
      if (e.key === '?') {
        e.preventDefault()
        shortcutsOverlaySignal.value = !shortcutsOverlaySignal.value
      } else if (e.key === '/') {
        e.preventDefault()
        document.querySelector('.side-filter input')?.focus()
      } else if (e.key === 'j') {
        e.preventDefault(); moveFocus(+1)
      } else if (e.key === 'k') {
        e.preventDefault(); moveFocus(-1)
      } else if (e.key === 'Enter') {
        const s = focusedSession()
        if (s) {
          e.preventDefault()
          selectedIdSignal.value = s.id
          activeTabSignal.value = 'terminal'
        }
      } else if (e.key === 'n' && mutationsEnabledSignal.value) {
        createSessionDialogSignal.value = true
      } else if (e.key === 'r') {
        // Web has no session-rename API yet (matrix gap); surface the gap
        // honestly instead of silently no-op'ing.
        const s = focusedSession()
        if (s) addToast(`Rename "${s.title}": use the TUI (web rename API not implemented yet)`, 'info')
      } else if (e.key === 'D') {
        // Shift+D — non-destructive close of focused session. Mirrors
        // TUI's `D` (closeSession): kills the tmux process but keeps the
        // session record so a later start/restart can resurrect it.
        if (!mutationsEnabledSignal.value) return
        const s = focusedSession()
        if (!s) return
        confirmDialogSignal.value = {
          message: `Close session "${s.title}"? The tmux process will be killed; metadata is preserved.`,
          onConfirm: () => apiFetch('POST', `/api/sessions/${s.id}/close`).catch(() => {}),
        }
      } else if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'z') {
        // Ctrl/Cmd+Z — Chrome-style undo of the most recent delete.
        // Mirrors TUI's ctrl+z (Home.undoStack). The server enforces the
        // configurable undo window (default 30s) and returns 404 once
        // the entry expires; surface the result as a toast either way.
        if (!mutationsEnabledSignal.value) return
        e.preventDefault()
        apiFetch('POST', '/api/sessions/undelete')
          .then(resp => {
            if (resp && resp.sessionId) addToast(`Restored session ${resp.sessionId}`, 'success')
            else addToast('Restored last deleted session', 'success')
          })
          .catch(() => addToast('Nothing to undo', 'info'))
      } else if (e.key === 'q') {
        // Mirrors TUI's `q`: dismiss the current modal/overlay. Only fires
        // when no input is focused (guarded above), so it never blocks
        // typing the letter `q` in the search box.
        closeAllModals()
      } else if (e.key === ']') {
        railSignal.value = railSignal.value === 'visible' ? 'hidden' : 'visible'
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // Esc closes info drawer (preserved from old AppShell).
  useEffect(() => {
    if (!drawerOpen) return
    const onKey = (e) => { if (e.key === 'Escape') (infoDrawerOpenSignal.value = false) }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [drawerOpen])

  return html`
    <div id="app-root-grid" class="app">
      <${Topbar}/>
      <${Sidebar}/>
      <div class="main">
        <${WorkHead}/>
        <div class="work-body">
          <${Panes} tab=${activeTab}/>
        </div>
      </div>
      <${RightRail}/>
      <${Footer}/>
      <${MobileTabs}/>

      ${showCreateSession && html`<${CreateSessionDialog}/>`}
      ${confirmData && html`<${ConfirmDialog} ...${confirmData}/>`}
      ${groupNameData && html`<${GroupNameDialog} ...${groupNameData}/>`}

      ${drawerOpen && html`
        <div class="overlay" onClick=${() => (infoDrawerOpenSignal.value = false)}>
          <div class="dialog" onClick=${e => e.stopPropagation()}>
            <div class="dh">
              <span class="kicker">SETTINGS</span>
              <div class="t">Settings</div>
              <button class="icon-btn" onClick=${() => (infoDrawerOpenSignal.value = false)} aria-label="Close settings">
                <${Icon} d=${ICONS.x}/>
              </button>
            </div>
            <div class="db">
              <${SettingsPanel}/>
            </div>
          </div>
        </div>
      `}

      <${CommandPalette}/>
      <${TweaksPanel}/>
      <${KeyboardShortcuts}/>
      <${ToastContainer}/>
      <${ToastHistoryDrawer}/>
    </div>
  `
}
