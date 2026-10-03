// The Fleet board's opt-in Status view: a kanban keyed on the semantic
// status hint. Runtime state never picks a column; it keeps its own tiles.
import { beforeEach, describe, expect, it } from 'vitest'
import { render } from 'preact'
import { html } from 'htm/preact'
import { waitFor } from '@testing-library/preact'

const stateModulePath = '../../../internal/web/static/app/state.js'
const uiStateModulePath = '../../../internal/web/static/app/uiState.js'
const annotationsModulePath = '../../../internal/web/static/app/annotations.js'
const boardModulePath = '../../../internal/web/static/app/panes/FleetStatusBoard.js'
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
  sess('untagged', 'waiting', undefined),
  sess('shipped', 'error', { status: 'done' }),
  sess('paused-one', 'running', { status: 'Paused' }),
  sess('broken', 'error', { status: 'in-progress' }),
  { type: 'group', level: 0, group: { name: 'conductor', path: 'conductor', expanded: true, order: 1 } },
  sess('brain-16', 'waiting', { note: '# Fleet Summary\n- **3** need you' }, 'conductor'),
]

// The Status board is fetched on demand (FleetPane useStatusBoard); wait for
// it to render before asserting on it.
async function mountStatusBoard() {
  const { FleetPane } = await import(paneModulePath)
  const c = mount(html`<${FleetPane}/>`)
  await waitFor(() => expect(c.querySelector('[data-testid="fleet-kanban"]')).not.toBeNull())
  return c
}

