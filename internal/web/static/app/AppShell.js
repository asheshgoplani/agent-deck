// AppShell.js -- Full-page responsive layout shell
// Replaces the vanilla JS .app div with Preact-rendered three-tier responsive layout.
// Phone (<768px): fixed overlay sidebar with backdrop
// Tablet (768-1023px): static sidebar, collapsible via toggle
// Desktop (1024px+): sidebar always visible
import { html } from 'htm/preact'
import { useEffect } from 'preact/hooks'
import { sidebarOpenSignal } from './state.js'
import { Sidebar } from './Sidebar.js'
import { Topbar } from './Topbar.js'

export function AppShell() {
  const sidebarOpen = sidebarOpenSignal.value

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
    </div>
  `
}
