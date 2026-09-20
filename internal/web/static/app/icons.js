// icons.js -- Icon primitives ported from bundle src/shell.jsx (LOGO, Icon, ICONS, Dot)
// Used by Topbar, Sidebar, RightRail, dialogs, panes. Zero runtime cost — pure render.
import { html } from 'htm/preact'

export function Logo() {
  return html`
    <svg width="28" height="18" viewBox="0 0 120 80" aria-hidden="true">
      <rect fill="#1a1b26" width="120" height="80" rx="12" stroke="var(--border-hi)" stroke-width="1"/>
      <line x1="40" y1="8" x2="40" y2="72" stroke="#414868" stroke-width="1.5"/>
      <line x1="80" y1="8" x2="80" y2="72" stroke="#414868" stroke-width="1.5"/>
      <circle cx="20" cy="40" r="11" fill="var(--tn-green)"/>
      <circle cx="60" cy="40" r="11" fill="var(--tn-yellow)"/>
      <circle cx="100" cy="40" r="11" fill="var(--tn-muted-2)"/>
    </svg>
  `
}

export function Icon({ d, size = 14 }) {
  return html`
    <svg width=${size} height=${size} viewBox="0 0 24 24" fill="none" stroke="currentColor"
         stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
      <path d=${d}/>
    </svg>
  `
}

export const ICONS = {
  play:    'M6 4l14 8-14 8V4z',
  stop:    'M6 6h12v12H6z',
  restart: 'M4 4v5h5 M20 20v-5h-5 M20 9a8 8 0 00-14-3 M4 15a8 8 0 0014 3',
  fork:    'M6 3v6a3 3 0 003 3h6a3 3 0 013 3v3 M6 3v0 M6 21v-6 M18 21v0 M6 21v0 M6 9v0',
  trash:   'M3 6h18 M8 6V4a2 2 0 012-2h4a2 2 0 012 2v2 M6 6v14a2 2 0 002 2h8a2 2 0 002-2V6',
  plus:    'M12 5v14 M5 12h14',
  filter:  'M3 5h18 M6 12h12 M10 19h4',
  search:  'M11 2a9 9 0 100 18 9 9 0 000-18z M22 22l-5-5',
  settings:'M12 8a4 4 0 100 8 4 4 0 000-8z M12 2v2 M12 20v2 M4.93 4.93l1.41 1.41 M17.66 17.66l1.41 1.41 M2 12h2 M20 12h2 M4.93 19.07l1.41-1.41 M17.66 6.34l1.41-1.41',
  chev:    'M6 9l6 6 6-6',
  chevR:   'M9 6l6 6-6 6',
  x:       'M6 6l12 12 M6 18L18 6',
  zap:     'M13 2L3 14h8l-1 8 10-12h-8l1-8z',
  wifi:    'M5 12a10 10 0 0114 0 M8.5 15.5a5 5 0 017 0 M12 19h.01',
  send:    'M22 2L11 13 M22 2l-7 20-4-9-9-4 20-7z',
  book:    'M4 4h12a4 4 0 014 4v12H8a4 4 0 01-4-4z M4 4v16',
  term:    'M4 4h16v16H4z M8 9l3 3-3 3 M13 15h4',
  // edit (pencil) — used by Sidebar SessionItem to open EditSessionDialog.
  edit:    'M12 20h9 M16.5 3.5a2.121 2.121 0 013 3L7 19l-4 1 1-4 12.5-12.5z',
  // archive (lid + box + handle slot). Replaces the ⌂ house glyph the sidebar
  // used, which is the *home* symbol and read as anything but archiving.
  archive: 'M21 8v13H3V8 M1 3h22v5H1z M10 12h4',
  // merge (two nodes, branch curving into the trunk) — worktree finish, which
  // merges the branch and cleans up. Replaces the ⎇✓ text glyph. Circles are
  // drawn as paired arcs because Icon renders a single <path>.
  merge:   'M15 18a3 3 0 106 0 3 3 0 10-6 0 M3 6a3 3 0 106 0 3 3 0 10-6 0 M6 21V9a9 9 0 009 9',
}

export function Dot({ status, size = 7 }) {
  return html`<span class=${`dot ${status || 'idle'}`} style=${{ width: size + 'px', height: size + 'px' }}/>`
}

export function kindSigil(k) {
  if (k === 'conductor') return '◆'
  if (k === 'watcher')   return '◇'
  return '›'
}
