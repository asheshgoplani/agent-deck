// unit/terminalLinks.test.js -- link-open policy for the web terminal (#1682).
//
// xterm's built-in OSC-8 handler confirms on every link. terminalLinks.js
// replaces it: hosts on `[web].trusted_domains` open straight away, everything
// else still confirms, and `confirm_link_open = false` drops the prompt
// entirely. The allowlist is a security control, so the bypass cases
// (lookalike suffixes, wildcards, non-http schemes, credentials in the URL)
// are pinned as hard as the happy path.

import { describe, it, expect, vi } from 'vitest'

const modulePath = '../../../internal/web/static/app/terminalLinks.js'

const load = () => import(modulePath)

describe('normalizeTrustedDomain', () => {
  const cases = [
    ['gitlab.corp.example', 'gitlab.corp.example'],
    ['GitLab.CORP.Example', 'gitlab.corp.example'],
    ['  gitlab.corp.example  ', 'gitlab.corp.example'],
    ['https://gitlab.corp.example/group/repo/-/merge_requests/7', 'gitlab.corp.example'],
    ['gerrit.corp.example:8443', 'gerrit.corp.example'],
    ['https://user:pw@gitlab.corp.example', 'gitlab.corp.example'],
    ['gitlab.corp.example.', 'gitlab.corp.example'],
    ['*.corp.example', '*.corp.example'],
    ['*.corp.example:8443', '*.corp.example'],
    ['[::1]:8443', '[::1]'],
    // Unusable entries are dropped rather than turned into something broad.
    ['', ''],
    ['   ', ''],
    ['*', ''],
    ['*.', ''],
    ['*.example', ''],
    ['git.*.corp.example', ''],
    ['https://', ''],
    [null, ''],
    [undefined, ''],
  ]
  for (const [input, want] of cases) {
    it(`${JSON.stringify(input)} -> ${JSON.stringify(want)}`, async () => {
      const { normalizeTrustedDomain } = await load()
      expect(normalizeTrustedDomain(input)).toBe(want)
    })
  }
})

describe('isTrustedLinkTarget', () => {
  const list = ['gitlab.corp.example', '*.ci.corp.example']

  it('matches an exact host', async () => {
    const { isTrustedLinkTarget } = await load()
    expect(isTrustedLinkTarget('https://gitlab.corp.example/x/-/mr/1', list)).toBe(true)
  })
  it('ignores the port and the path', async () => {
    const { isTrustedLinkTarget } = await load()
    expect(isTrustedLinkTarget('https://gitlab.corp.example:8443/a?b=c#d', list)).toBe(true)
  })
  it('is case-insensitive on the host', async () => {
    const { isTrustedLinkTarget } = await load()
    expect(isTrustedLinkTarget('https://GitLab.Corp.Example/x', list)).toBe(true)
  })
  it('matches plain http as well as https', async () => {
    const { isTrustedLinkTarget } = await load()
    expect(isTrustedLinkTarget('http://gitlab.corp.example/x', list)).toBe(true)
  })
  it('matches a subdomain under a *. entry', async () => {
    const { isTrustedLinkTarget } = await load()
    expect(isTrustedLinkTarget('https://build7.ci.corp.example/job/9', list)).toBe(true)
  })
  it('does NOT let a *. entry match its own bare base', async () => {
    const { isTrustedLinkTarget } = await load()
    expect(isTrustedLinkTarget('https://ci.corp.example/', list)).toBe(false)
  })
  it('does NOT match an unrelated host', async () => {
    const { isTrustedLinkTarget } = await load()
    expect(isTrustedLinkTarget('https://evil.example/pwn', list)).toBe(false)
  })
  it('does NOT match a suffix lookalike of an exact entry', async () => {
    const { isTrustedLinkTarget } = await load()
    expect(isTrustedLinkTarget('https://notgitlab.corp.example/x', list)).toBe(false)
    expect(isTrustedLinkTarget('https://gitlab.corp.example.evil.test/x', list)).toBe(false)
  })
  it('does NOT match a suffix lookalike of a wildcard base', async () => {
    const { isTrustedLinkTarget } = await load()
    expect(isTrustedLinkTarget('https://evilci.corp.example/x', list)).toBe(false)
  })
  it('does NOT trust a host smuggled into userinfo', async () => {
    const { isTrustedLinkTarget } = await load()
    expect(isTrustedLinkTarget('https://gitlab.corp.example@evil.example/x', list)).toBe(false)
  })
  it('does NOT trust non-http schemes even when the host matches', async () => {
    const { isTrustedLinkTarget } = await load()
    expect(isTrustedLinkTarget('file://gitlab.corp.example/etc/passwd', list)).toBe(false)
    expect(isTrustedLinkTarget('javascript:alert(1)', list)).toBe(false)
  })
  it('returns false for unparseable input and for an empty/missing list', async () => {
    const { isTrustedLinkTarget } = await load()
    expect(isTrustedLinkTarget('not a url', list)).toBe(false)
    expect(isTrustedLinkTarget('https://gitlab.corp.example/x', [])).toBe(false)
    expect(isTrustedLinkTarget('https://gitlab.corp.example/x', undefined)).toBe(false)
  })
})

