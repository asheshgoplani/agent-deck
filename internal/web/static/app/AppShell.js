// AppShell.js -- Five-zone layout shell for the redesigned WebUI.
//
// .app grid: [topbar / sidebar . main . rightrail / footer]. Panes switch
// inside .main via activeTabSignal. Overlays (CommandPalette, TweaksPanel,
// CreateSession/Confirm/GroupName dialogs, toasts) mount as siblings.
//
// Preserves existing dialog + toast components (still Tailwind-classed) so
// no functional regression. Restyling those is a follow-up.
import { html } from 'htm/preact'
import { useEffect, useState } from 'preact/hooks'
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
import { CommandCenterPane } from './panes/CommandCenterPane.js'
import { ArchivedPane } from './panes/ArchivedPane.js'
import { StubPane } from './panes/StubPane.js'
import { SearchPane } from './panes/SearchPane.js'
import { McpPane } from './panes/McpPane.js'
import { SkillsPane } from './panes/SkillsPane.js'
import { Icon, ICONS } from './icons.js'
import {
  menuModelSignal, sidebarRowsSignal, isGroupOpen, toggleGroupOpen,
  openCreateSessionForGroup, currentGroupPath,
} from './dataModel.js'
import {
  selectedIdSignal, selectedGroupSignal, selectSession, selectGroup, createSessionDialogSignal, confirmDialogSignal,
  groupNameDialogSignal, mutationsEnabledSignal, infoDrawerOpenSignal, editSessionDialogSignal, toastHistoryOpenSignal,
  profilesSignal, systemStatsSignal,
  toolFilterSignal, visibleToolsSignal, toolFilterFallbackSignal,
  hiddenToolsSignal, pickerToolsSignal,
  trustedDomainsSignal, confirmLinkOpenSignal,
} from './state.js'
import {
  activeTabSignal, paletteOpenSignal, tweaksOpenSignal,
  railSignal, profileSignal, groupExpandedSignal,
} from './uiState.js'
import { ConfirmDialog } from './ConfirmDialog.js'
import { GroupNameDialog } from './GroupNameDialog.js'
import { ToastContainer, addToast } from './Toast.js'
import { ToastHistoryDrawer } from './ToastHistoryDrawer.js'
import { SettingsPanel } from './SettingsPanel.js'
import { KeyboardShortcuts } from './KeyboardShortcuts.js'
import { apiFetch, authHeaders } from './api.js'
import { shortcutsOverlaySignal } from './state.js'

