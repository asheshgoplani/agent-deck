// SearchFilter.js -- Sidebar search input with fuzzy matching
import { html } from 'htm/preact'
import { useEffect, useRef } from 'preact/hooks'
import { searchQuerySignal, searchVisibleSignal } from './state.js'

export function SearchFilter() {
  const inputRef = useRef(null)
  const visible = searchVisibleSignal.value

  useEffect(() => {
    function onKeyDown(e) {
      // "/" or Cmd/Ctrl+K to toggle search
      if (e.key === '/' && !e.ctrlKey && !e.metaKey && !isInputFocused()) {
        e.preventDefault()
        searchVisibleSignal.value = true
        return
      }
      if (e.key === 'k' && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        searchVisibleSignal.value = !searchVisibleSignal.value
        if (!searchVisibleSignal.value) {
          searchQuerySignal.value = ''
        }
        return
      }
      if (e.key === 'Escape' && searchVisibleSignal.value) {
        searchVisibleSignal.value = false
        searchQuerySignal.value = ''
      }
    }

    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [])

  // Auto-focus input when search becomes visible
  useEffect(() => {
    if (visible && inputRef.current) {
      inputRef.current.focus()
    }
  }, [visible])

  if (!visible) {
    return html`
      <button
        type="button"
        onClick=${() => { searchVisibleSignal.value = true }}
        class="w-full flex items-center gap-2 px-sp-12 py-2 text-xs
               dark:text-tn-muted text-gray-600 hover:dark:text-tn-fg hover:text-gray-800
               hover:dark:bg-tn-muted/10 hover:bg-gray-100
               transition-colors rounded"
        title="Filter sessions (/ or ⌘K)"
      >
        <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"/>
        </svg>
        <span>Filter sessions...</span>
        <kbd class="ml-auto text-[10px] dark:bg-tn-muted/20 bg-gray-200 px-1.5 py-0.5 rounded font-mono">/</kbd>
      </button>
    `
  }

  return html`
    <div class="px-sp-8 py-sp-4">
      <div class="relative">
        <svg class="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 dark:text-tn-muted text-gray-400 pointer-events-none"
             fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"/>
        </svg>
        <input
          ref=${inputRef}
          type="text"
          placeholder="Filter sessions..."
          value=${searchQuerySignal.value}
          onInput=${(e) => { searchQuerySignal.value = e.target.value }}
          onKeyDown=${(e) => {
            if (e.key === 'Escape') {
              searchVisibleSignal.value = false
              searchQuerySignal.value = ''
            }
          }}
          class="w-full h-8 pl-8 pr-8 text-sm rounded
                 dark:bg-tn-bg bg-gray-50
                 dark:text-tn-fg text-gray-900
                 dark:border-tn-muted/30 border-gray-300 border
                 dark:placeholder-tn-muted placeholder-gray-400
                 focus:outline focus:outline-2 focus:outline-blue-500 focus:dark:outline-tn-blue
                 transition-[background-color,box-shadow] duration-150"
        />
        ${searchQuerySignal.value && html`
          <button
            type="button"
            onClick=${() => { searchQuerySignal.value = '' }}
            class="absolute right-2 top-1/2 -translate-y-1/2 dark:text-tn-muted text-gray-400
                   hover:dark:text-tn-fg hover:text-gray-600 transition-colors"
            aria-label="Clear search"
          >\u2715</button>
        `}
      </div>
    </div>
  `
}

function isInputFocused() {
  const el = document.activeElement
  if (!el) return false
  const tag = el.tagName.toLowerCase()
  return tag === 'input' || tag === 'textarea' || tag === 'select' || el.isContentEditable
}
