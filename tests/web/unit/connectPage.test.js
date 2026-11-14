import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

// connect.js is a classic script (an IIFE that wires the /connect page on
// load), so the test builds the page's elements and runs the file as is.
const src = readFileSync(
  resolve(import.meta.dirname, '../../../internal/web/static/connect.js'),
  'utf8',
)

const page = `
  <span id="setup-line"></span><button id="copy-setup">Copy</button><span id="setup-status"></span>
  <div id="scopes"></div>
  <select id="kind"></select><input id="name" value="" />
  <button id="copy">Copy</button><button id="test" disabled>Test</button>
  <span id="status"></span><pre id="block" hidden></pre><div id="tokens"></div>
`

const info = {
  setup_prompt: 'https://example.invalid/SETUP-PROMPT.md',
  scopes: [
    { id: 'read', description: 'see sessions', default: true },
    { id: 'send', description: 'send messages', default: false },
  ],
  write_scopes: true,
  assistants: [{ id: 'mcp', label: 'MCP' }],
  default: 'mcp',
  tokens: [],
}

function jsonResponse(body) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
}

const flush = () => new Promise((r) => setTimeout(r, 0))

function scopeBox(id) {
  return document.querySelector(`#scopes input[value="${id}"]`)
}

describe('/connect page: the block matches the ticks it was made for', () => {
  let tokenRequests
  let copied

  beforeEach(async () => {
    document.body.innerHTML = page
    tokenRequests = []
    copied = []
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true })
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: (t) => { copied.push(t); return Promise.resolve() } },
      configurable: true,
    })
    globalThis.fetch = vi.fn((path, opts) => {
      if (path === '/api/connect/assistants') return jsonResponse(info)
      if (path === '/api/connect/tokens') {
        const body = JSON.parse(opts.body)
        // Held open until the test resolves it.
        return new Promise((resolveReq) => {
          tokenRequests.push({
            scopes: body.scopes,
            resolve: () => resolveReq({
              ok: true,
              status: 200,
              json: () => Promise.resolve({
                name: 'mcp',
                token: 'adk_' + body.scopes.join('_'),
                scopes: body.scopes,
                block: 'BLOCK for ' + body.scopes.join(','),
              }),
            }),
          })
        })
      }
      return Promise.reject(new Error('unexpected fetch ' + path))
    })
    ;(0, eval)(src)
    await flush()
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('copies the block when the ticks did not change', async () => {
    document.getElementById('copy').click()
    expect(tokenRequests).toHaveLength(1)
    tokenRequests[0].resolve()
    await flush()
    await flush()
    expect(copied).toEqual(['BLOCK for read'])
  })

  it('does not reuse a broader block when a scope was cleared while it was being made', async () => {
    scopeBox('send').checked = true
    document.getElementById('copy').click()
    expect(tokenRequests).toHaveLength(1)
    expect(tokenRequests[0].scopes).toEqual(['read', 'send'])

    // The user clears "send" while the token is being created.
    scopeBox('send').checked = false
    tokenRequests[0].resolve()
    await flush()
    await flush()
    expect(copied).not.toContain('BLOCK for read,send')

    // The next Copy makes a read-only block instead of copying the old one.
    document.getElementById('copy').click()
    expect(tokenRequests).toHaveLength(2)
    expect(tokenRequests[1].scopes).toEqual(['read'])
    tokenRequests[1].resolve()
    await flush()
    await flush()
    expect(copied).toEqual(['BLOCK for read'])
  })
})
