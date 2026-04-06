// api.js -- Shared fetch helper for mutation API calls
// Applies auth token from state.js and handles JSON parsing uniformly.
import { authTokenSignal } from './state.js'
import { addToast } from './Toast.js'

export async function apiFetch(method, path, body) {
  const headers = { 'Content-Type': 'application/json', 'Accept': 'application/json' }
  const token = authTokenSignal.value
  if (token) headers['Authorization'] = 'Bearer ' + token
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
