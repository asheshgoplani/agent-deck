(function () {
  const menuRoot = document.getElementById("menu-root")
  const menuFilter = document.getElementById("menu-filter")
  const metaState = document.getElementById("meta-state")
  const terminalRoot = document.getElementById("terminal-root")

  const state = {
    snapshot: null,
    selectedSessionId: null,
    filter: "",
    authToken: readAuthTokenFromURL(),
    ws: null,
    wsSessionId: null,
    wsReconnectEnabled: false,
    reconnectTimer: null,
    reconnectAttempt: 0,
    menuEvents: null,
    terminalEvents: [],
    terminalAttached: false,
    terminalUI: null,
    decoder: new TextDecoder(),
    resizeTimer: null,
    readOnly: false,
    lastReadOnlyBlockAt: 0,
    connectionPhase: "idle",
    connectionDetail: "",
  }

  function readAuthTokenFromURL() {
    const params = new URLSearchParams(window.location.search || "")
    return String(params.get("token") || "").trim()
  }

  function apiPathWithToken(path) {
    if (!state.authToken) {
      return path
    }

    const url = new URL(path, window.location.origin)
    url.searchParams.set("token", state.authToken)
    return `${url.pathname}${url.search}`
  }

  function connectMenuEvents() {
    if (typeof window.EventSource !== "function") {
      return
    }
    if (state.menuEvents) {
      return
    }

    const source = new window.EventSource(apiPathWithToken("/events/menu"))
    state.menuEvents = source

    source.addEventListener("menu", (event) => {
      let snapshot
      try {
        snapshot = JSON.parse(event.data)
      } catch (_err) {
        return
      }
      if (!snapshot || !Array.isArray(snapshot.items)) {
        return
      }
      state.snapshot = snapshot
      renderMenu()
    })

    source.addEventListener("error", () => {
      // EventSource handles reconnection internally.
      // We only close and recreate on fatal auth/load errors via page reload.
    })
  }

  function disconnectMenuEvents() {
    if (!state.menuEvents) {
      return
    }
    state.menuEvents.close()
    state.menuEvents = null
  }

  function connectionLabel(phase) {
    switch (phase) {
      case "connecting":
        return "connecting"
      case "connected":
        return "connected"
      case "reconnecting":
        return "reconnecting"
      case "error":
        return "error"
      case "closed":
        return "closed"
      default:
        return "idle"
    }
  }

  function renderTopBarState() {
    const selected = findSessionById(state.selectedSessionId)
    const sessionLabel = selected
      ? selected.title || selected.id
      : "no session selected"
    const detailParts = [sessionLabel]
    if (state.readOnly) {
      detailParts.push("read-only")
    }
    if (state.connectionDetail) {
      detailParts.push(state.connectionDetail)
    }

    metaState.className = `meta state-${state.connectionPhase}`
    metaState.textContent = `${connectionLabel(state.connectionPhase)} | ${detailParts.join(" | ")}`
  }

  function setConnectionState(phase, detail) {
    state.connectionPhase = phase
    state.connectionDetail = detail || ""
    renderTopBarState()
  }

  async function loadMenu() {
    try {
      setConnectionState("idle", "loading menu")
      const headers = { Accept: "application/json" }
      if (state.authToken) {
        headers.Authorization = `Bearer ${state.authToken}`
      }

      const response = await fetch(apiPathWithToken("/api/menu"), {
        method: "GET",
        headers,
      })
      if (!response.ok) {
        throw new Error(`menu request failed: ${response.status}`)
      }

      state.snapshot = await response.json()
      applySelectionFromRoute()
      renderMenu()
      connectMenuEvents()
      if (!state.terminalUI) {
        setConnectionState("idle", "menu loaded")
      }
    } catch (error) {
      setConnectionState("error", "menu unavailable")
      menuRoot.textContent = `Failed to load menu: ${error.message}`
    }
  }

  function routeSessionID() {
    const path = window.location.pathname || "/"
    if (!path.startsWith("/s/")) {
      return ""
    }

    const raw = path.slice(3)
    if (!raw || raw.includes("/")) {
      return ""
    }

    try {
      return decodeURIComponent(raw)
    } catch (_err) {
      return ""
    }
  }

  function applySelectionFromRoute() {
    if (!state.snapshot || !Array.isArray(state.snapshot.items)) {
      return false
    }

    const id = routeSessionID()
    if (!id) {
      return false
    }

    const exists = !!findSessionById(id)
    if (!exists) {
      return false
    }

    state.selectedSessionId = id
    return true
  }

  function syncRouteToSelection(useReplace) {
    if (!state.selectedSessionId) {
      return
    }

    const targetPath = `/s/${encodeURIComponent(state.selectedSessionId)}${window.location.search || ""}`
    if (`${window.location.pathname}${window.location.search || ""}` === targetPath) {
      return
    }

    if (useReplace) {
      window.history.replaceState(
        { sessionId: state.selectedSessionId },
        "",
        targetPath,
      )
      return
    }

    window.history.pushState(
      { sessionId: state.selectedSessionId },
      "",
      targetPath,
    )
  }

  function normalize(text) {
    return String(text || "").toLowerCase()
  }

  function collectGroupAncestors(groupPath) {
    if (!groupPath) {
      return []
    }
    const parts = groupPath.split("/")
    const result = []
    for (let i = 0; i < parts.length; i += 1) {
      result.push(parts.slice(0, i + 1).join("/"))
    }
    return result
  }

  function computeVisibleGroups(items, query) {
    if (!query) {
      return null
    }

    const groups = new Set()
    for (const item of items) {
      if (item.type !== "session" || !item.session) {
        continue
      }
      if (!sessionMatches(item.session, query)) {
        continue
      }
      collectGroupAncestors(item.session.groupPath).forEach((path) =>
        groups.add(path),
      )
    }
    return groups
  }

  function sessionMatches(session, query) {
    const target = `${session.title || ""} ${session.tool || ""} ${session.status || ""} ${session.groupPath || ""} ${session.id || ""}`
    return normalize(target).includes(query)
  }

  function renderMenu() {
    const snapshot = state.snapshot
    if (!snapshot || !Array.isArray(snapshot.items)) {
      menuRoot.innerHTML = `<div class="menu-empty">No session data available.</div>`
      return
    }

    const query = normalize(state.filter.trim())
    const visibleGroups = computeVisibleGroups(snapshot.items, query)

    const fragment = document.createDocumentFragment()
    let visibleCount = 0
    let firstSessionId = null

    for (const item of snapshot.items) {
      if (item.type === "group" && item.group) {
        const groupName = normalize(item.group.name)
        const groupPath = item.group.path || ""
        const groupMatches = query ? groupName.includes(query) : true
        const hasMatchingChild =
          !query || (visibleGroups && visibleGroups.has(groupPath))
        if (!(groupMatches || hasMatchingChild)) {
          continue
        }
        fragment.appendChild(renderGroupRow(item))
        visibleCount += 1
        continue
      }

      if (item.type === "session" && item.session) {
        if (query && !sessionMatches(item.session, query)) {
          continue
        }
        if (!firstSessionId) {
          firstSessionId = item.session.id
        }
        fragment.appendChild(renderSessionRow(item))
        visibleCount += 1
      }
    }

    const selectedExists = !!findSessionById(state.selectedSessionId)
    if (!selectedExists) {
      state.selectedSessionId = firstSessionId
      if (firstSessionId) {
        syncRouteToSelection(true)
      }
    }

    if (visibleCount === 0) {
      menuRoot.innerHTML = `<div class="menu-empty">No matching sessions.</div>`
      renderTerminal(null)
      return
    }

    menuRoot.innerHTML = ""
    menuRoot.appendChild(fragment)

    const selected = findSessionById(state.selectedSessionId)
    renderTopBarState()
    renderTerminal(selected)
  }

  function renderGroupRow(item) {
    const btn = document.createElement("button")
    btn.type = "button"
    btn.className = "menu-item group"
    btn.disabled = true

    const row = document.createElement("div")
    row.className = "menu-row"

    const indent = document.createElement("span")
    indent.className = "menu-indent"
    indent.style.setProperty("--level", String(item.level || 0))

    const marker = document.createElement("span")
    marker.textContent = item.group.expanded ? "▾" : "▸"

    const name = document.createElement("span")
    name.textContent = item.group.name || item.path || "group"

    const count = document.createElement("span")
    count.className = "group-count"
    count.textContent = `(${item.group.sessionCount || 0})`

    row.appendChild(indent)
    row.appendChild(marker)
    row.appendChild(name)
    row.appendChild(count)
    btn.appendChild(row)
    return btn
  }

  function renderSessionRow(item) {
    const session = item.session
    const isSelected = state.selectedSessionId === session.id

    const btn = document.createElement("button")
    btn.type = "button"
    btn.className = `menu-item session${isSelected ? " selected" : ""}`
    btn.addEventListener("click", () => {
      selectSession(session.id, true)
    })

    const row = document.createElement("div")
    row.className = "menu-row"

    const indent = document.createElement("span")
    indent.className = "menu-indent"
    indent.style.setProperty("--level", String(item.level || 0))

    const status = document.createElement("span")
    status.className = `status-dot status-${normalize(session.status)}`

    const title = document.createElement("span")
    title.className = "session-title"
    title.textContent = session.title || session.id || "session"

    const tool = document.createElement("span")
    tool.className = "tool-badge"
    tool.textContent = session.tool || "shell"

    row.appendChild(indent)
    row.appendChild(status)
    row.appendChild(title)
    row.appendChild(tool)
    btn.appendChild(row)
    return btn
  }

  function findSessionById(sessionId) {
    if (!sessionId || !state.snapshot || !Array.isArray(state.snapshot.items)) {
      return null
    }
    for (const item of state.snapshot.items) {
      if (item.type !== "session" || !item.session) {
        continue
      }
      if (item.session.id === sessionId) {
        return item.session
      }
    }
    return null
  }

  function selectSession(sessionId, updatePath) {
    if (!sessionId) {
      return
    }
    state.selectedSessionId = sessionId

    if (updatePath) {
      syncRouteToSelection(false)
    }

    renderMenu()
  }

  function renderTerminal(session) {
    if (!session) {
      disconnectWS({ intentional: true })
      destroyTerminalUI()
      state.terminalEvents = []
      state.terminalAttached = false
      terminalRoot.className = "terminal-placeholder"
      terminalRoot.textContent =
        "Select a session from the menu to start terminal streaming."
      setConnectionState("idle", "menu ready")
      return
    }

    if (!state.terminalUI || state.terminalUI.sessionId !== session.id) {
      state.terminalEvents = []
      state.terminalAttached = false
      state.decoder = new TextDecoder()
      createTerminalUI(session.id)
    }

    const infoText = `Selected session: ${session.title || session.id} (${session.id}) | tool=${session.tool || "shell"} | status=${session.status || "unknown"}`
    state.terminalUI.info.textContent = infoText
    renderTerminalEvents()
    renderTopBarState()

    connectWS(session.id)
  }

  function createTerminalUI(sessionId) {
    destroyTerminalUI()

    terminalRoot.className = ""
    terminalRoot.innerHTML = ""

    const shell = document.createElement("div")
    shell.className = "terminal-shell"

    const info = document.createElement("div")
    info.className = "terminal-session"

    const modeBanner = document.createElement("div")
    modeBanner.className = "terminal-mode-banner"
    modeBanner.hidden = true
    modeBanner.textContent = "READ-ONLY MODE: input is disabled"

    const canvas = document.createElement("div")
    canvas.className = "terminal-canvas"

    const events = document.createElement("div")
    events.className = "terminal-events"

    shell.appendChild(info)
    shell.appendChild(modeBanner)
    shell.appendChild(canvas)
    shell.appendChild(events)
    terminalRoot.appendChild(shell)

    const ui = {
      sessionId,
      shell,
      info,
      modeBanner,
      canvas,
      events,
      terminal: null,
      fitAddon: null,
      terminalDispose: null,
      fallbackPre: null,
      resizeObserver: null,
      touchDispose: null,
    }

    const hasXterm =
      typeof window.Terminal === "function" &&
      window.FitAddon &&
      typeof window.FitAddon.FitAddon === "function"

    if (!hasXterm) {
      const pre = document.createElement("pre")
      pre.className = "terminal-fallback"
      pre.textContent =
        "Terminal emulator not available. Check xterm.js assets.\n"
      canvas.appendChild(pre)
      ui.fallbackPre = pre
      state.terminalUI = ui
      applyReadOnlyModeToTerminal()
      return
    }

    const terminal = new window.Terminal({
      convertEol: false,
      cursorBlink: true,
      fontFamily: "IBM Plex Mono, Menlo, Consolas, monospace",
      fontSize: 13,
      scrollback: 10000,
      theme: {
        background: "#0a1220",
        foreground: "#d9e2ec",
        cursor: "#9ecbff",
      },
    })

    const fitAddon = new window.FitAddon.FitAddon()
    terminal.loadAddon(fitAddon)
    terminal.open(canvas)
    fitAddon.fit()
    ui.terminal = terminal
    ui.touchDispose = installTerminalTouchScroll(ui)

    if (typeof window.ResizeObserver === "function") {
      const observer = new window.ResizeObserver(() => {
        scheduleFitAndResize(90)
      })
      observer.observe(canvas)
      ui.resizeObserver = observer
    }

    const inputDisposable = terminal.onData((data) => {
      sendInput(data)
    })

    ui.fitAddon = fitAddon
    ui.terminalDispose = () => {
      if (ui.resizeObserver) {
        ui.resizeObserver.disconnect()
      }
      if (ui.touchDispose) {
        ui.touchDispose()
      }
      inputDisposable.dispose()
      terminal.dispose()
    }

    state.terminalUI = ui
    applyReadOnlyModeToTerminal()
    terminal.writeln("Connecting to terminal...")
    terminal.focus()
  }

  function applyReadOnlyModeToTerminal() {
    if (!state.terminalUI) {
      return
    }

    if (state.terminalUI.modeBanner) {
      state.terminalUI.modeBanner.hidden = !state.readOnly
    }

    if (state.terminalUI.terminal) {
      state.terminalUI.terminal.options.disableStdin = state.readOnly
    }
  }

  function installTerminalTouchScroll(ui) {
    if (!ui || !ui.terminal || !ui.canvas) {
      return null
    }

    // Convert touch gestures into synthetic wheel events dispatched on the
    // xterm container element.  This is the exact same code path that desktop
    // mouse-wheel scrolling takes: xterm's Viewport class listens for 'wheel'
    // on the container, updates viewport.scrollTop, and re-renders.
    //
    // We listen on the canvas wrapper in capture phase because touches land
    // on .xterm-screen (a sibling of .xterm-viewport, not a child), and
    // xterm's own touch-to-mouse handler may call stopPropagation().
    const target = ui.canvas
    const xtermEl = ui.terminal.element
    let active = false
    let lastY = 0

    function onTouchStart(event) {
      if (!event.touches || event.touches.length !== 1) {
        return
      }
      active = true
      lastY = event.touches[0].clientY
    }

    function onTouchMove(event) {
      if (!active || !event.touches || event.touches.length !== 1) {
        return
      }

      event.preventDefault()

      const y = event.touches[0].clientY
      const delta = lastY - y // positive = finger moved up = scroll content down
      lastY = y

      if (xtermEl && delta !== 0) {
        xtermEl.dispatchEvent(
          new WheelEvent("wheel", {
            deltaY: delta,
            deltaMode: 0,
            bubbles: true,
            cancelable: true,
          }),
        )
      }
    }

    function onTouchEnd() {
      active = false
    }

    target.addEventListener("touchstart", onTouchStart, {
      capture: true,
      passive: true,
    })
    target.addEventListener("touchmove", onTouchMove, {
      capture: true,
      passive: false,
    })
    target.addEventListener("touchend", onTouchEnd, {
      capture: true,
      passive: true,
    })
    target.addEventListener("touchcancel", onTouchEnd, {
      capture: true,
      passive: true,
    })

    return function dispose() {
      target.removeEventListener("touchstart", onTouchStart, { capture: true })
      target.removeEventListener("touchmove", onTouchMove, { capture: true })
      target.removeEventListener("touchend", onTouchEnd, { capture: true })
      target.removeEventListener("touchcancel", onTouchEnd, { capture: true })
    }
  }

  function destroyTerminalUI() {
    if (!state.terminalUI) {
      return
    }

    if (state.terminalUI.terminalDispose) {
      state.terminalUI.terminalDispose()
    }

    state.terminalUI = null
  }

  function sendInput(data) {
    if (
      !data ||
      !state.ws ||
      state.ws.readyState !== WebSocket.OPEN ||
      !state.terminalAttached ||
      state.readOnly
    ) {
      if (data && state.readOnly) {
        const now = Date.now()
        if (now - state.lastReadOnlyBlockAt > 1200) {
          state.lastReadOnlyBlockAt = now
          addTerminalEvent("read-only: input blocked")
        }
      }
      return
    }

    state.ws.send(
      JSON.stringify({
        type: "input",
        data,
      }),
    )
  }

  function wsURLForSession(sessionId) {
    const wsProto = window.location.protocol === "https:" ? "wss" : "ws"
    const url = new URL(
      `${wsProto}://${window.location.host}/ws/session/${encodeURIComponent(
        sessionId,
      )}`,
    )
    if (state.authToken) {
      url.searchParams.set("token", state.authToken)
    }
    return url.toString()
  }

  function addTerminalEvent(text) {
    state.terminalEvents.push(text)
    if (state.terminalEvents.length > 80) {
      state.terminalEvents = state.terminalEvents.slice(-80)
    }
    renderTerminalEvents()
  }

  function renderTerminalEvents() {
    if (!state.terminalUI) {
      return
    }

    const lines = state.terminalEvents.slice(-8)
    state.terminalUI.events.textContent =
      lines.length > 0 ? `Events: ${lines.join(" | ")}` : "Events: waiting"
  }

  function scheduleFitAndResize(delayMs) {
    if (state.resizeTimer) {
      clearTimeout(state.resizeTimer)
    }
    state.resizeTimer = setTimeout(() => {
      fitTerminalCanvas()
      sendResize()
    }, delayMs)
  }

  function clearReconnectTimer() {
    if (!state.reconnectTimer) {
      return
    }
    clearTimeout(state.reconnectTimer)
    state.reconnectTimer = null
  }

  function reconnectDelayMs(attempt) {
    const cappedAttempt = Math.min(attempt, 8)
    return Math.min(8000, Math.round(350 * Math.pow(1.8, cappedAttempt - 1)))
  }

  function scheduleReconnect(sessionId) {
    if (!state.wsReconnectEnabled) {
      return
    }
    if (!sessionId || state.selectedSessionId !== sessionId) {
      return
    }
    if (state.reconnectTimer || state.ws) {
      return
    }

    state.reconnectAttempt += 1
    const delay = reconnectDelayMs(state.reconnectAttempt)
    setConnectionState(
      "reconnecting",
      `retry ${state.reconnectAttempt} in ${(delay / 1000).toFixed(1)}s`,
    )

    state.reconnectTimer = setTimeout(() => {
      state.reconnectTimer = null
      connectWS(sessionId, { reconnecting: true })
    }, delay)
  }

  function appendTerminalOutput(text) {
    if (!text || !state.terminalUI) {
      return
    }

    if (state.terminalUI.fallbackPre) {
      state.terminalUI.fallbackPre.textContent += text
      const maxChars = 250000
      if (state.terminalUI.fallbackPre.textContent.length > maxChars) {
        state.terminalUI.fallbackPre.textContent =
          state.terminalUI.fallbackPre.textContent.slice(-maxChars)
      }
      state.terminalUI.fallbackPre.scrollTop =
        state.terminalUI.fallbackPre.scrollHeight
      return
    }

    if (state.terminalUI.terminal) {
      state.terminalUI.terminal.write(text)
    }
  }

  function disconnectWS(options) {
    const opts = options || {}
    const intentional = opts.intentional !== false

    if (intentional) {
      state.wsReconnectEnabled = false
      clearReconnectTimer()
    }

    if (!state.ws) {
      if (intentional) {
        state.terminalAttached = false
      }
      return
    }

    const current = state.ws
    state.ws = null
    state.wsSessionId = null
    current.close()

    if (intentional) {
      state.terminalAttached = false
    }
  }

  function connectWS(sessionId, options) {
    const opts = options || {}
    if (!sessionId) {
      return
    }
    if (state.ws && state.wsSessionId === sessionId) {
      return
    }

    clearReconnectTimer()
    disconnectWS({ intentional: true })
    state.wsSessionId = sessionId
    state.terminalAttached = false
    state.wsReconnectEnabled = true

    const ws = new WebSocket(wsURLForSession(sessionId))
    ws.binaryType = "arraybuffer"
    state.ws = ws

    setConnectionState(
      opts.reconnecting ? "reconnecting" : "connecting",
      opts.reconnecting
        ? `retry ${state.reconnectAttempt}`
        : "opening websocket",
    )
    addTerminalEvent("socket connecting")

    ws.addEventListener("open", () => {
      if (state.ws !== ws) {
        return
      }
      clearReconnectTimer()
      state.reconnectAttempt = 0
      addTerminalEvent("socket open")
      setConnectionState("connected", "socket open")
      ws.send(JSON.stringify({ type: "ping" }))
    })

    ws.addEventListener("message", (event) => {
      if (state.ws !== ws) {
        return
      }

      if (typeof event.data === "string") {
        handleControlPayload(event.data)
        return
      }

      if (event.data instanceof ArrayBuffer) {
        const text = state.decoder.decode(new Uint8Array(event.data), {
          stream: true,
        })
        appendTerminalOutput(text)
      }
    })

    ws.addEventListener("error", () => {
      if (state.ws !== ws) {
        return
      }
      addTerminalEvent("socket error")
      setConnectionState("error", "socket error")
    })

    ws.addEventListener("close", () => {
      if (state.ws !== ws) {
        return
      }
      addTerminalEvent("socket closed")
      state.ws = null
      state.wsSessionId = null
      state.terminalAttached = false
      if (state.wsReconnectEnabled && state.selectedSessionId === sessionId) {
        scheduleReconnect(sessionId)
        return
      }
      setConnectionState("closed", "socket closed")
    })
  }

  function handleControlPayload(raw) {
    try {
      const payload = JSON.parse(raw)
      if (payload.type === "status") {
        addTerminalEvent(`status:${payload.event || "unknown"}`)
        if (payload.event === "connected") {
          state.readOnly = !!payload.readOnly
          applyReadOnlyModeToTerminal()
          setConnectionState("connected", "session connected")
        } else if (payload.event === "ready") {
          setConnectionState("connected", "ready")
        } else if (payload.event === "pong") {
          setConnectionState("connected", "alive")
        } else if (payload.event === "terminal_attached") {
          setConnectionState("connected", "terminal attached")
        } else if (payload.event === "session_closed") {
          setConnectionState("closed", "session closed")
        } else {
          setConnectionState("connected", payload.event || "status")
        }

        if (payload.event === "terminal_attached") {
          state.terminalAttached = true
          scheduleFitAndResize(0)
        }
        if (payload.event === "session_closed") {
          state.terminalAttached = false
        }
        return
      }

      if (payload.type === "error") {
        addTerminalEvent(`error:${payload.code || "unknown"}`)
        setConnectionState("error", payload.code || "terminal error")

        if (
          payload.code === "TERMINAL_ATTACH_FAILED" ||
          payload.code === "TMUX_SESSION_NOT_FOUND"
        ) {
          state.terminalAttached = false
        }

        appendTerminalOutput(
          `\r\n[error:${payload.code || "unknown"}] ${payload.message || "unknown error"}\r\n`,
        )
        return
      }

      addTerminalEvent(`message:${raw}`)
    } catch (_err) {
      addTerminalEvent(`raw:${raw}`)
    }
  }

  function fitTerminalCanvas() {
    if (
      !state.terminalUI ||
      !state.terminalUI.terminal ||
      !state.terminalUI.fitAddon
    ) {
      return
    }

    state.terminalUI.fitAddon.fit()
  }

  function estimateTerminalSize() {
    if (!state.terminalUI) {
      return null
    }

    if (state.terminalUI.terminal) {
      const cols = state.terminalUI.terminal.cols
      const rows = state.terminalUI.terminal.rows
      if (cols > 0 && rows > 0) {
        return { cols, rows }
      }
    }

    const rect = state.terminalUI.canvas.getBoundingClientRect()
    const cols = Math.max(20, Math.floor(rect.width / 8))
    const rows = Math.max(8, Math.floor(rect.height / 18))
    return { cols, rows }
  }

  function sendResize() {
    if (
      !state.ws ||
      state.ws.readyState !== WebSocket.OPEN ||
      !state.terminalAttached
    ) {
      return
    }

    const size = estimateTerminalSize()
    if (!size) {
      return
    }

    state.ws.send(
      JSON.stringify({
        type: "resize",
        cols: size.cols,
        rows: size.rows,
      }),
    )
  }

  window.addEventListener("resize", () => {
    scheduleFitAndResize(120)
  })

  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState !== "visible") {
      return
    }
    scheduleFitAndResize(0)
    if (!state.ws && state.selectedSessionId) {
      connectWS(state.selectedSessionId, { reconnecting: true })
    }
  })

  window.addEventListener("popstate", () => {
    const ok = applySelectionFromRoute()
    if (!ok) {
      state.selectedSessionId = null
    }
    renderMenu()
  })

  window.addEventListener("beforeunload", () => {
    disconnectMenuEvents()
  })

  menuFilter.addEventListener("input", (event) => {
    state.filter = event.target.value || ""
    renderMenu()
  })

  loadMenu()
})()
