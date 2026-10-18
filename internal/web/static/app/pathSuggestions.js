// Keep keyboard decisions shared by the input and its unit tests.
export function pathSuggestionAction(
  key, index, count,
  { shiftKey = false, ctrlKey = false, metaKey = false, altKey = false, isComposing = false } = {},
  pending = false,
) {
  if (isComposing || shiftKey || ctrlKey || metaKey || altKey) return null
  if (key === 'Escape' && (count || pending)) return { type: 'dismiss' }
  if (!count) return null
  if (key === 'ArrowDown') return { type: 'move', index: (index + 1) % count }
  if (key === 'ArrowUp') return { type: 'move', index: index <= 0 ? count - 1 : index - 1 }
  if (key === 'Enter' && index >= 0) return { type: 'accept', index }
  if (key === 'Tab') return { type: 'accept', index: Math.max(0, index) }
  return null
}
