// unit/terminalClipboard.test.js -- OSC 52 clipboard writes (#2370).
//
// Programs in a pane copy by printing OSC 52 with a base64 payload. The web
// terminal must put that text on the browser clipboard, and must never answer
// a "?" query, which would let a pane read the viewer's clipboard.

import { describe, it, expect, vi } from 'vitest'

const modulePath = '../../../internal/web/static/app/terminalClipboard.js'

const b64 = (text) => btoa(String.fromCharCode(...new TextEncoder().encode(text)))

// Captures the handler registerOsc52Handler installs, the way xterm's parser
// would call it with everything after "52;".
function fakeTerminal() {
  const terminal = {
    handler: null,
    ident: null,
    parser: {
      registerOscHandler: vi.fn((ident, handler) => {
        terminal.ident = ident
        terminal.handler = handler
        return { dispose: vi.fn() }
      }),
    },
  }
  return terminal
}

const fakeClipboard = () => ({ writeText: vi.fn(() => Promise.resolve()) })

describe('decodeOsc52', () => {
  it('decodes a UTF-8 payload for any selection target', async () => {
    const { decodeOsc52 } = await import(modulePath)
    expect(decodeOsc52(`c;${b64('git status')}`)).toBe('git status')
    expect(decodeOsc52(`;${b64('héllo ✓')}`)).toBe('héllo ✓')
    expect(decodeOsc52(`p;${b64('line 1\nline 2')}`)).toBe('line 1\nline 2')
  })

  it('refuses queries, empty and malformed payloads', async () => {
    const { decodeOsc52 } = await import(modulePath)
    expect(decodeOsc52('c;?')).toBeNull()
    expect(decodeOsc52('c;')).toBeNull()
    expect(decodeOsc52('no-separator')).toBeNull()
    expect(decodeOsc52('c;not base64!')).toBeNull()
    expect(decodeOsc52(`c;${btoa('\xff\xfe')}`)).toBeNull()
  })

  it('refuses a payload over the size cap', async () => {
    const { decodeOsc52, OSC52_MAX_PAYLOAD } = await import(modulePath)
    expect(decodeOsc52(`c;${'A'.repeat(OSC52_MAX_PAYLOAD + 4)}`)).toBeNull()
  })
})

describe('registerOsc52Handler', () => {
  it('registers on OSC 52 and writes decoded text to the clipboard', async () => {
    const { registerOsc52Handler } = await import(modulePath)
    const terminal = fakeTerminal()
    const clipboard = fakeClipboard()

    const disposable = registerOsc52Handler(terminal, clipboard)

    expect(terminal.ident).toBe(52)
    expect(typeof disposable.dispose).toBe('function')
    expect(terminal.handler(`c;${b64('copied text')}`)).toBe(true)
    expect(clipboard.writeText).toHaveBeenCalledWith('copied text')
  })

  it('consumes a query without reading or writing the clipboard', async () => {
    const { registerOsc52Handler } = await import(modulePath)
    const terminal = fakeTerminal()
    const clipboard = { ...fakeClipboard(), readText: vi.fn() }

    registerOsc52Handler(terminal, clipboard)

    expect(terminal.handler('c;?')).toBe(true)
    expect(clipboard.writeText).not.toHaveBeenCalled()
    expect(clipboard.readText).not.toHaveBeenCalled()
  })

  it('survives a missing clipboard and a rejected write', async () => {
    const { registerOsc52Handler } = await import(modulePath)
    const noClipboard = fakeTerminal()
    registerOsc52Handler(noClipboard, undefined)
    expect(noClipboard.handler(`c;${b64('x')}`)).toBe(true)

    const rejecting = fakeTerminal()
    const clipboard = { writeText: vi.fn(() => Promise.reject(new Error('not focused'))) }
    registerOsc52Handler(rejecting, clipboard)
    expect(rejecting.handler(`c;${b64('x')}`)).toBe(true)
    await Promise.resolve()
    expect(clipboard.writeText).toHaveBeenCalledWith('x')
  })
})
