import { beforeEach, describe, expect, it, vi } from 'vitest'

const statePath = '../../../internal/web/static/app/state.js'
const apiPath = '../../../internal/web/static/app/api.js'

// Overlapping loadArchivedSessions() calls must not apply out of order. The
// menu SSE stream triggers this on every archive/unarchive while a group panel
// is open, so a slow first request finishing after a fast second one would
// otherwise reinstate a stale archived set.
describe('loadArchivedSessions ordering', () => {
  beforeEach(() => { vi.resetModules() })

  it('ignores an older response that resolves after a newer one', async () => {
    const gate = []
    vi.doMock(apiPath, () => ({
      apiFetch: vi.fn(() => new Promise((resolve) => gate.push(resolve))),
      authHeaders: () => ({}),
    }))
    const { loadArchivedSessions, archivedSessionsSignal } = await import(statePath)
    archivedSessionsSignal.value = []

    const first = loadArchivedSessions()      // slow
    const second = loadArchivedSessions()     // fast, newer

    gate[1]({ sessions: [{ id: 'new' }] })    // newer lands first
    await second
    expect(archivedSessionsSignal.value.map(s => s.id)).toEqual(['new'])

    gate[0]({ sessions: [{ id: 'stale' }] })  // older lands late
    await first
    expect(archivedSessionsSignal.value.map(s => s.id)).toEqual(['new'])
  })

  it('does not let a late failure blank a newer success', async () => {
    const gate = []
    vi.doMock(apiPath, () => ({
      apiFetch: vi.fn(() => new Promise((_, reject) => gate.push(reject))),
      authHeaders: () => ({}),
    }))
    const { loadArchivedSessions, archivedSessionsSignal } = await import(statePath)
    archivedSessionsSignal.value = [{ id: 'seed' }]

    const first = loadArchivedSessions()
    const second = loadArchivedSessions()
    gate[1](new Error('newer failed'))
    await second
    expect(archivedSessionsSignal.value).toEqual([])

    archivedSessionsSignal.value = [{ id: 'newer-success' }]
    gate[0](new Error('older failed'))
    await first
    expect(archivedSessionsSignal.value.map(s => s.id)).toEqual(['newer-success'])
  })
})
