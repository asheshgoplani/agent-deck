// terminalReconnect.js -- when a halted web terminal reattaches (#2432).
//
// TMUX_SESSION_NOT_FOUND stops TerminalPanel's reconnect loop (#782). Once
// the session runs again the open terminal must reattach by itself, whether
// it was started from the banner, the header, the sidebar or the CLI.
import { signal } from '@preact/signals'

// A tmux pane exists in these states. `starting` is left out on purpose:
// tmux may not exist yet, and the step on to running/waiting/idle follows.
const LIVE = new Set(['running', 'waiting', 'idle'])

export function isLiveStatus(status) {
  return LIVE.has(status)
}

// The status of session `id` in the raw SSE menu items, or '' when absent.
export function sessionStatus(items, id) {
  for (const it of items || []) {
    if (it && it.type === 'session' && it.session && it.session.id === id) {
      return it.session.status || ''
    }
  }
  return ''
}

// Bumped whenever a start or restart POST succeeds in this tab.
export const sessionStartedSignal = signal({ id: null, seq: 0 })

export function noteSessionStarted(id) {
  sessionStartedSignal.value = { id, seq: sessionStartedSignal.value.seq + 1 }
}

// One observation of session `id`: its status, and the seq of the last
// start/restart that was for it (a start for another session keeps `prev`).
export function observeSession(items, started, id, prev) {
  return {
    status: sessionStatus(items, id),
    started: started.id === id ? started.seq : (prev ? prev.started : 0),
  }
}

// shouldReattach compares two observations of the open session. Only a
// halted terminal reattaches: when the session turned live, or a
// start/restart for it succeeded, since the previous observation.
export function shouldReattach(prev, next, halted) {
  if (!halted) return false
  if (next.started !== prev.started) return true
  return isLiveStatus(next.status) && !isLiveStatus(prev.status)
}
