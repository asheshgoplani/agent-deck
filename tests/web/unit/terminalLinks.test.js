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
