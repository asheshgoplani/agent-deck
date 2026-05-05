// panes/SearchPane.js -- Local session search.
//
// The TUI exposes "global search" (G key) across profiles. The web API
// does not have a search endpoint yet, so this pane filters the in-memory
// session list (titles + paths + tools). Adding cross-profile search would
// require a new endpoint per the parity matrix; flagged for follow-up.
import { html } from 'htm/preact'
import { useState, useMemo } from 'preact/hooks'
import { menuModelSignal } from '../dataModel.js'
import { selectedIdSignal } from '../state.js'
import { activeTabSignal } from '../uiState.js'

export function SearchPane() {
  const { sessions } = menuModelSignal.value
  const [q, setQ] = useState('')

  const filtered = useMemo(() => {
    if (!q) return sessions
    const t = q.toLowerCase()
    return sessions.filter(s =>
      ((s.title || '') + ' ' + (s.path || '') + ' ' + (s.tool || '') + ' ' + (s.group || ''))
        .toLowerCase().includes(t)
    )
  }, [sessions, q])

  const onSelect = (id) => {
    selectedIdSignal.value = id
    activeTabSignal.value = 'terminal'
  }

  return html`
    <div class="search-wrap">
      <div class="field">
        <label>SESSION SEARCH</label>
        <input autofocus placeholder="Search sessions by title, path, tool, group…"
               value=${q} onInput=${e => setQ(e.target.value)}/>
      </div>
      <div style="font-family: var(--mono); font-size: 10.5px; color: var(--muted); letter-spacing: 0.08em;">
        ${filtered.length} MATCH${filtered.length === 1 ? '' : 'ES'} · cross-profile search not exposed via web API
      </div>
      ${filtered.map(s => html`
        <div key=${s.id} class="sr" onClick=${() => onSelect(s.id)}>
          <div class="sr-h">
            <span class="s">${s.title}</span>
            <span class="w">${s.tool || '—'} · ${s.status}</span>
          </div>
          <div class="sr-b">${s.path || s.group || ''}</div>
        </div>
      `)}
    </div>
  `
}
