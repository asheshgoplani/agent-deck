// Collapse state is shared with the TUI.
//
// Before this, groupExpandedSignal was localStorage-only and uiState.js said
// so outright: "The server's own `expanded` is deliberately NOT honored —
// nothing can write it back." PATCH /api/groups/{path} {expanded} closes that
// loop, so the browser now both adopts the server's state and pushes its own.
//
// The subtle part is reconciliation. The menu stream is change-driven, but a
// snapshot triggered by an unrelated change (a session going idle, a cost
// update) can still carry the pre-toggle `expanded` for the group the user
// just clicked — so a naive "always adopt the snapshot" would flip the chevron
// back under the cursor.
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../../internal/web/static/app/toasts.js', () => ({ addToast: vi.fn() }))

const stateModulePath = '../../../internal/web/static/app/state.js'
const uiStateModulePath = '../../../internal/web/static/app/uiState.js'
const dataModelModulePath = '../../../internal/web/static/app/dataModel.js'

const group = (name, path, expanded) => ({
  type: 'group',
  level: path.split('/').length - 1,
  group: { name, path, expanded, order: 0 },
})

const okFetch = () => vi.fn().mockResolvedValue({ ok: true, json: async () => ({ ok: true }) })

async function reset({ mutations = true } = {}) {
  const { sessionsSignal, sessionCostsSignal, mutationsEnabledSignal, authTokenSignal } = await import(stateModulePath)
  const { sidebarFilterSignal, groupExpandedSignal, statusFiltersSignal } = await import(uiStateModulePath)
  sessionCostsSignal.value = {}
  sidebarFilterSignal.value = ''
  statusFiltersSignal.value = []
  groupExpandedSignal.value = {}
  mutationsEnabledSignal.value = mutations
  authTokenSignal.value = ''
  sessionsSignal.value = []
  return { sessionsSignal, groupExpandedSignal, mutationsEnabledSignal }
}

describe('adopting the server collapse state', () => {
  beforeEach(() => reset())

  it('collapses a group the TUI collapsed', async () => {
    const { sessionsSignal, groupExpandedSignal } = await reset()
    const { isGroupOpen } = await import(dataModelModulePath)

    sessionsSignal.value = [group('stride', 'stride', false), group('redwood', 'redwood', true)]

    expect(isGroupOpen(groupExpandedSignal.value, 'stride')).toBe(false)
    expect(isGroupOpen(groupExpandedSignal.value, 'redwood')).toBe(true)
  })

  it('reopens a group the TUI reopened', async () => {
    const { sessionsSignal, groupExpandedSignal } = await reset()
    const { isGroupOpen } = await import(dataModelModulePath)

    sessionsSignal.value = [group('stride', 'stride', false)]
    expect(isGroupOpen(groupExpandedSignal.value, 'stride')).toBe(false)

    sessionsSignal.value = [group('stride', 'stride', true)]
    expect(isGroupOpen(groupExpandedSignal.value, 'stride')).toBe(true)
  })

  // Only closures are stored, so the map stays small and a group deleted in
  // the TUI cannot linger in localStorage forever.
  it('stores only closures, never explicit opens', async () => {
    const { sessionsSignal, groupExpandedSignal } = await reset()
    await import(dataModelModulePath)

    sessionsSignal.value = [group('stride', 'stride', false), group('redwood', 'redwood', true)]

    expect(groupExpandedSignal.value).toEqual({ stride: false })
  })

  // Rows this module invents for a parent the wire never sent carry a
  // placeholder `expanded: true` that is not server truth.
  it('ignores client-synthesized ancestor rows', async () => {
    const { sessionsSignal, groupExpandedSignal } = await reset()
    await import(dataModelModulePath)

    // 'stride' is never sent; only the child is. The parent row is synthesized.
    sessionsSignal.value = [group('ws1', 'stride/ws1', false)]

    expect(groupExpandedSignal.value).toEqual({ 'stride/ws1': false })
  })
})

