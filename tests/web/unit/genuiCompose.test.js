// unit/genuiCompose.test.js -- genui-1: the CLIENT-side compose logic.
//
// This unit-tests composeSpec(), the pane's intent→spec call, in isolation
// (mocked fetch, no DOM mount). It proves the wiring the pane depends on:
//   (1) a successful compose returns the server-VALIDATED spec + trace,
//   (2) the Bearer token + intent are sent correctly,
//   (3) a server rejection (the validator caught a bad composed widget) THROWS
//       with the server's clean error message — so the pane shows WHY and never
//       renders unvalidated output,
//   (4) a non-JSON / opaque failure still throws (never resolves to a spec).
//
// The full mount-and-click pane interaction (type intent → Compose → the whole
// UI reshapes to the composed spec, plus the error path) is covered end-to-end
// in the real browser by tests/web/e2e/genui.spec.js — there is exactly one
// preact instance there, so the stateful component mounts cleanly.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { composeSpec } from '../../../internal/web/static/app/panes/GenuiPane.js'
import { authTokenSignal } from '../../../internal/web/static/app/state.js'

const composedBlockedSpec = {
  schema: 1, specId: 'composed-blocked', title: 't', version: 1,
  root: { type: 'col', children: [{ type: 'decision-list', bind: 'decisionsWaiting' }] },
}

beforeEach(() => { authTokenSignal.value = '' })
afterEach(() => { vi.restoreAllMocks() })

describe('genui-1 composeSpec()', () => {
  it('returns the server-validated spec + trace on success', async () => {
    global.fetch = vi.fn(async () => ({
      ok: true, status: 200,
      json: async () => ({ spec: composedBlockedSpec, trace: { composer: 'stub', tries: 1, repaired: false } }),
    }))
    const out = await composeSpec("show me what's blocked")
    expect(out.spec.specId).toBe('composed-blocked')
    expect(out.trace.composer).toBe('stub')
  })

  it('POSTs the intent and the Bearer token to the compose endpoint', async () => {
    authTokenSignal.value = 'secret-token'
    let url, opts
    global.fetch = vi.fn(async (u, o) => {
      url = u; opts = o
      return { ok: true, status: 200, json: async () => ({ spec: composedBlockedSpec, trace: {} }) }
    })
    await composeSpec('group by project')
    expect(String(url)).toContain('/api/command-center/genui/compose')
    expect(opts.method).toBe('POST')
    expect(opts.headers['Authorization']).toBe('Bearer secret-token')
    expect(opts.headers['Content-Type']).toBe('application/json')
    expect(JSON.parse(opts.body).intent).toBe('group by project')
  })

  it('throws the server clean error when the compose is rejected (no spec)', async () => {
    global.fetch = vi.fn(async () => ({
      ok: false, status: 422,
      json: async () => ({ error: 'could not compose a valid view for that intent', code: 'COMPOSE_FAILED' }),
    }))
    await expect(composeSpec('use an unknown widget')).rejects.toThrow(/could not compose a valid view/)
  })

  it('throws on an opaque (non-JSON) failure rather than returning a spec', async () => {
    global.fetch = vi.fn(async () => ({
      ok: false, status: 500,
      json: async () => { throw new Error('not json') },
    }))
    await expect(composeSpec('anything')).rejects.toThrow(/HTTP 500/)
  })
})
