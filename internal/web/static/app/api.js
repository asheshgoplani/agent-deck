// api.js -- Shared fetch helper for mutation API calls
// Applies auth token from state.js and handles JSON parsing uniformly.
import { authTokenSignal } from './state.js'
import { addToast } from './Toast.js'

// authHeaders returns the headers every /api/ request must carry.
//
// The bearer token lives only in authTokenSignal (main.js reads it from the
// URL and strips it), so any call site that hand-rolls its own fetch silently
// drops it and 401s the moment the server is started with --token/--token-file.
// That is exactly how the MCP pane broke: it had a private copy of this helper
// that never grew the Authorization header. Every /api/ caller must go through
// apiFetch or at minimum spread authHeaders() — see
// TestAppScriptsUseTheSharedAuthenticatedFetch for the regression gate.
export function authHeaders(extra) {
  const headers = { 'Content-Type': 'application/json', 'Accept': 'application/json', ...extra }
  const token = authTokenSignal.value
  if (token) headers['Authorization'] = 'Bearer ' + token
  return headers
}

export async function apiFetch(method, path, body) {
  const headers = authHeaders()
  let res
  try {
    res = await fetch(path, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
    })
  } catch (err) {
    const msg = 'Network error: ' + (err.message || 'request failed')
    addToast(msg)
    throw new Error(msg)
  }
  const data = await res.json()
  if (!res.ok) {
    const msg = data?.error?.message || res.statusText
    // Only show toast for mutation errors (not GET requests, which are often background)
    if (method !== 'GET') addToast(msg)
    throw new Error(msg)
  }
  return data
}