describe('shouldConfirmLinkOpen', () => {
  it('confirms by default (no options at all)', async () => {
    const { shouldConfirmLinkOpen } = await load()
    expect(shouldConfirmLinkOpen('https://gitlab.corp.example/x')).toBe(true)
  })
  it('skips the confirm for an allowlisted host', async () => {
    const { shouldConfirmLinkOpen } = await load()
    expect(shouldConfirmLinkOpen('https://gitlab.corp.example/x', {
      trustedDomains: ['gitlab.corp.example'],
    })).toBe(false)
  })
  it('still confirms for a host that is not allowlisted', async () => {
    const { shouldConfirmLinkOpen } = await load()
    expect(shouldConfirmLinkOpen('https://evil.example/x', {
      trustedDomains: ['gitlab.corp.example'],
    })).toBe(true)
  })
  it('confirm_link_open = false drops the prompt for every host', async () => {
    const { shouldConfirmLinkOpen } = await load()
    expect(shouldConfirmLinkOpen('https://evil.example/x', {
      trustedDomains: [],
      confirmLinkOpen: false,
    })).toBe(false)
  })
})

describe('openTerminalLink', () => {
  // Fake window: records open() calls and hands back a stub child window.
  const fakeWin = () => {
    const child = { opener: {}, location: { href: '' } }
    return {
      child,
      open: vi.fn(() => child),
    }
  }

  it('opens an allowlisted link without asking', async () => {
    const { openTerminalLink } = await load()
    const win = fakeWin()
    const ask = vi.fn(() => true)
    const ok = openTerminalLink('https://gitlab.corp.example/x', {
      trustedDomains: ['gitlab.corp.example'],
    }, { win, confirm: ask })

    expect(ok).toBe(true)
    expect(ask).not.toHaveBeenCalled()
    expect(win.open).toHaveBeenCalledTimes(1)
    expect(win.child.location.href).toBe('https://gitlab.corp.example/x')
    // The opener must be cleared before navigating, exactly as xterm does.
    expect(win.child.opener).toBe(null)
  })

  it('asks before opening a link that is not allowlisted', async () => {
    const { openTerminalLink } = await load()
    const win = fakeWin()
    const ask = vi.fn(() => true)
    openTerminalLink('https://evil.example/x', {
      trustedDomains: ['gitlab.corp.example'],
    }, { win, confirm: ask })

    expect(ask).toHaveBeenCalledTimes(1)
    expect(ask.mock.calls[0][0]).toContain('https://evil.example/x')
    expect(win.open).toHaveBeenCalledTimes(1)
  })

  it('opens nothing when the confirm is declined', async () => {
    const { openTerminalLink } = await load()
    const win = fakeWin()
    const ask = vi.fn(() => false)
    const ok = openTerminalLink('https://evil.example/x', { trustedDomains: [] }, { win, confirm: ask })

    expect(ok).toBe(false)
    expect(win.open).not.toHaveBeenCalled()
  })

  it('refuses to navigate when the popup was blocked', async () => {
    const { openTerminalLink } = await load()
    const win = { open: vi.fn(() => null) }
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const ok = openTerminalLink('https://gitlab.corp.example/x', {
      trustedDomains: ['gitlab.corp.example'],
    }, { win, confirm: () => true })

    expect(ok).toBe(false)
    expect(warn).toHaveBeenCalled()
    warn.mockRestore()
  })
})

