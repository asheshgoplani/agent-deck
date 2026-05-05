// CommandPalette.js -- Cmd+K / Ctrl+K palette for tab + session navigation.
//
// Bundle's `.cmdk` class. Sections: COMMANDS (tab navigation, new session,
// open settings), SESSIONS (jump to session by name). Esc to close.
import { html } from 'htm/preact'
import { useState, useEffect, useMemo, useRef } from 'preact/hooks'
import { Icon, ICONS } from './icons.js'
import { menuModelSignal } from './dataModel.js'
import { selectedIdSignal, createSessionDialogSignal, infoDrawerOpenSignal, mutationsEnabledSignal } from './state.js'
import { paletteOpenSignal, activeTabSignal, tweaksOpenSignal } from './uiState.js'

export function CommandPalette() {
  const open = paletteOpenSignal.value
  const [q, setQ] = useState('')
  const inputRef = useRef(null)

  useEffect(() => {
    if (!open) return
    setQ('')
    setTimeout(() => inputRef.current?.focus(), 0)
  }, [open])

  if (!open) return null

  const close = () => (paletteOpenSignal.value = false)
  const { sessions } = menuModelSignal.value

  const cmds = useMemo(() => {
    const list = [
      { id: 'cmd-fleet',     sec: 'COMMANDS', label: 'Open Fleet',     tool: '▦', run: () => { activeTabSignal.value = 'fleet'; close() } },
      { id: 'cmd-terminal',  sec: 'COMMANDS', label: 'Open Terminal',  tool: '›_', run: () => { activeTabSignal.value = 'terminal'; close() } },
      { id: 'cmd-costs',     sec: 'COMMANDS', label: 'Costs dashboard', tool: '$', run: () => { activeTabSignal.value = 'costs'; close() } },
      { id: 'cmd-search',    sec: 'COMMANDS', label: 'Session search', tool: '/', run: () => { activeTabSignal.value = 'search'; close() } },
      { id: 'cmd-tweaks',    sec: 'COMMANDS', label: 'Open Tweaks',    tool: '?', run: () => { tweaksOpenSignal.value = true; close() } },
      { id: 'cmd-settings',  sec: 'COMMANDS', label: 'Settings drawer', tool: 'S', run: () => { infoDrawerOpenSignal.value = true; close() } },
    ]
    if (mutationsEnabledSignal.value) {
      list.unshift({ id: 'cmd-new', sec: 'COMMANDS', label: 'New session', tool: 'n', run: () => { createSessionDialogSignal.value = true; close() } })
    }
    return list
  }, [])

  const sessRows = sessions.map(s => ({
    id: s.id,
    sec: 'SESSIONS',
    label: s.title,
    tool: s.tool || s.kind,
    run: () => { selectedIdSignal.value = s.id; activeTabSignal.value = 'terminal'; close() },
  }))

  const all = [...cmds, ...sessRows].filter(r => !q || r.label.toLowerCase().includes(q.toLowerCase()))
  const sections = {}
  all.forEach(r => { (sections[r.sec] ||= []).push(r) })

  return html`
    <div class="overlay" onClick=${close}>
      <div class="cmdk" onClick=${e => e.stopPropagation()}>
        <div class="inp">
          <${Icon} d=${ICONS.search}/>
          <input ref=${inputRef} value=${q} onInput=${e => setQ(e.target.value)}
                 placeholder="Type a command or session name…"
                 onKeyDown=${e => { if (e.key === 'Escape') close() }}/>
          <span class="kbd">esc</span>
        </div>
        <div class="list">
          ${Object.entries(sections).map(([name, rows]) => html`
            <div key=${name}>
              <div class="sec">${name}</div>
              ${rows.map((r, i) => html`
                <div key=${r.id} class=${`row ${i === 0 && name === Object.keys(sections)[0] ? 'f' : ''}`} onClick=${r.run}>
                  <span>${r.label}</span>
                  <span class="tool">${r.tool || ''}</span>
                </div>
              `)}
            </div>
          `)}
          ${all.length === 0 && html`
            <div style="padding: 16px; font-family: var(--mono); font-size: 12px; color: var(--muted); text-align: center;">
              No matches.
            </div>
          `}
        </div>
      </div>
    </div>
  `
}
