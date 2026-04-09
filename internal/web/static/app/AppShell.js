// AppShell.js -- Full-page responsive layout shell
// Replaces the vanilla JS .app div with Preact-rendered three-tier responsive layout.
// Phone (<768px): fixed overlay sidebar with backdrop
// Tablet (768-1023px): static sidebar, collapsible via toggle
// Desktop (1024px+): sidebar always visible
import { html } from 'htm/preact'
import { useEffect } from 'preact/hooks'
import { sidebarOpenSignal, sidebarWidthSignal, clampSidebarWidth, selectedIdSignal, createSessionDialogSignal, confirmDialogSignal, groupNameDialogSignal, activeTabSignal, infoDrawerOpenSignal, mutationsEnabledSignal } from './state.js'
import { Sidebar } from './Sidebar.js'
import { Topbar } from './Topbar.js'
import { CreateSessionDialog } from './CreateSessionDialog.js'
import { ConfirmDialog } from './ConfirmDialog.js'
import { GroupNameDialog } from './GroupNameDialog.js'
import { TerminalPanel } from './TerminalPanel.js'
import { CostDashboard } from './CostDashboard.js'
import { SettingsPanel } from './SettingsPanel.js'
import { ToastContainer } from './Toast.js'
import { ToastHistoryDrawer } from './ToastHistoryDrawer.js'

function SidebarResizeHandle() {
  function onPointerDown(e) {
    e.preventDefault()
    const startX = e.clientX
    const startWidth = sidebarWidthSignal.value
    const target = e.currentTarget
    try { target.setPointerCapture(e.pointerId) } catch (_) {}

    function onMove(ev) {
      const delta = ev.clientX - startX
      sidebarWidthSignal.value = clampSidebarWidth(startWidth + delta)
    }
    function onUp(ev) {
      target.removeEventListener('pointermove', onMove)
      target.removeEventListener('pointerup', onUp)
      target.removeEventListener('pointercancel', onUp)
      try { target.releasePointerCapture(ev.pointerId) } catch (_) {}
      try {
        localStorage.setItem('sidebar-width', String(sidebarWidthSignal.value))
      } catch (_) {}
    }
    target.addEventListener('pointermove', onMove)
    target.addEventListener('pointerup', onUp)
    target.addEventListener('pointercancel', onUp)
  }

  return html`
    <div
      class="hidden md:block absolute top-0 bottom-0 w-2 -ml-1 cursor-col-resize z-20 select-none
             hover:dark:bg-tn-blue/30 hover:bg-blue-200"
      style="left: ${sidebarWidthSignal.value}px;"
      data-testid="sidebar-resize-handle"
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize sidebar"
      onPointerDown=${onPointerDown}
    ></div>
  `
}

