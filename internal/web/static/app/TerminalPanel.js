// TerminalPanel.js -- Preact component wrapping xterm.js 6.0.0 terminal lifecycle
// Ports createTerminalUI, connectWS, installTerminalTouchScroll from app.js
import { html } from 'htm/preact'
import { useEffect, useRef, useCallback, useState } from 'preact/hooks'
import { selectedIdSignal, authTokenSignal, wsStateSignal, readOnlySignal } from './state.js'
import { apiFetch } from './api.js'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebglAddon } from '@xterm/addon-webgl'
import { EmptyStateDashboard } from './EmptyStateDashboard.js'

// Mobile detection: pointer:coarse for touch devices
function isMobileDevice() {
  return typeof window.matchMedia === 'function' &&
    window.matchMedia('(pointer: coarse)').matches
}

// Build WebSocket URL for a session (same pattern as app.js wsURLForSession)
function wsURLForSession(sessionId, token) {
  const wsProto = window.location.protocol === 'https:' ? 'wss' : 'ws'
  const url = new URL(
    wsProto + '://' + window.location.host + '/ws/session/' + encodeURIComponent(sessionId)
  )
  if (token) url.searchParams.set('token', token)
  return url.toString()
}

// Install touch-to-scroll on the terminal container.
// PERF-E: takes the shared AbortController so every listener registered here
// is torn down by the single controller.abort() in the useEffect cleanup. No
// local dispose() return value is needed.
function installTouchScroll(container, xtermEl, controller) {
  if (!container || !xtermEl) return

  let active = false
  let lastY = 0

  function onTouchStart(event) {
    if (!event.touches || event.touches.length !== 1) return
    active = true
    lastY = event.touches[0].clientY
  }

  function onTouchMove(event) {
    if (!active || !event.touches || event.touches.length !== 1) return
    event.preventDefault()
    const y = event.touches[0].clientY
    const delta = lastY - y
    lastY = y
    if (xtermEl && delta !== 0) {
      xtermEl.dispatchEvent(
        new WheelEvent('wheel', {
          deltaY: delta,
          deltaMode: 0,
          bubbles: true,
          cancelable: true,
        })
      )
    }
  }

  function onTouchEnd() { active = false }

  container.addEventListener('touchstart', onTouchStart, { capture: true, passive: true, signal: controller.signal })
  container.addEventListener('touchmove', onTouchMove, { capture: true, passive: false, signal: controller.signal })
  container.addEventListener('touchend', onTouchEnd, { capture: true, passive: true, signal: controller.signal })
  container.addEventListener('touchcancel', onTouchEnd, { capture: true, passive: true, signal: controller.signal })
}

