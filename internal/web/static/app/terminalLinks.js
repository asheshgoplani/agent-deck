// terminalLinks.js -- link-open policy for the web terminal (issue #1682).
//
// xterm's built-in OSC-8 handler confirms on EVERY link, which is pure
// friction for anyone clicking self-hosted GitLab/Gerrit/CI links all day.
// TerminalPanel installs this handler instead and consults `[web]` config
// (served by GET /api/settings, hydrated into signals by AppShell):
//
//   trusted_domains   = ["gitlab.corp.example", "*.ci.corp.example"]
//   confirm_link_open = true   # default; false accepts the risk globally
//
// Trusted host -> opens directly. Everything else keeps the confirm. The open
// mirrors xterm's: blank window, `opener` cleared, then navigate.
//
// Issue #2676: a file:// link can't load from an http(s) origin. Under a
// `[web] file_link_roots` directory it opens through the deck's GET /api/files;
// anywhere else the path is copied and a toast says so.
// Issue #2675: Cmd-click (Ctrl-click off macOS) opens the hovered link even
// when the app captures the mouse; see installModifierClickLinks.
import { trustedDomainsSignal, confirmLinkOpenSignal, fileLinkRootsSignal, fileLinkHostSignal } from './state.js'
import { addToast } from './toasts.js'

