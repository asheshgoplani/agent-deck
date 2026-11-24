// Review of PR 3245: xterm loads on demand; the effect must not build a
// terminal or open a socket for a panel that was unmounted / replaced while
// the dynamic import was pending, and a failed load must be visible.
import { describe, it, expect, vi, beforeEach } from 'vitest'

const h = vi.hoisted(() => ({ gate: null, release: null, fail: false, terms: [], ws: [] }))

const factory = async () => {
  await h.gate
  if (h.fail) throw new Error('Failed to fetch dynamically imported module')
  class Terminal {
    constructor() {
      this.disposed = false; this.cols = 80; this.rows = 24; h.terms.push(this)
      // any other xterm API the panel touches is a harmless no-op disposable
      return new Proxy(this, { get: (t, k) => (k in t ? t[k] : () => ({ dispose() {} })) })
    }
    loadAddon() {} open() {} focus() {} write() {} onData() { return { dispose() {} } }
    onResize() { return { dispose() {} } } attachCustomKeyEventHandler() {}
    registerLinkProvider() { return { dispose() {} } }
    dispose() { this.disposed = true }
    get parser() { return { registerOscHandler: () => ({ dispose() {} }) } }
    get options() { return {} } set options(_) {}
  }
  // vitest.config aliases all three @xterm/* specifiers to one stub file, so one mock serves all.
  return { Terminal, FitAddon: class { fit() {} }, WebglAddon: class { onContextLoss() {} dispose() {} } }
}

describe('TerminalPanel lazy xterm load', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.doMock('@xterm/xterm', factory) // re-register so every test re-awaits its own gate
    h.terms.length = 0; h.ws.length = 0; h.fail = false
    h.gate = new Promise(r => { h.release = r })
    globalThis.WebSocket = class {
      static OPEN = 1
      constructor(u) { this.url = u; this.readyState = 0; h.ws.push(this) }
      addEventListener() {} close() {} send() {}
    }
  })

  async function mount(id) {
    const { render, h: ph } = await import('preact')
    const state = await import('../../../internal/web/static/app/state.js')
    const { TerminalPanel } = await import('../../../internal/web/static/app/TerminalPanel.js')
    state.sessionsSignal.value = [{ id: 'a', title: 'a' }, { id: 'b', title: 'b' }]
    state.selectedIdSignal.value = id
    const root = document.createElement('div'); document.body.appendChild(root)
    render(ph(TerminalPanel, {}), root)
    return { root, render, ph, TerminalPanel, state }
  }
  const tick = () => new Promise(r => setTimeout(r, 20))

  it('unmount during load builds no terminal and opens no socket', async () => {
    const m = await mount('a'); await tick()
    m.render(null, m.root)
    h.release(); await tick()
    expect(h.terms).toHaveLength(0)
    expect(h.ws).toHaveLength(0)
  })

  it('switching sessions while loading yields exactly one terminal, for the newest', async () => {
    const m = await mount('a'); await tick()
    m.state.selectedIdSignal.value = 'b'; await tick()
    h.release(); await tick()
    expect(h.terms).toHaveLength(1)
    expect(h.ws).toHaveLength(1)
    expect(h.ws[0].url).toContain('/b')
  })

  it('a failed chunk load shows an error to the user, not a blank panel', async () => {
    h.fail = true
    const m = await mount('a'); h.release(); await tick(); await tick()
    expect(m.root.textContent).toMatch(/fail|error|retry/i)
  })

  it('Retry after a failed chunk load re-fetches and builds the terminal', async () => {
    h.fail = true
    const m = await mount('a'); h.release(); await tick(); await tick()
    const retry = [...m.root.querySelectorAll('button')].find(b => /retry/i.test(b.textContent))
    expect(retry).toBeTruthy()
    h.fail = false
    retry.click(); await tick(); await tick()
    expect(h.terms).toHaveLength(1)
    expect(m.root.textContent).not.toMatch(/failed to load/i)
  })
})
