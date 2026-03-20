// GroupRow.js -- Collapsible group header row
import { html } from 'htm/preact'
import { groupExpandedSignal, toggleGroup, isGroupExpanded } from './groupState.js'
import { groupNameDialogSignal, confirmDialogSignal } from './state.js'
import { apiFetch } from './api.js'

export function GroupRow({ item }) {
  const group = item.group
  // Read groupExpandedSignal.value to subscribe this component
  void groupExpandedSignal.value
  const expanded = isGroupExpanded(group.path, group.expanded)

  function handleRename(e) {
    e.stopPropagation()
    groupNameDialogSignal.value = {
      mode: 'rename',
      groupPath: group.path,
      currentName: group.name || group.path,
      onSubmit: null
    }
  }

  function handleDeleteGroup(e) {
    e.stopPropagation()
    confirmDialogSignal.value = {
      message: 'Delete group "' + (group.name || group.path) + '"? Sessions will be moved to the default group.',
      onConfirm: () => apiFetch('DELETE', '/api/groups/' + encodeURIComponent(group.path))
    }
  }

  function handleCreateGroup(e) {
    e.stopPropagation()
    groupNameDialogSignal.value = {
      mode: 'create',
      groupPath: '',
      currentName: '',
      onSubmit: null
    }
  }

  return html`
    <li>
      <button
        type="button"
        onClick=${() => toggleGroup(group.path, group.expanded)}
        class="group w-full flex items-center gap-sp-8 px-sp-12 py-2.5 min-h-[44px] text-xs font-semibold
          uppercase tracking-wide dark:text-tn-muted text-gray-500
          dark:bg-tn-muted/5 bg-gray-50/50
          hover:dark:bg-tn-muted/10 hover:bg-gray-100
          hover:dark:text-tn-fg hover:text-gray-700 transition-colors"
        style="padding-left: calc(${item.level || 0} * 1rem + 0.75rem)"
        aria-expanded=${expanded}
      >
        <span class="text-base leading-none select-none">${expanded ? '\u25BE' : '\u25B8'}</span>
        <span class="flex-1 truncate min-w-0 text-left">${group.name || group.path}</span>
        <span class="dark:text-tn-muted/60 text-gray-400 font-normal">
          (${group.sessionCount || 0})
        </span>
        <span class="flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition-opacity flex-shrink-0">
          <button type="button" onClick=${handleCreateGroup} title="New subgroup" aria-label="Create subgroup"
            class="min-w-[44px] min-h-[44px] flex items-center justify-center rounded
                   dark:text-tn-muted hover:dark:text-tn-blue hover:text-gray-700 transition-colors">
            <svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/>
            </svg>
          </button>
          <button type="button" onClick=${handleRename} title="Rename group" aria-label="Rename group"
            class="min-w-[44px] min-h-[44px] flex items-center justify-center rounded
                   dark:text-tn-muted hover:dark:text-tn-blue hover:text-gray-700 transition-colors">
            <svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                    d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"/>
            </svg>
          </button>
          <button type="button" onClick=${handleDeleteGroup} title="Delete group" aria-label="Delete group"
            class="min-w-[44px] min-h-[44px] flex items-center justify-center rounded
                   dark:text-tn-muted hover:dark:text-tn-red hover:text-gray-700 transition-colors">
            <svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                    d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"/>
            </svg>
          </button>
        </span>
      </button>
    </li>
  `
}