export function TerminalPanel() {
  const containerRef = useRef(null)
  const ctxRef = useRef(null)  // { terminal, fitAddon, ws, resizeObserver, controller, decoder, reconnectTimer, reconnectAttempt, wsReconnectEnabled, terminalAttached }
  const sessionId = selectedIdSignal.value
  // #782: terminal-fatal errors (e.g. TMUX_SESSION_NOT_FOUND) render as a
  // banner overlay rather than a `[error:CODE]` line on every WS reconnect.
  // null when there's no fatal error; an object { code, message, hint }
  // when one has been signalled by the server.
  const [fatalError, setFatalError] = useState(null)
  // #782 (codex review): bumping reconnectKey forces the main useEffect to
  // tear down the disabled-reconnect ctx and rebuild a fresh terminal +
  // WebSocket. Without this, after the user clicks "Restart session" the
  // banner clears but ctx.wsReconnectEnabled is stuck at false from the
  // earlier TMUX_SESSION_NOT_FOUND, and the terminal never reattaches.
  const [reconnectKey, setReconnectKey] = useState(0)

  // Signal vanilla app.js to suppress its terminal path while TerminalPanel is mounted
  useEffect(() => {
    window.__preactTerminalActive = true
    return () => { window.__preactTerminalActive = false }
  }, [])

  // Cleanup function: dispose terminal, close WS, remove observers.
  // PERF-E: a single controller.abort() detaches every event listener
  // registered inside the main useEffect (8 total: 4 touch, 1 window
  // resize, 4 ws).
  const cleanup = useCallback(() => {
    const ctx = ctxRef.current
    if (!ctx) return
    if (ctx.reconnectTimer) clearTimeout(ctx.reconnectTimer)
    if (ctx.ws) { ctx.ws.close(); ctx.ws = null }
    if (ctx.resizeObserver) ctx.resizeObserver.disconnect()
    if (ctx.controller) ctx.controller.abort()
    if (ctx.terminal) ctx.terminal.dispose()
    ctxRef.current = null
    wsStateSignal.value = 'disconnected'
  }, [])

  useEffect(() => {
    if (!containerRef.current || !sessionId) {
      cleanup()
      return
    }

    // Prevent double-init. Both sessionId AND reconnectKey are part of
    // the identity: bumping reconnectKey (after a successful Restart from
    // the #782 fatal banner) forces a fresh terminal + ws even though
    // sessionId is unchanged.
    if (
      ctxRef.current &&
      ctxRef.current.sessionId === sessionId &&
      ctxRef.current.reconnectKey === reconnectKey
    ) return
    cleanup()
    // #782: a fresh session connection clears any prior fatal banner.
    setFatalError(null)

    const container = containerRef.current
    const token = authTokenSignal.value
    const mobile = isMobileDevice()

    // Create Terminal
    const terminal = new Terminal({
      convertEol: false,
      cursorBlink: !mobile,
      disableStdin: false,
      fontFamily: 'IBM Plex Mono, Menlo, Consolas, monospace',
      fontSize: 13,
      scrollback: 10000,
      theme: {
        background: '#0a1220',
        foreground: '#d9e2ec',
        cursor: '#9ecbff',
      },
    })

    const fitAddon = new FitAddon()
    terminal.loadAddon(fitAddon)
    terminal.open(container)

    // WebGL renderer with canvas fallback
    try {
      const webglAddon = new WebglAddon()
      webglAddon.onContextLoss(() => {
        webglAddon.dispose()
        // CanvasAddon loaded as UMD global from <script src="/static/vendor/addon-canvas.js">
        if (typeof window.CanvasAddon !== 'undefined') {
          terminal.loadAddon(new window.CanvasAddon.CanvasAddon())
        }
        // xterm DOM renderer is the final fallback (built-in, always available)
      })
      terminal.loadAddon(webglAddon)
    } catch (_e) {
      // WebGL not available, try canvas
      if (typeof window.CanvasAddon !== 'undefined') {
        try {
          terminal.loadAddon(new window.CanvasAddon.CanvasAddon())
        } catch (_e2) { /* DOM renderer fallback */ }
      }
    }

    if (container.offsetWidth && container.offsetHeight) {
      fitAddon.fit()
    }

    // PERF-E: single AbortController for every listener registered in this
    // effect. Calling controller.abort() in the cleanup detaches all 8
    // listeners in one call -- replaces the previously incomplete manual
    // cleanup that only removed touchstart.
    const controller = new AbortController()

    // PERF-D: hint the browser to preload the WebGL addon in parallel with
    // the WebSocket open. The static import at the top of this file still
    // does the actual load — the <link rel="preload"> tag only nudges the
    // browser to fetch the bytes early rather than deferring until the
    // module graph walks to addon-webgl. Pitfall 5: we do NOT switch to a
    // dynamic import() here because that causes a renderer switch FOUC +
    // tmux byte race during the gap. Desktop only (mobile skips WebGL).
    // The preload <link> is bound to controller.signal so it is removed
    // when the terminal unmounts, preventing stale tags from accumulating
    // across session reconnects.
    if (!mobile && typeof document !== 'undefined') {
      const preloadLink = document.createElement('link')
      preloadLink.rel = 'preload'
      preloadLink.as = 'script'
      preloadLink.crossOrigin = 'anonymous'
      preloadLink.href = '/static/vendor/addon-webgl.mjs'
      document.head.appendChild(preloadLink)
      controller.signal.addEventListener('abort', () => {
        if (preloadLink.parentNode) preloadLink.parentNode.removeChild(preloadLink)
      })
    }

    // Context object for this session
    const ctx = {
      sessionId,
      reconnectKey, // #782: stamp the key so the double-init guard can detect a forced reconnect
      terminal,
      fitAddon,
      ws: null,
      resizeObserver: null,
      controller,
      decoder: new TextDecoder(),
      reconnectTimer: null,
      reconnectAttempt: 0,
      wsReconnectEnabled: true,
      terminalAttached: false,
    }
    ctxRef.current = ctx

    // Resize observer with debounce
    let resizeTimer = null
    function scheduleFitAndResize(delayMs) {
      clearTimeout(resizeTimer)
      resizeTimer = setTimeout(() => {
        if (!container.offsetWidth || !container.offsetHeight) return
        fitAddon.fit()
        const { cols, rows } = terminal
        if (cols >= 10 && rows >= 3 && ctx.ws && ctx.ws.readyState === WebSocket.OPEN && ctx.terminalAttached) {
          ctx.ws.send(JSON.stringify({ type: 'resize', cols, rows }))
        }
      }, delayMs)
    }

    if (typeof ResizeObserver === 'function') {
      const observer = new ResizeObserver(() => scheduleFitAndResize(90))
      observer.observe(container)
      ctx.resizeObserver = observer
    }

    // WEB-P1-1: Window resize fallback. ResizeObserver fires on container
    // changes, but the viewport can change without the immediate parent
    // resizing (devtools open, mobile soft keyboard, orientation change).
    // The window resize listener catches those cases. Registered on the
    // shared PERF-E AbortController so cleanup is a single controller.abort().
    window.addEventListener('resize', () => scheduleFitAndResize(120), {
      signal: controller.signal,
    })

    // Touch scrolling for mobile -- listeners attach via the shared
    // AbortController (PERF-E). No local dispose handle is needed.
    installTouchScroll(container, terminal.element, controller)

    // Keyboard input forwarding (desktop + mobile; server gates on ReadOnly).
    const inputDisposable = terminal.onData((data) => {
      if (!ctx.ws || ctx.ws.readyState !== WebSocket.OPEN || !ctx.terminalAttached || readOnlySignal.value) return
      ctx.ws.send(JSON.stringify({ type: 'input', data }))
    })

    // WSL2+Chrome paste fix: xterm.js 6.0's default paste path can fail and
    // destroy the system clipboard when focus is not on .xterm-helper-textarea.
    // Capture the paste on the container first, read clipboardData directly,
    // and forward through the same path as terminal.onData.
    if (!mobile) {
      container.addEventListener('paste', (event) => {
        if (readOnlySignal.value) return
        if (!ctx.ws || ctx.ws.readyState !== WebSocket.OPEN || !ctx.terminalAttached) return
        const cd = event.clipboardData
        if (!cd) return
        let text = cd.getData('text/plain')
        if (!text) return
        // Normalize CRLF/CR to LF; shells expect LF, bare CR re-runs input.
        text = text.replace(/\r\n?/g, '\n')
        event.preventDefault()
        event.stopPropagation()
        ctx.ws.send(JSON.stringify({ type: 'input', data: text }))
      }, { capture: true, signal: controller.signal })
    }

    terminal.writeln('Connecting to terminal...')

    // WebSocket connection
    function reconnectDelayMs(attempt) {
      const capped = Math.min(attempt, 8)
      return Math.min(8000, Math.round(350 * Math.pow(1.8, capped - 1)))
    }

    function scheduleReconnect() {
      if (!ctx.wsReconnectEnabled) return
      if (ctx.reconnectTimer || ctx.ws) return
      ctx.reconnectAttempt += 1
      const delay = reconnectDelayMs(ctx.reconnectAttempt)
      wsStateSignal.value = 'connecting'
      ctx.reconnectTimer = setTimeout(() => {
        ctx.reconnectTimer = null
        connectWS(true)
      }, delay)
    }

    function connectWS(reconnecting) {
      if (ctx.ws) { ctx.ws.close(); ctx.ws = null }
      ctx.terminalAttached = false
      ctx.wsReconnectEnabled = true
      wsStateSignal.value = 'connecting'

      const ws = new WebSocket(wsURLForSession(sessionId, token))
      ws.binaryType = 'arraybuffer'
      ctx.ws = ws

      // PERF-E: extract handlers so each addEventListener call stays compact
      // and the { signal: controller.signal } option sits within a few chars
      // of the call site. This is required by the structural regression spec
      // (bare-addEventListener scanner uses a 300-char window).
      function onWsOpen() {
        if (ctx.ws !== ws) return
        if (ctx.reconnectTimer) { clearTimeout(ctx.reconnectTimer); ctx.reconnectTimer = null }
        ctx.reconnectAttempt = 0
        wsStateSignal.value = 'connected'
        ws.send(JSON.stringify({ type: 'ping' }))
      }
      function onWsMessage(event) {
        if (ctx.ws !== ws) return
        if (typeof event.data === 'string') {
          try {
            const payload = JSON.parse(event.data)
            if (payload.type === 'status') {
              if (payload.event === 'connected') {
                readOnlySignal.value = !!payload.readOnly
                if (terminal) terminal.options.disableStdin = !!payload.readOnly
                wsStateSignal.value = 'connected'
              } else if (payload.event === 'terminal_attached') {
                ctx.terminalAttached = true
                scheduleFitAndResize(0)
              } else if (payload.event === 'session_closed') {
                ctx.terminalAttached = false
              }
            } else if (payload.type === 'error') {
              if (payload.code === 'TERMINAL_ATTACH_FAILED' || payload.code === 'TMUX_SESSION_NOT_FOUND') {
                ctx.terminalAttached = false
              }
              // #782: TMUX_SESSION_NOT_FOUND is terminal-fatal — the
              // session is gone, so reconnecting will just emit the same
              // error in a tight loop and spam the terminal. Stop the
              // reconnect cycle and surface a banner with the actionable
              // hint from the server.
              if (payload.code === 'TMUX_SESSION_NOT_FOUND') {
                ctx.wsReconnectEnabled = false
                setFatalError({
                  code: payload.code,
                  message: payload.message || 'tmux session is not available',
                  hint: payload.hint || '',
                })
                wsStateSignal.value = 'disconnected'
                return
              }
              terminal.write('\r\n[error:' + (payload.code || 'unknown') + '] ' + (payload.message || 'unknown error') + '\r\n')
            }
          } catch (_e) { /* ignore non-JSON control messages */ }
          return
        }
        if (event.data instanceof ArrayBuffer) {
          const text = ctx.decoder.decode(new Uint8Array(event.data), { stream: true })
          terminal.write(text)
        }
      }
      function onWsError() {
        if (ctx.ws !== ws) return
        wsStateSignal.value = 'error'
      }
      function onWsClose() {
        if (ctx.ws !== ws) return
        ctx.ws = null
        ctx.terminalAttached = false
        if (ctx.wsReconnectEnabled) {
          scheduleReconnect()
          return
        }
        wsStateSignal.value = 'disconnected'
      }

      ws.addEventListener('open', onWsOpen, { signal: controller.signal })
      ws.addEventListener('message', onWsMessage, { signal: controller.signal })
      ws.addEventListener('error', onWsError, { signal: controller.signal })
      ws.addEventListener('close', onWsClose, { signal: controller.signal })
    }

    connectWS(false)
    if (!mobile) terminal.focus()

    // Cleanup on unmount or sessionId change
    return () => {
      inputDisposable.dispose()
      clearTimeout(resizeTimer)
      cleanup()
    }
  }, [sessionId, reconnectKey, cleanup])

  if (!sessionId) {
    return html`<${EmptyStateDashboard} />`
  }

  // #782: actionable banner for terminal-fatal errors (currently only
  // TMUX_SESSION_NOT_FOUND). The xterm canvas stays mounted underneath so
  // the banner can be dismissed without losing terminal state, and the
  // user gets a one-click Restart action that calls the same endpoint as
  // the sidebar Restart icon.
  async function handleFatalRestart() {
    try {
      await apiFetch('POST', '/api/sessions/' + sessionId + '/restart')
      setFatalError(null)
      // #782 (codex review): bumping reconnectKey forces the main effect
      // to tear down the disabled-reconnect ctx and rebuild a fresh
      // terminal + WebSocket. Without this, ctx.wsReconnectEnabled stays
      // false from the prior TMUX_SESSION_NOT_FOUND and the terminal
      // never reattaches to the freshly-restarted tmux session.
      setReconnectKey((k) => k + 1)
    } catch (_e) {
      // Errors surface via the global toast layer; leave the banner up.
    }
  }

  // PR-B: outer chrome uses the bundle's `.term-frame` look.
  // The `.term-wrap` parent (provided by panes/TerminalPane.js) supplies
  // outer padding and flex sizing, so this component only renders the
  // inner frame + xterm canvas.
  return html`
    <div class="term-frame" style="position: relative;">
      <div class="term-strip">
        <span class="tdots"><i/><i/><i/></span>
        <span class="tpath">session · ${sessionId}</span>
        <span style="flex: 1;"/>
      </div>
      <div style="flex: 1; min-height: 0; min-width: 0; overflow: hidden; padding: 14px 16px;">
        <div ref=${containerRef} style="height: 100%; width: 100%; overflow: hidden;"/>
      </div>
      ${fatalError && html`
        <div role="alert"
             style=${{
               position: 'absolute', inset: '12px 12px auto 12px',
               border: '1px solid rgba(247,118,142,0.4)',
               background: 'rgba(22,22,30,0.95)',
               borderRadius: 'var(--radius-lg)',
               boxShadow: '0 30px 60px -20px rgba(0,0,0,0.55)',
               padding: '14px 16px',
             }}>
          <div style="display: flex; align-items: flex-start; gap: 12px;">
            <span style="color: var(--tn-red); font-size: 18px; line-height: 1;">⚠</span>
            <div style="flex: 1; min-width: 0;">
              <div style="font-weight: 600; color: var(--text-hi);">Terminal disconnected</div>
              <div style="font-size: 12.5px; color: var(--text); margin-top: 4px;">${fatalError.message}</div>
              ${fatalError.hint && html`<div style="font-size: 11.5px; color: var(--muted); margin-top: 6px;">${fatalError.hint}</div>`}
              <div style="display: flex; gap: 8px; margin-top: 10px;">
                <button type="button" class="btn primary" onClick=${handleFatalRestart}>Restart session</button>
                <button type="button" class="btn ghost" onClick=${() => setFatalError(null)}>Dismiss</button>
              </div>
            </div>
          </div>
        </div>
      `}
    </div>
  `
}
