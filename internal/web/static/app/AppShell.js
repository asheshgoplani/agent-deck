// AppShell.js -- Full-page responsive layout shell
// Replaces the vanilla JS .app div with Preact-rendered three-tier responsive layout.
// Phone (<768px): fixed overlay sidebar with backdrop
// Tablet (768-1023px): static sidebar, collapsible via toggle
// Desktop (1024px+): sidebar always visible
import { html } from 'htm/preact'
import { useEffect } from 'preact/hooks'
import { sidebarOpenSignal, createSessionDialogSignal, confirmDialogSignal, groupNameDialogSignal } from './state.js'
import { Sidebar } from './Sidebar.js'
import { Topbar } from './Topbar.js'
import { CreateSessionDialog } from './CreateSessionDialog.js'
import { ConfirmDialog } from './ConfirmDialog.js'
import { GroupNameDialog } from './GroupNameDialog.js'

export function AppShell() {
  const sidebarOpen = sidebarOpenSignal.value
  const showCreateSession = createSessionDialogSignal.value
  const confirmData = confirmDialogSignal.value
  const groupNameData = groupNameDialogSignal.value

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

  return html`
    <div class="flex flex-col h-screen dark:bg-tn-bg bg-tn-light-bg">
      <${Topbar} onToggleSidebar=${toggleSidebar} sidebarOpen=${sidebarOpen} />
      <div class="flex flex-1 min-h-0 relative">

        <!-- Overlay backdrop: phone only, hidden on md+ -->
        ${sidebarOpen && html`
          <div
            class="fixed inset-0 z-30 bg-black/50 md:hidden"
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
          md:relative md:z-auto md:w-64
          lg:translate-x-0
        ">
          <div class="flex items-center justify-between px-3 py-2 border-b dark:border-tn-muted/20 border-gray-200">
            <span class="text-xs font-semibold uppercase tracking-wide dark:text-tn-muted text-gray-500">Sessions</span>
            <span class="flex items-center gap-1">
              <button type="button"
                onClick=${() => (groupNameDialogSignal.value = { mode: 'create', groupPath: '', currentName: '', onSubmit: null })}
                class="p-1 rounded dark:text-tn-muted text-gray-400
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
                class="p-1 rounded dark:text-tn-muted text-gray-400
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

        <!-- Main content: terminal placeholder until Phase 5 -->
        <main class="flex-1 min-w-0 overflow-hidden dark:bg-tn-bg bg-tn-light-bg">
          <div id="terminal-root-preact" class="h-full flex items-center justify-center">
            <span class="dark:text-tn-muted text-gray-400 text-sm">
              Select a session from the sidebar
            </span>
          </div>
        </main>
      </div>

      ${showCreateSession && html`<${CreateSessionDialog} />`}
      ${confirmData && html`<${ConfirmDialog} ...${confirmData} />`}
      ${groupNameData && html`<${GroupNameDialog} ...${groupNameData} />`}
    </div>
  `
}
