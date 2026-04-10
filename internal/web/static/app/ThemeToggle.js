// ThemeToggle.js -- Dark/Light/System toggle
// THEME-01: toggle between dark and light
// THEME-02: system preference as default
// THEME-03: Tokyo Night palette (via Tailwind config in index.html)
import { html } from 'htm/preact'
import { themeSignal } from './state.js'

function applyTheme(mode) {
  themeSignal.value = mode
  if (mode === 'system') {
    localStorage.removeItem('theme')
    const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches
    document.documentElement.classList.toggle('dark', prefersDark)
  } else {
    localStorage.setItem('theme', mode)
    document.documentElement.classList.toggle('dark', mode === 'dark')
  }
}

// Listen for system preference changes when in 'system' mode
if (typeof window !== 'undefined') {
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', (e) => {
    if (themeSignal.value === 'system') {
      document.documentElement.classList.toggle('dark', e.matches)
    }
  })
}

export function ThemeToggle() {
  const current = themeSignal.value

  const btn = (mode, label) => html`
    <button
      type="button"
      onClick=${() => applyTheme(mode)}
      class="px-3 py-1.5 text-xs font-medium transition-colors
        ${current === mode
          ? 'dark:bg-tn-blue bg-tn-light-blue text-white'
          : 'dark:text-tn-muted text-gray-500 hover:dark:text-tn-fg hover:text-gray-700'}"
      aria-pressed=${current === mode}
      title="${label} theme"
    >${label}</button>
  `

  return html`
    <div class="flex items-center rounded border dark:border-tn-muted/30 border-gray-200 overflow-hidden">
      ${btn('light', 'Light')}
      ${btn('dark', 'Dark')}
      ${btn('system', 'Auto')}
    </div>
  `
}
