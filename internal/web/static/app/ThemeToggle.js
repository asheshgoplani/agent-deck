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

  return html`
    <div class="flex items-center gap-sp-4">
      <button
        onClick=${() => applyTheme('light')}
        class="px-2 py-1 rounded text-xs font-medium transition-colors
          ${current === 'light'
            ? 'dark:bg-tn-blue bg-tn-light-blue text-white'
            : 'dark:text-tn-muted text-gray-500 hover:dark:text-tn-fg hover:text-gray-700'}"
        aria-pressed=${current === 'light'}
        title="Light theme"
      >
        Light
      </button>
      <button
        onClick=${() => applyTheme('dark')}
        class="px-2 py-1 rounded text-xs font-medium transition-colors
          ${current === 'dark'
            ? 'dark:bg-tn-blue bg-tn-light-blue text-white'
            : 'dark:text-tn-muted text-gray-500 hover:dark:text-tn-fg hover:text-gray-700'}"
        aria-pressed=${current === 'dark'}
        title="Dark theme"
      >
        Dark
      </button>
      <button
        onClick=${() => applyTheme('system')}
        class="px-2 py-1 rounded text-xs font-medium transition-colors
          ${current === 'system'
            ? 'dark:bg-tn-blue bg-tn-light-blue text-white'
            : 'dark:text-tn-muted text-gray-500 hover:dark:text-tn-fg hover:text-gray-700'}"
        aria-pressed=${current === 'system'}
        title="Follow system preference"
      >
        System
      </button>
    </div>
  `
}