describe('createTerminalLinkHandler', () => {
  it('reads the live signals at click time', async () => {
    const { createTerminalLinkHandler } = await load()
    const { trustedDomainsSignal, confirmLinkOpenSignal } =
      await import('../../../internal/web/static/app/state.js')

    const handler = createTerminalLinkHandler()
    const opened = []
    const child = { opener: {}, location: {} }
    Object.defineProperty(child.location, 'href', {
      set(v) { opened.push(v) },
      get() { return opened[opened.length - 1] },
    })
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => child)
    const confirmSpy = vi.spyOn(window, 'confirm').mockImplementation(() => false)

    // Not trusted yet -> confirm fires, declined, nothing opens.
    handler.activate(new MouseEvent('click'), 'https://gitlab.corp.example/x')
    expect(confirmSpy).toHaveBeenCalledTimes(1)
    expect(opened).toEqual([])

    // Hydrating the signal (what AppShell does from /api/settings) flips the
    // decision without rebuilding the handler.
    trustedDomainsSignal.value = ['gitlab.corp.example']
    handler.activate(new MouseEvent('click'), 'https://gitlab.corp.example/x')
    expect(confirmSpy).toHaveBeenCalledTimes(1)
    expect(opened).toEqual(['https://gitlab.corp.example/x'])

    // The global toggle suppresses the prompt for untrusted hosts too.
    confirmLinkOpenSignal.value = false
    handler.activate(new MouseEvent('click'), 'https://evil.example/x')
    expect(confirmSpy).toHaveBeenCalledTimes(1)
    expect(opened).toEqual(['https://gitlab.corp.example/x', 'https://evil.example/x'])

    trustedDomainsSignal.value = []
    confirmLinkOpenSignal.value = true
    openSpy.mockRestore()
    confirmSpy.mockRestore()
  })
})

