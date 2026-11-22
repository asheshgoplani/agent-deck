// CommandPalette.js -- Cmd+K / Ctrl+K palette for tab + session navigation.
//
// Bundle's `.cmdk` class. Sections: COMMANDS (tab navigation, new session,
// open settings), SESSIONS (jump to session by name). Esc to close.
import { html } from 'htm/preact'
import { useState, useEffect, useMemo, useRef } from 'preact/hooks'
import { Icon, ICONS } from './icons.js'
import { menuModelSignal, openCreateSessionForGroup, currentGroupPath } from './dataModel.js'
import { selectSession, openTab, infoDrawerOpenSignal, mutationsEnabledSignal, shortcutsOverlaySignal } from './state.js'
import { paletteOpenSignal, activeTabSignal, tweaksOpenSignal } from './uiState.js'

export function CommandPalette() {
  const open = paletteOpenSignal.value
  const [q, setQ] = useState('')
  // Index into the filtered rows of the highlighted row; Enter runs it.
  const [active, setActive] = useState(0)
  const inputRef = useRef(null)
  const listRef = useRef(null)

  useEffect(() => {
    if (!open) return
    setQ('')
    setActive(0)
    setTimeout(() => inputRef.current?.focus(), 0)
  }, [open])

  if (!open) return null

  const close = () => (paletteOpenSignal.value = false)
  const { sessions } = menuModelSignal.value

  const cmds = useMemo(() => {
    const list = [
      { id: 'cmd-fleet',     sec: 'COMMANDS', label: 'Open Fleet',     tool: '▦', run: () => { openTab('fleet'); close() } },
      { id: 'cmd-terminal',  sec: 'COMMANDS', label: 'Open Terminal',  tool: '›_', run: () => { openTab('terminal'); close() } },
      { id: 'cmd-costs',     sec: 'COMMANDS', label: 'Costs dashboard', tool: '$', run: () => { openTab('costs'); close() } },
      { id: 'cmd-search',    sec: 'COMMANDS', label: 'Session search', tool: '/', run: () => { openTab('search'); close() } },
      { id: 'cmd-archived',  sec: 'COMMANDS', label: 'Archived sessions', tool: '^', run: () => { openTab('archived'); close() } },
      { id: 'cmd-tweaks',    sec: 'COMMANDS', label: 'Open Tweaks',    tool: 'T', run: () => { tweaksOpenSignal.value = true; close() } },
      { id: 'cmd-shortcuts', sec: 'COMMANDS', label: 'Keyboard shortcuts', tool: '?', run: () => { shortcutsOverlaySignal.value = true; close() } },
      { id: 'cmd-settings',  sec: 'COMMANDS', label: 'Settings drawer', tool: 'S', run: () => { infoDrawerOpenSignal.value = true; close() } },
    ]
    if (mutationsEnabledSignal.value) {
      list.unshift({ id: 'cmd-new', sec: 'COMMANDS', label: 'New session', tool: 'n', run: () => { openCreateSessionForGroup(currentGroupPath()); close() } })
    }
    return list
  }, [])

  const sessRows = sessions.map(s => ({
    id: s.id,
    sec: 'SESSIONS',
    label: s.title,
    tool: s.tool || s.kind,
    run: () => { selectSession(s.id); activeTabSignal.value = 'terminal'; close() },
  }))

  const all = [...cmds, ...sessRows].filter(r => !q || r.label.toLowerCase().includes(q.toLowerCase()))
  const sections = {}
  all.forEach(r => { (sections[r.sec] ||= []).push(r) })
  // `all` lists COMMANDS before SESSIONS, the same order the sections render
  // in, so a row's index in `all` is its position on screen.
  const cur = Math.min(active, Math.max(0, all.length - 1))
  const onKeyDown = (e) => {
    if (e.key === 'Escape') { close(); return }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      if (all.length === 0) return
      const next = (cur + (e.key === 'ArrowDown' ? 1 : -1) + all.length) % all.length
      setActive(next)
      listRef.current?.querySelector(`[data-idx="${next}"]`)?.scrollIntoView?.({ block: 'nearest' })
      return
    }
    if (e.key === 'Enter' && !e.isComposing) {
      e.preventDefault()
      all[cur]?.run()
    }
  }

  return html`
    <div class="overlay" onClick=${close}>
      <div class="cmdk" data-testid="command-palette" onClick=${e => e.stopPropagation()}>
        <div class="inp">
          <${Icon} d=${ICONS.search}/>
          <input ref=${inputRef} data-testid="palette-input" value=${q} onInput=${e => { setQ(e.target.value); setActive(0) }}
                 placeholder="Type a command or session name…"
                 role="combobox" aria-expanded="true" aria-controls="palette-list"
                 aria-activedescendant=${all[cur] ? `palette-row-${cur}` : undefined}
                 onKeyDown=${onKeyDown}/>
          <span class="kbd">esc</span>
        </div>
        <div class="list" id="palette-list" role="listbox" ref=${listRef}>
          ${Object.entries(sections).map(([name, rows]) => html`
            <div key=${name}>
              <div class="sec">${name}</div>
              ${rows.map(r => {
                const idx = all.indexOf(r)
                return html`
                <div key=${r.id} id=${`palette-row-${idx}`} data-idx=${idx} role="option" aria-selected=${idx === cur ? 'true' : 'false'}
                     data-testid=${r.sec === 'SESSIONS' ? 'palette-session-row' : 'palette-cmd-row'} class=${`row ${idx === cur ? 'f' : ''}`}
                     onMouseMove=${() => { if (idx !== cur) setActive(idx) }} onClick=${r.run}>
                  <span>${r.label}</span>
                  <span class="tool">${r.tool || ''}</span>
                </div>
              `
              })}
            </div>
          `)}
          ${all.length === 0 && html`
            <div data-testid="palette-empty" style="padding: 16px; font-family: var(--mono); font-size: 12px; color: var(--muted); text-align: center;">
              No matches.
            </div>
          `}
        </div>
      </div>
    </div>
  `
}
