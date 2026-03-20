// EmptyStateDashboard.js -- Empty state shown in the content area when no session is selected.
// Displays live session counts (running/waiting/error), quick action buttons, and keyboard hints.
import { html } from 'htm/preact'
import { sessionsSignal, createSessionDialogSignal, groupNameDialogSignal } from './state.js'

export function EmptyStateDashboard() {
  const items = sessionsSignal.value
  const sessions = items.filter(i => i.type === 'session' && i.session)

  const running = sessions.filter(s => s.session.status === 'running' || s.session.status === 'starting').length
  const waiting = sessions.filter(s => s.session.status === 'waiting').length
  const error = sessions.filter(s => s.session.status === 'error').length
  const total = sessions.length

  function openNewSession() {
    createSessionDialogSignal.value = true
  }

  function openNewGroup() {
    groupNameDialogSignal.value = { mode: 'create', groupPath: '', currentName: '', onSubmit: null }
  }

  return html`
    <div class="h-full flex flex-col items-center justify-center gap-sp-24 dark:text-tn-fg text-gray-700 p-sp-32">
      <p class="text-lg font-semibold dark:text-tn-fg text-gray-700">Agent Deck</p>

      <div class="flex gap-sp-24">
        <div class="flex flex-col items-center gap-1">
          <span class="text-2xl font-bold dark:text-tn-green text-green-600">${running}</span>
          <span class="text-xs dark:text-tn-muted text-gray-400">Running</span>
        </div>
        <div class="flex flex-col items-center gap-1">
          <span class="text-2xl font-bold dark:text-tn-yellow text-yellow-600">${waiting}</span>
          <span class="text-xs dark:text-tn-muted text-gray-400">Waiting</span>
        </div>
        <div class="flex flex-col items-center gap-1">
          <span class="text-2xl font-bold dark:text-tn-red text-red-600">${error}</span>
          <span class="text-xs dark:text-tn-muted text-gray-400">Error</span>
        </div>
      </div>

      <div class="flex gap-sp-12">
        <button
          onClick=${openNewSession}
          class="px-sp-16 py-sp-8 rounded dark:bg-tn-blue/20 dark:text-tn-blue dark:hover:bg-tn-blue/30 bg-blue-100 text-blue-700 hover:bg-blue-200 transition-colors text-sm font-medium"
        >
          New Session (n)
        </button>
        <button
          onClick=${openNewGroup}
          class="px-sp-16 py-sp-8 rounded dark:bg-tn-muted/20 dark:text-tn-muted dark:hover:bg-tn-muted/30 bg-gray-100 text-gray-600 hover:bg-gray-200 transition-colors text-sm font-medium"
        >
          New Group
        </button>
      </div>

      <p class="text-xs dark:text-tn-muted text-gray-400 text-center">
        Press <kbd class="px-1.5 py-0.5 rounded dark:bg-tn-card bg-gray-100 font-mono text-xs">n</kbd> to create a session,
        <kbd class="px-1.5 py-0.5 rounded dark:bg-tn-card bg-gray-100 font-mono text-xs">j</kbd>/<kbd class="px-1.5 py-0.5 rounded dark:bg-tn-card bg-gray-100 font-mono text-xs">k</kbd> to navigate
      </p>

      ${total === 0 && html`
        <p class="text-sm dark:text-tn-muted/70 text-gray-400">
          No sessions yet. Create your first one to get started.
        </p>
      `}
    </div>
  `
}
