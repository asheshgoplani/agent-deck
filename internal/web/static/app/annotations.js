// annotations.js -- recall annotation hints (`agent-deck session annotate`)
// as the web UI shows them: shared by the sidebar cards, the Fleet board and the
// Command Center.
import { html } from 'htm/preact'

// The semantic status hint (`session annotate --hint status=...`) says where
// the WORK is. It is the board's only grouping: runtime process state (the
// tmux/agent running|waiting|idle|error) never decides a column or a tile.
//
// Accepted values: the five canonical ids below, or any alias. Matching is
// case-insensitive and treats spaces/underscores as hyphens. Anything else
// (or no status at all) is "untriaged".
export const STATUS_ALIASES = {
  'needs-input':      ['needs-you', 'needs-human', 'blocked', 'waiting-on-you', 'waiting-for-input'],
  'ready-for-review': ['review', 'in-review', 'needs-review'],
  'in-progress':      ['working', 'active', 'wip', 'in-flight'],
  'parked':           ['paused', 'on-hold', 'hold', 'waiting', 'deferred', 'backlog'],
  'done':             ['complete', 'completed', 'finished', 'closed', 'merged', 'shipped'],
}
const STATUS_LOOKUP = Object.fromEntries(
  Object.entries(STATUS_ALIASES).flatMap(([id, aliases]) => [[id, id], ...aliases.map(a => [a, id])]),
)

// normalizeStatus maps a raw status hint to its canonical id, or '' when it
// is unset or not one the board knows.
export function normalizeStatus(raw) {
  const key = String(raw || '').trim().toLowerCase().replace(/[\s_]+/g, '-')
  return STATUS_LOOKUP[key] || ''
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

// processHint is the secondary runtime indicator for a card: always a
// tooltip, plus a short warning only when the process being down matters.
// A parked/done session whose process stopped or errored is expected, so it
// gets at most a quiet "process not running".
export function processHint(s) {
  const canon = normalizeStatus((s.hints || {}).status)
  const down = s.status === 'error' || s.status === 'stopped'
  let warn = ''
  if (down) warn = (canon === 'parked' || canon === 'done' || s.status === 'stopped') ? 'process not running' : 'process error'
  return { title: 'process: ' + (s.status || 'unknown'), warn, severe: warn === 'process error' }
}

// AnnotationLine renders status chip + ticket badge + headline for a session
// (or null when it has none). The headline wraps to two lines in CSS so a
// "goal · state · next" summary stays readable on narrow cards.
export function AnnotationLine({ s, class: cls = '' }) {
  const ann = sessionAnnotation(s)
  const note = noteExcerpt((s.hints || {}).note)
  if (!ann.headline && !ann.status && !ann.ticket && !note) return null
  return html`
    <div class=${`annot ${cls}`} data-testid="session-annotation">
      ${ann.status && html`<span class=${`hint-status ${ann.statusTone}`} data-testid="session-hint-status">${ann.status}</span>`}
      ${ann.ticket && html`<span class="hint-ticket" data-testid="session-hint-ticket">${ann.ticket}</span>`}
      ${ann.headline && html`<span class="hint-headline" title=${ann.headline} data-testid="session-hint-headline">${ann.headline}</span>`}
      ${note && html`<span class="hint-note" title=${note} data-testid="session-hint-note">${note}</span>`}
    </div>
  `
}

// Status-kanban columns for the Fleet board, in display order; the stat
// tiles use the same list (`tile` is the tile label). `always` columns render
// even when empty so the board keeps its shape; Untriaged only appears when a
// session has no recognised status.
export const KANBAN_COLUMNS = [
  { id: 'needs-input',      label: 'Needs input',      tile: 'NEEDS YOU',        tone: 'err',   always: true },
  { id: 'ready-for-review', label: 'Ready for review', tile: 'READY FOR REVIEW', tone: 'warn',  always: true },
  { id: 'in-progress',      label: 'In progress',      tile: 'IN PROGRESS',      tone: 'info',  always: true },
  { id: 'parked',           label: 'Parked',           tile: 'PARKED',           tone: 'muted', always: true },
  { id: 'done',             label: 'Done',             tile: 'DONE',             tone: 'ok',    always: true },
  { id: 'untriaged',        label: 'Untriaged',        tile: 'UNTRIAGED',        tone: '',      always: false },
]

// kanbanColumn places a session on the board purely by its semantic status.
// Runtime state is never consulted: a set status always wins, and a session
// without a recognised one is untriaged.
export function kanbanColumn(s) {
  return normalizeStatus((s.hints || {}).status) || 'untriaged'
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

// Structured card body written by conductors: goal / state / decision hints
// (`session annotate --hint goal=... --hint state=... --decision ...`). Empty
// array when none are set, so callers fall back to the headline.
export const CARD_FIELDS = [
  { key: 'goal',     label: 'Goal' },
  { key: 'state',    label: 'Current state' },
  { key: 'decision', label: 'Decision needed' },
]

export function cardFields(s) {
  const h = s.hints || {}
  return CARD_FIELDS.filter(f => (h[f.key] || '').trim()).map(f => ({ ...f, value: h[f.key].trim() }))
}
