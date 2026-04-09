// ProfileDropdown.js -- Topbar profile indicator, Option B (read-only label)
// WEB-P0-2: backend server.go:79 binds cfg.Profile ONCE at NewServer() time.
// Per-request profile override (Option A, query-string reload) would require
// re-architecting profile isolation and is explicitly OUT OF SCOPE per
// REQUIREMENTS.md line 121. This component is display-only:
//   - 1 profile  -> <div role="status"> non-interactive label
//   - >1 profiles -> expandable listbox with NO click handlers + always-visible help text
import { html } from 'htm/preact'
import { useState, useEffect, useRef } from 'preact/hooks'
import { authTokenSignal } from './state.js'

const HELP_TEXT = 'Switch profiles by restarting agent-deck with -p <name>'

export function ProfileDropdown() {
  const [open, setOpen] = useState(false)
  const [profiles, setProfiles] = useState(null)
  const [current, setCurrent] = useState('')
  const ref = useRef(null)

  // Fetch profiles once on mount (display only, no switch action).
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

  // Close on outside click (multi-profile dropdown only).
  useEffect(() => {
    if (!open) return
    function onClickOutside(e) {
      if (ref.current && !ref.current.contains(e.target)) setOpen(false)
    }
    document.addEventListener('mousedown', onClickOutside)
    return () => document.removeEventListener('mousedown', onClickOutside)
  }, [open])

  if (!profiles) return null // loading

  // Single-profile case: render a non-interactive status element. Screen
  // readers announce this as status text (not a disabled button).
  if (profiles.length <= 1) {
    return html`
      <div
        role="status"
        aria-label=${'Current profile: ' + current}
        data-testid="profile-indicator"
        class="text-xs dark:text-tn-muted text-gray-500 px-2 py-1 rounded
               flex items-center gap-1 min-h-[44px] cursor-default"
        title=${HELP_TEXT}
      >
        <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M16 7a4 4 0 11-8 0 4 4 0 018 0zM12 14a7 7 0 00-7 7h14a7 7 0 00-7-7z"/>
        </svg>
        <span>${current}</span>
      </div>
    `
  }

  // Multi-profile case: expandable listbox, but each option is
  // non-interactive (no onClick) and the help text is always visible when the
  // dropdown is open.
  return html`
    <div class="relative" ref=${ref} data-testid="profile-indicator">
      <button
        type="button"
        onClick=${() => setOpen(!open)}
        class="text-xs dark:text-tn-muted text-gray-500 hover:dark:text-tn-fg hover:text-gray-700
               transition-colors px-2 py-1 rounded hover:dark:bg-tn-muted/10 hover:bg-gray-100
               flex items-center gap-1 min-h-[44px]"
        aria-haspopup="listbox"
        aria-expanded=${open}
        aria-label=${'Current profile: ' + current + '. ' + HELP_TEXT}
        title=${HELP_TEXT}
      >
        <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M16 7a4 4 0 11-8 0 4 4 0 018 0zM12 14a7 7 0 00-7 7h14a7 7 0 00-7-7z"/>
        </svg>
        <span>${current}</span>
        <svg class="w-3 h-3 transition-transform ${open ? 'rotate-180' : ''}" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/>
        </svg>
      </button>
      ${open && html`
        <div class="absolute top-full right-0 mt-1 z-dropdown rounded-lg shadow-lg
                    dark:bg-tn-panel bg-white border dark:border-tn-muted/20 border-gray-200
                    min-w-[220px] max-w-[90vw] py-1"
             role="listbox"
             aria-label="Available profiles (read-only)">
          ${profiles.map(p => html`
            <div
              key=${p}
              role="option"
              aria-selected=${p === current}
              aria-disabled="true"
              class="px-3 py-1.5 text-xs cursor-default
                ${p === current
                  ? 'dark:text-tn-blue text-tn-light-blue font-medium'
                  : 'dark:text-tn-muted text-gray-500'}"
            >${p}${p === current ? html` <span class="dark:text-tn-muted text-gray-400 ml-1">(active)</span>` : ''}</div>
          `)}
          <div class="border-t dark:border-tn-muted/20 border-gray-200 mt-1 pt-1 px-3 py-1.5">
            <span class="text-[11px] dark:text-tn-muted/80 text-gray-500 italic">${HELP_TEXT}</span>
          </div>
        </div>
      `}
    </div>
  `
}
