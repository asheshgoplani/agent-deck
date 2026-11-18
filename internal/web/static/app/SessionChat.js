import { html } from 'htm/preact'
import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks'
import { authHeaders } from './api.js'
import { chatRows } from './chatRows.js'
import { renderMarkdown } from './miniMarkdown.js'

function appendChanges(previous, data) {
  const byId = new Map(previous.map(row => [row.id, row]))
  for (const row of data.turns || []) byId.set(row.id, row)
  for (const patch of data.updates || []) {
    const row = byId.get(patch.id)
    if (!row) continue
    if (patch.queued === false && patch.ts) byId.delete(row.id)
    byId.set(row.id, { ...row, ...patch, meta: { ...row.meta, ...patch.meta } })
  }
  for (const id of data.removed || []) byId.delete(id)
  return [...byId.values()]
}

function Bubble({ row }) {
  if (row.type === 'assistant') return html`<div class="cc-md">${renderMarkdown(row.body || '')}</div>`
  if (row.type === 'user') return html`<div class="chat-user">${row.body}</div>`
  if (row.type === 'tool') return html`
    <details class="chat-tool">
      <summary>${[row.title || row.kind, row.summary].filter(Boolean).join(' · ')}</summary>
      <pre>${row.body}</pre>
    </details>`
  const line = (row.title || row.body || row.kind || '').split('\n')[0]
  return html`<div class="chat-note">${line}</div>`
}

export function SessionChat({ sessionId, remoteName, active }) {
  const [rows, setRows] = useState([])
  const [notice, setNotice] = useState('')
  const [visible, setVisible] = useState(document.visibilityState === 'visible')
  const list = useRef(null)
  const scrollAfterRender = useRef(null)
  const cursors = useRef({ through: '', before: '' })
  const prefix = remoteName ? '/api/remotes/' + encodeURIComponent(remoteName) : '/api'
  const path = prefix + '/sessions/' + encodeURIComponent(sessionId) + '/timeline?'

  useEffect(() => {
    const changed = () => setVisible(document.visibilityState === 'visible')
    document.addEventListener('visibilitychange', changed)
    changed()
    return () => document.removeEventListener('visibilitychange', changed)
  }, [])

  useLayoutEffect(() => {
    const restore = scrollAfterRender.current
    if (restore) restore()
    scrollAfterRender.current = null
  }, [rows])

  useEffect(() => {
    if (!active || !visible || notice) return
    const controller = new AbortController()
    const el = list.current
    const cursor = cursors.current
    let busy = false
    let older = false

    async function read() {
      if (busy || controller.signal.aborted || document.visibilityState !== 'visible') return
      busy = true
      const paging = older && !!cursor.before
      const tail = !paging && !cursor.through
      const query = paging ? { before: cursor.before, limit: 60 } : tail ? { tail: 60 } : { since: cursor.through }
      let resync = false
      try {
        const res = await fetch(path + new URLSearchParams(query), {
          headers: authHeaders(), signal: controller.signal, cache: 'no-store',
        })
        const data = await res.json()
        if (controller.signal.aborted) return
        if (!res.ok) {
          if (res.status === 409 && data.error?.code === 'RESYNC_REQUIRED' && !tail) {
            cursor.through = cursor.before = ''
            older = false
            resync = true
          } else if (res.status === 503 && data.error?.code === 'RECALL_DISABLED') {
            setNotice('To read chat, set [recall] enabled = true in config.toml.')
          } else if (res.status === 400 || res.status === 501) {
            setNotice('No chat for this session. Use Terminal.')
          }
          return
        }
        if (tail || paging) cursor.before = data.before_cursor || ''
        if (!paging) cursor.through = data.through_cursor
        const turns = data.turns || []
        if (paging) {
          older = false
          if (!turns.length) return
          const height = el.scrollHeight
          scrollAfterRender.current = () => { el.scrollTop += el.scrollHeight - height }
          setRows(previous => [...turns, ...previous])
        } else if (tail || turns.length || data.updates?.length || data.removed?.length) {
          if (el.scrollHeight - el.scrollTop - el.clientHeight <= 64) {
            scrollAfterRender.current = () => { el.scrollTop = el.scrollHeight }
          }
          setRows(previous => tail ? turns : appendChanges(previous, data))
        }
      } catch {
        // Keep the current page and cursor. The next poll retries this read.
      } finally {
        busy = false
        if (resync) void read()
      }
    }

    function onScroll() {
      if (el.scrollTop > 0 || !cursor.before) return
      older = true
      void read()
    }

    void read()
    const timer = setInterval(read, 2000)
    el.addEventListener('scroll', onScroll)
    return () => {
      clearInterval(timer)
      controller.abort()
      el.removeEventListener('scroll', onScroll)
    }
  }, [active, visible, path, notice])

  return html`<section ref=${list} class="session-chat" aria-label="Chat history">
    ${notice && html`<p role="status">${notice}</p>`}
    ${chatRows(rows).map(row => html`<${Bubble} key=${row.id} row=${row}/>`)}
  </section>`
}
