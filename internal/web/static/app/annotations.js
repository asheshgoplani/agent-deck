// annotations.js -- recall annotation hints (`agent-deck session annotate`)
// as the web UI shows them: shared by the sidebar cards, the Fleet board and the
// Command Center.
import { html } from 'htm/preact'

// The semantic status hint (`session annotate --hint status=...`) says where
// the WORK is. It is the Status board's only grouping: runtime process state
// (the tmux/agent running|waiting|idle|error) never decides a kanban column or
// a semantic tile; it keeps its own Running/Waiting/Error/Idle tiles.
//
// Accepted values: the five canonical ids below, or any alias. Matching is
// case-insensitive and treats spaces/underscores as hyphens. Anything else
// (or no status at all) is "untriaged". `waiting` means what it means
// everywhere else in agent-deck: waiting for the human, so Needs input.
export const STATUS_ALIASES = {
  'needs-input':      ['needs-you', 'needs-human', 'blocked', 'waiting', 'waiting-on-you', 'waiting-for-input'],
  'ready-for-review': ['review', 'in-review', 'needs-review'],
  'in-progress':      ['working', 'active', 'wip', 'in-flight'],
  'parked':           ['paused', 'on-hold', 'hold', 'deferred', 'backlog'],
  'done':             ['complete', 'completed', 'finished', 'closed', 'merged', 'shipped'],
}
const STATUS_LOOKUP = Object.fromEntries(
  Object.entries(STATUS_ALIASES).flatMap(([id, aliases]) => [[id, id], ...aliases.map(a => [a, id])]),
)

// normalizeStatus maps a raw status hint to its canonical id, or '' when it
// is unset or not one the board knows.
export function normalizeStatus(raw) {
  const key = String(raw || '').trim().toLowerCase().replace(/[\s_]+/g, '-')
  return Object.hasOwn(STATUS_LOOKUP, key) ? STATUS_LOOKUP[key] : ''
}

// Chip colour per canonical status. Parked is deliberately quiet.
export const HINT_STATUS_TONE = {
  'needs-input': 'err',
  'ready-for-review': 'warn',
  'in-progress': 'info',
  'parked': 'muted',
  'done': 'ok',
}

// The card's annotation line: headline (falling back to goal, then the
// creation-time purpose hint), status and ticket. `status` is the canonical
// id when recognised, else the raw value (shown in a neutral chip).
export function sessionAnnotation(s) {
  const h = s.hints || {}
  const canon = normalizeStatus(h.status)
  return {
    headline: h.headline || h.goal || h.purpose || '',
    status: canon || (h.status || '').trim(),
    statusTone: HINT_STATUS_TONE[canon] || '',
    ticket: h.ticket || '',
  }
}

// AnnotationLine renders status chip + ticket badge + headline for a session
// (or null when it has none). The headline is ellipsized to one line in CSS
// with the full text in its tooltip; the (often long, markdown) note stays
// off the row and shows in the Overview rail and on Status-board cards.
export function AnnotationLine({ s, class: cls = '' }) {
  const ann = sessionAnnotation(s)
  if (!ann.headline && !ann.status && !ann.ticket) return null
  return html`
    <div class=${`annot ${cls}`} data-testid="session-annotation">
      ${ann.status && html`<span class=${`hint-status ${ann.statusTone}`} data-testid="session-hint-status">${ann.status}</span>`}
      ${ann.ticket && html`<span class="hint-ticket" data-testid="session-hint-ticket">${ann.ticket}</span>`}
      ${ann.headline && html`<span class="hint-headline" title=${ann.headline} data-testid="session-hint-headline">${ann.headline}</span>`}
    </div>
  `
}

// noteExcerpt turns a (possibly markdown) note hint into a short plain-text
// excerpt for a card: first non-empty lines, list/heading markers stripped.
export function noteExcerpt(note, maxLines = 2) {
  return String(note || '')
    .split(/\r?\n/)
    .map(l => l.trim().replace(/^(#{1,6}|[-*]|\d+\.)\s+/, '').replace(/\*\*/g, '').replace(/`/g, ''))
    .filter(Boolean)
    .slice(0, maxLines)
    .join(' · ')
}
