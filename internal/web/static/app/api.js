// api.js -- Shared fetch helper for mutation API calls
// Applies auth token from state.js and handles JSON parsing uniformly.
import { authTokenSignal } from './state.js'

export async function apiFetch(method, path, body) {
  const headers = { 'Content-Type': 'application/json', 'Accept': 'application/json' }
  const token = authTokenSignal.value
  if (token) headers['Authorization'] = 'Bearer ' + token
  const res = await fetch(path, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  })
  const data = await res.json()
  if (!res.ok) throw new Error(data?.error?.message || res.statusText)
  return data
}
