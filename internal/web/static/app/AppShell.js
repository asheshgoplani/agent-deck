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
import { ToastContainer } from './Toast.js'
import { ToastHistoryDrawer } from './ToastHistoryDrawer.js'
import { SettingsPanel } from './SettingsPanel.js'
import { apiFetch } from './api.js'

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
    ${tab === 'mcp'       && html`<${StubPane} title="MCP Manager"
                              message="MCP attachments are managed in the TUI today. The web API does not expose attach / detach / pool toggles yet."
                              hotkey="m"/>`}
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

  // Global keyboard shortcuts.
  useEffect(() => {
    const onKey = (e) => {
      const t = e.target
      const inField = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)
      // Cmd+K / Ctrl+K opens palette anywhere.
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        paletteOpenSignal.value = true
        return
      }
      if (inField) return
      if (e.key === '?') { e.preventDefault(); tweaksOpenSignal.value = !tweaksOpenSignal.value }
      else if (e.key === '/') {
        e.preventDefault()
        document.querySelector('.side-filter input')?.focus()
      }
      else if (e.key === 'n' && mutationsEnabledSignal.value) { createSessionDialogSignal.value = true }
      else if (e.key === ']') { railSignal.value = railSignal.value === 'visible' ? 'hidden' : 'visible' }
      else if (e.key === 'Escape') {
        paletteOpenSignal.value = false
        tweaksOpenSignal.value = false
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
      <${ToastContainer}/>
      <${ToastHistoryDrawer}/>
    </div>
  `
}
