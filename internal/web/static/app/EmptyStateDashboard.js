// EmptyStateDashboard.js -- Empty state shown in the content area when no session is selected.
// Displays live session counts (running/waiting/error), a "Recently active" list of up to 5
// sessions when any exist (LAYT-07), quick action buttons, and keyboard hints.
import { html } from 'htm/preact'
import { sessionsSignal, selectedIdSignal, createSessionDialogSignal, groupNameDialogSignal } from './state.js'

// Status priority for sorting the Recently active list: running > waiting > others.
const STATUS_PRIORITY = {
  running: 0,
  starting: 1,
  waiting: 2,
  idle: 3,
  error: 4,
  stopped: 5,
}

function statusPriority(status) {
  if (status in STATUS_PRIORITY) return STATUS_PRIORITY[status]
  return 99
}

export function EmptyStateDashboard() {
  const items = sessionsSignal.value
  const sessions = items.filter(i => i.type === 'session' && i.session)

  const running = sessions.filter(s => s.session.status === 'running' || s.session.status === 'starting').length
  const waiting = sessions.filter(s => s.session.status === 'waiting').length
  const error = sessions.filter(s => s.session.status === 'error').length
  const total = sessions.length

  // Recently active: sort by status priority, then by id for stable ordering; take first 5.
  const recentlyActive = sessions
    .slice()
    .sort((a, b) => {
      const pa = statusPriority(a.session.status)
      const pb = statusPriority(b.session.status)
      if (pa !== pb) return pa - pb
      return String(a.session.id).localeCompare(String(b.session.id))
    })
    .slice(0, 5)

  function openNewSession() {
    createSessionDialogSignal.value = true
  }

  function openNewGroup() {
    groupNameDialogSignal.value = { mode: 'create', groupPath: '', currentName: '', onSubmit: null }
  }

  function selectRecent(id) {
    selectedIdSignal.value = id
  }

  return html`
    <div class="h-full overflow-y-auto p-sp-24">
      <div data-testid="empty-state-dashboard" class="max-w-4xl mx-auto flex flex-col gap-sp-24 dark:text-tn-fg text-gray-700">

        <!-- Brand header -->
        <div class="flex flex-col items-center gap-sp-8">
          <svg class="w-16 h-16" viewBox="0 0 64 64" fill="none" aria-hidden="true">
            <rect x="18" y="8" width="36" height="44" rx="6" fill="#565f89" opacity="0.5"/>
            <rect x="13" y="12" width="36" height="44" rx="6" fill="#7aa2f7" opacity="0.7"/>
            <rect x="8" y="16" width="36" height="44" rx="6" fill="#7aa2f7"/>
            <rect x="14" y="28" width="16" height="3" rx="1.5" fill="#73daca"/>
            <circle cx="34" cy="29.5" r="2" fill="#73daca"/>
            <rect x="14" y="36" width="12" height="2.5" rx="1.25" fill="#a9b1d6" opacity="0.5"/>
            <rect x="14" y="42" width="20" height="2.5" rx="1.25" fill="#a9b1d6" opacity="0.3"/>
          </svg>
          <p class="text-lg font-semibold dark:text-tn-fg text-gray-700">Agent Deck</p>
        </div>

        <!-- Stats grid: 1 col on mobile, 3 cols on lg+ -->
        <div class="grid grid-cols-1 lg:grid-cols-3 gap-sp-16">
          <div class="flex flex-col items-center gap-1 p-sp-16 rounded-lg dark:bg-tn-card bg-white border dark:border-tn-muted/20 border-gray-200">
            <span class="text-3xl font-bold dark:text-tn-green text-green-600">${running}</span>
            <span class="text-xs uppercase tracking-wide dark:text-tn-muted text-gray-500">Running</span>
          </div>
          <div class="flex flex-col items-center gap-1 p-sp-16 rounded-lg dark:bg-tn-card bg-white border dark:border-tn-muted/20 border-gray-200">
            <span class="text-3xl font-bold dark:text-tn-yellow text-yellow-600">${waiting}</span>
            <span class="text-xs uppercase tracking-wide dark:text-tn-muted text-gray-500">Waiting</span>
          </div>
          <div class="flex flex-col items-center gap-1 p-sp-16 rounded-lg dark:bg-tn-card bg-white border dark:border-tn-muted/20 border-gray-200">
            <span class="text-3xl font-bold dark:text-tn-red text-red-600">${error}</span>
            <span class="text-xs uppercase tracking-wide dark:text-tn-muted text-gray-500">Error</span>
          </div>
        </div>

        <!-- Recently active card (only when sessions exist) -->
        ${total > 0 && html`
          <div class="p-sp-16 rounded-lg dark:bg-tn-card bg-white border dark:border-tn-muted/20 border-gray-200">
            <span class="block text-xs font-semibold uppercase tracking-wide dark:text-tn-muted text-gray-500 mb-sp-8">Recently active</span>
            <ul class="flex flex-col gap-1" role="list">
              ${recentlyActive.map(item => html`
                <li key=${item.session.id}>
                  <button
                    type="button"
                    onClick=${() => selectRecent(item.session.id)}
                    class="w-full min-w-0 flex items-center gap-sp-8 px-sp-12 py-2 min-h-[44px] rounded text-left text-sm
                           dark:bg-tn-muted/10 bg-gray-50 dark:hover:bg-tn-muted/20 hover:bg-gray-100 transition-colors
                           dark:text-tn-fg text-gray-700"
                    data-session-id=${item.session.id}
                    title=${item.session.title || item.session.id}
                  >
                    <span class="w-2 h-2 rounded-full flex-shrink-0 ${item.session.status === 'running' || item.session.status === 'starting' ? 'bg-tn-green' : item.session.status === 'waiting' ? 'bg-tn-yellow' : item.session.status === 'error' ? 'bg-tn-red' : 'bg-tn-muted'}"></span>
                    <span class="flex-1 truncate min-w-0" title=${item.session.title || item.session.id}>${item.session.title || item.session.id}</span>
                    <span class="text-xs dark:text-tn-muted text-gray-600 flex-shrink-0">${item.session.status}</span>
                  </button>
                </li>
              `)}
            </ul>
          </div>
        `}

        <!-- Quick actions card -->
        <div class="p-sp-16 rounded-lg dark:bg-tn-card bg-white border dark:border-tn-muted/20 border-gray-200 flex flex-col items-center gap-sp-12">
          <div class="flex gap-sp-12 flex-wrap justify-center">
            <button
              onClick=${openNewSession}
              class="px-sp-16 py-sp-8 min-h-[44px] rounded dark:bg-tn-blue/20 dark:text-tn-blue dark:hover:bg-tn-blue/30 bg-blue-100 text-blue-700 hover:bg-blue-200 transition-colors text-sm font-medium"
            >
              New Session (n)
            </button>
            <button
              onClick=${openNewGroup}
              class="px-sp-16 py-sp-8 min-h-[44px] rounded dark:bg-tn-muted/20 dark:text-tn-muted dark:hover:bg-tn-muted/30 bg-gray-100 text-gray-600 hover:bg-gray-200 transition-colors text-sm font-medium"
            >
              New Group
            </button>
          </div>
          <p class="text-xs dark:text-tn-muted text-gray-600 text-center">
            Press <kbd class="px-1.5 py-0.5 rounded dark:bg-tn-card bg-gray-100 font-mono text-xs">n</kbd> to create a session,
            <kbd class="px-1.5 py-0.5 rounded dark:bg-tn-card bg-gray-100 font-mono text-xs">j</kbd>/<kbd class="px-1.5 py-0.5 rounded dark:bg-tn-card bg-gray-100 font-mono text-xs">k</kbd> to navigate
          </p>
          ${total === 0 && html`
            <p class="text-sm dark:text-tn-muted/70 text-gray-600">
              No sessions yet. Create your first one to get started.
            </p>
          `}
        </div>

      </div>
    </div>
  `
}
