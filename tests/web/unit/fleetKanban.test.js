// The Fleet board's default view: a kanban keyed on the semantic status hint,
// with process errors overriding and unannotated sessions falling back on
// their process status.
import { beforeEach, describe, expect, it } from 'vitest'
import { render } from 'preact'
import { html } from 'htm/preact'

const stateModulePath = '../../../internal/web/static/app/state.js'
const uiStateModulePath = '../../../internal/web/static/app/uiState.js'
const annotationsModulePath = '../../../internal/web/static/app/annotations.js'
const paneModulePath = '../../../internal/web/static/app/panes/FleetPane.js'

function mount(vnode) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  render(vnode, container)
  return container
}

const sess = (id, status, hints, groupPath = 'scoping') => ({
  type: 'session',
  session: { id, title: id, groupPath, tool: 'claude', status, hints },
})

const MENU = [
  { type: 'group', level: 0, group: { name: 'scoping', path: 'scoping', expanded: true, order: 0 } },
  sess('asks', 'waiting', { status: 'needs-input', headline: 'FDP-2115 scoping · needs your call on rule scope', note: '## Open\n- **which** rules qualify?\n- second line\n- third\n- fourth' }),
  sess('review', 'idle', { status: 'ready-for-review', ticket: 'BILL-590', goal: 'Audit B&R error copy', state: '45 msgs reviewed, 4 central fixes', decision: 'Approve the 4 fixes?', headline: 'ignored when fields exist' }),
  sess('busy', 'running', {}),
  sess('parked', 'waiting', undefined),
  sess('shipped', 'idle', { status: 'done' }),
  sess('broken', 'error', { status: 'in-progress' }),
]

describe('kanbanColumn', () => {
  it('uses the hint, lets a process error win, and falls back on process status', async () => {
    const { kanbanColumn } = await import(annotationsModulePath)
    expect(kanbanColumn({ status: 'waiting', hints: { status: 'needs-input' } })).toBe('needs-input')
    expect(kanbanColumn({ status: 'error', hints: { status: 'done' } })).toBe('error')
    expect(kanbanColumn({ status: 'running', hints: {} })).toBe('in-progress')
    expect(kanbanColumn({ status: 'idle' })).toBe('waiting')
    expect(kanbanColumn({ status: 'idle', hints: { status: 'blocked' } })).toBe('waiting')
  })

  it('excerpts markdown notes as plain text', async () => {
    const { noteExcerpt } = await import(annotationsModulePath)
    expect(noteExcerpt('## Open\n- **which** rules?\n\n- `x`', 2)).toBe('Open · which rules?')
    expect(noteExcerpt(undefined)).toBe('')
  })
})

describe('Fleet status kanban', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal } = await import(stateModulePath)
    const { fleetViewSignal } = await import(uiStateModulePath)
    sessionsSignal.value = MENU
    sessionCostsSignal.value = {}
    fleetViewSignal.value = 'status'
  })

  it('is the default view, with columns in order and cards in the right column', async () => {
    const { FleetPane } = await import(paneModulePath)
    const c = mount(html`<${FleetPane}/>`)
    const cols = [...c.querySelectorAll('[data-testid^="kanban-col-"]')].map(e => e.dataset.testid.replace('kanban-col-', ''))
    expect(cols).toEqual(['needs-input', 'ready-for-review', 'in-progress', 'waiting', 'done', 'error'])
    const idsIn = (col) => [...c.querySelectorAll(`[data-testid="kanban-col-${col}"] [data-testid="kanban-card"]`)].map(e => e.dataset.sessionId)
    expect(idsIn('needs-input')).toEqual(['asks'])
    expect(idsIn('ready-for-review')).toEqual(['review'])
    expect(idsIn('in-progress')).toEqual(['busy'])
    expect(idsIn('waiting')).toEqual(['parked'])
    expect(idsIn('done')).toEqual(['shipped'])
    expect(idsIn('error')).toEqual(['broken'])
  })

  it('shows the full headline, a note excerpt and the group badge on the card', async () => {
    const { FleetPane } = await import(paneModulePath)
    const c = mount(html`<${FleetPane}/>`)
    const card = c.querySelector('[data-testid="kanban-card"][data-session-id="asks"]')
    expect(card.querySelector('[data-testid="kanban-headline"]').textContent).toBe('FDP-2115 scoping · needs your call on rule scope')
    expect(card.querySelector('[data-testid="kanban-note"]').textContent).toBe('Open · which rules qualify? · second line')
    expect(card.querySelector('.kb-group').textContent).toBe('scoping')
  })

  it('renders Goal / Current state / Decision needed with chip, ticket and group', async () => {
    const { FleetPane } = await import(paneModulePath)
    const c = mount(html`<${FleetPane}/>`)
    const card = c.querySelector('[data-testid="kanban-card"][data-session-id="review"]')
    const fields = [...card.querySelectorAll('.kb-field')].map(f => [f.querySelector('dt').textContent, f.querySelector('dd').textContent])
    expect(fields).toEqual([
      ['Goal', 'Audit B&R error copy'],
      ['Current state', '45 msgs reviewed, 4 central fixes'],
      ['Decision needed', 'Approve the 4 fixes?'],
    ])
    expect(card.querySelector('[data-testid="kanban-headline"]')).toBeNull()
    expect(card.querySelector('[data-testid="kanban-status"]').textContent).toBe('ready-for-review')
    expect(card.querySelector('[data-testid="kanban-ticket"]').textContent).toBe('BILL-590')
    expect(card.querySelector('.kb-group').textContent).toBe('scoping')
  })

  it('switches to the group grid and back', async () => {
    const { FleetPane } = await import(paneModulePath)
    const c = mount(html`<${FleetPane}/>`)
    c.querySelector('[data-testid="fleet-view-groups"]').click()
    await new Promise(r => setTimeout(r, 0))
    expect(c.querySelector('[data-testid="fleet-kanban"]')).toBeNull()
    expect(c.querySelector('[data-testid="fleet-group-card"]')).not.toBeNull()
    c.querySelector('[data-testid="fleet-view-status"]').click()
    await new Promise(r => setTimeout(r, 0))
    expect(c.querySelector('[data-testid="fleet-kanban"]')).not.toBeNull()
  })
})
