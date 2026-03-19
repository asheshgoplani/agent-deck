// SessionRow.js -- Single session item with status dot, title, tool badge, cost badge
import { html } from 'htm/preact'
import { selectedIdSignal, sessionCostsSignal, confirmDialogSignal } from './state.js'
import { apiFetch } from './api.js'

const STATUS_COLORS = {
  running:  'bg-tn-blue',
  waiting:  'bg-tn-yellow animate-pulse',
  idle:     'bg-tn-muted',
  error:    'bg-tn-red',
  starting: 'bg-tn-purple animate-pulse',
  stopped:  'bg-tn-muted/50',
}

export function SessionRow({ item, focused }) {
  const session = item.session
  const isSelected = selectedIdSignal.value === session.id
  const costUSD = sessionCostsSignal.value[session.id]
  const costLabel = (costUSD != null && costUSD >= 0.001)
    ? '$' + costUSD.toFixed(2)
    : null
  const dotColor = STATUS_COLORS[session.status] || 'bg-tn-muted'

  function handleClick() {
    selectedIdSignal.value = session.id
  }

  function handleStop(e) {
    e.stopPropagation()
    apiFetch('POST', '/api/sessions/' + session.id + '/stop')
  }

  function handleRestart(e) {
    e.stopPropagation()
    apiFetch('POST', '/api/sessions/' + session.id + '/restart')
  }

  function handleDelete(e) {
    e.stopPropagation()
    confirmDialogSignal.value = {
      message: 'Delete session "' + (session.title || session.id) + '"? This cannot be undone.',
      onConfirm: () => apiFetch('DELETE', '/api/sessions/' + session.id)
    }
  }

  function handleFork(e) {
    e.stopPropagation()
    apiFetch('POST', '/api/sessions/' + session.id + '/fork')
  }

  return html`
    <li>
      <button
        type="button"
        onClick=${handleClick}
        class="w-full flex items-center gap-2 px-3 py-1.5 rounded text-left text-sm
          transition-colors
          ${isSelected
            ? 'dark:bg-tn-blue/20 bg-blue-100 dark:text-tn-fg text-gray-900'
            : focused
              ? 'dark:bg-tn-muted/10 bg-gray-100 dark:text-tn-fg text-gray-700'
              : 'dark:hover:bg-tn-muted/10 hover:bg-gray-50 dark:text-tn-fg text-gray-700'
          }"
        style="padding-left: calc(${item.level || 0} * 1rem + 0.75rem)"
        data-session-id=${session.id}
      >
        <span class="w-2 h-2 rounded-full flex-shrink-0 ${dotColor}"></span>
        <span class="flex-1 truncate">${session.title || session.id}</span>
        <span class="text-xs dark:text-tn-muted text-gray-400 flex-shrink-0">
          ${session.tool || 'shell'}
        </span>
        ${costLabel && html`
          <span class="text-xs dark:text-tn-green text-green-600 flex-shrink-0 font-mono">
            ${costLabel}
          </span>
        `}
        ${(focused || isSelected) && html`
          <span class="flex items-center gap-0.5 flex-shrink-0 ml-1">
            ${(session.status === 'running' || session.status === 'waiting') && html`
              <button type="button" onClick=${handleStop} title="Stop (s)"
                class="p-0.5 rounded dark:text-tn-muted hover:dark:text-tn-yellow hover:dark:bg-tn-yellow/10
                       text-gray-400 hover:text-yellow-600 hover:bg-yellow-50 transition-colors">
                <svg class="w-3.5 h-3.5" fill="currentColor" viewBox="0 0 20 20">
                  <rect x="5" y="5" width="10" height="10" rx="1"/>
                </svg>
              </button>
            `}
            ${(session.status === 'idle' || session.status === 'stopped' || session.status === 'error') && html`
              <button type="button" onClick=${handleRestart} title="Restart (r)"
                class="p-0.5 rounded dark:text-tn-muted hover:dark:text-tn-green hover:dark:bg-tn-green/10
                       text-gray-400 hover:text-green-600 hover:bg-green-50 transition-colors">
                <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                        d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/>
                </svg>
              </button>
            `}
            ${session.tool === 'claude' && html`
              <button type="button" onClick=${handleFork} title="Fork"
                class="p-0.5 rounded dark:text-tn-muted hover:dark:text-tn-purple hover:dark:bg-tn-purple/10
                       text-gray-400 hover:text-purple-600 hover:bg-purple-50 transition-colors">
                <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                        d="M8 7v8a2 2 0 002 2h6M8 7V5a2 2 0 012-2h4.586a1 1 0 01.707.293l4.414 4.414a1 1 0 01.293.707V15a2 2 0 01-2 2h-2"/>
                </svg>
              </button>
            `}
            <button type="button" onClick=${handleDelete} title="Delete (d)"
              class="p-0.5 rounded dark:text-tn-muted hover:dark:text-tn-red hover:dark:bg-tn-red/10
                     text-gray-400 hover:text-red-600 hover:bg-red-50 transition-colors">
              <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                      d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/>
              </svg>
            </button>
          </span>
        `}
      </button>
    </li>
  `
}
