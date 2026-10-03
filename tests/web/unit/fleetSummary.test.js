// The Command Center's conductor-written fleet summary: markdown rendered as
// vnodes (never innerHTML), plus annotation chips on the session rows.
import { beforeEach, describe, expect, it } from 'vitest'
import { render } from 'preact'
import { html } from 'htm/preact'
import { waitFor } from '@testing-library/preact'

const stateModulePath = '../../../internal/web/static/app/state.js'
const mdModulePath = '../../../internal/web/static/app/miniMarkdown.js'
const paneModulePath = '../../../internal/web/static/app/panes/CommandCenterPane.js'

function mount(vnode) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  render(vnode, container)
  return container
}

describe('miniMarkdown', () => {
  it('renders headings, lists, bold, code and http links', async () => {
    const { renderMarkdown } = await import(mdModulePath)
    const c = mount(html`<div>${renderMarkdown('# Fleet\n- **2** need input\n- run `make ci`\n1. [PR](https://x.test/1)\n\nplain para')}</div>`)
    expect(c.querySelector('h3').textContent).toBe('Fleet')
    expect(c.querySelectorAll('ul li')).toHaveLength(2)
    expect(c.querySelector('ul li strong').textContent).toBe('2')
    expect(c.querySelector('code').textContent).toBe('make ci')
    const a = c.querySelector('ol a')
    expect(a.getAttribute('href')).toBe('https://x.test/1')
    expect(a.getAttribute('rel')).toContain('noopener')
    expect(c.querySelector('p').textContent).toBe('plain para')
  })

  it('never turns annotation text into markup', async () => {
    const { renderMarkdown } = await import(mdModulePath)
    const c = mount(html`<div>${renderMarkdown('<img src=x onerror=alert(1)> [bad](javascript:alert(1))')}</div>`)
    expect(c.querySelector('img')).toBeNull()
    expect(c.querySelector('a')).toBeNull()
    expect(c.textContent).toContain('<img src=x onerror=alert(1)>')
  })
})

describe('Command Center fleet summary', () => {
  beforeEach(async () => {
    const { commandCenterSignal } = await import(stateModulePath)
    commandCenterSignal.value = {
      profile: 'default', conductors: [], totals: {}, decisionsWaiting: [], askTargets: ['maestro'],
      fleetSummaries: [{ sessionId: 'brain', title: 'brain-16', markdown: '## Today\n- 3 sessions need input' }],
    }
  })

  it('renders the summary panel above the columns', async () => {
    const { CommandCenterPane } = await import(paneModulePath)
    const c = mount(html`<${CommandCenterPane}/>`)
    // The markdown renderer is fetched on demand once a summary exists.
    await waitFor(() => expect(c.querySelector('[data-testid="cc-fleet-summary"]')).not.toBeNull())
    const panel = c.querySelector('[data-testid="cc-fleet-summary"]')
    expect(panel.textContent).toContain('brain-16')
    expect(panel.querySelector('h4').textContent).toBe('Today')
    expect(panel.querySelector('li').textContent).toBe('3 sessions need input')
  })

  it('renders nothing when no conductor has written a summary', async () => {
    const { commandCenterSignal } = await import(stateModulePath)
    commandCenterSignal.value = { ...commandCenterSignal.value, fleetSummaries: [] }
    const { CommandCenterPane } = await import(paneModulePath)
    const c = mount(html`<${CommandCenterPane}/>`)
    expect(c.querySelector('[data-testid="cc-fleet-summary"]')).toBeNull()
  })
})
