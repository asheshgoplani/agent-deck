// panes/SkillsPane.js -- Web UI for managing project-scoped skill attachments.
//
// Mirrors the TUI `s` key dialog (internal/ui/skill_dialog.go) by showing
// two columns: "Attached" (skills present in the selected session's
// project) and "Catalog" (skills discoverable across the user's pool /
// claude-global sources). The user can attach a catalog entry or detach
// an attached one with a single click; the buttons hit the new
// /api/sessions/{id}/skills endpoints added in this PR.
import { html } from 'htm/preact'
import { useEffect, useState } from 'preact/hooks'
import { selectedIdSignal } from '../state.js'
import { menuModelSignal } from '../dataModel.js'
import { apiFetch } from '../api.js'
import { addToast } from '../Toast.js'

const TOOLS_WITH_SKILLS = new Set(['claude', 'gemini', 'codex', 'pi'])

export function SkillsPane() {
  const selectedId = selectedIdSignal.value
  const { sessions } = menuModelSignal.value
  const session = sessions.find(s => s.id === selectedId)

  const [catalog, setCatalog] = useState([])
  const [attached, setAttached] = useState([])
  const [loading, setLoading] = useState(false)
  const [busyName, setBusyName] = useState('')

  async function refresh() {
    setLoading(true)
    try {
      const cat = await apiFetch('GET', '/api/skills')
      setCatalog(cat?.skills || [])
      if (session) {
        const att = await apiFetch('GET', `/api/sessions/${encodeURIComponent(session.id)}/skills`)
        setAttached(att?.skills || [])
      } else {
        setAttached([])
      }
    } catch (e) {
      // apiFetch already toasts for non-GET errors; GET is silent — surface here.
      addToast('Failed to load skills: ' + (e.message || 'request failed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { refresh() }, [selectedId])

  if (!session) {
    return html`
      <div class="costs">
        <div class="chart-card" style="padding: 32px; text-align: center;">
          <div class="title">No session selected</div>
          <div style="color: var(--text-dim); margin-top: 12px;">
            Pick a session from the sidebar to manage its skills.
          </div>
        </div>
      </div>`
  }

  const toolSupports = TOOLS_WITH_SKILLS.has((session.tool || '').toLowerCase())
  if (!toolSupports) {
    return html`
      <div class="costs">
        <div class="chart-card" style="padding: 32px; text-align: center;">
          <div class="title">Skills not supported for ${session.tool}</div>
          <div style="color: var(--text-dim); margin-top: 12px;">
            Project-scoped skills are available for Claude, Gemini, Codex, and Pi sessions only.
          </div>
        </div>
      </div>`
  }

  // Build a Set of attached IDs so the catalog can hide already-attached skills.
  const attachedIDs = new Set(attached.map(s => s.id))
  const available = catalog.filter(c =>
    !attachedIDs.has(c.id) && (c.kind || 'dir') === 'dir')

  async function attach(skill) {
    if (busyName) return
    setBusyName(skill.id)
    try {
      const path = `/api/sessions/${encodeURIComponent(session.id)}/skills/${encodeURIComponent(skill.name)}?source=${encodeURIComponent(skill.source)}`
      await apiFetch('POST', path)
      addToast(`Attached ${skill.name}`)
      await refresh()
    } catch (e) {
      // apiFetch already toasts mutation errors.
    } finally {
      setBusyName('')
    }
  }

  async function detach(skill) {
    if (busyName) return
    setBusyName(skill.id)
    try {
      const path = `/api/sessions/${encodeURIComponent(session.id)}/skills/${encodeURIComponent(skill.name)}?source=${encodeURIComponent(skill.source)}`
      await apiFetch('DELETE', path)
      addToast(`Detached ${skill.name}`)
      await refresh()
    } catch (e) {
      // toasted upstream
    } finally {
      setBusyName('')
    }
  }

  return html`
    <div class="skills-pane" data-testid="skills-pane" style="padding: 16px; display: flex; flex-direction: column; gap: 16px; height: 100%; overflow: auto;">
      <div style="display: flex; justify-content: space-between; align-items: center;">
        <div class="title" style="font-size: 14px;">Skills · ${session.title}</div>
        <button class="btn" data-testid="skills-refresh" onClick=${refresh} disabled=${loading}>${loading ? 'Loading…' : 'Refresh'}</button>
      </div>

      <section data-testid="skills-attached" style="border: 1px solid var(--border); border-radius: 6px; padding: 12px;">
        <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); margin-bottom: 8px;">
          ATTACHED (${attached.length})
        </div>
        ${attached.length === 0
          ? html`<div data-testid="skills-attached-empty" style="color: var(--muted); font-size: 12px;">No skills attached.</div>`
          : html`<ul style="list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px;">
              ${attached.map(s => html`
                <li data-testid="skill-attached-row" data-skill-id=${s.id} style="display: flex; justify-content: space-between; gap: 8px; align-items: center; padding: 6px 8px; background: var(--surface); border-radius: 4px;">
                  <span><strong>${s.name}</strong> <span style="color: var(--muted); font-size: 11px;">${s.source}</span></span>
                  <button class="btn btn-danger" data-testid="skill-detach-btn" disabled=${busyName === s.id} onClick=${() => detach(s)}>Detach</button>
                </li>`)}
            </ul>`}
      </section>

      <section data-testid="skills-catalog" style="border: 1px solid var(--border); border-radius: 6px; padding: 12px;">
        <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); margin-bottom: 8px;">
          CATALOG (${available.length})
        </div>
        ${available.length === 0
          ? html`<div data-testid="skills-catalog-empty" style="color: var(--muted); font-size: 12px;">No additional skills available to attach.</div>`
          : html`<ul style="list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px;">
              ${available.map(c => html`
                <li data-testid="skill-catalog-row" data-skill-id=${c.id} style="display: flex; justify-content: space-between; gap: 8px; align-items: center; padding: 6px 8px;">
                  <span>
                    <strong>${c.name}</strong>
                    <span style="color: var(--muted); font-size: 11px;"> ${c.source}</span>
                    ${c.description && html`<div style="color: var(--text-dim); font-size: 11px;">${c.description}</div>`}
                  </span>
                  <button class="btn" data-testid="skill-attach-btn" disabled=${busyName === c.id} onClick=${() => attach(c)}>Attach</button>
                </li>`)}
            </ul>`}
      </section>
    </div>
  `
}
