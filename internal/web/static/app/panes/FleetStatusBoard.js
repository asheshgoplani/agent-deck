// panes/FleetStatusBoard.js -- the opt-in Status view of the Fleet board: a
// kanban keyed on the semantic `status` hint, its tiles, and the pinned
// conductor banner. FleetPane imports this module only once a viewer picks
// Status in the Status | Groups toggle, so the default Groups view does not
// ship it (the page is under a hard total-byte-weight budget, .lighthouserc.json).
import { html } from 'htm/preact'
import { conductorBannerOpenSignal } from '../uiState.js'
import { renderMarkdown } from '../miniMarkdown.js'
import { noteExcerpt, normalizeStatus, sessionAnnotation } from '../annotations.js'

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

// Structured card body written by conductors: goal / state / decision hints
// (`session annotate --hint goal=... --hint state=... --decision ...`). Empty
// array when none are set, so callers fall back to the headline. `decision`
// is the CLI's "decision taken in this session", not an open ask: an ask is
// what the Needs input column is for.
export const CARD_FIELDS = [
  { key: 'goal',     label: 'Goal' },
  { key: 'state',    label: 'Current state' },
  { key: 'decision', label: 'Decision' },
]

export function cardFields(s) {
  const h = s.hints || {}
  return CARD_FIELDS.filter(f => (h[f.key] || '').trim()).map(f => ({ ...f, value: h[f.key].trim() }))
}

// Within a column, most recently active first; ties break on title. Runtime
// state does not rank cards: the column already says where the work is.
const byRecency = (a, b) =>
  String(b.lastAccessedAt || '').localeCompare(String(a.lastAccessedAt || '')) || a.title.localeCompare(b.title)

// One kanban card, written to be read at normal zoom: name, then status
// chip · ticket · group, then the conductor's labeled Goal / Current state /
// Decision. Sessions without those hints fall back to the full
// headline plus a few lines of their note.
function KanbanCard({ s, groupLabel, onSelect }) {
  const ann = sessionAnnotation(s)
  const proc = processHint(s)
  const fields = cardFields(s)
  const note = fields.length ? '' : noteExcerpt((s.hints || {}).note, 3)
  return html`
    <button class="kb-card" data-testid="kanban-card" data-session-id=${s.id} onClick=${() => onSelect(s.id)}>
      <div class="kb-top">
        <span class="kb-title">${s.title}</span>
        ${proc.warn && html`<span class=${`kb-proc-warn ${proc.severe ? 'severe' : ''}`} data-testid="kanban-proc-warn">${proc.warn}</span>`}
        <span class=${`tdot ${s.status}`} title=${proc.title} aria-label=${proc.title} data-testid="kanban-proc-dot"/>
      </div>
      <div class="kb-meta">
        ${ann.status && html`<span class=${`hint-status ${ann.statusTone}`} data-testid="kanban-status">${ann.status}</span>`}
        ${ann.ticket && html`<span class="hint-ticket" data-testid="kanban-ticket">${ann.ticket}</span>`}
        ${groupLabel && html`<span class="kb-group">${groupLabel}</span>`}
      </div>
      ${fields.length
        ? html`<dl class="kb-fields" data-testid="kanban-fields">
            ${fields.map(f => html`
              <div class=${`kb-field ${f.key}`} key=${f.key} data-testid=${`kanban-field-${f.key}`}>
                <dt>${f.label}</dt><dd>${f.value}</dd>
              </div>`)}
          </dl>`
        : ann.headline && html`<div class="kb-headline" data-testid="kanban-headline">${ann.headline}</div>`}
      ${note && html`<div class="kb-note" data-testid="kanban-note">${note}</div>`}
    </button>
  `
}

// Conductors orchestrate the fleet, so they are pinned above the board rather
// than filed as a card in a status column or group.
export const isConductorSession = (s) => s.kind === 'conductor' || !!(s.raw && s.raw.isConductor)

// The pinned orchestrator banner: name + process status, its headline, and
// (expanded by default) the markdown fleet summary it keeps in its `note`
// (or `summary`) hint. Collapse state persists per browser.
export function ConductorBanner({ s, onSelect }) {
  const open = conductorBannerOpenSignal.value
  const h = s.hints || {}
  const summary = h.summary || h.note || ''
  const ann = sessionAnnotation(s)
  return html`
    <section class=${`conductor-banner ${s.status}`} data-testid="conductor-banner" data-session-id=${s.id}>
      <div class="cb-head">
        <span class="cb-kicker">CONDUCTOR</span>
        <span class=${`tdot ${s.status}`} title=${'process: ' + s.status}/>
        <button class="cb-name" title="Open conductor terminal" onClick=${() => onSelect(s.id)}>${s.title}</button>
        <span class=${`cb-proc ${s.status}`} data-testid="conductor-banner-status">${s.status}</span>
        ${ann.headline && html`<span class="cb-headline" title=${ann.headline}>${ann.headline}</span>`}
        ${summary && html`
          <button class="cb-toggle" aria-expanded=${open} data-testid="conductor-banner-toggle"
                  onClick=${() => { conductorBannerOpenSignal.value = !open }}>
            ${open ? 'Hide summary ▴' : 'Show summary ▾'}
          </button>`}
      </div>
      ${summary && open && html`<div class="cb-body cc-md" data-testid="conductor-banner-summary">${renderMarkdown(summary)}</div>`}
      ${!summary && html`<div class="cb-empty">No fleet summary yet: the conductor writes one with <code>agent-deck session annotate ${s.title} --note-stdin</code>.</div>`}
    </section>
  `
}

export function StatusKanban({ sessions, groupLabels, onSelect }) {
  const buckets = {}
  for (const s of sessions) (buckets[kanbanColumn(s)] ||= []).push(s)
  const cols = KANBAN_COLUMNS.filter(c => c.always || (buckets[c.id] || []).length)
  return html`
    <div class="kanban" data-testid="fleet-kanban" style=${`--kb-cols:${cols.length}`}>
      ${cols.map(c => {
        const items = (buckets[c.id] || []).slice().sort(byRecency)
        return html`
          <div class=${`kb-col ${c.tone}`} key=${c.id} data-testid=${`kanban-col-${c.id}`}>
            <div class="kb-col-head">
              <span class="kb-col-name">${c.label}</span>
              <span class="kb-col-count">${items.length}</span>
            </div>
            <div class="kb-stack">
              ${items.length
                ? items.map(s => html`<${KanbanCard} key=${s.id} s=${s} groupLabel=${groupLabels[s.group] || s.group} onSelect=${onSelect}/>`)
                : html`<div class="kb-empty">—</div>`}
            </div>
          </div>
        `
      })}
    </div>
  `
}


// Semantic tiles: one per kanban column, counting exactly what the columns
// hold (workers by status). They sit in their own row under the runtime
// Running / Waiting / Error / Idle tiles, which every view keeps.
export function StatusTiles({ sessions }) {
  const n = {}
  for (const s of sessions) n[kanbanColumn(s)] = (n[kanbanColumn(s)] || 0) + 1
  return html`
    <div class="fleet-stats fleet-status-stats" data-testid="fleet-status-stats">
      ${KANBAN_COLUMNS.filter(c => c.always || n[c.id]).map(c => html`
        <div key=${c.id} class=${`stat tone-${c.tone || 'none'}`} data-testid=${`fleet-stat-${c.id}`}>
          <div class="lbl">${c.tile}</div><div class="num">${n[c.id] || 0}</div>
        </div>`)}
    </div>
  `
}
