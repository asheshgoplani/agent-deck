// annotations.js -- recall annotation hints (`agent-deck session annotate`)
// as the web UI shows them: shared by the sidebar cards, the Fleet board and the
// Command Center.
import { html } from 'htm/preact'

// Colour for the semantic status hint (`session annotate --hint status=...`),
// which says where the WORK is, as opposed to the process dot. Unknown values
// still render, in the neutral chip.
export const HINT_STATUS_TONE = {
  'needs-input': 'err',
  'ready-for-review': 'warn',
  'in-progress': 'info',
  'done': 'ok',
}

// The card's annotation line: headline (falling back to the creation-time
// purpose hint), status and ticket. Empty strings when unset.
export function sessionAnnotation(s) {
  const h = s.hints || {}
  return {
    headline: h.headline || h.goal || h.purpose || '',
    status: h.status || '',
    statusTone: HINT_STATUS_TONE[h.status] || '',
    ticket: h.ticket || '',
  }
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

// Status-kanban columns for the Fleet board, in display order. `always`
// columns render even when empty so the board keeps its shape; the fallback
// column only appears when an unannotated session lands in it.
export const KANBAN_COLUMNS = [
  { id: 'needs-input',      label: 'Needs Input',      tone: 'err',  always: true },
  { id: 'ready-for-review', label: 'Ready for Review', tone: 'warn', always: true },
  { id: 'in-progress',      label: 'In Progress',      tone: 'info', always: true },
  { id: 'waiting',          label: 'Waiting · no status', tone: '',  always: false },
  { id: 'done',             label: 'Done',             tone: 'ok',   always: true },
  { id: 'error',            label: 'Error',            tone: 'err',  always: true },
]

// kanbanColumn places a session on the board. A process error wins (the
// work cannot progress whatever the hint says); otherwise the semantic status
// hint decides. Sessions without one fall back on their process status: a
// running agent is in progress, anything else is parked in "Waiting".
export function kanbanColumn(s) {
  if (s.status === 'error') return 'error'
  const hint = (s.hints || {}).status
  if (hint && HINT_STATUS_TONE[hint]) return hint
  if (s.status === 'running' || s.status === 'starting') return 'in-progress'
  return 'waiting'
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
