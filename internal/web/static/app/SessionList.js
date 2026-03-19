// SessionList.js -- Renders groups + sessions from sessionsSignal
import { html } from 'htm/preact'
import { useEffect } from 'preact/hooks'
import { sessionsSignal, selectedIdSignal, authTokenSignal, sessionCostsSignal, focusedIdSignal } from './state.js'
import { isGroupExpanded, groupExpandedSignal } from './groupState.js'
import { GroupRow } from './GroupRow.js'
import { SessionRow } from './SessionRow.js'
import { useKeyboardNav } from './useKeyboardNav.js'

// Fetch batch costs once after the session list first loads
let costsFetched = false
async function fetchBatchCosts(items) {
  if (costsFetched) return
  const ids = (items || [])
    .filter(i => i.type === 'session' && i.session)
    .map(i => i.session.id)
  if (ids.length === 0) return
  costsFetched = true

  const url = '/api/costs/batch?ids=' + ids.join(',')
  const headers = { Accept: 'application/json' }
  const token = authTokenSignal.value
  if (token) headers.Authorization = 'Bearer ' + token

  try {
    const res = await fetch(url, { headers })
    if (!res.ok) return
    const data = await res.json()
    sessionCostsSignal.value = data.costs || {}
  } catch (_) {
    // Cost badges unavailable; fail silently
  }
}

function hasCollapsedAncestor(path) {
  if (!path) return false
  // Read the signal to subscribe
  void groupExpandedSignal.value
  const parts = path.split('/')
  for (let i = 1; i <= parts.length; i++) {
    const ancestor = parts.slice(0, i).join('/')
    if (!isGroupExpanded(ancestor, true)) return true
  }
  return false
}

export function SessionList() {
  const items = sessionsSignal.value
  const focusedId = focusedIdSignal.value

  useKeyboardNav()

  // Trigger batch cost fetch on first non-empty items
  useEffect(() => {
    if (items && items.length > 0) fetchBatchCosts(items)
  }, [items && items.length])

  // Signal Preact has taken over session list rendering
  useEffect(() => {
    window.__preactSessionListActive = true
    return () => { window.__preactSessionListActive = false }
  }, [])

  if (!items || items.length === 0) {
    return html`<div class="px-3 py-4 dark:text-tn-muted text-gray-400 text-sm">
      No sessions
    </div>`
  }

  return html`<ul class="flex flex-col gap-0.5 py-1" role="list" id="preact-session-list">
    ${items.map(item => {
      if (item.type === 'group' && item.group) {
        if (hasCollapsedAncestor(item.group.path)) return null
        return html`<${GroupRow} key=${item.group.path} item=${item} />`
      }
      if (item.type === 'session' && item.session) {
        if (hasCollapsedAncestor(item.session.groupPath)) return null
        const isFocused = focusedId === item.session.id
        return html`<${SessionRow} key=${item.session.id} item=${item} focused=${isFocused} />`
      }
      return null
    })}
  </ul>`
}
