// Recall annotations (`agent-deck session annotate`) arrive on MenuSession as
// hints/tags. These assert the projection, the sidebar card line, the Overview
// rows and the filter, so the goal + semantic status stay visible on the board.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, describe, expect, it } from 'vitest'
import { render } from 'preact'
import { html } from 'htm/preact'

const stateModulePath = '../../../internal/web/static/app/state.js'
const uiStateModulePath = '../../../internal/web/static/app/uiState.js'
const dataModelModulePath = '../../../internal/web/static/app/dataModel.js'
const sidebarModulePath = '../../../internal/web/static/app/Sidebar.js'

function mount(vnode) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  render(vnode, container)
  return container
}

const MENU = [
  { type: 'group', level: 0, group: { name: 'work', path: 'work', expanded: true, order: 0 } },
  {
    type: 'session',
    session: {
      id: 'annotated', title: 'annotated', groupPath: 'work', tool: 'claude', status: 'running',
      hints: { headline: 'Expose hints on web cards', status: 'needs-input', ticket: 'ENG-42', why: 'board', note: '## Fleet\n- long markdown note' },
      tags: ['tooling', 'web'],
    },
  },
  {
    type: 'session',
    session: { id: 'purpose-only', title: 'purpose-only', groupPath: 'work', tool: 'claude', status: 'idle', hints: { purpose: 'fallback goal' } },
  },
  { type: 'session', session: { id: 'plain', title: 'plain', groupPath: 'work', tool: 'claude', status: 'idle' } },
]

describe('session annotations', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal, selectedIdSignal, selectedGroupSignal } = await import(stateModulePath)
    const { sidebarFilterSignal, groupExpandedSignal, statusFiltersSignal, showColsSignal } = await import(uiStateModulePath)
    sessionsSignal.value = MENU
    sessionCostsSignal.value = {}
    selectedIdSignal.value = null
    selectedGroupSignal.value = null
    sidebarFilterSignal.value = ''
    groupExpandedSignal.value = {}
    statusFiltersSignal.value = []
    // A persisted showCols from before the option existed: annotations default on.
    showColsSignal.value = { tool: true }
  })

  it('projects hints and tags, defaulting to empty', async () => {
    const { menuModelSignal } = await import(dataModelModulePath)
    const byId = Object.fromEntries(menuModelSignal.value.sessions.map(s => [s.id, s]))
    expect(byId.annotated.hints.ticket).toBe('ENG-42')
    expect(byId.annotated.tags).toEqual(['tooling', 'web'])
    expect(byId.plain.hints).toEqual({})
    expect(byId.plain.tags).toEqual([])
  })

  it('renders status chip, ticket badge and headline on the card', async () => {
    const { Sidebar } = await import(sidebarModulePath)
    const c = mount(html`<${Sidebar}/>`)
    const row = c.querySelector('[data-row-key="s:annotated"]')
    const chip = row.querySelector('[data-testid="session-hint-status"]')
    expect(chip.textContent).toBe('needs-input')
    expect(chip.classList.contains('err')).toBe(true)
    expect(row.querySelector('[data-testid="session-hint-ticket"]').textContent).toBe('ENG-42')
    expect(row.querySelector('[data-testid="session-hint-headline"]').textContent).toBe('Expose hints on web cards')

    expect(c.querySelector('[data-row-key="s:purpose-only"] [data-testid="session-hint-headline"]').textContent).toBe('fallback goal')
    expect(c.querySelector('[data-row-key="s:plain"] [data-testid="session-annotation"]')).toBeNull()
  })

  // Issue #2446(b): the row gains one subtitle line, not a block. `launch`
  // derives a purpose hint of up to 200 chars and conductors keep long
  // markdown notes, so the note stays off the row and the headline is
  // ellipsized to a single line (full text in the tooltip).
  it('keeps the note off the row and ellipsizes the headline to one line', async () => {
    const { Sidebar } = await import(sidebarModulePath)
    const c = mount(html`<${Sidebar}/>`)
    const row = c.querySelector('[data-row-key="s:annotated"]')
    expect(row.querySelector('[data-testid="session-hint-note"]')).toBeNull()
    expect(row.textContent).not.toContain('long markdown note')
    expect(row.querySelector('[data-testid="session-hint-headline"]').getAttribute('title')).toBe('Expose hints on web cards')

    const css = readFileSync(resolve(import.meta.dirname, '../../../internal/web/static/app/app.css'), 'utf8')
    const rule = css.match(/\.annot \.hint-headline\s*\{([^}]*)\}/)
    expect(rule).not.toBeNull()
    expect(rule[1]).toMatch(/white-space:\s*nowrap/)
    expect(rule[1]).toMatch(/text-overflow:\s*ellipsis/)
    expect(rule[1]).not.toMatch(/line-clamp/)
  })

  it('hides the line when the show-in-row option is off', async () => {
    const { showColsSignal } = await import(uiStateModulePath)
    showColsSignal.value = { tool: true, annotations: false }
    const { Sidebar } = await import(sidebarModulePath)
    const c = mount(html`<${Sidebar}/>`)
    expect(c.querySelector('[data-testid="session-annotation"]')).toBeNull()
  })

  it('maps known status values to tones and leaves others neutral', async () => {
    const { sessionAnnotation } = await import('../../../internal/web/static/app/annotations.js')
    const tone = (status) => sessionAnnotation({ hints: { status } }).statusTone
    expect(tone('needs-input')).toBe('err')
    expect(tone('ready-for-review')).toBe('warn')
    expect(tone('in-progress')).toBe('info')
    expect(tone('done')).toBe('ok')
    expect(tone('parked')).toBe('muted')
    expect(tone('blocked')).toBe('err') // alias of needs-input
    expect(tone('something-else')).toBe('')
  })

  it('reads a `waiting` status as Needs input, as agent-deck itself means it', async () => {
    const { normalizeStatus, sessionAnnotation } = await import('../../../internal/web/static/app/annotations.js')
    expect(normalizeStatus('waiting')).toBe('needs-input')
    expect(normalizeStatus('Waiting')).toBe('needs-input')
    expect(sessionAnnotation({ hints: { status: 'waiting' } })).toMatchObject({ status: 'needs-input', statusTone: 'err' })
    // Inherited object keys are not statuses: shown raw in a neutral chip.
    expect(normalizeStatus('constructor')).toBe('')
    expect(sessionAnnotation({ hints: { status: 'toString' } })).toMatchObject({ status: 'toString', statusTone: '' })
  })

  it('lets the sidebar filter match hint values and tags', async () => {
    const { sessionMatches, menuModelSignal } = await import(dataModelModulePath)
    const s = menuModelSignal.value.sessions.find(x => x.id === 'annotated')
    expect(sessionMatches(s, 'needs-input', [])).toBe(true)
    expect(sessionMatches(s, 'eng-42', [])).toBe(true)
    expect(sessionMatches(s, 'tooling', [])).toBe(true)
    expect(sessionMatches(s, 'nope', [])).toBe(false)
  })
})