// #2676: an http(s) page cannot navigate to file://, so the old path opened a
// blank tab that stayed blank. A file link now never opens a window; the user
// gets the path (copied when the clipboard is available) and a message instead.
describe('file:// links (#2676)', () => {
  it('localFilePath decodes a local file URL and rejects everything else', async () => {
    const { localFilePath } = await load()
    expect(localFilePath('file:///Users/me/proj/reports/x.html')).toBe('/Users/me/proj/reports/x.html')
    expect(localFilePath('file:///tmp/a%20b.txt')).toBe('/tmp/a b.txt')
    expect(localFilePath('file://localhost/etc/hosts')).toBe('/etc/hosts')
    expect(localFilePath('FILE:///etc/hosts')).toBe('/etc/hosts')
    expect(localFilePath('https://example.com/file:///etc/hosts')).toBe('')
    expect(localFilePath('not a url')).toBe('')
  })

  it('keeps control characters percent-encoded so a pasted path is one line', async () => {
    const { localFilePath } = await load()
    expect(localFilePath('file:///tmp/x%0Acurl%20evil%7Csh')).toBe('/tmp/x%0Acurl%20evil%7Csh')
    expect(localFilePath('file:///tmp/x%7F')).toBe('/tmp/x%7F')
  })

  it('maps a Windows drive path', async () => {
    const { localFilePath } = await load()
    expect(localFilePath('file:///C:/Users/me/r.html')).toBe('C:/Users/me/r.html')
  })

  it('keeps C1 controls and bidi overrides percent-encoded too', async () => {
    const { localFilePath } = await load()
    expect(localFilePath('file:///tmp/x%C2%9By')).toBe('/tmp/x%C2%9By')
    expect(localFilePath('file:///tmp/%E2%80%AEtxt.exe')).toBe('/tmp/%E2%80%AEtxt.exe')
    expect(localFilePath('file:///tmp/x%C2%85y')).toBe('/tmp/x%C2%85y')
    expect(localFilePath('file:///tmp/x%E2%80%A8y')).toBe('/tmp/x%E2%80%A8y')
    expect(localFilePath('file:///tmp/x%E2%80%A9y')).toBe('/tmp/x%E2%80%A9y')
    expect(localFilePath('file:///tmp/caf%C3%A9.txt')).toBe('/tmp/café.txt')
  })

  // `ls --hyperlink` and the OSC 8 convention put the pane's hostname in the
  // URL. The browser can't tell whether that is this host, so the path is
  // used and the toast names the host.
  it('uses the path of a file link that names a host', async () => {
    const { localFilePath, fileLinkHost } = await load()
    expect(localFilePath('file://deckbox/home/me/r%20x.txt')).toBe('/home/me/r x.txt')
    expect(fileLinkHost('file://deckbox/home/me/r.txt')).toBe('deckbox')
    expect(fileLinkHost('file://localhost/home/me/r.txt')).toBe('')
    expect(fileLinkHost('file:///home/me/r.txt')).toBe('')
  })

  it('names the host in the toast for a hosted file link', async () => {
    const { openTerminalLink } = await load()
    const { toastsSignal } = await import('../../../internal/web/static/app/toasts.js')
    toastsSignal.value = []
    const writeText = vi.fn(() => Promise.resolve())
    openTerminalLink('file://deckbox/home/me/r.txt', {}, { win: { open: vi.fn(), navigator: { clipboard: { writeText } } } })
    await new Promise((r) => setTimeout(r))
    expect(writeText).toHaveBeenCalledWith('/home/me/r.txt')
    const last = toastsSignal.value[toastsSignal.value.length - 1]
    expect(last.message).toContain('Path copied: /home/me/r.txt')
    expect(last.message).toContain('deckbox')
  })

  it('cuts a very long path short in the toast but copies all of it', async () => {
    const { openTerminalLink } = await load()
    const { toastsSignal } = await import('../../../internal/web/static/app/toasts.js')
    toastsSignal.value = []
    const long = '/tmp/' + 'a'.repeat(1000)
    const writeText = vi.fn(() => Promise.resolve())
    openTerminalLink('file://' + long, {}, { win: { open: vi.fn(), navigator: { clipboard: { writeText } } } })
    await new Promise((r) => setTimeout(r))
    expect(writeText).toHaveBeenCalledWith(long)
    expect(toastsSignal.value[toastsSignal.value.length - 1].message.length).toBeLessThan(300)
  })

  it('never opens a window or confirms; hands the path to onFileLink', async () => {
    const { openTerminalLink } = await load()
    const win = { open: vi.fn() }
    const ask = vi.fn(() => true)
    const onFileLink = vi.fn()
    const ok = openTerminalLink('file:///etc/hosts', { confirmLinkOpen: true }, { win, confirm: ask, onFileLink })

    expect(ok).toBe(false)
    expect(win.open).not.toHaveBeenCalled()
    expect(ask).not.toHaveBeenCalled()
    expect(onFileLink).toHaveBeenCalledWith('/etc/hosts')
  })

  it('by default copies the path and raises an info toast', async () => {
    const { openTerminalLink } = await load()
    const { toastsSignal } = await import('../../../internal/web/static/app/toasts.js')
    const writeText = vi.fn(() => Promise.resolve())
    const win = { open: vi.fn(), navigator: { clipboard: { writeText } } }
    toastsSignal.value = []

    openTerminalLink('file:///Users/me/r.html', {}, { win })
    await new Promise((r) => setTimeout(r))

    expect(win.open).not.toHaveBeenCalled()
    expect(writeText).toHaveBeenCalledWith('/Users/me/r.html')
    const last = toastsSignal.value[toastsSignal.value.length - 1]
    expect(last.type).toBe('info')
    expect(last.message).toContain('Path copied: /Users/me/r.html')
  })

  it('without a clipboard it still names the path', async () => {
    const { openTerminalLink } = await load()
    const { toastsSignal } = await import('../../../internal/web/static/app/toasts.js')
    toastsSignal.value = []
    openTerminalLink('file:///Users/me/r.html', {}, { win: { open: vi.fn(), navigator: {} } })
    const last = toastsSignal.value[toastsSignal.value.length - 1]
    expect(last.message).toContain('/Users/me/r.html')
    expect(last.message).not.toContain('copied')
  })
})

