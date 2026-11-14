// A sandbox's remote card on the Fleet pane names whose sandbox it is
// (/api/remotes `owner`, ASK-172); a plain remote has no owner chip. The
// owner is text from config.toml, so markup in it is shown, not parsed.
import { describe, expect, it, vi } from 'vitest'
import { render } from 'preact'
import { waitFor } from '@testing-library/preact'
import { html } from 'htm/preact'

vi.mock('../../../internal/web/static/app/api.js', () => ({
  apiFetch: vi.fn(async (method, path) => {
    if (path === '/api/remotes') {
      return {
        counts: { remotesOnline: 3, remotesOffline: 0, sessions: 0, running: 0, waiting: 0, idle: 0, error: 0, stopped: 0 },
        remotes: [
          { name: 'sb1', online: true, kind: 'sandbox (provider docker)', owner: 'alice', sessions: [] },
          { name: 'sb2', online: true, kind: 'sandbox (provider docker)', owner: '<img src=x onerror=alert(1)>', sessions: [] },
          { name: 'build', online: true, sessions: [] },
        ],
      }
    }
    return {}
  }),
}))

describe('Fleet pane remote cards', () => {
  it('show the sandbox owner chip, and none on a plain remote', async () => {
    const { sessionsSignal, sessionCostsSignal } = await import('../../../internal/web/static/app/state.js')
    sessionCostsSignal.value = {}
    sessionsSignal.value = []
    const { FleetPane } = await import('../../../internal/web/static/app/panes/FleetPane.js')
    const c = document.createElement('div')
    document.body.appendChild(c)
    render(html`<${FleetPane}/>`, c)
    const card = name => c.querySelector(`[data-testid="fleet-remote-card"][data-remote-name="${name}"]`)
    await waitFor(() => expect(card('sb1')).not.toBeNull())

    expect(card('sb1').querySelector('[data-testid="fleet-remote-owner"]').textContent.trim()).toBe('owner: alice')
    expect(card('build').querySelector('[data-testid="fleet-remote-owner"]')).toBeNull()

    const chip = card('sb2').querySelector('[data-testid="fleet-remote-owner"]')
    expect(chip.textContent.trim()).toBe('owner: <img src=x onerror=alert(1)>')
    expect(chip.querySelector('img')).toBeNull()
  })
})