describe('kanbanColumn', () => {
  it('places a session by its status hint only, never by runtime state', async () => {
    const { kanbanColumn } = await import(boardModulePath)
    expect(kanbanColumn({ status: 'waiting', hints: { status: 'needs-input' } })).toBe('needs-input')
    // A set status always wins over runtime state.
    expect(kanbanColumn({ status: 'error', hints: { status: 'done' } })).toBe('done')
    expect(kanbanColumn({ status: 'running', hints: { status: 'parked' } })).toBe('parked')
    // Aliases, case and separators.
    expect(kanbanColumn({ status: 'idle', hints: { status: 'paused' } })).toBe('parked')
    expect(kanbanColumn({ status: 'idle', hints: { status: 'Ready For_Review' } })).toBe('ready-for-review')
    expect(kanbanColumn({ status: 'idle', hints: { status: 'blocked' } })).toBe('needs-input')
    // `waiting` is agent-deck's word for "waiting for the human": Needs input.
    expect(kanbanColumn({ status: 'idle', hints: { status: 'waiting' } })).toBe('needs-input')
    expect(kanbanColumn({ status: 'idle', hints: { status: 'Waiting' } })).toBe('needs-input')
    // Inherited object keys are not statuses.
    expect(kanbanColumn({ status: 'idle', hints: { status: 'constructor' } })).toBe('untriaged')
    // No (or an unknown) status is untriaged, never runtime-derived.
    expect(kanbanColumn({ status: 'running', hints: {} })).toBe('untriaged')
    expect(kanbanColumn({ status: 'idle' })).toBe('untriaged')
    expect(kanbanColumn({ status: 'idle', hints: { status: 'mystery' } })).toBe('untriaged')
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

  it('groups workers into semantic columns, with tiles that agree', async () => {
    const c = await mountStatusBoard()
    const cols = [...c.querySelectorAll('[data-testid^="kanban-col-"]')].map(e => e.dataset.testid.replace('kanban-col-', ''))
    expect(cols).toEqual(['needs-input', 'ready-for-review', 'in-progress', 'parked', 'done', 'untriaged'])
    const idsIn = (col) => [...c.querySelectorAll(`[data-testid="kanban-col-${col}"] [data-testid="kanban-card"]`)].map(e => e.dataset.sessionId).sort()
    expect(idsIn('needs-input')).toEqual(['asks'])
    expect(idsIn('ready-for-review')).toEqual(['review'])
    expect(idsIn('in-progress')).toEqual(['broken'])
    expect(idsIn('parked')).toEqual(['paused-one'])
    expect(idsIn('done')).toEqual(['shipped'])
    expect(idsIn('untriaged')).toEqual(['busy', 'untagged'])
    expect(c.querySelector('[data-testid="kanban-col-error"]')).toBeNull()

    // Semantic tiles count the same buckets, in their own row; the runtime
    // tiles stay and still count every local session (conductor included).
    const tile = (id) => c.querySelector(`[data-testid="fleet-stat-${id}"] .num`)?.textContent
    expect([tile('needs-input'), tile('ready-for-review'), tile('in-progress'), tile('parked'), tile('done'), tile('untriaged')])
      .toEqual(['1', '1', '1', '1', '1', '2'])
    expect(c.querySelector('[data-testid="fleet-status-stats"] [data-testid="fleet-stat-running"]')).toBeNull()
    expect([tile('running'), tile('waiting'), tile('error'), tile('idle')]).toEqual(['2', '3', '2', '1'])
  })

  it('shows runtime only as a secondary dot, with a quiet hint for a stopped parked/done session', async () => {
    const c = await mountStatusBoard()
    const card = (id) => c.querySelector(`[data-testid="kanban-card"][data-session-id="${id}"]`)
    expect(card('busy').querySelector('[data-testid="kanban-proc-dot"]').getAttribute('title')).toBe('process: running')
    expect(card('busy').querySelector('[data-testid="kanban-proc-warn"]')).toBeNull()
    const quiet = card('shipped').querySelector('[data-testid="kanban-proc-warn"]')
    expect(quiet.textContent).toBe('process not running')
    expect(quiet.classList.contains('severe')).toBe(false)
    const loud = card('broken').querySelector('[data-testid="kanban-proc-warn"]')
    expect(loud.textContent).toBe('process error')
    expect(loud.classList.contains('severe')).toBe(true)
  })

  it('shows the full headline, a note excerpt and the group badge on the card', async () => {
    const c = await mountStatusBoard()
    const card = c.querySelector('[data-testid="kanban-card"][data-session-id="asks"]')
    expect(card.querySelector('[data-testid="kanban-headline"]').textContent).toBe('FDP-2115 scoping · needs your call on rule scope')
    expect(card.querySelector('[data-testid="kanban-note"]').textContent).toBe('Open · which rules qualify? · second line')
    expect(card.querySelector('.kb-group').textContent).toBe('scoping')
  })

  // `decision` is the CLI's --decision: a decision already taken in the
  // session (session_annotate_cmd.go), so it is not labeled as an open ask.
  it('renders Goal / Current state / Decision with chip, ticket and group', async () => {
    const c = await mountStatusBoard()
    const card = c.querySelector('[data-testid="kanban-card"][data-session-id="review"]')
    const fields = [...card.querySelectorAll('.kb-field')].map(f => [f.querySelector('dt').textContent, f.querySelector('dd').textContent])
    expect(fields).toEqual([
      ['Goal', 'Audit B&R error copy'],
      ['Current state', '45 msgs reviewed, 4 central fixes'],
      ['Decision', 'Approve the 4 fixes?'],
    ])
    expect(card.querySelector('[data-testid="kanban-headline"]')).toBeNull()
    expect(card.querySelector('[data-testid="kanban-status"]').textContent).toBe('ready-for-review')
    expect(card.querySelector('[data-testid="kanban-ticket"]').textContent).toBe('BILL-590')
    expect(card.querySelector('.kb-group').textContent).toBe('scoping')
  })

  it('pins the conductor in a banner above the board, out of the kanban', async () => {
    const { conductorBannerOpenSignal } = await import(uiStateModulePath)
    conductorBannerOpenSignal.value = true
    const c = await mountStatusBoard()
    const banner = c.querySelector('[data-testid="conductor-banner"]')
    expect(banner.dataset.sessionId).toBe('brain-16')
    expect(c.querySelector('[data-testid="fleet-pane"]').firstElementChild).toBe(banner)
    expect(banner.querySelector('[data-testid="conductor-banner-status"]').textContent).toBe('waiting')
    expect(banner.querySelector('[data-testid="conductor-banner-summary"] h3').textContent).toBe('Fleet Summary')
    expect(c.querySelector('[data-testid="kanban-card"][data-session-id="brain-16"]')).toBeNull()

    banner.querySelector('[data-testid="conductor-banner-toggle"]').click()
    await new Promise(r => setTimeout(r, 0))
    expect(c.querySelector('[data-testid="conductor-banner-summary"]')).toBeNull()
    expect(c.querySelector('[data-testid="conductor-banner"]')).not.toBeNull()
  })

  it('switches to the group grid and back', async () => {
    const c = await mountStatusBoard()
    c.querySelector('[data-testid="fleet-view-groups"]').click()
    await waitFor(() => expect(c.querySelector('[data-testid="fleet-kanban"]')).toBeNull())
    expect(c.querySelector('[data-testid="fleet-group-card"]')).not.toBeNull()
    expect(c.querySelector('[data-testid="conductor-banner"]')).toBeNull()
    expect(c.querySelector('[data-testid="fleet-status-stats"]')).toBeNull()
    c.querySelector('[data-testid="fleet-view-status"]').click()
    await waitFor(() => expect(c.querySelector('[data-testid="fleet-kanban"]')).not.toBeNull())
  })
})
