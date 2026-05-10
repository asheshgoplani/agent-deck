// helpers/parity-matrix.js -- single source of truth for parity tests.
//
// Parses tests/web/PARITY_MATRIX.md into JS objects so the e2e specs can
// iterate every documented row instead of hard-coding a subset. If a row is
// added or removed without updating the pinned row counts in the spec
// files, the suite fails loudly.

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const MATRIX_PATH = resolve(import.meta.dirname, '..', 'PARITY_MATRIX.md')

export function loadMatrix() {
  const raw = readFileSync(MATRIX_PATH, 'utf8')
  return {
    actions: parseActions(extractSection(raw, '## TUI Action Matrix')),
    stateFields: parseStateFields(extractSection(raw, '## State Fields Matrix')),
  }
}

function extractSection(raw, heading) {
  const start = raw.indexOf(heading)
  if (start === -1) throw new Error(`PARITY_MATRIX.md: missing section ${heading}`)
  // section ends at next horizontal rule
  const end = raw.indexOf('\n---', start + heading.length)
  return raw.slice(start, end === -1 ? raw.length : end)
}

function tableRows(section) {
  const rows = []
  let sawSeparator = false
  for (const line of section.split('\n')) {
    if (!line.startsWith('|')) continue
    if (/^\|\s*-+\s*\|/.test(line)) { sawSeparator = true; continue }
    if (!sawSeparator) continue
    const cells = line.split('|').slice(1, -1).map((c) => c.trim())
    // Section dividers like `| **SESSION LIFECYCLE** |` collapse to a
    // single non-empty cell; skip them.
    const nonEmpty = cells.filter((c) => c !== '').length
    if (nonEmpty <= 1) continue
    rows.push(cells)
  }
  return rows
}

function parseActions(section) {
  return tableRows(section).map((cells) => {
    const [action, tuiTrigger, webEndpoint, mutator, test, notes = ''] = cells
    const isMissing = /^MISSING\b/i.test(webEndpoint)
    return {
      action,
      tuiTrigger,
      webEndpoint,
      mutator,
      test,
      notes,
      isMissing,
      // Parse "POST `/api/sessions`" → {method, path}
      ...parseEndpoint(webEndpoint),
    }
  })
}

function parseEndpoint(cell) {
  // Handles: `POST /api/sessions`, `DELETE /api/groups/{path}`, etc.
  const m = cell.match(/^(GET|POST|DELETE|PATCH|PUT)\s+`?([^`\s]+)`?/i)
  if (!m) return { method: null, path: null }
  return { method: m[1].toUpperCase(), path: m[2] }
}

function parseStateFields(section) {
  return tableRows(section).map((cells) => {
    const [field, tuiDisplay, webJSON, notes = ''] = cells
    const isMissing = /^MISSING\b/i.test(webJSON)
    // `MenuSession.parentSessionId` → "parentSessionId"
    const jsonKeyMatch = webJSON.match(/`MenuSession\.([A-Za-z0-9_]+)`/)
    return {
      field: field.replace(/`/g, ''),
      tuiDisplay,
      webJSON,
      notes,
      isMissing,
      jsonKey: jsonKeyMatch ? jsonKeyMatch[1] : null,
    }
  })
}

// inferMissingProbe maps a MISSING action row to an HTTP {method, path}
// probe suitable for asserting the endpoint really stays unimplemented.
// Returns null for actions that are TUI-UX-only (search, copy, jump, help,
// etc.) where no plausible web endpoint would ever exist.
export function inferMissingProbe(row) {
  if (!row.isMissing) return null
  const a = row.action.toLowerCase()
  // Map matrix action label → probe path. Use sess-001 as the seeded id.
  const map = {
    'restart fresh':           { method: 'POST',   path: '/api/sessions/sess-001/restart-fresh' },
    'close session':           { method: 'POST',   path: '/api/sessions/sess-001/close' },
    'rename session':          { method: 'POST',   path: '/api/sessions/sess-001/rename' },
    'undo delete':             { method: 'POST',   path: '/api/sessions/undo-delete' },
    'move session to group':   { method: 'POST',   path: '/api/sessions/sess-001/group' },
    'attach mcp':              { method: 'POST',   path: '/api/sessions/sess-001/mcps/exa' },
    'detach mcp':              { method: 'DELETE', path: '/api/sessions/sess-001/mcps/exa' },
    'list mcps':               { method: 'GET',    path: '/api/sessions/sess-001/mcps' },
    'attach skill':            { method: 'POST',   path: '/api/sessions/sess-001/skills/x' },
    'detach skill':            { method: 'DELETE', path: '/api/sessions/sess-001/skills/x' },
    'list skills':             { method: 'GET',    path: '/api/sessions/sess-001/skills' },
    'edit session settings':   { method: 'PATCH',  path: '/api/sessions/sess-001' },
    'edit notes inline':       { method: 'POST',   path: '/api/sessions/sess-001/notes' },
    'finish worktree':         { method: 'POST',   path: '/api/sessions/sess-001/worktree/finish' },
    'mark session unread':     { method: 'POST',   path: '/api/sessions/sess-001/unread' },
  }
  return map[a] || null
}