// allowNonHttpProtocols lets file:// links through xterm's OSC 8 provider,
// and with them every other scheme; only http(s) may navigate.
describe('non-http schemes', () => {
  it('the handler asks xterm for non-http links', async () => {
    const { createTerminalLinkHandler } = await load()
    expect(createTerminalLinkHandler().allowNonHttpProtocols).toBe(true)
  })

  for (const url of ['javascript:alert(1)', 'JavaScript:alert(1)', ' javascript:alert(1)', 'java\tscript:alert(1)',
    'data:text/html,<b>x</b>', 'vscode://file/etc/hosts', 'mailto:a@b.example', '/relative/path']) {
    it(`never opens ${url}, even with the confirm off`, async () => {
      const { openTerminalLink } = await load()
      const win = { open: vi.fn() }
      const ask = vi.fn(() => true)
      expect(openTerminalLink(url, { confirmLinkOpen: false }, { win, confirm: ask })).toBe(false)
      expect(win.open).not.toHaveBeenCalled()
    })
  }

  it('says why, with a long URL cut short', async () => {
    const { openTerminalLink } = await load()
    const { toastsSignal } = await import('../../../internal/web/static/app/toasts.js')
    toastsSignal.value = []
    openTerminalLink('data:text/plain,' + 'x'.repeat(5000), {}, { win: { open: vi.fn() } })
    const last = toastsSignal.value[toastsSignal.value.length - 1]
    expect(last.message).toContain('Only http(s) and file links')
    expect(last.message.length).toBeLessThan(300)
  })
})

