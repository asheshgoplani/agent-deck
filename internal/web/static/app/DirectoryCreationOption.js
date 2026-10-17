import { html } from 'htm/preact'
import { useEffect, useId, useState } from 'preact/hooks'
import { apiFetch } from './api.js'
import { Icon, ICONS } from './icons.js'

// Consent belongs to one preview of one input, never to a later path edit.
export function DirectoryCreationOption({ path, refresh, onChange }) {
  const [result, setResult] = useState(null)
  const [checked, setChecked] = useState(false)
  const previewId = useId()

  useEffect(() => {
    let active = true
    setResult(null)
    setChecked(false)
    onChange(null)
    if (!path) return
    const timer = setTimeout(async () => {
      try {
        const preview = await apiFetch('GET', '/api/session-directory?path=' + encodeURIComponent(path))
        if (active) setResult({ inputPath: path, ...preview })
      } catch (err) {
        if (active) setResult({ inputPath: path, error: err.message })
      }
    }, 200)
    return () => { active = false; clearTimeout(timer) }
  }, [path, refresh, onChange])

  if (!result || result.inputPath !== path) return null
  if (result.error) return html`<div class="dir-create-error" role="status">${result.error}</div>`
  if (!result.missing.length) return null

  function choose(e) {
    setChecked(e.target.checked)
    onChange(e.target.checked ? result : null)
  }

  return html`
    <div class=${`dir-create-panel${checked ? ' dir-create-panel--selected' : ''}`} role="group" aria-label="Missing working directory">
      <div class="dir-create-heading" role="status">
        <span class="dir-create-mark" aria-hidden="true"><${Icon} d=${ICONS.plus} size=${16}/></span>
        <div>
          <div class="dir-create-title">Folder does not exist.</div>
          <div class="dir-create-hint">Opt in below to create it when you submit the session.</div>
        </div>
      </div>
      <label class="dir-create-choice">
        <input class="dir-create-check" type="checkbox" checked=${checked} onChange=${choose} aria-describedby=${previewId}/>
        <span>Create this folder</span>
      </label>
      <div id=${previewId}>
        <div class="dir-create-caption">Directories to create:</div>
        <ul class="dir-create-paths" role="list">${result.missing.map(dir => html`
          <li class="dir-create-path-row" key=${dir}>
            <span class="dir-create-path-icon" aria-hidden="true"><${Icon} d=${ICONS.plus} size=${13}/></span>
            <code class="dir-create-path">${dir}</code>
          </li>
        `)}</ul>
      </div>
    </div>
  `
}
