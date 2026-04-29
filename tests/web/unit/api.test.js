// unit/api.test.js -- apiFetch is the single mutation chokepoint, so its
// behavior on success / network error / API error / auth-token presence is
// part of the parity contract. If any of these regress, every other component
// that calls a mutation is silently broken.

import { describe, it, expect, beforeEach, vi } from 'vitest'

// Mock Toast.addToast to avoid pulling the rendering layer into a unit test.
vi.mock('../../../internal/web/static/app/Toast.js', () => ({
  addToast: vi.fn(),
}))

const apiModulePath = '../../../internal/web/static/app/api.js'
const stateModulePath = '../../../internal/web/static/app/state.js'

describe('apiFetch', () => {
  beforeEach(async () => {
    // Reset auth token before each test.
    const state = await import(stateModulePath)
    state.authTokenSignal.value = ''
  })

  it('attaches Content-Type and Accept headers on every call', async () => {
    const { apiFetch } = await import(apiModulePath)
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ ok: true }),
    })
    vi.stubGlobal('fetch', fetchMock)

    await apiFetch('GET', '/api/sessions')
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [, init] = fetchMock.mock.calls[0]
    expect(init.headers['Content-Type']).toBe('application/json')
    expect(init.headers['Accept']).toBe('application/json')
    expect(init.headers['Authorization']).toBeUndefined()
  })

  it('attaches Authorization when an auth token is set', async () => {
    const state = await import(stateModulePath)
    state.authTokenSignal.value = 'tok-abc'
    const { apiFetch } = await import(apiModulePath)
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({}),
    })
    vi.stubGlobal('fetch', fetchMock)

    await apiFetch('POST', '/api/sessions', { title: 'x', projectPath: '/y' })
    const [, init] = fetchMock.mock.calls[0]
    expect(init.headers['Authorization']).toBe('Bearer tok-abc')
    expect(init.method).toBe('POST')
    expect(init.body).toBe(JSON.stringify({ title: 'x', projectPath: '/y' }))
  })

  it('throws and surfaces toast on network error', async () => {
    const { apiFetch } = await import(apiModulePath)
    const Toast = await import('../../../internal/web/static/app/Toast.js')
    const fetchMock = vi.fn().mockRejectedValue(new Error('boom'))
    vi.stubGlobal('fetch', fetchMock)

    await expect(apiFetch('POST', '/api/sessions', {})).rejects.toThrow(/Network error/)
    expect(Toast.addToast).toHaveBeenCalled()
  })

  it('throws on non-ok response and surfaces error.message via toast for mutations', async () => {
    const { apiFetch } = await import(apiModulePath)
    const Toast = await import('../../../internal/web/static/app/Toast.js')
    Toast.addToast.mockClear()
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      statusText: 'Forbidden',
      json: async () => ({ error: { message: 'mutations disabled' } }),
    })
    vi.stubGlobal('fetch', fetchMock)
    await expect(apiFetch('POST', '/api/sessions', {})).rejects.toThrow(/mutations disabled/)
    expect(Toast.addToast).toHaveBeenCalledWith('mutations disabled')
  })

  it('does NOT toast for failing GET (background reads)', async () => {
    const { apiFetch } = await import(apiModulePath)
    const Toast = await import('../../../internal/web/static/app/Toast.js')
    Toast.addToast.mockClear()
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      statusText: 'Internal',
      json: async () => ({}),
    })
    vi.stubGlobal('fetch', fetchMock)
    await expect(apiFetch('GET', '/api/sessions')).rejects.toThrow()
    expect(Toast.addToast).not.toHaveBeenCalled()
  })

  it('does NOT include a body for GET requests', async () => {
    const { apiFetch } = await import(apiModulePath)
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({}),
    })
    vi.stubGlobal('fetch', fetchMock)
    await apiFetch('GET', '/api/menu')
    const [, init] = fetchMock.mock.calls[0]
    expect(init.body).toBeUndefined()
  })
})
