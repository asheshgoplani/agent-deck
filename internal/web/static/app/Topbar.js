// Topbar.js -- Top navigation bar (rewrite). Brand left, tabs+search center,
// connection pill / profile / rail toggle / tweaks right.
//
// Replaces the previous Tailwind-based Topbar. Uses bundle's `.topbar` class
// from app.css. Tabs are NOT wired to API endpoints that don't exist —
// MCP/Skills/Conductor/Watchers panes render informative placeholders.
import { html } from 'htm/preact'
import { Logo, Icon, ICONS } from './icons.js'
import { menuModelSignal } from './dataModel.js'
import { connectionSignal, profilesSignal } from './state.js'
import {
  activeTabSignal, paletteOpenSignal, tweaksOpenSignal,
  railSignal, profileSignal,
} from './uiState.js'
import { ToastHistoryDrawerToggle } from './ToastHistoryDrawer.js'

const TABS = [
  { id: 'fleet',     label: 'Fleet'     },
  { id: 'terminal',  label: 'Terminal'  },
  { id: 'mcp',       label: 'MCPs'      },
  { id: 'skills',    label: 'Skills'    },
  { id: 'conductor', label: 'Conductor' },
  { id: 'watchers',  label: 'Watchers'  },
  { id: 'costs',     label: 'Costs'     },
  { id: 'search',    label: 'Search'    },
]

export function Topbar() {
  const activeTab = activeTabSignal.value
  const conn = connectionSignal.value
  const rail = railSignal.value
  const { sessions } = menuModelSignal.value
  const sessionsBadge = sessions.filter(s => s.status === 'waiting' || s.status === 'error').length
  const pendingNeeds = sessions.reduce((n, s) => n + (s.pendingNeeds || 0), 0)

  const connClass = conn === 'connected' ? '' : 'off'
  const connDotStyle = conn === 'connected'
    ? {}
    : { background: 'var(--tn-red)', boxShadow: '0 0 6px var(--tn-red)' }

  return html`
    <header class="topbar">
      <div class="top-brand">
        <${Logo}/>
        <div class="brand-text">agent-deck<span class="dim">web</span></div>
      </div>
      <div class="top-mid">
        <button class="top-search" onClick=${() => (paletteOpenSignal.value = true)}>
          <${Icon} d=${ICONS.search} size=${13}/>
          <input readonly placeholder="Jump to session, search conversations, run command…"/>
          <span class="kbd">⌘K</span>
        </button>
        <div class="top-tabs">
          ${TABS.map(t => html`
            <button key=${t.id}
              class=${`top-tab ${activeTab === t.id ? 'active' : ''}`}
              onClick=${() => (activeTabSignal.value = t.id)}>
              ${t.label}
              ${t.id === 'conductor' && pendingNeeds > 0 && html`<span class="badge">${pendingNeeds}</span>`}
              ${t.id === 'fleet' && sessionsBadge > 0 && html`<span class="badge">${sessionsBadge}</span>`}
            </button>
          `)}
        </div>
      </div>
      <div class="top-right">
        <div class=${`conn-pill ${connClass}`}>
          <span class="dot" style=${connDotStyle}/>ws · ${conn === 'connected' ? 'live' : conn}
        </div>
        ${(() => {
          const p = profilesSignal.value
          const list = p && Array.isArray(p.profiles) ? p.profiles : null
          // Hold the dropdown until /api/profiles resolves so we never
          // flash a hardcoded default on cold load.
          if (!list || list.length === 0) return null
          return html`
            <select class="icon-btn"
              style=${{ width: 'auto', padding: '0 8px', fontFamily: 'var(--mono)', fontSize: '11px' }}
              value=${profileSignal.value || (p.current || list[0])}
              onChange=${e => (profileSignal.value = e.target.value)}>
              ${list.map(name => html`<option key=${name} value=${name}>${name}</option>`)}
            </select>
          `
        })()}
        <${ToastHistoryDrawerToggle}/>
        <button
          class=${`icon-btn ${rail === 'visible' ? 'active' : ''}`}
          onClick=${() => (railSignal.value = rail === 'visible' ? 'hidden' : 'visible')}
          title=${rail === 'visible' ? 'Hide right rail (])' : 'Show right rail (])'}
          aria-label="Toggle right rail">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor"
               stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <rect x="3" y="3" width="18" height="18" rx="2"/>
            <line x1="15" y1="3" x2="15" y2="21"/>
            ${rail === 'visible' && html`<line x1="18" y1="8" x2="18" y2="16" opacity="0.5"/>`}
          </svg>
        </button>
        <button class="icon-btn" onClick=${() => (tweaksOpenSignal.value = !tweaksOpenSignal.value)} title="Tweaks (?)" aria-label="Tweaks">
          <${Icon} d=${ICONS.settings}/>
        </button>
      </div>
    </header>
  `
}
