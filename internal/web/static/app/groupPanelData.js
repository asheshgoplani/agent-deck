// groupPanelData.js -- data the group stats panel needs, and nothing else.
//
// Split out of dataModel.js deliberately: these four are the panel's only
// consumers, and the panel is lazy-loaded (AppShell.js), so keeping them here
// keeps them out of the initial payload. The page is under a hard
// total-byte-weight budget (.lighthouserc.json) that this feature crossed --
// see PR #2047 review, item 5.
import { computed } from '@preact/signals'
import { archivedSessionsSignal } from './state.js'
import { menuModelSignal, projectSession, statusBucket } from './dataModel.js'

// Status buckets for the group stats panel, in the TUI's fixed display order
// with the TUI's glyphs (internal/ui/home.go:19418-19444).
export const GROUP_STATUS_BUCKETS = [
  { id: 'running', glyph: '●', label: 'running' },
  { id: 'waiting', glyph: '◐', label: 'waiting' },
  { id: 'idle',    glyph: '○', label: 'idle' },
  { id: 'stopped', glyph: '■', label: 'stopped' },
  { id: 'error',   glyph: '✕', label: 'error' },
]

// Archived sessions bucketed by group path. GET /api/sessions/archived returns
// bare MenuSession objects, so wrap each before reusing projectSession.
export const archivedByGroupSignal = computed(() => {
  const byGroup = {}
  for (const raw of (archivedSessionsSignal.value || [])) {
    if (!raw) continue
    const s = projectSession({ session: raw })
    s.archived = true
    ;(byGroup[s.group] ||= []).push(s)
  }
  return byGroup
})

// Panel membership: active members first, then archived. TUI parity — its
// group tree is built from the full instance set (home.go:3540) so the preview
// counts archived, while its left list partitions them out (home.go:2470-2493).
// The web mirrors that split: the sidebar's snapshot is archive-filtered
// server-side, and the archived feed is folded back in here for the panel.
export function groupMembers(groupPath) {
  const active = menuModelSignal.value.byGroup[groupPath] || []
  const archived = archivedByGroupSignal.value[groupPath] || []
  return [...active, ...archived]
}

export function groupStats(groupPath) {
  const members = groupMembers(groupPath)
  const counts = { running: 0, waiting: 0, idle: 0, stopped: 0, error: 0 }
  for (const s of members) counts[statusBucket(s.status)]++
  return {
    total: members.length,
    fragments: GROUP_STATUS_BUCKETS
      .filter((b) => counts[b.id] > 0)
      .map((b) => ({ id: b.id, glyph: b.glyph, count: counts[b.id], label: b.label })),
  }
}
