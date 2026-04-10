// GroupRow.js -- Collapsible group header row
import { html } from 'htm/preact'
import { useState } from 'preact/hooks'
import { groupExpandedSignal, toggleGroup, isGroupExpanded } from './groupState.js'
import { groupNameDialogSignal, confirmDialogSignal, sessionsSignal, searchQuerySignal } from './state.js'
import { apiFetch } from './api.js'

// countVisibleChildren returns the number of sessions whose groupPath
// belongs to the given group (or any subgroup) and that pass the active
// search filter. The count is derived from sessionsSignal + searchQuerySignal
// so it stays in sync with whatever SessionList.js actually renders.
//
// Closes BUG #16 / UX-05: GroupRow.js previously read the sessionCount
// field off the API-provided MenuGroup struct. The backend at
// internal/web/menu_snapshot_builder.go populates that count from
// groupTree.SessionCountForGroup, which counts the full subtree and ignores
// the search filter. The frontend then renders only matching/visible
// children, so the displayed count drifted from the rendered count whenever
// the user activated search or SSE delivered a session-removed event before
// the snapshot was refetched. The fix is frontend-only — the backend
// SessionCountForGroup is correct for "total sessions in group + subgroups"
// and is left untouched.
function countVisibleChildren(groupPath, query) {
  // Subscribe to both signals so this re-runs on updates
  void sessionsSignal.value
  void searchQuerySignal.value
  const items = sessionsSignal.value || []
  let count = 0
  for (const item of items) {
    if (item.type !== 'session' || !item.session) continue
    const gp = item.session.groupPath || ''
    // Match this group OR any subgroup
    if (gp !== groupPath && !gp.startsWith(groupPath + '/')) continue
    if (query) {
      // Mirror SessionList.matchesSearch logic minimally
      const s = item.session
      const text = [s.title, s.id, s.groupPath, s.path, s.tool].join(' ').toLowerCase()
      const terms = query.toLowerCase().split(/\s+/).filter(Boolean)
      if (!terms.every(term => text.includes(term))) continue
    }
    count++
  }
  return count
}

export function GroupRow({ item }) {
  const group = item.group
  // PERF-G: hold the collapse state locally in a useState so toggling one
  // group does not force a render of every other GroupRow via shared
  // signal subscription alone. We still subscribe to groupExpandedSignal
  // so external changes (SSE deltas, cross-tab sync) propagate to this
  // row; when they do, we mirror the signal into local state via a safe
  // set-state-during-render check. Preact's useState bailout short-circuits
  // the rerender when local state already matches the derived external
  // value, so unrelated group toggles cost zero extra renders for this row.
  void groupExpandedSignal.value
  const externalExpanded = isGroupExpanded(group.path, group.expanded)
  const [isOpen, setIsOpen] = useState(externalExpanded)
  if (externalExpanded !== isOpen) {
    setIsOpen(externalExpanded)
  }
  const expanded = isOpen

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
        class="group w-full min-w-0 flex items-center gap-sp-8 px-sp-12 py-1 min-h-[40px] text-xs font-semibold
          uppercase tracking-wide dark:text-tn-muted text-gray-700
          dark:bg-tn-muted/5 bg-gray-50/50
          hover:dark:bg-tn-muted/10 hover:bg-gray-100
          hover:dark:text-tn-fg hover:text-gray-900 transition-colors"
        style="padding-left: calc(${item.level || 0} * 1rem + 0.75rem)"
        aria-expanded=${expanded}
      >
        <span class="text-base leading-none select-none">${expanded ? '\u25BE' : '\u25B8'}</span>
        <span class="flex-1 truncate min-w-0 text-left" title=${group.name || group.path}>${group.name || group.path}</span>
        <span class="dark:text-tn-muted/60 text-gray-600 font-normal">
          (${countVisibleChildren(group.path, searchQuerySignal.value)})
        </span>
        <span
          onClick=${(e) => e.stopPropagation()}
          onMouseDown=${(e) => e.stopPropagation()}
          class="flex items-center gap-0.5 flex-shrink-0
            opacity-0 pointer-events-none
            group-hover:opacity-100 group-hover:pointer-events-auto
            group-focus-within:opacity-100 group-focus-within:pointer-events-auto
            transition-opacity duration-[120ms] motion-reduce:transition-none"
        >
          <button type="button" onClick=${handleCreateGroup} title="New subgroup" aria-label="Create subgroup"
            class="min-w-[36px] min-h-[36px] flex items-center justify-center rounded
                   dark:text-tn-muted hover:dark:text-tn-blue hover:text-gray-700 transition-colors">
            <svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/>
            </svg>
          </button>
          <button type="button" onClick=${handleRename} title="Rename group" aria-label="Rename group"
            class="min-w-[36px] min-h-[36px] flex items-center justify-center rounded
                   dark:text-tn-muted hover:dark:text-tn-blue hover:text-gray-700 transition-colors">
            <svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                    d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"/>
            </svg>
          </button>
          <button type="button" onClick=${handleDeleteGroup} title="Delete group" aria-label="Delete group"
            class="min-w-[36px] min-h-[36px] flex items-center justify-center rounded
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
