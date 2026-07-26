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
import { trustedDomainsSignal, confirmLinkOpenSignal } from './state.js'

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

// Applies the policy, then opens the link the way xterm's built-in does.
// `env` is injectable so tests can supply fakes instead of real dialogs.
export function openTerminalLink(url, options = {}, env = {}) {
  const win = env.win || (typeof window !== 'undefined' ? window : undefined)
  if (!win) return false
  if (shouldConfirmLinkOpen(url, options)) {
    const ask = env.confirm || win.confirm.bind(win)
    if (!ask(`Do you want to navigate to ${url}?\n\nWARNING: This link could potentially be dangerous`)) {
      return false
    }
  }
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

// The xterm `linkHandler` option. Reads the live signals at click time so a
// settings refresh takes effect without rebuilding the terminal.
export function createTerminalLinkHandler() {
  return {
    activate(_event, text) {
      openTerminalLink(text, {
        trustedDomains: trustedDomainsSignal.value,
        confirmLinkOpen: confirmLinkOpenSignal.value,
      })
    },
  }
}
