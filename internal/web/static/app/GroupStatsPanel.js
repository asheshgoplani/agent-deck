// GroupStatsPanel.js -- Main-area view shown when a GROUP (not a session) is
// selected in the sidebar. Web port of the TUI's renderGroupPreview
// (internal/ui/home.go:19382).
//
// Deliberate divergences from the TUI, all documented in
// docs/specs/WEB-GROUP-SELECTION-PARITY-SPEC.md:
//   - `starting` counts as running, `queued` as idle, so the fragments always
//     sum to the headline (the TUI drops both).
//   - Archived sessions are excluded (the web menu snapshot never carries
//     them; the TUI preview counts them).
//   - Direct members only — no subgroup rollup, matching the TUI preview.
//   - No Repository/worktree block: per-branch dirty state is not on the wire.
import { html } from 'htm/preact'
import { menuModelSignal, groupStats } from './dataModel.js'
import { selectSession } from './state.js'
import { activeTabSignal } from './uiState.js'
import { Dot } from './icons.js'

export function GroupStatsPanel({ path }) {
  const { groups, byGroup } = menuModelSignal.value
  const group = groups.find(g => g.path === path)

  // The selected group can vanish out from under the panel — deleted in the
  // TUI while a browser tab has it selected, or a stale /g/{path} URL/reload
  // for a group that was never real. Say so rather than fabricating "0
  // sessions" for a group that is not in the current menu snapshot, which
  // reads identically to a real but empty group.
  if (!group) {
    return html`
      <div class="group-stats" data-testid="group-stats-panel" data-group-path=${path}>
        <div class="gs-head">
          <span class="gs-folder" aria-hidden="true">📁</span>
          <span class="gs-name">${path}</span>
        </div>
        <div class="gs-empty" data-testid="group-stats-missing">This group no longer exists.</div>
      </div>
    `
  }

  const members = byGroup[path] || []
  const stats = groupStats(path)

  const openSession = (id) => {
    selectSession(id)
    activeTabSignal.value = 'terminal'
  }

  return html`
    <div class="group-stats" data-testid="group-stats-panel" data-group-path=${path}>
      <div class="gs-head">
        <span class="gs-folder" aria-hidden="true">📁</span>
        <span class="gs-name">${group.name}</span>
      </div>

      <div class="gs-total" data-testid="group-stats-total">${stats.total} sessions</div>

      ${stats.fragments.length > 0 && html`
        <div class="gs-fragments" data-testid="group-stats-fragments">
          ${stats.fragments.map(f => html`
            <span key=${f.id} class=${`gs-frag ${f.id}`}>
              <span class="gs-glyph">${f.glyph}</span> ${f.count} ${f.label}
            </span>
          `)}
        </div>
      `}

      ${group && group.defaultPath && html`
        <div class="gs-path" title=${group.defaultPath}>
          <span class="gs-k">default path</span>
          <span class="gs-v">${group.defaultPath}</span>
        </div>
      `}

      <div class="gs-divider"><span>SESSIONS</span></div>

      ${members.length === 0
        ? html`<div class="gs-empty">No sessions in this group</div>`
        : members.map(s => html`
            <div key=${s.id} class="gs-row" onClick=${() => openSession(s.id)}>
              <${Dot} status=${s.status}/>
              <span class="gs-title">${s.title}</span>
              <span class="gs-tool">${s.tool}</span>
            </div>
          `)}
    </div>
  `
}
