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

// Install touch-to-scroll on the terminal container
// Ported verbatim from app.js installTerminalTouchScroll
function installTouchScroll(container, xtermEl) {
  if (!container || !xtermEl) return null

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

  container.addEventListener('touchstart', onTouchStart, { capture: true, passive: true })
  container.addEventListener('touchmove', onTouchMove, { capture: true, passive: false })
  container.addEventListener('touchend', onTouchEnd, { capture: true, passive: true })
  container.addEventListener('touchcancel', onTouchEnd, { capture: true, passive: true })

  return function dispose() {
    container.removeEventListener('touchstart', onTouchStart, { capture: true })
    container.removeEventListener('touchmove', onTouchMove, { capture: true })
    container.removeEventListener('touchend', onTouchEnd, { capture: true })
    container.removeEventListener('touchcancel', onTouchEnd, { capture: true })
  }
}

export function TerminalPanel() {
  const containerRef = useRef(null)
  const ctxRef = useRef(null)  // { terminal, fitAddon, ws, resizeObserver, touchDispose, decoder, reconnectTimer, reconnectAttempt, wsReconnectEnabled, terminalAttached }
  const sessionId = selectedIdSignal.value
  const isMobile = isMobileDevice()

  // Signal vanilla app.js to suppress its terminal path while TerminalPanel is mounted
  useEffect(() => {
    window.__preactTerminalActive = true
    return () => { window.__preactTerminalActive = false }
  }, [])

  // Cleanup function: dispose terminal, close WS, remove observers
  const cleanup = useCallback(() => {
    const ctx = ctxRef.current
    if (!ctx) return
    if (ctx.reconnectTimer) clearTimeout(ctx.reconnectTimer)
    if (ctx.ws) { ctx.ws.close(); ctx.ws = null }
    if (ctx.resizeObserver) ctx.resizeObserver.disconnect()
    if (ctx.touchDispose) ctx.touchDispose()
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
      disableStdin: mobile,
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

    // Context object for this session
    const ctx = {
      sessionId,
      terminal,
      fitAddon,
      ws: null,
      resizeObserver: null,
      touchDispose: null,
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

    // Touch scrolling for mobile
    ctx.touchDispose = installTouchScroll(container, terminal.element)

    // Keyboard input forwarding (desktop only)
    let inputDisposable = null
    if (!mobile) {
      inputDisposable = terminal.onData((data) => {
        if (!ctx.ws || ctx.ws.readyState !== WebSocket.OPEN || !ctx.terminalAttached || readOnlySignal.value) return
        ctx.ws.send(JSON.stringify({ type: 'input', data }))
      })
    }

    // Prevent mobile soft keyboard by blocking touch-focus on the hidden textarea
    if (mobile) {
      container.addEventListener('touchstart', (e) => { e.preventDefault() }, { passive: false })
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

      ws.addEventListener('open', () => {
        if (ctx.ws !== ws) return
        if (ctx.reconnectTimer) { clearTimeout(ctx.reconnectTimer); ctx.reconnectTimer = null }
        ctx.reconnectAttempt = 0
        wsStateSignal.value = 'connected'
        ws.send(JSON.stringify({ type: 'ping' }))
      })

      ws.addEventListener('message', (event) => {
        if (ctx.ws !== ws) return
        if (typeof event.data === 'string') {
          try {
            const payload = JSON.parse(event.data)
            if (payload.type === 'status') {
              if (payload.event === 'connected') {
                readOnlySignal.value = !!payload.readOnly
                if (terminal) terminal.options.disableStdin = !!payload.readOnly || mobile
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
      })

      ws.addEventListener('error', () => {
        if (ctx.ws !== ws) return
        wsStateSignal.value = 'error'
      })

      ws.addEventListener('close', () => {
        if (ctx.ws !== ws) return
        ctx.ws = null
        ctx.terminalAttached = false
        if (ctx.wsReconnectEnabled) {
          scheduleReconnect()
          return
        }
        wsStateSignal.value = 'disconnected'
      })
    }

    connectWS(false)
    if (!mobile) terminal.focus()

    // Cleanup on unmount or sessionId change
    return () => {
      if (inputDisposable) inputDisposable.dispose()
      clearTimeout(resizeTimer)
      cleanup()
    }
  }, [sessionId, cleanup])

  if (!sessionId) {
    return html`<${EmptyStateDashboard} />`
  }

  return html`
    <div class="flex flex-col h-full">
      ${isMobile && html`
        <div class="px-3 py-1.5 text-xs font-medium text-center
                     dark:bg-tn-yellow/20 dark:text-tn-yellow
                     bg-yellow-100 text-yellow-800
                     border-b dark:border-tn-muted/20 border-yellow-200">
          READ-ONLY: terminal input is disabled on mobile
        </div>
      `}
      <div ref=${containerRef} class="flex-1 min-h-0 overflow-hidden" />
    </div>
  `
}
