import { describe, expect, it } from 'vitest'

const modulePath = '../../../internal/web/static/app/pathSuggestions.js'

describe('path suggestion keys', () => {
  it('moves through suggestions and wraps in either direction', async () => {
    const { pathSuggestionAction: action } = await import(modulePath)
    expect(action('ArrowDown', -1, 2)).toEqual({ type: 'move', index: 0 })
    expect(action('ArrowDown', 1, 2)).toEqual({ type: 'move', index: 0 })
    expect(action('ArrowUp', -1, 2)).toEqual({ type: 'move', index: 1 })
    expect(action('ArrowUp', 0, 2)).toEqual({ type: 'move', index: 1 })
  })

  it('accepts a selection with Enter and completes with Tab', async () => {
    const { pathSuggestionAction: action } = await import(modulePath)
    expect(action('Enter', 1, 2)).toEqual({ type: 'accept', index: 1 })
    expect(action('Enter', -1, 2)).toBeNull()
    expect(action('Tab', -1, 2)).toEqual({ type: 'accept', index: 0 })
    expect(action('Tab', 1, 2)).toEqual({ type: 'accept', index: 1 })
    expect(action('Tab', 1, 2, { shiftKey: true })).toBeNull()
  })

  it('dismisses with Escape and leaves unrelated keys and IME alone', async () => {
    const { pathSuggestionAction: action } = await import(modulePath)
    expect(action('Escape', 1, 2)).toEqual({ type: 'dismiss' })
    expect(action('ArrowDown', -1, 0)).toBeNull()
    expect(action('a', 0, 2)).toBeNull()
    expect(action('Enter', 0, 2, { isComposing: true })).toBeNull()
  })

  it('preserves modified editing and browser shortcuts', async () => {
    const { pathSuggestionAction: action } = await import(modulePath)
    for (const modifier of ['shiftKey', 'ctrlKey', 'metaKey', 'altKey']) {
      for (const key of ['ArrowUp', 'ArrowDown', 'Enter', 'Tab', 'Escape']) {
        expect(action(key, 0, 2, { [modifier]: true })).toBeNull()
      }
    }
  })

  it('dismisses a pending lookup even before options arrive', async () => {
    const { pathSuggestionAction: action } = await import(modulePath)
    expect(action('Escape', -1, 0, {}, true)).toEqual({ type: 'dismiss' })
    expect(action('Enter', -1, 0, {}, true)).toBeNull()
  })
})
