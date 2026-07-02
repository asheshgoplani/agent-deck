// panes/artifactConsole.js -- Pure helpers for the Fleet Console artifact pane.
//
// These are deliberately DOM-free so the selection-relay protocol and the
// owner-resolution shown before send can be unit-tested headlessly (the
// web-change test gate). The pane (ArtifactPane.js) wires them to Preact.

// The message type the server-injected relay postMessages on every selection.
// Kept in lockstep with internal/web/handlers_artifacts.go (injectSelectionRelay).
export const SELECTION_MESSAGE_TYPE = 'fleet-artifact-selection'

// parseSelectionMessage validates a postMessage from the artifact iframe and,
// for the currently-open artifact, returns the highlighted text + rect. Returns
// null for anything that isn't a usable selection from the expected artifact —
// cross-iframe selections can't bubble, so this relayed message is the ONLY way
// the parent learns what the operator highlighted.
export function parseSelectionMessage(event, expectedPath) {
  const d = event && event.data
  if (!d || d.type !== SELECTION_MESSAGE_TYPE) return null
  // Ignore stray messages from a different artifact than the one in view.
  if (expectedPath && d.path && d.path !== expectedPath) return null
  const text = typeof d.text === 'string' ? d.text.trim() : ''
  if (!text) return null
  return { text, rect: d.rect || null, path: d.path || expectedPath || '' }
}

// ownerLabel is the resolved-target label shown BEFORE send, so routing is
// never a surprise. Sidecar session wins; else the owning conductor (still
// routable — never a dead end); else a prompt to pick.
export function ownerLabel(entry) {
  if (!entry) return 'choose a session'
  if (entry.sessionId) return entry.sessionId
  if (entry.conductor) return 'conductor-' + entry.conductor
  return 'choose a session'
}

// commentBody builds the POST /api/artifacts/comment payload. The server
// re-resolves the owner authoritatively from the sidecar at `path`; the client
// never asserts the target.
export function commentBody(entry, excerpt, comment) {
  return {
    path: entry ? entry.path : '',
    excerpt: excerpt || '',
    comment: comment || '',
  }
}

// serveUrl is the confined read-only artifact URL loaded into the sandbox
// iframe. A token is carried as a query param because an iframe src cannot set
// an Authorization header.
export function serveUrl(entry, token) {
  if (!entry || !entry.path) return ''
  let u = '/api/artifacts/serve?path=' + encodeURIComponent(entry.path)
  if (token) u += '&token=' + encodeURIComponent(token)
  return u
}
