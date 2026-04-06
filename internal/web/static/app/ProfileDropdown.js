// ProfileDropdown.js -- Topbar profile indicator with dropdown list
// TOPBAR-02: Shows current profile, lists available profiles from /api/profiles
// Display-only: switching profiles requires restarting with -p flag
import { html } from 'htm/preact'
import { useState, useEffect, useRef } from 'preact/hooks'
import { authTokenSignal } from './state.js'

export function ProfileDropdown() {
  const [open, setOpen] = useState(false)
  const [profiles, setProfiles] = useState(null)
  const [current, setCurrent] = useState('')
  const ref = useRef(null)

  // Fetch profiles once on mount
  useEffect(() => {
    const token = authTokenSignal.value
    const headers = token ? { Authorization: 'Bearer ' + token } : {}
    fetch('/api/profiles', { headers })
      .then(r => r.json())
      .then(data => {
        setCurrent(data.current || 'default')
        setProfiles(data.profiles || [data.current])
      })
      .catch(() => setProfiles([]))
  }, [])

  // Close on outside click
  useEffect(() => {
    if (!open) return
    function onClickOutside(e) {
      if (ref.current && !ref.current.contains(e.target)) setOpen(false)
    }
    document.addEventListener('mousedown', onClickOutside)
    return () => document.removeEventListener('mousedown', onClickOutside)
  }, [open])

  if (!profiles) return null // loading

  return html`
    <div class="relative" ref=${ref}>
      <button
        type="button"
        onClick=${() => setOpen(!open)}
        class="text-xs dark:text-tn-muted text-gray-500 hover:dark:text-tn-fg hover:text-gray-700
               transition-colors px-2 py-1 rounded hover:dark:bg-tn-muted/10 hover:bg-gray-100
               flex items-center gap-1"
        aria-haspopup="listbox"
        aria-expanded=${open}
        title=${profiles.length > 1 ? 'Profiles (switch via -p flag)' : 'Current profile'}
      >
        <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M16 7a4 4 0 11-8 0 4 4 0 018 0zM12 14a7 7 0 00-7 7h14a7 7 0 00-7-7z"/>
        </svg>
        <span>${current}</span>
        <svg class="w-3 h-3 transition-transform ${open ? 'rotate-180' : ''}" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/>
        </svg>
      </button>
      ${open && html`
        <div class="absolute top-full right-0 mt-1 z-50 rounded-lg shadow-lg
                    dark:bg-tn-panel bg-white border dark:border-tn-muted/20 border-gray-200
                    min-w-[140px] max-w-[90vw] py-1"
             role="listbox"
             aria-label="Available profiles">
          ${profiles.map(p => html`
            <div
              key=${p}
              role="option"
              aria-selected=${p === current}
              class="px-3 py-1.5 text-xs
                ${p === current
                  ? 'dark:text-tn-blue text-tn-light-blue font-medium'
                  : 'dark:text-tn-fg text-gray-700'}"
            >${p}${p === current ? html` <span class="dark:text-tn-muted text-gray-400 ml-1">(active)</span>` : ''}</div>
          `)}
          ${profiles.length > 1 && html`
            <div class="border-t dark:border-tn-muted/20 border-gray-200 mt-1 pt-1 px-3 py-1">
              <span class="text-[10px] dark:text-tn-muted/60 text-gray-400 italic">Switch: restart with -p flag</span>
            </div>
          `}
        </div>
      `}
    </div>
  `
}
