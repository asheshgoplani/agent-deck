// unit/terminalReconnect.test.js -- a halted web terminal reattaches (#2432).
//
// TMUX_SESSION_NOT_FOUND stops TerminalPanel's reconnect loop and shows the
// #782 banner. Before #2432 only the banner's own Restart button rebuilt the
// connection, so starting the session from the header, the sidebar or the
// CLI left the open terminal on "Terminal disconnected" until the viewer
// switched sessions and back. These pin the pure decision in
// terminalReconnect.js; the rendered terminal (banner hit testing, header
// Start, a CLI start seen over SSE) is pinned in a real browser by
// e2e/terminal-restart-in-place.spec.js.

import { describe, it, expect } from 'vitest'

const APP = '../../../internal/web/static/app/'
const reconnect = () => import(APP + 'terminalReconnect.js')

const sessionItem = (id, status) => ({ type: 'session', session: { id, status } })

describe('terminalReconnect: pure decisions', () => {
  it('treats running, waiting and idle as live; starting, stopped and error as not', async () => {
    const { isLiveStatus } = await reconnect()
    for (const s of ['running', 'waiting', 'idle']) expect(isLiveStatus(s)).toBe(true)
    for (const s of ['starting', 'stopped', 'error', 'queued', '', undefined]) expect(isLiveStatus(s)).toBe(false)
  })

  it('reads the status of one session out of the raw SSE items', async () => {
    const { sessionStatus } = await reconnect()
    const items = [{ type: 'group', group: { path: 'g' } }, sessionItem('a', 'error'), sessionItem('b', 'running')]
    expect(sessionStatus(items, 'b')).toBe('running')
    expect(sessionStatus(items, 'a')).toBe('error')
    expect(sessionStatus(items, 'missing')).toBe('')
    expect(sessionStatus(null, 'a')).toBe('')
  })

  it('a start for another session does not count as a start for this one', async () => {
    const { observeSession } = await reconnect()
    const prev = observeSession([sessionItem('a', 'error')], { id: 'a', seq: 3 }, 'a', null)
    expect(prev).toEqual({ status: 'error', started: 3 })
    const other = observeSession([sessionItem('a', 'error')], { id: 'b', seq: 4 }, 'a', prev)
    expect(other.started).toBe(3)
    const mine = observeSession([sessionItem('a', 'error')], { id: 'a', seq: 5 }, 'a', other)
    expect(mine.started).toBe(5)
  })

  it('reattaches a halted terminal on a turn to live or a fresh start, and never otherwise', async () => {
    const { shouldReattach } = await reconnect()
    const obs = (status, started = 0) => ({ status, started })
    // Halted terminal.
    expect(shouldReattach(obs('error'), obs('running'), true)).toBe(true)
    expect(shouldReattach(obs('stopped'), obs('waiting'), true)).toBe(true)
    expect(shouldReattach(obs('starting'), obs('idle'), true)).toBe(true)
    expect(shouldReattach(obs('error'), obs('starting'), true)).toBe(false)
    expect(shouldReattach(obs('running'), obs('waiting'), true)).toBe(false)
    expect(shouldReattach(obs('error'), obs('error'), true)).toBe(false)
    // A start/restart that succeeded counts even when the status did not move.
    expect(shouldReattach(obs('running', 1), obs('running', 2), true)).toBe(true)
    // A healthy terminal is left alone: its own reconnect loop is in charge.
    expect(shouldReattach(obs('error'), obs('running'), false)).toBe(false)
    expect(shouldReattach(obs('running', 1), obs('running', 2), false)).toBe(false)
  })
})

describe('terminalReconnect: one stopped-then-started sequence', () => {
  // Replays the observations TerminalPanel makes while its terminal is
  // halted and counts the reattaches, the way the component's effect does.
  async function replay(steps) {
    const { observeSession, shouldReattach } = await reconnect()
    let prev = observeSession(steps[0].items, steps[0].started, 'a', null)
    let reattaches = 0
    for (const step of steps.slice(1)) {
      const next = observeSession(step.items, step.started, 'a', prev)
      if (shouldReattach(prev, next, step.halted)) reattaches += 1
      prev = next
    }
    return reattaches
  }
  const none = { id: null, seq: 0 }

  it('header Start: stopped -> starting -> running reattaches exactly once', async () => {
    const n = await replay([
      { items: [sessionItem('a', 'stopped')], started: none, halted: true },
      { items: [sessionItem('a', 'starting')], started: { id: 'a', seq: 1 }, halted: true },
      // The first reattach rebuilt the connection; it is not halted now.
      { items: [sessionItem('a', 'running')], started: { id: 'a', seq: 1 }, halted: false },
      { items: [sessionItem('a', 'waiting')], started: { id: 'a', seq: 1 }, halted: false },
    ])
    expect(n).toBe(1)
  })

  it('CLI start: only the status feed moves, and the turn to live reattaches', async () => {
    const n = await replay([
      { items: [sessionItem('a', 'error')], started: none, halted: true },
      { items: [sessionItem('a', 'starting')], started: none, halted: true },
      { items: [sessionItem('a', 'running')], started: none, halted: true },
    ])
    expect(n).toBe(1)
  })

  it('a start that raced tmux and halted again still reattaches when the status turns live', async () => {
    const n = await replay([
      { items: [sessionItem('a', 'stopped')], started: none, halted: true },
      { items: [sessionItem('a', 'starting')], started: { id: 'a', seq: 1 }, halted: true },
      { items: [sessionItem('a', 'running')], started: { id: 'a', seq: 1 }, halted: true },
    ])
    expect(n).toBe(2)
  })

  it('starting a different session never touches this terminal', async () => {
    const n = await replay([
      { items: [sessionItem('a', 'error'), sessionItem('b', 'stopped')], started: none, halted: true },
      { items: [sessionItem('a', 'error'), sessionItem('b', 'running')], started: { id: 'b', seq: 1 }, halted: true },
    ])
    expect(n).toBe(0)
  })
})