// Reduces one allowlist entry to a comparable lowercase host, mirroring
// session.NormalizeTrustedDomains in Go. '' means "unusable, ignore it".
export function normalizeTrustedDomain(entry) {
  let s = String(entry == null ? '' : entry).trim().toLowerCase()
  if (!s) return ''
  const scheme = s.indexOf('://')
  if (scheme >= 0) s = s.slice(scheme + 3)
  const path = s.search(/[/?#]/)
  if (path >= 0) s = s.slice(0, path)
  const at = s.lastIndexOf('@')
  if (at >= 0) s = s.slice(at + 1)
  const wildcard = s.startsWith('*.')
  if (wildcard) s = s.slice(2)
  if (s.startsWith('[')) {
    // IPv6 literal: keep the brackets, drop any :port after them.
    const end = s.indexOf(']')
    if (end >= 0) s = s.slice(0, end + 1)
  } else {
    const colon = s.lastIndexOf(':')
    if (colon >= 0 && !s.slice(colon + 1).includes(':')) s = s.slice(0, colon)
  }
  if (s.endsWith('.')) s = s.slice(0, -1)
  if (!s || /[\s*]/.test(s)) return ''
  // A wildcard needs a base with a dot, else `*.example` would allow every
  // single-label host on the network.
  if (wildcard) return s.includes('.') ? '*.' + s : ''
  return s
}

function isWebLink(url) {
  try {
    const { protocol } = new URL(String(url))
    return protocol === 'http:' || protocol === 'https:'
  } catch (_e) {
    return false
  }
}

// Lowercase host of an http(s) URL; '' when unparseable or another scheme
// (a non-http link never auto-opens, whatever its host claims to be).
function hostOf(url) {
  let parsed
  try {
    parsed = new URL(String(url))
  } catch (_e) {
    return ''
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return ''
  return parsed.hostname.toLowerCase().replace(/\.$/, '')
}

// Host-only match: exact (case- and port-insensitive), or a `*.base` entry
// matching strictly deeper subdomains of `base`.
export function isTrustedLinkTarget(url, trustedDomains) {
  const host = hostOf(url)
  if (!host) return false
  for (const raw of Array.isArray(trustedDomains) ? trustedDomains : []) {
    const entry = normalizeTrustedDomain(raw)
    if (!entry) continue
    if (entry.startsWith('*.')) {
      const base = entry.slice(2)
      if (host.length > base.length + 1 && host.endsWith('.' + base)) return true
    } else if (host === entry) {
      return true
    }
  }
  return false
}

// The whole policy: confirm unless the prompt is off or the host is trusted.
export function shouldConfirmLinkOpen(url, options = {}) {
  const { trustedDomains = [], confirmLinkOpen = true } = options
  if (!confirmLinkOpen) return false
  return !isTrustedLinkTarget(url, trustedDomains)
}

// Characters a copied path must not carry decoded: C0 and C1 controls and
// the U+2028/U+2029 line separators (a newline would add a shell line), and
// bidi overrides (they can make a path read differently from what it is).
const UNSAFE_PATH_CHARS = /[\p{Cc}\u2028\u2029\u200e\u200f\u202a-\u202e\u2066-\u2069]/u

function parseFileLink(url) {
  let parsed
  try {
    parsed = new URL(String(url))
  } catch (_e) {
    return null
  }
  return parsed.protocol === 'file:' ? parsed : null
}

// The path a file:// URL names, decoded; '' for any other scheme or an
// unparseable URL. A path that would decode to unsafe characters stays
// percent-encoded, so a pasted path can't smuggle in a second shell line.
export function localFilePath(url) {
  const parsed = parseFileLink(url)
  if (!parsed) return ''
  let path = parsed.pathname
  try {
    const decoded = decodeURIComponent(path)
    if (!UNSAFE_PATH_CHARS.test(decoded)) path = decoded
  } catch (_e) { /* malformed escape: keep it encoded */ }
  // file:///C:/x names the Windows path C:/x.
  return /^\/[A-Za-z]:\//.test(path) ? path.slice(1) : path
}

// The host a file:// URL names, '' for none or localhost. `ls --hyperlink`
// and the OSC 8 convention put the pane's hostname here; the browser can't
// tell whether that is this machine, so the path is used either way and the
// toast names the host.
export function fileLinkHost(url) {
  const parsed = parseFileLink(url)
  if (!parsed) return ''
  const host = parsed.hostname.toLowerCase()
  return host === 'localhost' ? '' : host
}

// Keeps a toast readable (and the toast history small) for a huge data: URL.
function clip(text, max = 200) {
  return text.length > max ? text.slice(0, max) + '…' : text
}

// Default for a file:// link: copy the path where the clipboard API exists
// (secure origins only) and tell the user, instead of a tab that stays blank.
function reportFileLink(path, host, win) {
  const shown = clip(path) + (host ? ` (on ${host})` : '')
  const clipboard = win && win.navigator && win.navigator.clipboard
  if (clipboard && clipboard.writeText) {
    clipboard.writeText(path).then(
      () => addToast(`Local file links can't open in the browser. Path copied: ${shown}`, 'info'),
      () => addToast(`Local file links can't open in the browser: ${shown}`, 'info'),
    )
    return
  }
  addToast(`Local file links can't open in the browser: ${shown}`, 'info')
}

// Whether path is a `[web] file_link_roots` entry or below one. Only a hint
// for which links to send to GET /api/files: the server resolves symlinks and
// enforces the roots itself.
export function isUnderFileLinkRoot(path, roots) {
  if (typeof path !== 'string' || !path.startsWith('/')) return false
  if (path.split('/').some((seg) => seg === '.' || seg === '..')) return false
  for (const raw of Array.isArray(roots) ? roots : []) {
    const root = String(raw || '').replace(/\/+$/, '')
    if (!String(raw || '').startsWith('/')) continue
    // A root of `/` strips to '' and covers every absolute path.
    if (root === '' || path === root || path.startsWith(root + '/')) return true
  }
  return false
}

// Whether a file link's host (from fileLinkHost) is the deck machine: none,
// localhost, or the deck's hostname (short or full form).
export function isDeckFileHost(host, deckHost) {
  if (!host) return true
  const want = String(deckHost || '').toLowerCase()
  if (!want) return false
  const h = String(host).toLowerCase()
  return h === want || h === want.split('.')[0] || h.split('.')[0] === want
}

// The deck URL that serves a file link (issue #2676).
function fileLinkURL(path, win) {
  const origin = (win.location && win.location.origin) || ''
  return `${origin}/api/files?path=${encodeURIComponent(path)}`
}

// Opens url the way xterm's built-in does: blank window, `opener` cleared,
// then navigate.
function navigateNewWindow(win, url) {
  const opened = win.open()
  if (!opened) {
    // Same as xterm: refuse to navigate rather than hand the target a live
    // opener reference we could not clear.
    console.warn('Opening link blocked as opener could not be cleared')
    return false
  }
  try {
    opened.opener = null
  } catch (_e) { /* read-only in some browsers; window.open already noopener'd */ }
  opened.location.href = url
  return true
}

// Applies the policy, then opens the link the way xterm's built-in does.
// `env` is injectable so tests can supply fakes instead of real dialogs.
export function openTerminalLink(url, options = {}, env = {}) {
  const win = env.win || (typeof window !== 'undefined' ? window : undefined)
  if (!win) return false
  const filePath = localFilePath(url)
  if (filePath) {
    // Under a configured root the deck serves it; no confirm, the roots are
    // the user's own allowlist.
    // A link naming another machine's file never opens the deck's file
    // at the same path.
    if (isDeckFileHost(fileLinkHost(url), options.fileLinkHost) && isUnderFileLinkRoot(filePath, options.fileLinkRoots)) {
      return navigateNewWindow(win, fileLinkURL(filePath, win))
    }
    if (env.onFileLink) env.onFileLink(filePath)
    else reportFileLink(filePath, fileLinkHost(url), win)
    return false
  }
  // The handler sets allowNonHttpProtocols so file:// links exist at all, which
  // also lets other schemes through. Only http(s) navigates: a javascript: URL
  // would run in the blank window, which shares the deck's origin.
  if (!isWebLink(url)) {
    addToast(`Only http(s) and file links open from the web terminal: ${clip(String(url))}`, 'info')
    return false
  }
  if (shouldConfirmLinkOpen(url, options)) {
    const ask = env.confirm || win.confirm.bind(win)
    if (!ask(`Do you want to navigate to ${url}?\n\nWARNING: This link could potentially be dangerous`)) {
      return false
    }
  }
  return navigateNewWindow(win, url)
}

// The xterm `linkHandler` option. Reads the live signals at click time so a
// settings refresh takes effect without rebuilding the terminal. hover/leave
// record the link under the pointer for installModifierClickLinks; xterm
// still fires them while the app captures the mouse. `env.open` replaces the
// policy call in tests.
export function createTerminalLinkHandler(env = {}) {
  const open = env.open || ((text) => openTerminalLink(text, {
    trustedDomains: trustedDomainsSignal.value,
    confirmLinkOpen: confirmLinkOpenSignal.value,
    fileLinkRoots: fileLinkRootsSignal.value,
    fileLinkHost: fileLinkHostSignal.value,
  }))
  return {
    // xterm drops every non-http(s) OSC 8 link without this; openTerminalLink
    // handles file:// and refuses the other schemes.
    allowNonHttpProtocols: true,
    hoveredLink: '',
    activate(_event, text) {
      open(text)
    },
    hover(_event, text) {
      this.hoveredLink = text
    },
    leave(_event, text) {
      if (this.hoveredLink === text) this.hoveredLink = ''
    },
  }
}

// Cmd-click on macOS, Ctrl-click elsewhere, primary button, no other modifier
// (Option/Alt stays the force-selection modifier from #2372).
export function isLinkOpenClick(event, isMac) {
  if (!event || event.button !== 0 || event.altKey || event.shiftKey) return false
  return isMac
    ? event.metaKey && !event.ctrlKey
    : event.ctrlKey && !event.metaKey
}

// Issue #2675: with mouse reporting on, xterm forwards a click to the app and
// its Linkifier never calls activate. This capture-phase listener on the
// terminal's container sees the press first: a modifier-press on the hovered
// link stops the mousedown, its mouseup and click so neither the app nor
// xterm's own Linkifier sees them (which also keeps the link from opening
// twice when mouse reporting is off). Like xterm, the link opens on release,
// and only if the pointer is still on it, so dragging off cancels.
export function installModifierClickLinks(container, handler, { isMac, signal } = {}) {
  let swallowing = false
  let pressedLink = ''
  const opts = { capture: true, signal }
  const swallow = (ev) => {
    ev.preventDefault()
    ev.stopPropagation()
    ev.stopImmediatePropagation()
  }
  container.addEventListener('mousedown', (ev) => {
    swallowing = false
    const link = handler.hoveredLink
    if (!link || !isLinkOpenClick(ev, isMac)) return
    swallowing = true
    // The second press of a double-click must not open the link again.
    pressedLink = ev.detail > 1 ? '' : link
    swallow(ev)
  }, opts)
  container.addEventListener('mouseup', (ev) => {
    if (!swallowing || ev.button !== 0) return
    swallow(ev)
    const link = pressedLink
    pressedLink = ''
    if (link && handler.hoveredLink === link) handler.activate(ev, link)
  }, opts)
  container.addEventListener('click', (ev) => {
    if (!swallowing) return
    swallowing = false
    swallow(ev)
  }, opts)
  // A press dragged out and released outside the terminal never reaches the
  // listeners above, so forget it; else a later plain release over the same
  // link would open it and every mouseup would be swallowed.
  const doc = container.ownerDocument
  const forgetOutside = (ev) => {
    if (container.contains(ev.target)) return
    swallowing = false
    pressedLink = ''
  }
  doc.addEventListener('mousedown', forgetOutside, opts)
  doc.addEventListener('mouseup', forgetOutside, opts)
}
