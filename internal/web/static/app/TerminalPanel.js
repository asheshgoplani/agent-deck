// TerminalPanel.js -- Preact component wrapping xterm.js 6.0.0 terminal lifecycle
// Ports createTerminalUI, connectWS, installTerminalTouchScroll from app.js
import { html } from 'htm/preact'
import { useEffect, useRef, useCallback } from 'preact/hooks'
import { selectedIdSignal, authTokenSignal, wsStateSignal, readOnlySignal } from './state.js'
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

    // Prevent double-init
    if (ctxRef.current && ctxRef.current.sessionId === sessionId) return
    cleanup()

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

    fitAddon.fit()

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
        fitAddon.fit()
        const { cols, rows } = terminal
        if (cols > 1 && rows > 0 && ctx.ws && ctx.ws.readyState === WebSocket.OPEN && ctx.terminalAttached) {
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
  }, [sessionId, cleanup])

  if (!sessionId) {
    return html`<${EmptyStateDashboard} />`
  }

  return html`
    <div class="flex flex-col h-full">
      <div class="flex-1 min-h-0 min-w-0 p-sp-16 overflow-hidden">
        <div ref=${containerRef} class="h-full w-full overflow-hidden" />
      </div>
    </div>
  `
}