function WorkHead() {
  const { sessions, groups } = menuModelSignal.value
  const selected = selectedIdSignal.value
  const selectedGroup = selectedGroupSignal.value
  const profile = profileSignal.value || ''
  const canMutate = mutationsEnabledSignal.value

  // A selected group owns the head: never fall through to sessions[0], which
  // would render an unrelated session's controls above the group stats.
  if (selectedGroup) {
    const group = groups.find(g => g.path === selectedGroup)
    return html`
      <div class="work-head" data-testid="work-head-group">
        <div class="path">
          <span class="kind">GROUP</span>
          ${profile && html`<span class="seg">${profile} /</span>`}
          <span class="cur">${group ? group.name : selectedGroup}</span>
        </div>
        <span class="spacer"/>
      </div>
    `
  }

  const session = sessions.find(s => s.id === selected) || sessions[0]
  if (!session) return null

  const kindLabel = (session.kind || 'agent').toUpperCase()
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
          ${session.canFork && html`<button class="btn" onClick=${() => action('fork')}><${Icon} d=${ICONS.fork} size=${12}/>Fork</button>`}
          <button class="btn primary" onClick=${() => openCreateSessionForGroup(session.group || '')}>
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
// The group stats panel and its data module are only reachable once a viewer
// selects a group, so they are fetched on demand rather than shipped in the
// initial payload -- the page is under a hard total-byte-weight budget
// (.lighthouserc.json) that this feature crossed (PR #2047 review, item 5).
// Same approach the Costs route already uses for chart.umd (issue #1022).
// Generic on-demand module loader for view code that is not needed at first
// paint. Keeps the module out of the initial payload; the promise is cached so
// repeated opens fetch once, and a failed fetch resets it so a later attempt
// retries. Same approach the Costs route uses for chart.umd (issue #1022).
function useLazyComponent(needed, loader, cacheKey, pick) {
  const [Comp, setComp] = useState(null)
  useEffect(() => {
    if (!needed || Comp) return
    let alive = true
    lazyCache[cacheKey] = lazyCache[cacheKey] || loader()
    lazyCache[cacheKey]
      .then(m => { if (alive) setComp(() => pick(m)) })
      .catch(() => { lazyCache[cacheKey] = null })
    return () => { alive = false }
  }, [needed, Comp])
  return Comp
}
const lazyCache = {}

function useLazyGroupStatsPanel(needed) {
  return useLazyComponent(needed, () => import('./GroupStatsPanel.js'), 'groupPanel', m => m.GroupStatsPanel)
}

function Panes({ tab }) {
  // A selected GROUP takes over the work area regardless of which tab is
  // active. The group panel is not a tab, and housing it inside the terminal
  // pane meant a /g/{path} link had to mutate activeTabSignal just to be
  // visible -- which then stomped session-scoped pane choices like Skills
  // (PR #2047 review, item 1). Nulling the tab here hides every tab pane,
  // including the terminal, without unmounting TerminalPane: xterm and its
  // WebSocket stay alive behind the panel, so returning to a session does not
  // reconnect or lose scrollback.
  const groupPath = selectedGroupSignal.value
  const t = groupPath ? null : tab
  const GroupPanel = useLazyGroupStatsPanel(!!groupPath)
  return html`
    <div style=${{ display: t === 'terminal' ? 'flex' : 'none', flex: 1, minHeight: 0, flexDirection: 'column' }}>
      <${TerminalPane}/>
    </div>
    ${groupPath && GroupPanel && html`<${GroupPanel} path=${groupPath}/>`}
    ${t === 'command-center' && html`<${CommandCenterPane}/>`}
    ${t === 'fleet'     && html`<${FleetPane}/>`}
    ${t === 'costs'     && html`<${CostsPane}/>`}
    ${t === 'search'    && html`<${SearchPane}/>`}
    ${t === 'archived'  && html`<${ArchivedPane}/>`}
    ${t === 'mcp'       && html`<${McpPane}/>`}
    ${t === 'skills'    && html`<${SkillsPane}/>`}
    ${t === 'conductor' && html`<${StubPane} title="Conductor"
                              message="Conductor orchestration view is TUI-only. The web API does not expose child topology, bridges, or NEED escalation."/>`}
    ${t === 'watchers'  && html`<${StubPane} title="Watchers"
                              message="Watcher framework events are routed in the backend; the web API does not surface event streams or routing config."/>`}
  `
}

export function AppShell() {
  const activeTab = activeTabSignal.value
  const showCreateSession = createSessionDialogSignal.value
  // Dialog code is never needed at first paint -- fetched when first opened.
  const CreateSessionDialogLazy = useLazyComponent(
    !!showCreateSession, () => import('./CreateSessionDialog.js'), 'createDialog', m => m.CreateSessionDialog)
  // Same case: EditSessionDialog is dialog-only and was eagerly imported.
  // Deferring it keeps this PR's net first-paint weight comfortably under the
  // budget rather than sitting on the line (PR #2047 review, item 5).
  const EditSessionDialogLazy = useLazyComponent(
    !!editSessionDialogSignal.value, () => import('./EditSessionDialog.js'), 'editDialog', m => m.EditSessionDialog)
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
  // Also hydrates the show_only_installed_tools filter (issue #1259) so the
  // new-session dialog can hide tools whose command is not on PATH.
  useEffect(() => {
    fetch('/api/settings', { headers: authHeaders() })
      .then(r => r.ok ? r.json() : null)
      .then(data => {
        if (!data) return
        if (typeof data.webMutations === 'boolean') {
          mutationsEnabledSignal.value = data.webMutations
        }
        if (typeof data.toolFilter === 'boolean') {
          toolFilterSignal.value = data.toolFilter
        }
        if (Array.isArray(data.visibleTools)) {
          visibleToolsSignal.value = data.visibleTools
        }
        if (typeof data.toolFilterFallback === 'boolean') {
          toolFilterFallbackSignal.value = data.toolFilterFallback
        }
        if (Array.isArray(data.hiddenTools)) {
          hiddenToolsSignal.value = data.hiddenTools
        }
        if (Array.isArray(data.pickerTools) && data.pickerTools.length > 0) {
          pickerToolsSignal.value = data.pickerTools
        }
        // Terminal link-open policy (issue #1682).
        if (Array.isArray(data.trustedDomains)) {
          trustedDomainsSignal.value = data.trustedDomains
        }
        if (typeof data.confirmLinkOpen === 'boolean') {
          confirmLinkOpenSignal.value = data.confirmLinkOpen
        }
      })
      .catch(() => {})
  }, [])

  // Hydrate profilesSignal once. The Topbar reads this for the profile
  // dropdown options and uses the `current` field to seed profileSignal
  // (UI-side selection) on first load.
  useEffect(() => {
    fetch('/api/profiles', { headers: authHeaders() })
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
      fetch('/api/system/stats', { headers: authHeaders() })
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
    // Scroll the newly selected row into view. Rows carry data-row-key
    // matching sidebarRowsSignal keys (Sidebar.js).
    const revealRow = (key) => {
      const el = document.querySelector(`[data-row-key="${CSS.escape(key)}"]`)
      if (el && typeof el.scrollIntoView === 'function') {
        el.scrollIntoView({ block: 'nearest' })
      }
    }

    // Move the selection by `delta` through the sidebar's RENDERED rows —
    // group headers included, collapse state and filters honored. Walking
    // the raw session array (the previous behavior) could land on a row
    // hidden inside a collapsed group or filtered off screen.
    const moveFocus = (delta) => {
      const rows = sidebarRowsSignal.value
      if (rows.length === 0) return

      const groupPath = selectedGroupSignal.value
      const sessionId = selectedIdSignal.value
      let idx = -1
      if (groupPath) idx = rows.findIndex(r => r.type === 'group' && r.path === groupPath)
      else if (sessionId) idx = rows.findIndex(r => r.type === 'session' && r.id === sessionId)
      if (idx === -1) idx = delta > 0 ? -1 : rows.length

      const next = rows[Math.max(0, Math.min(rows.length - 1, idx + delta))]
      if (!next) return
      // Only move the selection; do NOT switch to the terminal tab. Activating
      // the terminal hands focus to xterm.js, which swallows later keypresses
      // (issue #780 review). Enter is what opens.
      if (next.type === 'group') selectGroup(next.path)
      else selectSession(next.id)
      revealRow(next.key)
    }

    // Collapse/expand the focused group. Mirrors the TUI's h/left and
    // l/right/tab (internal/ui/home.go:999, :8786). No-op unless a group is
    // actually selected. Routes through the shared toggleGroupOpen (review
    // finding #7) — a toggle is equivalent to a forced set here because the
    // current state is checked first, so this keeps its own "did a group
    // exist to act on" return value that ArrowLeft/ArrowRight rely on for
    // preventDefault.
    const setGroupOpen = (open) => {
      const p = selectedGroupSignal.value
      if (!p) return false
      if (isGroupOpen(groupExpandedSignal.value, p) !== open) toggleGroupOpen(p)
      return true
    }

    const focusedSession = () => {
      const sessions = (menuModelSignal.value?.sessions) || []
      const id = selectedIdSignal.value
      if (!id) return null
      return sessions.find(s => s.id === id) || null
    }
    const closeAllModals = () => {
      paletteOpenSignal.value = false
      tweaksOpenSignal.value = false
      shortcutsOverlaySignal.value = false
      createSessionDialogSignal.value = null
      editSessionDialogSignal.value = null
      confirmDialogSignal.value = null
      groupNameDialogSignal.value = null
      infoDrawerOpenSignal.value = false
      toastHistoryOpenSignal.value = false
    }
    // Every overlay/modal signal closeAllModals resets, kept as one list so
    // this predicate and closeAllModals cannot drift apart (review finding
    // #1). Anything here must block keyboard shortcuts — like the group
    // Tab-toggle below — that would otherwise fire invisibly behind an open
    // dialog.
    const anyOverlayOpen = () => !!(
      paletteOpenSignal.value ||
      tweaksOpenSignal.value ||
      shortcutsOverlaySignal.value ||
      createSessionDialogSignal.value ||
      editSessionDialogSignal.value ||
      confirmDialogSignal.value ||
      groupNameDialogSignal.value ||
      infoDrawerOpenSignal.value ||
      toastHistoryOpenSignal.value
    )
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
      } else if (e.key === 'j' || e.key === 'ArrowDown') {
        e.preventDefault(); moveFocus(+1)
      } else if (e.key === 'k' || e.key === 'ArrowUp') {
        e.preventDefault(); moveFocus(-1)
      } else if (e.key === 'ArrowLeft' || e.key === 'h') {
        if (setGroupOpen(false)) e.preventDefault()
      } else if (e.key === 'ArrowRight' || e.key === 'l') {
        if (setGroupOpen(true)) e.preventDefault()
      } else if (e.key === 'Enter')  {
        if (selectedGroupSignal.value) {
          e.preventDefault()
          toggleGroupOpen(selectedGroupSignal.value)
          return
        }
        const s = focusedSession()
        if (s) {
          e.preventDefault()
          selectSession(s.id)
          activeTabSignal.value = 'terminal'
        }
      } else if (e.key === 'n' && mutationsEnabledSignal.value) {
        e.preventDefault()
        openCreateSessionForGroup(currentGroupPath())
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

      ${showCreateSession && CreateSessionDialogLazy && html`<${CreateSessionDialogLazy}/>`}
      ${EditSessionDialogLazy && html`<${EditSessionDialogLazy}/>`}
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
