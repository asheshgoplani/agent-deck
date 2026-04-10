// groupState.js -- Group expansion state with localStorage persistence
// Replaces vanilla JS state.groupExpandedByPath during Phase 3 migration
import { signal, effect } from '@preact/signals'

const STORAGE_KEY = 'agentdeck.groupExpanded'

function loadFromStorage() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) return new Map(JSON.parse(raw))
  } catch (_) {}
  return new Map()
}

export const groupExpandedSignal = signal(loadFromStorage())

// Persist mutations back to localStorage (module-scope effect, runs once on init)
effect(() => {
  const map = groupExpandedSignal.value
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify([...map]))
  } catch (_) {}
})

export function toggleGroup(path, serverDefault) {
  const current = new Map(groupExpandedSignal.value)
  const was = current.has(path) ? current.get(path) : (serverDefault !== false)
  current.set(path, !was)
  // Assign new Map to trigger signal subscribers
  groupExpandedSignal.value = current
}

export function isGroupExpanded(path, serverDefault) {
  const map = groupExpandedSignal.value
  return map.has(path) ? map.get(path) : (serverDefault !== false)
}
