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
    headline: h.headline || h.purpose || '',
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
  if (!ann.headline && !ann.status && !ann.ticket) return null
  return html`
    <div class=${`annot ${cls}`} data-testid="session-annotation">
      ${ann.status && html`<span class=${`hint-status ${ann.statusTone}`} data-testid="session-hint-status">${ann.status}</span>`}
      ${ann.ticket && html`<span class="hint-ticket" data-testid="session-hint-ticket">${ann.ticket}</span>`}
      ${ann.headline && html`<span class="hint-headline" title=${ann.headline} data-testid="session-hint-headline">${ann.headline}</span>`}
    </div>
  `
}