describe('pushing a collapse back to the server', () => {
  beforeEach(() => reset())

  it('PATCHes the group with its new state', async () => {
    const { sessionsSignal } = await reset()
    const { toggleGroupOpen } = await import(dataModelModulePath)
    sessionsSignal.value = [group('stride', 'stride', true)]

    const fetchMock = okFetch()
    vi.stubGlobal('fetch', fetchMock)

    await toggleGroupOpen('stride')

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/groups/stride')
    expect(init.method).toBe('PATCH')
    expect(JSON.parse(init.body)).toEqual({ expanded: false })
  })

  // '/' separates real path segments and the handler splits on it, so it must
  // survive unescaped; anything else in a segment must not.
  it('encodes each path segment but keeps the separators', async () => {
    const { sessionsSignal } = await reset()
    const { toggleGroupOpen } = await import(dataModelModulePath)
    sessionsSignal.value = [group('my ws', 'stride/my ws', true)]

    const fetchMock = okFetch()
    vi.stubGlobal('fetch', fetchMock)

    await toggleGroupOpen('stride/my ws')

    expect(fetchMock.mock.calls[0][0]).toBe('/api/groups/stride/my%20ws')
  })

  it('applies optimistically, before the request settles', async () => {
    const { sessionsSignal, groupExpandedSignal } = await reset()
    const { toggleGroupOpen, isGroupOpen } = await import(dataModelModulePath)
    sessionsSignal.value = [group('stride', 'stride', true)]

    let release
    vi.stubGlobal('fetch', vi.fn(() => new Promise((res) => {
      release = () => res({ ok: true, json: async () => ({ ok: true }) })
    })))

    const inflight = toggleGroupOpen('stride')
    expect(isGroupOpen(groupExpandedSignal.value, 'stride')).toBe(false)
    release()
    await inflight
  })

  // A read-only server has nothing to persist to; the flip must still work AND
  // survive. Asserting only the immediate flip hid a real bug: with no write
  // there is no pending guard, so the next snapshot re-adopted the server's
  // expanded:true and sprang the group back open within ~2s.
  it('stays local, and survives later snapshots, when mutations are disabled', async () => {
    const { sessionsSignal, groupExpandedSignal } = await reset({ mutations: false })
    const { toggleGroupOpen, isGroupOpen } = await import(dataModelModulePath)
    sessionsSignal.value = [group('ro1', 'ro1', true)]

    const fetchMock = okFetch()
    vi.stubGlobal('fetch', fetchMock)

    await toggleGroupOpen('ro1')
    expect(fetchMock).not.toHaveBeenCalled()
    expect(isGroupOpen(groupExpandedSignal.value, 'ro1')).toBe(false)

    // An unrelated change pushes a fresh snapshot still reporting expanded:true.
    sessionsSignal.value = [group('ro1', 'ro1', true), group('ro2', 'ro2', true)]
    expect(isGroupOpen(groupExpandedSignal.value, 'ro1')).toBe(false)
  })

  // Only the paths the viewer actually touched are pinned; everything else
  // still inherits the TUI's collapse state.
  it('still adopts server state for untouched groups on a read-only server', async () => {
    const { sessionsSignal, groupExpandedSignal } = await reset({ mutations: false })
    const { toggleGroupOpen, isGroupOpen } = await import(dataModelModulePath)
    sessionsSignal.value = [group('ro3', 'ro3', true), group('ro4', 'ro4', true)]

    vi.stubGlobal('fetch', okFetch())
    await toggleGroupOpen('ro3')

    sessionsSignal.value = [group('ro3', 'ro3', true), group('ro4', 'ro4', false)]
    expect(isGroupOpen(groupExpandedSignal.value, 'ro3')).toBe(false)  // ours, pinned
    expect(isGroupOpen(groupExpandedSignal.value, 'ro4')).toBe(false)  // theirs, adopted
  })
})

describe('reconciliation races', () => {
  beforeEach(() => reset())

  // The crux: a snapshot for an UNRELATED change lands while the collapse is
  // still in flight, still carrying expanded:true. It must not win.
  it('does not let an in-flight snapshot revert the pending toggle', async () => {
    const { sessionsSignal, groupExpandedSignal } = await reset()
    const { toggleGroupOpen, isGroupOpen } = await import(dataModelModulePath)
    sessionsSignal.value = [group('stride', 'stride', true)]

    let release
    vi.stubGlobal('fetch', vi.fn(() => new Promise((res) => {
      release = () => res({ ok: true, json: async () => ({ ok: true }) })
    })))

    const inflight = toggleGroupOpen('stride')

    // Stale snapshot arrives mid-write: same group, pre-toggle state.
    sessionsSignal.value = [group('stride', 'stride', true), group('redwood', 'redwood', true)]
    expect(isGroupOpen(groupExpandedSignal.value, 'stride')).toBe(false)

    // The write lands, and the server's next snapshot agrees.
    release()
    await inflight
    sessionsSignal.value = [group('stride', 'stride', false), group('redwood', 'redwood', true)]
    expect(isGroupOpen(groupExpandedSignal.value, 'stride')).toBe(false)
  })

  // A FAILED write rolls back: the server never took the value, so the
  // optimistic flip has to go.
  it('reverts the optimistic flip when the write fails', async () => {
    const { sessionsSignal, groupExpandedSignal } = await reset()
    const { toggleGroupOpen, isGroupOpen } = await import(dataModelModulePath)
    sessionsSignal.value = [group('fail1', 'fail1', true)]

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: false,
      statusText: 'Not Found',
      json: async () => ({ error: { message: 'group not found' } }),
    }))

    await toggleGroupOpen('fail1')

    expect(isGroupOpen(groupExpandedSignal.value, 'fail1')).toBe(true)
  })

  // ...and a SUCCESSFUL write must NOT. The PATCH response beats the SSE
  // fan-out almost every time, so at this moment menuModelSignal still holds
  // the pre-toggle snapshot; reconciling against it flipped the chevron back
  // under the user until the real snapshot arrived. The previous version of
  // the failure test above passed identically with ok:true, so it could not
  // tell these two paths apart at all.
  it('keeps the flip after a successful write, even with no snapshot yet', async () => {
    const { sessionsSignal, groupExpandedSignal } = await reset()
    const { toggleGroupOpen, isGroupOpen } = await import(dataModelModulePath)
    sessionsSignal.value = [group('okw', 'okw', true)]

    vi.stubGlobal('fetch', okFetch())

    await toggleGroupOpen('okw')

    // Still the pre-toggle snapshot in hand — the flip must survive it.
    expect(isGroupOpen(groupExpandedSignal.value, 'okw')).toBe(false)

    // And the authoritative snapshot, when it lands, simply agrees.
    sessionsSignal.value = [group('okw', 'okw', false)]
    expect(isGroupOpen(groupExpandedSignal.value, 'okw')).toBe(false)
  })

  it('leaves other groups reconciling normally while one is pending', async () => {
    const { sessionsSignal, groupExpandedSignal } = await reset()
    const { toggleGroupOpen, isGroupOpen } = await import(dataModelModulePath)
    sessionsSignal.value = [group('stride', 'stride', true), group('redwood', 'redwood', true)]

    let release
    vi.stubGlobal('fetch', vi.fn(() => new Promise((res) => {
      release = () => res({ ok: true, json: async () => ({ ok: true }) })
    })))

    const inflight = toggleGroupOpen('stride')

    // The TUI collapses redwood while stride's write is still open.
    sessionsSignal.value = [group('stride', 'stride', true), group('redwood', 'redwood', false)]

    expect(isGroupOpen(groupExpandedSignal.value, 'stride')).toBe(false)  // pending, held
    expect(isGroupOpen(groupExpandedSignal.value, 'redwood')).toBe(false) // adopted

    release()
    await inflight
  })
})

