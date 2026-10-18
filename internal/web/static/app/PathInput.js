import { html } from 'htm/preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { authHeaders } from './api.js'
import { Icon, ICONS } from './icons.js'
import { pathSuggestionAction } from './pathSuggestions.js'

export function PathInput({ value, onInput }) {
  const [focused, setFocused] = useState(false)
  const [dismissed, setDismissed] = useState(false)
  const [pending, setPending] = useState(false)
  const [result, setResult] = useState(null)
  const [index, setIndex] = useState(-1)
  const inputRef = useRef(null)
  const listRef = useRef(null)
  const generation = useRef(0)
  const paths = focused && !dismissed && result?.prefix === value ? result.paths : []

  useEffect(() => {
    const visual = window.visualViewport
    const overlay = inputRef.current?.closest('.path-dialog-overlay')
    if (!visual || !overlay) return
    const resize = () => {
      overlay.style.setProperty('--path-viewport-height', visual.height + 'px')
      overlay.style.setProperty('--path-viewport-top', visual.offsetTop + 'px')
    }
    resize()
    visual.addEventListener('resize', resize)
    visual.addEventListener('scroll', resize)
    return () => {
      visual.removeEventListener('resize', resize)
      visual.removeEventListener('scroll', resize)
      overlay.style.removeProperty('--path-viewport-height')
      overlay.style.removeProperty('--path-viewport-top')
    }
  }, [])

  useEffect(() => {
    const request = ++generation.current
    if (!focused || dismissed || !value) return
    setPending(true)
    const controller = new AbortController()
    const timer = setTimeout(async () => {
      try {
        const response = await fetch('/api/path-suggestions?prefix=' + encodeURIComponent(value), {
          headers: authHeaders(), signal: controller.signal,
        })
        if (!response.ok) return
        const data = await response.json()
        if (generation.current === request && !controller.signal.aborted) {
          setResult({ prefix: value, paths: data.suggestions || [] })
          setIndex(-1)
        }
      } catch {
        // Completion is optional; keep manual entry usable on network errors.
      } finally {
        if (generation.current === request && !controller.signal.aborted) setPending(false)
      }
    }, 150)
    return () => { clearTimeout(timer); controller.abort() }
  }, [value, focused, dismissed])

  useEffect(() => {
    if (!paths.length) return
    const target = index >= 0 ? listRef.current?.children[index] : listRef.current
    const scroll = () => target?.scrollIntoView?.({ block: 'nearest' })
    scroll()
    // Keep options reachable when a phone keyboard reduces the dialog body.
    const observer = new ResizeObserver(scroll)
    const body = inputRef.current?.closest('.db')
    if (body) observer.observe(body)
    return () => observer.disconnect()
  }, [index, paths])

  function dismiss() {
    ++generation.current
    setDismissed(true)
    setPending(false)
    setResult(null)
    setIndex(-1)
  }

  function accept(selected) {
    dismiss()
    // A slash lets the next edit discover children of the selected directory.
    const path = selected.endsWith('/') ? selected : selected + '/'
    onInput({ target: { value: path } })
    inputRef.current?.focus()
  }

  function handleInput(e) {
    ++generation.current
    setPending(false)
    setResult(null)
    setIndex(-1)
    setDismissed(false)
    onInput(e)
  }

  function handleKey(e) {
    const action = pathSuggestionAction(e.key, index, paths.length, e, pending)
    if (!action) return
    e.preventDefault()
    e.stopPropagation()
    if (action.type === 'dismiss') dismiss()
    if (action.type === 'move') setIndex(action.index)
    if (action.type === 'accept') accept(paths[action.index])
  }

  return html`
    <div class="path-input">
      <input id="create-session-path" ref=${inputRef} required value=${value}
             role="combobox" aria-autocomplete="list" autocomplete="off"
             aria-expanded=${paths.length > 0} aria-controls="create-session-path-list"
             aria-activedescendant=${index >= 0 && paths.length ? 'create-session-path-' + index : undefined}
             onInput=${handleInput} onKeyDown=${handleKey}
             onFocus=${() => { setFocused(true); setDismissed(false) }}
             onBlur=${() => { dismiss(); setFocused(false) }}
             placeholder="/absolute/path/to/project"/>
      ${paths.length > 0 && html`
        <div id="create-session-path-list" class="path-suggestions" role="listbox" aria-label="Directories" ref=${listRef}>
          ${paths.map((path, i) => html`
            <div id=${'create-session-path-' + i} key=${path} role="option" class="path-suggestion"
                 aria-selected=${index === i} title=${path}
                 onPointerDown=${e => e.preventDefault()} onClick=${() => accept(path)}>
              <span class="path-suggestion-icon" aria-hidden="true"><${Icon} d=${ICONS.folder} size=${16}/></span>
              <span class="path-suggestion-label">${path.split('/').filter(Boolean).pop()}</span>
            </div>
          `)}
        </div>
      `}
    </div>
  `
}
