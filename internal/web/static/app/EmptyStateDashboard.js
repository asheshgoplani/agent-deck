// EmptyStateDashboard.js -- Empty state shown by TerminalPanel when no
// session is selected.
//
// PR-B: simplified to a bundle-styled empty state. The redesigned shell
// lands on the Fleet tab by default (rich session overview), so the
// Terminal tab is only reached after the user selects a session.
// This component is the fallback when they haven't.
import { html } from 'htm/preact'
import { sessionsSignal, mutationsEnabledSignal } from './state.js'
import { openCreateSessionForGroup, currentGroupPath } from './dataModel.js'
import { activeTabSignal } from './uiState.js'
import { Logo, Icon, ICONS } from './icons.js'

export function EmptyStateDashboard() {
  const items = sessionsSignal.value
  const sessions = items.filter(i => i.type === 'session' && i.session)
  const total = sessions.length
  const canMutate = mutationsEnabledSignal.value

  return html`
    <div style="flex: 1; min-height: 0; display: flex; align-items: center; justify-content: center; padding: 32px;">
      <div data-testid="empty-state-dashboard"
           style="display: flex; flex-direction: column; align-items: center; gap: 18px; max-width: 420px; text-align: center;">
        <${Logo}/>
        <div>
          <div style="font-size: 16px; font-weight: 600; color: var(--text-hi); margin-bottom: 6px;">
            No session selected
          </div>
          <div style="font-family: var(--mono); font-size: 12px; color: var(--muted); line-height: 1.55;">
            ${total === 0
              ? 'Your deck is empty. Create a session to get started, or browse the fleet view from the sidebar.'
              : `You have ${total} session${total === 1 ? '' : 's'}. Pick one from the sidebar, or open the Fleet tab.`}
          </div>
        </div>
        <div style="display: flex; gap: 8px;">
          <button class="btn ghost" onClick=${() => (activeTabSignal.value = 'fleet')}>
            Open Fleet
          </button>
          ${canMutate && html`
            <button class="btn primary" onClick=${() => openCreateSessionForGroup(currentGroupPath())}>
              <${Icon} d=${ICONS.plus} size=${12}/>New session <span class="kbd">n</span>
            </button>
          `}
        </div>
      </div>
    </div>
  `
}