// #2675: in a mouse-capturing session (tmux `mouse on`, Claude Code) xterm
// turns a click into mouse-protocol input and never activates the link. The
// hovered link is tracked through the linkHandler's hover/leave callbacks, and
// a Cmd-click (Ctrl-click off macOS) on it opens it through the same policy
// without the click reaching the app.
describe('modifier-click links (#2675)', () => {
  const press = (target, type, init) => {
    const ev = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, ...init })
    target.dispatchEvent(ev)
    return ev
  }

  const setup = async ({ isMac = true } = {}) => {
    const { createTerminalLinkHandler, installModifierClickLinks } = await load()
    const opened = []
    const handler = createTerminalLinkHandler({ open: (url) => opened.push(url) })
    const container = document.createElement('div')
    const inner = document.createElement('div')
    container.appendChild(inner)
    document.body.appendChild(container)
    const reached = []
    for (const type of ['mousedown', 'mouseup', 'click']) {
      inner.addEventListener(type, () => reached.push(type))
    }
    const controller = new AbortController()
    installModifierClickLinks(container, handler, { isMac, signal: controller.signal })
    return { handler, container, inner, opened, reached, controller }
  }

  it('isLinkOpenClick: Cmd on macOS, Ctrl elsewhere, primary button only', async () => {
    const { isLinkOpenClick } = await load()
    const ev = (init) => new MouseEvent('mousedown', { button: 0, ...init })
    expect(isLinkOpenClick(ev({ metaKey: true }), true)).toBe(true)
    expect(isLinkOpenClick(ev({ ctrlKey: true }), true)).toBe(false)
    expect(isLinkOpenClick(ev({ ctrlKey: true }), false)).toBe(true)
    expect(isLinkOpenClick(ev({ metaKey: true }), false)).toBe(false)
    expect(isLinkOpenClick(ev({}), true)).toBe(false)
    expect(isLinkOpenClick(ev({ altKey: true }), true)).toBe(false)
    expect(isLinkOpenClick(ev({ metaKey: true, altKey: true }), true)).toBe(false)
    expect(isLinkOpenClick(ev({ metaKey: true, button: 2 }), true)).toBe(false)
  })

  it('Cmd-click on a hovered link opens it and swallows the whole click', async () => {
    const { handler, inner, opened, reached } = await setup()
    handler.hover(new MouseEvent('mousemove'), 'https://example.com/a', {})
    const down = press(inner, 'mousedown', { metaKey: true })
    // Opens on release, like xterm.
    expect(opened).toEqual([])
    press(inner, 'mouseup', { metaKey: true })
    press(inner, 'click', { metaKey: true })

    expect(opened).toEqual(['https://example.com/a'])
    expect(down.defaultPrevented).toBe(true)
    expect(reached).toEqual([])
  })

  it('dragging off the link before release cancels', async () => {
    const { handler, inner, opened, reached } = await setup()
    handler.hover(new MouseEvent('mousemove'), 'https://example.com/a', {})
    press(inner, 'mousedown', { metaKey: true })
    handler.leave(new MouseEvent('mousemove'), 'https://example.com/a')
    press(inner, 'mouseup', { metaKey: true })
    expect(opened).toEqual([])
    expect(reached).toEqual([])
  })

  it('a double Cmd-click opens the link once', async () => {
    const { handler, inner, opened, reached } = await setup()
    handler.hover(new MouseEvent('mousemove'), 'https://example.com/a', {})
    press(inner, 'mousedown', { metaKey: true, detail: 1 })
    press(inner, 'mouseup', { metaKey: true, detail: 1 })
    press(inner, 'click', { metaKey: true, detail: 1 })
    press(inner, 'mousedown', { metaKey: true, detail: 2 })
    press(inner, 'mouseup', { metaKey: true, detail: 2 })
    press(inner, 'click', { metaKey: true, detail: 2 })
    expect(opened).toEqual(['https://example.com/a'])
    expect(reached).toEqual([])
  })

  // Lane review: a Cmd-press on the link, dragged out and released outside
  // the terminal, must not leave the press armed for a later plain release.
  it('a modifier press released outside the terminal is forgotten', async () => {
    const { handler, inner, opened, reached } = await setup()
    const outside = document.createElement('div')
    document.body.appendChild(outside)
    handler.hover(new MouseEvent('mousemove'), 'https://example.com/a', {})
    press(inner, 'mousedown', { metaKey: true })
    press(outside, 'mouseup', { metaKey: true })
    // A plain drag started outside and released over the same link.
    press(outside, 'mousedown', {})
    press(inner, 'mouseup', {})
    expect(opened).toEqual([])
    expect(reached).toEqual(['mouseup'])
  })

  it('a release outside the terminal stops the swallowing at once', async () => {
    const { handler, inner, opened, reached } = await setup()
    const outside = document.createElement('div')
    document.body.appendChild(outside)
    handler.hover(new MouseEvent('mousemove'), 'https://example.com/a', {})
    press(inner, 'mousedown', { metaKey: true })
    press(outside, 'mouseup', { metaKey: true })
    // No new press inside yet: the next release over the link is not ours.
    press(inner, 'mouseup', {})
    expect(opened).toEqual([])
    expect(reached).toEqual(['mouseup'])
  })

  it('a plain click on a hovered link still reaches the terminal', async () => {
    const { handler, inner, opened, reached } = await setup()
    handler.hover(new MouseEvent('mousemove'), 'https://example.com/a', {})
    press(inner, 'mousedown', {})
    press(inner, 'mouseup', {})

    expect(opened).toEqual([])
    expect(reached).toEqual(['mousedown', 'mouseup'])
  })

  it('a Cmd-click with no hovered link passes through', async () => {
    const { handler, inner, opened, reached } = await setup()
    handler.hover(new MouseEvent('mousemove'), 'https://example.com/a', {})
    handler.leave(new MouseEvent('mousemove'), 'https://example.com/a')
    press(inner, 'mousedown', { metaKey: true })

    expect(opened).toEqual([])
    expect(reached).toEqual(['mousedown'])
  })

  it('uses Ctrl off macOS', async () => {
    const { handler, inner, opened, reached } = await setup({ isMac: false })
    handler.hover(new MouseEvent('mousemove'), 'https://example.com/b', {})
    press(inner, 'mousedown', { metaKey: true })
    expect(opened).toEqual([])
    press(inner, 'mouseup', {})
    press(inner, 'mousedown', { ctrlKey: true })
    press(inner, 'mouseup', { ctrlKey: true })
    expect(opened).toEqual(['https://example.com/b'])
    expect(reached).toEqual(['mousedown', 'mouseup'])
  })

  it('detaches when the signal aborts', async () => {
    const { handler, inner, opened, reached, controller } = await setup()
    controller.abort()
    handler.hover(new MouseEvent('mousemove'), 'https://example.com/a', {})
    press(inner, 'mousedown', { metaKey: true })
    expect(opened).toEqual([])
    expect(reached).toEqual(['mousedown'])
  })

  it('activate (the unmodified, non-captured click) still opens through the policy', async () => {
    const { handler, opened } = await setup()
    handler.activate(new MouseEvent('click'), 'https://example.com/c')
    expect(opened).toEqual(['https://example.com/c'])
  })
})

