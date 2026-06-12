// panes/ArchivedPane.js -- Browse archived sessions (stopped, hidden from sidebar).
import { html } from 'htm/preact'
import { useState, useMemo, useEffect } from 'preact/hooks'
import { Dot } from '../icons.js'
import {
  selectedIdSignal, mutationsEnabledSignal, confirmDialogSignal,
} from '../state.js'
import { archivedSessionsSignal, loadArchivedSessions } from '../state.js'
import { apiFetch } from '../api.js'
import { addToast } from '../Toast.js'

function projectArchived(raw) {
  const s = raw || {}
  return {
    id: s.id || '',
    title: s.title || s.id,
    tool: s.tool || '',
    status: s.status || 'idle',
    group: s.groupPath || '',
    path: s.projectPath || '',
    archivedAt: s.archivedAt || null,
  }
}

function formatArchivedAt(iso) {
  if (!iso) return '—'
  try {
    const d = new Date(iso)
    return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
  } catch (_) {
    return String(iso)
  }
}

function clearSelectionIf(id) {
  if (selectedIdSignal.value !== id) return
  selectedIdSignal.value = null
  if (typeof window !== 'undefined' && window.location.pathname.startsWith('/s/')) {
    history.replaceState(null, '', '/')
  }
}

export function ArchivedPane() {
  const raw = archivedSessionsSignal.value || []
  const [q, setQ] = useState('')

  useEffect(() => {
    loadArchivedSessions()
  }, [])

  const sessions = useMemo(() => raw.map(projectArchived), [raw])

  const filtered = useMemo(() => {
    if (!q) return sessions
    const t = q.toLowerCase()
    return sessions.filter(s =>
      ((s.title || '') + ' ' + (s.path || '') + ' ' + (s.tool || '') + ' ' + (s.group || ''))
        .toLowerCase().includes(t)
    )
  }, [sessions, q])

  const unarchive = (s) => {
    if (!mutationsEnabledSignal.value) {
      addToast('mutations disabled')
      return
    }
    apiFetch('POST', `/api/sessions/${s.id}/unarchive`)
      .then(() => {
        clearSelectionIf(s.id)
        loadArchivedSessions()
        addToast(`Unarchived "${s.title}"`, 'success')
      })
      .catch(() => {})
  }

  const remove = (s) => {
    if (!mutationsEnabledSignal.value) {
      addToast('mutations disabled')
      return
    }
    confirmDialogSignal.value = {
      message: `Delete archived session "${s.title}"? This removes it permanently.`,
      onConfirm: () => apiFetch('DELETE', `/api/sessions/${s.id}`)
        .then(() => {
          clearSelectionIf(s.id)
          loadArchivedSessions()
        })
        .catch(() => {}),
    }
  }

  return html`
    <div class="search-wrap archived-wrap">
      <div class="field">
        <label>ARCHIVED SESSIONS</label>
        <input placeholder="Filter by title, path, tool, group…"
               value=${q} onInput=${e => setQ(e.target.value)}/>
      </div>
      <div style="font-family: var(--mono); font-size: 10.5px; color: var(--muted); letter-spacing: 0.08em;">
        ${filtered.length} ARCHIVED · unarchive to return to the active list
      </div>
      ${filtered.length === 0 && html`
        <div class="archived-empty">No archived sessions.</div>
      `}
      ${filtered.map(s => html`
        <div key=${s.id} class="sr archived-row">
          <div class="sr-h">
            <${Dot} status=${s.status}/>
            <span class="s">${s.title}</span>
            <span class="w">${s.tool || '—'} · archived ${formatArchivedAt(s.archivedAt)}</span>
          </div>
          <div class="sr-b">${s.path || s.group || ''}</div>
          <div class="archived-actions" onClick=${e => e.stopPropagation()}>
            <button class="mini good" title="Unarchive" onClick=${() => unarchive(s)}>Unarchive</button>
            <button class="mini danger" title="Delete" onClick=${() => remove(s)}>Delete</button>
          </div>
        </div>
      `)}
    </div>
  `
}
