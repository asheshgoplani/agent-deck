// unit/terminalKeys.test.js -- terminalKeymap translates browser keystrokes the
// web terminal mishandles into the bytes a native terminal would send.
//
// xterm-in-a-browser emits a bare `\r` for Shift+Enter (submits), nothing for
// Cmd+Arrow, and shell-broken CSI for Option+Arrow. terminalKeymap restores the
// native bytes; TerminalPanel's key handler writes them and suppresses xterm's
// own output. Every mapping here was verified against real zsh + Claude/Codex.

import { describe, it, expect } from 'vitest'

const modulePath = '../../../internal/web/static/app/terminalKeys.js'

// KeyboardEvent factory. `type` defaults to keydown; extra props override.
const key = (k, init = {}) => new KeyboardEvent(init.type || 'keydown', { key: k, ...init })

const map = async (...args) => {
  const { terminalKeymap } = await import(modulePath)
  return terminalKeymap(...args)
}

describe('terminalKeymap', () => {
  describe('Shift+Enter (platform-independent)', () => {
    it('maps a lone Shift+Enter keydown to \\n on macOS', async () => {
      expect(await map(key('Enter', { shiftKey: true }), true)).toBe('\n')
    })
    it('maps Shift+Enter to \\n on non-mac too', async () => {
      expect(await map(key('Enter', { shiftKey: true }), false)).toBe('\n')
    })
    it('ignores plain Enter so submit still works', async () => {
      expect(await map(key('Enter'), true)).toBe(null)
    })
    it('ignores Shift+Enter combined with Ctrl/Alt/Meta', async () => {
      expect(await map(key('Enter', { shiftKey: true, ctrlKey: true }), true)).toBe(null)
      expect(await map(key('Enter', { shiftKey: true, altKey: true }), true)).toBe(null)
      expect(await map(key('Enter', { shiftKey: true, metaKey: true }), true)).toBe(null)
    })
  })

  describe('macOS line editing (isMac=true)', () => {
    it('Cmd+Left -> Ctrl+A (line start)', async () => {
      expect(await map(key('ArrowLeft', { metaKey: true }), true)).toBe('\x01')
    })
    it('Cmd+Right -> Ctrl+E (line end)', async () => {
      expect(await map(key('ArrowRight', { metaKey: true }), true)).toBe('\x05')
    })
    it('Cmd+Backspace -> Ctrl+U (delete to line start)', async () => {
      expect(await map(key('Backspace', { metaKey: true }), true)).toBe('\x15')
    })
    it('Option+Left -> ESC b (word back)', async () => {
      expect(await map(key('ArrowLeft', { altKey: true }), true)).toBe('\x1bb')
    })
    it('Option+Right -> ESC f (word forward)', async () => {
      expect(await map(key('ArrowRight', { altKey: true }), true)).toBe('\x1bf')
    })
    it('leaves Option+Backspace to xterm (already emits ESC DEL)', async () => {
      expect(await map(key('Backspace', { altKey: true }), true)).toBe(null)
    })
    it('ignores unmapped combos like Ctrl+Left', async () => {
      expect(await map(key('ArrowLeft', { ctrlKey: true }), true)).toBe(null)
    })
    // Each mac mapping requires a LONE naming modifier; any extra modifier must
    // fall through to xterm. Exercise every extra-modifier guard so a partial
    // regression of the cmd/opt predicates can't slip through.
    it.each([
      ['Cmd+Ctrl+Left', 'ArrowLeft', { metaKey: true, ctrlKey: true }],
      ['Cmd+Alt+Left', 'ArrowLeft', { metaKey: true, altKey: true }],
      ['Cmd+Shift+Left', 'ArrowLeft', { metaKey: true, shiftKey: true }],
      ['Option+Shift+Left', 'ArrowLeft', { altKey: true, shiftKey: true }],
      ['Option+Ctrl+Left', 'ArrowLeft', { altKey: true, ctrlKey: true }],
      ['Option+Meta+Left', 'ArrowLeft', { altKey: true, metaKey: true }],
    ])('does not remap %s (extra modifier)', async (_label, k, init) => {
      expect(await map(key(k, init), true)).toBe(null)
    })
  })

  describe('non-mac leaves Cmd/Option keys alone', () => {
    it('Cmd(Super)+Left is not remapped off macOS', async () => {
      expect(await map(key('ArrowLeft', { metaKey: true }), false)).toBe(null)
    })
    it('Option(Alt)+Left is not remapped off macOS', async () => {
      expect(await map(key('ArrowLeft', { altKey: true }), false)).toBe(null)
    })
  })

  describe('guards', () => {
    it('ignores keyup', async () => {
      expect(await map(key('ArrowLeft', { metaKey: true, type: 'keyup' }), true)).toBe(null)
      expect(await map(key('Enter', { shiftKey: true, type: 'keyup' }), true)).toBe(null)
    })
    it('ignores keystrokes during IME composition', async () => {
      expect(await map(key('Enter', { shiftKey: true, isComposing: true }), true)).toBe(null)
    })
  })
})