// #2676 serve: a file:// link under `[web] file_link_roots` opens through the
// deck's GET /api/files; everything else keeps the copy-path fallback.
describe('file links under file_link_roots', () => {
  const fakeWin = () => {
    const child = { opener: {}, location: { href: '' } }
    return { child, open: vi.fn(() => child), location: { origin: 'https://deck.example:8420' } }
  }

  it('isUnderFileLinkRoot matches the root and below, never a sibling prefix', async () => {
    const { isUnderFileLinkRoot } = await load()
    const roots = ['/home/me/projects', '/srv/reports/']
    expect(isUnderFileLinkRoot('/home/me/projects/a/r.html', roots)).toBe(true)
    expect(isUnderFileLinkRoot('/home/me/projects', roots)).toBe(true)
    expect(isUnderFileLinkRoot('/srv/reports/x.txt', roots)).toBe(true)
    expect(isUnderFileLinkRoot('/home/me/projects-old/r.html', roots)).toBe(false)
    expect(isUnderFileLinkRoot('/home/me/projects/../.ssh/id_rsa', roots)).toBe(false)
    expect(isUnderFileLinkRoot('/etc/hosts', roots)).toBe(false)
    expect(isUnderFileLinkRoot('/home/me/projects/r.html', [])).toBe(false)
    expect(isUnderFileLinkRoot('/home/me/projects/r.html', undefined)).toBe(false)
  })

  it('opens a file under a root through /api/files, without the confirm', async () => {
    const { openTerminalLink } = await load()
    const win = fakeWin()
    const ask = vi.fn(() => true)
    const onFileLink = vi.fn()
    const ok = openTerminalLink('file:///home/me/projects/a%20b/r.html', {
      fileLinkRoots: ['/home/me/projects'],
    }, { win, confirm: ask, onFileLink })

    expect(ok).toBe(true)
    expect(ask).not.toHaveBeenCalled()
    expect(onFileLink).not.toHaveBeenCalled()
    expect(win.child.opener).toBe(null)
    expect(win.child.location.href).toBe(
      'https://deck.example:8420/api/files?path=' + encodeURIComponent('/home/me/projects/a b/r.html'))
  })

  it('a root of / covers every absolute path', async () => {
    const { isUnderFileLinkRoot } = await load()
    expect(isUnderFileLinkRoot('/etc/hosts', ['/'])).toBe(true)
  })

  it('opens a file://<deck host>/ link (ls --hyperlink) but never another host', async () => {
    const { openTerminalLink, isDeckFileHost } = await load()
    expect(isDeckFileHost('', 'deckbox')).toBe(true)
    expect(isDeckFileHost('deckbox', 'deckbox.lan')).toBe(true)
    expect(isDeckFileHost('DeckBox.lan', 'deckbox')).toBe(true)
    expect(isDeckFileHost('laptop', 'deckbox')).toBe(false)
    expect(isDeckFileHost('deckbox', '')).toBe(false)

    const opts = { fileLinkRoots: ['/home/me/projects'], fileLinkHost: 'deckbox' }
    const mine = fakeWin()
    expect(openTerminalLink('file://deckbox/home/me/projects/r.txt', opts, { win: mine })).toBe(true)
    expect(mine.child.location.href).toContain('/api/files?path=' + encodeURIComponent('/home/me/projects/r.txt'))

    const other = fakeWin()
    const onFileLink = vi.fn()
    expect(openTerminalLink('file://laptop/home/me/projects/r.txt', opts, { win: other, onFileLink })).toBe(false)
    expect(other.open).not.toHaveBeenCalled()
    expect(onFileLink).toHaveBeenCalledWith('/home/me/projects/r.txt')
  })

  it('keeps the copy-path fallback outside the roots', async () => {
    const { openTerminalLink } = await load()
    const win = fakeWin()
    const onFileLink = vi.fn()
    const ok = openTerminalLink('file:///etc/hosts', { fileLinkRoots: ['/home/me/projects'] }, { win, onFileLink })
    expect(ok).toBe(false)
    expect(win.open).not.toHaveBeenCalled()
    expect(onFileLink).toHaveBeenCalledWith('/etc/hosts')
  })

  it('the handler reads the roots from the live signal', async () => {
    const { createTerminalLinkHandler } = await load()
    const { fileLinkRootsSignal } = await import('../../../internal/web/static/app/state.js')
    const child = { opener: {}, location: { href: '' } }
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => child)
    fileLinkRootsSignal.value = ['/home/me/projects']
    createTerminalLinkHandler().activate(new MouseEvent('click'), 'file:///home/me/projects/r.txt')
    expect(child.location.href).toContain('/api/files?path=' + encodeURIComponent('/home/me/projects/r.txt'))
    fileLinkRootsSignal.value = []
    openSpy.mockRestore()
  })
})
