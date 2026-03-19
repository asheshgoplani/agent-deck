// useKeyboardNav.js -- Keyboard navigation for session list (j/k/arrows + Enter)
// Uses focusedIdSignal (session ID) instead of numeric index for stability across SSE updates.
// NOTE: focusedIdSignal lives in state.js (not SessionList.js) to avoid circular imports.
import { useEffect } from 'preact/hooks'
import { sessionsSignal, selectedIdSignal, focusedIdSignal, createSessionDialogSignal, confirmDialogSignal, groupNameDialogSignal } from './state.js'
import { isGroupExpanded, groupExpandedSignal } from './groupState.js'
import { apiFetch } from './api.js'

function isTypingTarget(el) {
  if (!el) return false
  const tag = el.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || el.isContentEditable
}

function getVisibleSessions() {
  const items = sessionsSignal.value
  if (!items || items.length === 0) return []

  // Read group signal to stay reactive
  void groupExpandedSignal.value

  const visible = []
  for (const item of items) {
    if (item.type !== 'session' || !item.session) continue
    // Check if any ancestor group is collapsed
    const gp = item.session.groupPath || ''
    if (gp) {
      let collapsed = false
      const parts = gp.split('/')
      for (let i = 1; i <= parts.length; i++) {
        const ancestor = parts.slice(0, i).join('/')
        if (!isGroupExpanded(ancestor, true)) {
          collapsed = true
          break
        }
      }
      if (collapsed) continue
    }
    visible.push(item.session)
  }
  return visible
}

export function useKeyboardNav() {
  useEffect(() => {
    function handler(e) {
      if (isTypingTarget(document.activeElement)) return

      const visible = getVisibleSessions()

      const currentId = focusedIdSignal.value
      const currentIdx = currentId
        ? visible.findIndex(s => s.id === currentId)
        : -1

      if (e.key === 'j' || e.key === 'ArrowDown') {
        if (visible.length === 0) return
        e.preventDefault()
        const nextIdx = Math.min(currentIdx + 1, visible.length - 1)
        focusedIdSignal.value = visible[nextIdx].id
      } else if (e.key === 'k' || e.key === 'ArrowUp') {
        if (visible.length === 0) return
        e.preventDefault()
        const nextIdx = currentIdx <= 0 ? 0 : currentIdx - 1
        focusedIdSignal.value = visible[nextIdx].id
      } else if (e.key === 'Enter' && currentIdx >= 0) {
        e.preventDefault()
        selectedIdSignal.value = visible[currentIdx].id
      } else if (e.key === 'n') {
        e.preventDefault()
        createSessionDialogSignal.value = true
      } else if (e.key === 'Escape') {
        e.preventDefault()
        // Close dialogs in priority order (most recently opened / most specific first)
        if (confirmDialogSignal.value) { confirmDialogSignal.value = null; return }
        if (groupNameDialogSignal.value) { groupNameDialogSignal.value = null; return }
        if (createSessionDialogSignal.value) { createSessionDialogSignal.value = false; return }
      } else if (e.key === 's' && currentIdx >= 0) {
        e.preventDefault()
        const sess = visible[currentIdx]
        if (sess.status === 'running' || sess.status === 'waiting') {
          apiFetch('POST', '/api/sessions/' + sess.id + '/stop')
        }
      } else if (e.key === 'r' && currentIdx >= 0) {
        e.preventDefault()
        const sess = visible[currentIdx]
        if (sess.status === 'idle' || sess.status === 'stopped' || sess.status === 'error') {
          apiFetch('POST', '/api/sessions/' + sess.id + '/restart')
        }
      } else if (e.key === 'd' && currentIdx >= 0) {
        e.preventDefault()
        const sess = visible[currentIdx]
        confirmDialogSignal.value = {
          message: 'Delete session "' + (sess.title || sess.id) + '"? This cannot be undone.',
          onConfirm: () => apiFetch('DELETE', '/api/sessions/' + sess.id)
        }
      }
    }

    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [])
}
