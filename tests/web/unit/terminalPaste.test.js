// unit/terminalPaste.test.js -- the web terminal's clipboard bridge.
//
// TerminalPanel captures the paste event itself (xterm 6.0's own paste path can
// fail and destroy the system clipboard on WSL2+Chrome when focus is not on
// .xterm-helper-textarea), which means it must hand the text back to
// terminal.paste() rather than write it to the pane directly. xterm's paste
// path is what applies the encoding a native terminal uses -- CR line breaks,
// wrapped in the DECSET 2004 bracketed-paste markers when the pane's app
// enabled them -- and only that encoding tells an agent TUI "this is a paste,
// insert the newlines". Forwarding the raw clipboard text made Claude Code
// swallow every line break and land a multi-line paste as one run-on line.

import { describe, it, expect, vi } from 'vitest'

const modulePath = '../../../internal/web/static/app/terminalPaste.js'

const handlerFor = async (terminal) => {
  const { createPasteHandler } = await import(modulePath)
  return createPasteHandler(terminal)
}

// jsdom implements neither ClipboardEvent nor DataTransfer, and the handler
// only ever reads clipboardData.getData('text/plain').
function pasteEvent(clipboardData) {
  const event = new Event('paste', { bubbles: true, cancelable: true })
  Object.defineProperty(event, 'clipboardData', { value: clipboardData })
  vi.spyOn(event, 'stopPropagation')
  return event
}

const clipboard = (text) => ({ getData: vi.fn(() => text) })
const fakeTerminal = () => ({ paste: vi.fn() })

describe('createPasteHandler', () => {
  it('hands multi-line clipboard text to terminal.paste', async () => {
    const terminal = fakeTerminal()
    const handler = await handlerFor(terminal)

    handler(pasteEvent(clipboard('This is a test:\n - abc\n - def')))

    expect(terminal.paste).toHaveBeenCalledTimes(1)
    expect(terminal.paste).toHaveBeenCalledWith('This is a test:\n - abc\n - def')
  })

  // The bug this file exists for: the old handler rewrote line breaks itself
  // and wrote the result straight to the pane, so the bytes never picked up the
  // bracketed-paste markers. Line-break encoding belongs to xterm -- the text
  // must arrive verbatim.
  it('passes CRLF through untouched instead of normalizing it', async () => {
    const terminal = fakeTerminal()
    const handler = await handlerFor(terminal)

    handler(pasteEvent(clipboard('a\r\nb')))

    expect(terminal.paste).toHaveBeenCalledWith('a\r\nb')
  })

  it('reads the text/plain flavour of the clipboard', async () => {
    const terminal = fakeTerminal()
    const handler = await handlerFor(terminal)
    const cd = clipboard('hi')

    handler(pasteEvent(cd))

    expect(cd.getData).toHaveBeenCalledWith('text/plain')
  })

  it('suppresses xterm\'s own paste path so the text is not sent twice', async () => {
    const terminal = fakeTerminal()
    const handler = await handlerFor(terminal)
    const event = pasteEvent(clipboard('hi'))

    handler(event)

    expect(event.defaultPrevented).toBe(true)
    expect(event.stopPropagation).toHaveBeenCalled()
  })

  describe('nothing to paste', () => {
    it('ignores an event with no clipboardData', async () => {
      const terminal = fakeTerminal()
      const handler = await handlerFor(terminal)
      const event = pasteEvent(null)

      handler(event)

      expect(terminal.paste).not.toHaveBeenCalled()
      expect(event.defaultPrevented).toBe(false)
    })

    it('ignores an empty clipboard', async () => {
      const terminal = fakeTerminal()
      const handler = await handlerFor(terminal)
      const event = pasteEvent(clipboard(''))

      handler(event)

      expect(terminal.paste).not.toHaveBeenCalled()
      expect(event.defaultPrevented).toBe(false)
    })
  })
})
