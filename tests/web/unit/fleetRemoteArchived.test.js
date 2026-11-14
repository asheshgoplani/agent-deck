// The Fleet pane's remote cards must leave out sessions the remote reports
// archived. /api/remotes keeps those rows (with archived: true) as
// `remote sessions --json` does, but an archived session keeps a live-looking
// status, so showing it would paint the card waiting/error and inflate its
// per-card counts. Same rule as `remote sessions` text output (#2972).
import { describe, expect, it, vi } from 'vitest'
import { render } from 'preact'
import { waitFor } from '@testing-library/preact'
import { html } from 'htm/preact'

vi.mock('../../../internal/web/static/app/api.js', () => ({
  apiFetch: vi.fn(async (method, path) => {
    if (path === '/api/remotes') {
      return {
        counts: { remotesOnline: 1, remotesOffline: 0, sessions: 1, running: 1, waiting: 0, idle: 0, error: 0, stopped: 0 },
        remotes: [{
          name: 'box', online: true, latencyMs: 12,
          sessions: [
            { id: 'live-1', title: 'live', status: 'running', tool: 'claude' },
            { id: 'arch-w', title: 'old waiting', status: 'waiting', tool: 'claude', archived: true },
            { id: 'arch-e', title: 'old error', status: 'error', tool: 'claude', archived: true },
          ],
        }],
      }
    }
    return {}
  }),
}))

describe('Fleet pane remote cards', () => {
  it('leave out archived remote sessions', async () => {
    const { sessionsSignal, sessionCostsSignal } = await import('../../../internal/web/static/app/state.js')
    sessionCostsSignal.value = {}
    sessionsSignal.value = []
    const { FleetPane } = await import('../../../internal/web/static/app/panes/FleetPane.js')
    const c = document.createElement('div')
    document.body.appendChild(c)
    render(html`<${FleetPane}/>`, c)
    // The card appears only after the mount effect's fetch resolves and
    // Preact re-renders; wait for it instead of a fixed sleep.
    await waitFor(() => expect(c.querySelector('[data-testid="fleet-remote-card"][data-remote-name="box"]')).not.toBeNull())

    const card = c.querySelector('[data-testid="fleet-remote-card"][data-remote-name="box"]')
    const tiles = [...card.querySelectorAll('[data-testid="fleet-remote-session-tile"]')]
      .map(t => t.getAttribute('data-session-id'))
    expect(tiles).toEqual(['live-1'])
    expect(card.querySelector('[data-testid="fleet-remote-session-count"]').textContent.trim()).toBe('1 session')
    // Health comes from live sessions only: one running, nothing waiting.
    expect(card.classList.contains('running')).toBe(true)
    expect(card.classList.contains('waiting')).toBe(false)
    expect(card.classList.contains('error')).toBe(false)
  })
})
