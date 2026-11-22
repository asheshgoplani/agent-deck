import { h, options, render } from 'preact'
import { fireEvent, screen } from '@testing-library/preact'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { SessionChat } from '../../../internal/web/static/app/SessionChat.js'

const user = id => ({ id, kind: 'user', body: id })
const page = (ids, extra = {}) => ({
  turns: ids.map(user), through_cursor: 'end', before_cursor: 'start', ...extra,
})
const reply = (data, status = 200) => fetch.mockResolvedValueOnce({ ok: status === 200, status, json: async () => data })
const view = (id = 'one', active = true, remoteName) => h(SessionChat, { key: id, sessionId: id, active, remoteName })
const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms)
const urls = () => fetch.mock.calls.map(([url]) => url)

let root
const mount = node => render(node, root)
const originalRAF = options.requestAnimationFrame
beforeEach(() => {
  root = document.body.appendChild(document.createElement('div'))
  options.requestAnimationFrame = callback => setTimeout(callback, 0)
  vi.useFakeTimers()
  vi.stubGlobal('fetch', vi.fn())
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
})
afterEach(() => {
  render(null, root)
  root.remove()
  options.requestAnimationFrame = originalRAF
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

it('tails once, applies since updates and removals, then advances the cursor', async () => {
  reply(page(['first', 'removed']))
  mount(view())
  await tick()
  expect(screen.getByText('first')).toBeVisible()
  reply(page(['new'], { updates: [{ id: 'first', body: 'updated' }], removed: ['removed'], through_cursor: 'next' }))
  await tick(2000)
  expect(screen.getByText('updated')).toBeVisible()
  expect(screen.queryByText('removed')).toBeNull()
  expect(screen.getByText('new')).toBeVisible()
  reply(page([]))
  await tick(2000)
  expect(urls()).toEqual(['tail=60', 'since=end', 'since=next'].map(q => '/api/sessions/one/timeline?' + q))
})

it('pauses when hidden or inactive and releases requests on unmount', async () => {
  reply(page(['first']))
  mount(view())
  await tick()
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
  fireEvent(document, new Event('visibilitychange'))
  await tick(6000)
  mount(view('one', false))
  visibility.mockReturnValue('visible')
  fireEvent(document, new Event('visibilitychange'))
  await tick(6000)
  expect(fetch).toHaveBeenCalledTimes(1)
  fetch.mockImplementation(() => new Promise(() => {}))
  mount(view())
  await tick(6000)
  expect(fetch).toHaveBeenCalledTimes(2)
  const signal = fetch.mock.calls[1][1].signal
  mount(null)
  expect(signal.aborted).toBe(true)
  await tick(6000)
  expect(fetch).toHaveBeenCalledTimes(2)
})

it('prepends history, preserves the scroll offset and keeps the forward cursor', async () => {
  reply(page(['middle']))
  mount(view())
  await tick()
  const list = screen.getByRole('region', { name: 'Chat history' })
  Object.defineProperty(list, 'scrollHeight', { get: () => list.textContent.includes('older') ? 500 : 300 })
  list.scrollTop = 0
  reply(page(['older'], { before_cursor: '', through_cursor: 'old-end' }))
  fireEvent.scroll(list)
  await tick()
  expect(list.textContent.indexOf('older')).toBeLessThan(list.textContent.indexOf('middle'))
  expect(list.scrollTop).toBe(200)
  expect(urls()[1]).toContain('?before=start&limit=60')
  reply(page(['newest']))
  list.scrollTop = 12
  await tick(2000)
  expect(urls()[2]).toContain('?since=end')
  expect(list.scrollTop).toBe(12)
})

it('resyncs stale cursors from the tail', async () => {
  reply(page(['stale']))
  mount(view())
  await tick()
  reply({ error: { code: 'RESYNC_REQUIRED' } }, 409)
  reply(page(['replacement']))
  await tick(2000)
  expect(urls().at(-1)).toContain('?tail=60')
  expect(screen.queryByText('stale')).toBeNull()
  expect(screen.getByText('replacement')).toBeVisible()
})

it.each([[503, 'RECALL_DISABLED', /\[recall\] enabled/i], [501, 'NOT_IMPLEMENTED', /use Terminal/i], [400, 'INVALID_REQUEST', /use Terminal/i]])(
  'explains permanent HTTP %i errors', async (status, code, notice) => {
    reply({ error: { code } }, status)
    mount(view())
    await tick(4000)
    expect(screen.getByText(notice)).toBeVisible()
    expect(fetch).toHaveBeenCalledTimes(1)
  })

it('retries a transient remote failure without losing the transcript', async () => {
  reply(page(['keep']))
  mount(view('one', true, 'remote box'))
  await tick()
  reply({ error: { code: 'REMOTE_OFFLINE' } }, 503)
  await tick(2000)
  expect(screen.getByText('keep')).toBeVisible()
  reply(page(['later']))
  await tick(4000)
  expect(screen.getByText('later')).toBeVisible()
  expect(urls()).toEqual(['tail=60', 'since=end', 'since=end'].map(q => '/api/remotes/remote%20box/sessions/one/timeline?' + q))
})

it('switches sessions without applying a late reply from the old one', async () => {
  let finish
  fetch.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  mount(view())
  await tick()
  reply(page(['second']))
  mount(view('two'))
  finish({ ok: true, json: async () => page(['late']) })
  await tick()
  expect(urls()[1]).toBe('/api/sessions/two/timeline?tail=60')
  expect(screen.queryByText('late')).toBeNull()
  expect(screen.getByText('second')).toBeVisible()
})

it('follows new rows when the reader is near the bottom', async () => {
  reply(page(['first']))
  mount(view())
  await tick()
  const list = screen.getByRole('region', { name: 'Chat history' })
  Object.defineProperty(list, 'clientHeight', { value: 100 })
  Object.defineProperty(list, 'scrollHeight', { get: () => list.textContent.includes('second') ? 400 : 300 })
  list.scrollTop = 190
  reply(page(['second']))
  await tick(2000)
  expect(list.scrollTop).toBe(400)
})

it('shows a plain tool summary until the tool has output', async () => {
  reply(page([], { turns: [{ id: 'tool', kind: 'bash', title: 'Run tests', summary: 'Started', body: '' }] }))
  mount(view())
  await tick()
  expect(screen.getByText('Run tests · Started')).toBeVisible()
  expect(root.querySelector('details, pre')).toBeNull()

  reply(page([], { updates: [{ id: 'tool', body: 'Tests passed' }] }))
  await tick(2000)
  expect(root.querySelector('details > summary')).toHaveTextContent('Run tests · Started')
  expect(root.querySelector('details > pre')).toHaveTextContent('Tests passed')
})

it('backs off failed polls up to 30 s and resets after a good read', async () => {
  reply(page(['first']))
  mount(view())
  await tick()
  fetch.mockRejectedValue(new Error('offline'))
  await tick(2000)
  expect(fetch).toHaveBeenCalledTimes(2)
  await tick(3999)
  expect(fetch).toHaveBeenCalledTimes(2)
  await tick(1)
  expect(fetch).toHaveBeenCalledTimes(3)
  await tick(8000 + 16000 + 30000)
  expect(fetch).toHaveBeenCalledTimes(6)
  await tick(29999)
  expect(fetch).toHaveBeenCalledTimes(6)
  fetch.mockReset()
  reply({ error: { code: 'INTERNAL_ERROR' } }, 500)
  reply(page([]))
  await tick(1)
  expect(fetch).toHaveBeenCalledTimes(1)
  await tick(30000)
  expect(fetch).toHaveBeenCalledTimes(2)
  reply(page([]))
  await tick(2000)
  expect(fetch).toHaveBeenCalledTimes(3)
})

it('stops polling with a notice on 401 or 403', async () => {
  for (const status of [401, 403]) {
    fetch.mockReset()
    reply({ error: { code: 'UNAUTHORIZED' } }, status)
    mount(view('s' + status))
    await tick()
    expect(screen.getByRole('status').textContent).toContain('Not authorized')
    await tick(60000)
    expect(fetch).toHaveBeenCalledTimes(1)
  }
})

it('loads older rows while the first page is shorter than the pane', async () => {
  const sizes = { clientHeight: 400, scrollHeight: 100 }
  for (const key of Object.keys(sizes)) {
    Object.defineProperty(HTMLElement.prototype, key, { configurable: true, get() { return this.classList.contains('session-chat') ? sizes[key] : 0 } })
  }
  try {
    reply(page(['recent']))
    reply(page(['older'], { before_cursor: '' }))
    mount(view())
    await tick()
    await tick()
    expect(urls()[1]).toContain('?before=start&limit=60')
    expect(screen.getByText('older')).toBeVisible()
    reply(page([]))
    await tick(2000)
    expect(urls()).toHaveLength(3)
    expect(urls()[2]).toContain('?since=end')
  } finally {
    for (const key of Object.keys(sizes)) delete HTMLElement.prototype[key]
  }
})

it('goes back to since polls after a failed page of older rows', async () => {
  reply(page(['middle']))
  mount(view())
  await tick()
  const list = screen.getByRole('region', { name: 'Chat history' })
  list.scrollTop = 0
  reply({ error: { code: 'INVALID_CURSOR' } }, 400)
  fireEvent.scroll(list)
  await tick()
  expect(urls()[1]).toContain('?before=start&limit=60')
  reply(page(['newest']))
  await tick(4000)
  expect(urls()[2]).toContain('?since=end')
  expect(screen.getByText('newest')).toBeVisible()
})