// Collapse looks like a cheap click but is an expensive write: SaveGroupsOnly
// upserts every group in the tree, and each PATCH fans a full menu snapshot
// out to every connected browser. One request per keypress would also outrun
// the server's 20/s mutation limiter on a held Tab — and a 429 reverts the
// optimistic flip, so the chevron would visibly flip-flop.
//
// pendingGroupWrites is module state, so each test below uses its own group
// path rather than relying on the previous one having drained cleanly.
describe('coalescing rapid toggles', () => {
  beforeEach(() => reset())

  // apiFetch awaits both fetch() and res.json(), so the continuation is
  // several microtask hops deep. A macrotask tick drains all of them.
  const drain = () => new Promise((r) => setTimeout(r, 0))

  const deferredFetch = () => {
    const releases = []
    const mock = vi.fn(() => new Promise((res) => {
      releases.push(() => res({ ok: true, json: async () => ({ ok: true }) }))
    }))
    vi.stubGlobal('fetch', mock)
    return { mock, releases }
  }

  it('keeps at most one request per path in flight', async () => {
    const { sessionsSignal } = await reset()
    const { toggleGroupOpen } = await import(dataModelModulePath)
    sessionsSignal.value = [group('ca', 'ca', true)]
    const { mock, releases } = deferredFetch()

    const inflight = toggleGroupOpen('ca')   // -> false, sends
    toggleGroupOpen('ca')                    // -> true,  coalesced
    toggleGroupOpen('ca')                    // -> false, coalesced
    expect(mock).toHaveBeenCalledTimes(1)

    releases[0]()
    await inflight
    // Final state matched what was already sent, so no follow-up was needed.
    expect(mock).toHaveBeenCalledTimes(1)
  })

  it('sends a follow-up when the user moved on mid-flight', async () => {
    const { sessionsSignal } = await reset()
    const { toggleGroupOpen } = await import(dataModelModulePath)
    sessionsSignal.value = [group('cb', 'cb', true)]
    const { mock, releases } = deferredFetch()

    const inflight = toggleGroupOpen('cb')   // -> false, sends
    toggleGroupOpen('cb')                    // -> true,  queued behind it
    expect(JSON.parse(mock.mock.calls[0][1].body)).toEqual({ expanded: false })

    releases[0]()
    await drain()

    // The queued state goes out rather than leaving the server stale.
    expect(mock).toHaveBeenCalledTimes(2)
    expect(JSON.parse(mock.mock.calls[1][1].body)).toEqual({ expanded: true })

    releases[1]()
    await inflight
  })

  it('does not serialize writes for different groups', async () => {
    const { sessionsSignal } = await reset()
    const { toggleGroupOpen } = await import(dataModelModulePath)
    sessionsSignal.value = [group('cc1', 'cc1', true), group('cc2', 'cc2', true)]
    const { mock, releases } = deferredFetch()

    const a = toggleGroupOpen('cc1')
    const b = toggleGroupOpen('cc2')
    expect(mock).toHaveBeenCalledTimes(2)

    releases.forEach((r) => r())
    await Promise.all([a, b])
  })
})
