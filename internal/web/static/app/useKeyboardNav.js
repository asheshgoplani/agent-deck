// useKeyboardNav.js -- Keyboard navigation for session list (j/k/arrows + Enter)
// Uses focusedIdSignal (session ID) instead of numeric index for stability across SSE updates.
// NOTE: focusedIdSignal lives in state.js (not SessionList.js) to avoid circular imports.
import { useEffect } from 'preact/hooks'
import { sessionsSignal, selectedIdSignal, focusedIdSignal } from './state.js'
import { isGroupExpanded, groupExpandedSignal } from './groupState.js'

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
      if (visible.length === 0) return

      const currentId = focusedIdSignal.value
      const currentIdx = currentId
        ? visible.findIndex(s => s.id === currentId)
        : -1

      if (e.key === 'j' || e.key === 'ArrowDown') {
        e.preventDefault()
        const nextIdx = Math.min(currentIdx + 1, visible.length - 1)
        focusedIdSignal.value = visible[nextIdx].id
      } else if (e.key === 'k' || e.key === 'ArrowUp') {
        e.preventDefault()
        const nextIdx = currentIdx <= 0 ? 0 : currentIdx - 1
        focusedIdSignal.value = visible[nextIdx].id
      } else if (e.key === 'Enter' && currentIdx >= 0) {
        e.preventDefault()
        selectedIdSignal.value = visible[currentIdx].id
      }
    }

    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [])
}