export function AppShell() {
  function isDesktop() {
    return typeof window !== 'undefined' && window.innerWidth >= 768
  }
  // Subscribe to sidebarWidthSignal so the inline width re-renders on drag.
  const sidebarWidth = sidebarWidthSignal.value
  const sidebarOpen = sidebarOpenSignal.value
  const showCreateSession = createSessionDialogSignal.value
  const confirmData = confirmDialogSignal.value
  const groupNameData = groupNameDialogSignal.value
  const activeTab = activeTabSignal.value
  const drawerOpen = infoDrawerOpenSignal.value

  function toggleSidebar() {
    const next = !sidebarOpenSignal.value
    sidebarOpenSignal.value = next
    localStorage.setItem('agentdeck.sidebarOpen', String(next))
  }

  // Hide the vanilla .app div once AppShell mounts
  useEffect(() => {
    const vanillaApp = document.querySelector('.app')
    if (vanillaApp) vanillaApp.style.display = 'none'
    return () => {
      if (vanillaApp) vanillaApp.style.display = ''
    }
  }, [])

  // WEB-P0-4 prevention layer: read webMutations from /api/settings on mount.
  // Defaults to true (optimistic); flips to false if the server is in
  // read-only mutations mode. When false, SessionRow hides its write
  // toolbar and CreateSessionDialog early-returns null, so users cannot
  // click write buttons and generate 403 error spam that would flood the
  // toast stack (06-04's mitigation layer handles any leaks).
  useEffect(() => {
    fetch('/api/settings')
      .then(r => r.ok ? r.json() : null)
      .then(data => {
        if (data && typeof data.webMutations === 'boolean') {
          mutationsEnabledSignal.value = data.webMutations
        }
      })
      .catch(() => {
        // Network failure — keep optimistic default (true) so the UI
        // does not lock out legitimate users on transient failures.
      })
  }, [])

  // Close info drawer on Escape key
  useEffect(() => {
    if (!drawerOpen) return
    function onKey(e) {
      if (e.key === 'Escape') { infoDrawerOpenSignal.value = false }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [drawerOpen])

  // Auto-close mobile drawer when a session is selected
  useEffect(() => {
    if (selectedIdSignal.value && window.innerWidth < 768) {
      sidebarOpenSignal.value = false
    }
  }, [selectedIdSignal.value])

  return html`
    <div class="flex flex-col h-screen dark:bg-tn-bg bg-tn-light-bg">
      <${Topbar} onToggleSidebar=${toggleSidebar} sidebarOpen=${sidebarOpen} />
      <div class="flex flex-1 min-h-0 relative">

        <!-- Overlay backdrop: phone only, hidden on md+ -->
        ${sidebarOpen && html`
          <div
            class="fixed inset-0 z-30 bg-black/50 md:hidden cursor-pointer"
            onClick=${toggleSidebar}
            aria-hidden="true"
          />`}

        <!-- Sidebar:
             phone:   fixed overlay, slides from left
             tablet:  static, collapsible via sidebarOpen
             desktop: always visible via lg:translate-x-0 -->
        <aside class="
          fixed inset-y-0 left-0 z-40 w-72 flex flex-col
          dark:bg-tn-panel bg-white
          border-r dark:border-tn-muted/20 border-gray-200
          transform transition-transform duration-200
          ${sidebarOpen ? 'translate-x-0' : '-translate-x-full'}
          md:relative md:z-auto
          lg:translate-x-0
        " style=${isDesktop() ? `width: ${sidebarWidth}px;` : ''}>
          <div class="flex items-center justify-between px-sp-12 py-sp-8 border-b dark:border-tn-muted/20 border-gray-200">
            <span class="text-xs font-semibold uppercase tracking-wide dark:text-tn-muted text-gray-500">Sessions</span>
            <span class="flex items-center gap-1">
              <button type="button"
                onClick=${() => (groupNameDialogSignal.value = { mode: 'create', groupPath: '', currentName: '', onSubmit: null })}
                class="p-2 min-w-[44px] min-h-[44px] flex items-center justify-center rounded dark:text-tn-muted text-gray-400
                       hover:dark:text-tn-fg hover:text-gray-700
                       hover:dark:bg-tn-muted/10 hover:bg-gray-100 transition-colors"
                title="New group"
                aria-label="New group">
                <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                        d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/>
                </svg>
              </button>
              <button type="button"
                onClick=${() => (createSessionDialogSignal.value = true)}
                class="p-2 min-w-[44px] min-h-[44px] flex items-center justify-center rounded dark:text-tn-muted text-gray-400
                       hover:dark:text-tn-fg hover:text-gray-700
                       hover:dark:bg-tn-muted/10 hover:bg-gray-100 transition-colors"
                title="New session (n)"
                aria-label="New session">
                <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/>
                </svg>
              </button>
            </span>
          </div>
          <${Sidebar} />
        </aside>

        <${SidebarResizeHandle} />

        <!-- Main content: terminal and costs tabs -->
        <!-- TerminalPanel is always rendered (CSS hidden when costs active) to preserve xterm.js + WebSocket -->
        <main class="flex-1 min-w-0 overflow-hidden dark:bg-tn-bg bg-tn-light-bg relative">
          <div class="${activeTab === 'terminal' ? 'h-full' : 'hidden'}">
            <${TerminalPanel} />
          </div>
          ${activeTab === 'costs' && html`<${CostDashboard} />`}
        </main>
      </div>

      ${showCreateSession && html`<${CreateSessionDialog} />`}
      ${confirmData && html`<${ConfirmDialog} ...${confirmData} />`}
      ${groupNameData && html`<${GroupNameDialog} ...${groupNameData} />`}

      ${drawerOpen && html`
        <div
          class="fixed inset-0 z-40 bg-black/40"
          onClick=${() => { infoDrawerOpenSignal.value = false }}
          onKeyDown=${(e) => { if (e.key === 'Escape') infoDrawerOpenSignal.value = false }}
          aria-hidden="true"
        />
        <div class="fixed top-0 right-0 bottom-0 z-50 w-72 max-w-[90vw]
                    dark:bg-tn-panel bg-white
                    border-l dark:border-tn-muted/20 border-gray-200
                    shadow-xl flex flex-col"
             role="dialog"
             aria-label="Info panel">
          <div class="flex items-center justify-between px-sp-12 py-sp-8
                      border-b dark:border-tn-muted/20 border-gray-200">
            <span class="text-sm font-semibold dark:text-tn-fg text-gray-900">Info</span>
            <button
              type="button"
              onClick=${() => { infoDrawerOpenSignal.value = false }}
              class="dark:text-tn-muted text-gray-400 hover:dark:text-tn-fg hover:text-gray-700 transition-colors p-1 rounded hover:dark:bg-tn-muted/10 hover:bg-gray-100"
              aria-label="Close info panel"
            >\u2715</button>
          </div>
          <div class="flex-1 overflow-y-auto px-sp-12 py-sp-12">
            <${SettingsPanel} />
          </div>
        </div>
      `}
      <${ToastContainer} />
      <${ToastHistoryDrawer} />
    </div>
  `
}
