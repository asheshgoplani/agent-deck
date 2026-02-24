(function () {
  "use strict"

  // ── State ───────────────────────────────────────────────────────
  var state = {
    tasks: [],
    projects: [],
    selectedTaskId: null,
    activeView: "agents",
    projectFilter: "",
    authToken: readAuthTokenFromURL(),
    menuEvents: null,
    terminal: null,
    terminalWs: null,
    fitAddon: null,
    chatMode: null,
    chatModeOverride: null,
  }

  // ── Status metadata ─────────────────────────────────────────────
  var AGENT_STATUS_META = {
    thinking: { icon: "\u25CF", label: "Thinking", color: "var(--orange)" },
    waiting:  { icon: "\u25D0", label: "Input needed", color: "var(--orange)" },
    running:  { icon: "\u27F3", label: "Running", color: "var(--blue)" },
    idle:     { icon: "\u25CB", label: "Idle", color: "var(--text-dim)" },
    error:    { icon: "\u2715", label: "Error", color: "var(--red)" },
    complete: { icon: "\u2713", label: "Complete", color: "var(--green)" },
  }

  var TASK_STATUS_COLORS = {
    backlog:  "var(--text-dim)",
    planning: "var(--phase-plan)",
    running:  "var(--phase-execute)",
    review:   "var(--phase-review)",
    done:     "var(--phase-done)",
  }

  var PHASES = ["brainstorm", "plan", "execute", "review"]
  var PHASE_DOT_LABELS = { brainstorm: "B", plan: "P", execute: "E", review: "R" }

  // ── Auth ──────────────────────────────────────────────────────────
  function readAuthTokenFromURL() {
    var params = new URLSearchParams(window.location.search || "")
    return String(params.get("token") || "").trim()
  }

  function apiPathWithToken(path) {
    if (!state.authToken) return path
    var url = new URL(path, window.location.origin)
    url.searchParams.set("token", state.authToken)
    return url.pathname + url.search
  }

  function authHeaders() {
    var h = { Accept: "application/json" }
    if (state.authToken) h.Authorization = "Bearer " + state.authToken
    return h
  }

  // ── Helpers: safe DOM construction ────────────────────────────────
  function el(tag, className, textContent) {
    var node = document.createElement(tag)
    if (className) node.className = className
    if (textContent != null) node.textContent = textContent
    return node
  }

  function clearChildren(parent) {
    while (parent.firstChild) parent.removeChild(parent.firstChild)
  }

  // ── Data fetching ─────────────────────────────────────────────────
  function fetchTasks() {
    return fetch(apiPathWithToken("/api/tasks"), { headers: authHeaders() })
      .then(function (r) {
        if (!r.ok) throw new Error("tasks fetch failed: " + r.status)
        return r.json()
      })
      .then(function (data) {
        state.tasks = data.tasks || []
        renderTaskList()
        updateAgentCount()
        if (state.selectedTaskId) {
          var task = findTask(state.selectedTaskId)
          if (task) renderRightPanel(task)
        }
      })
      .catch(function (err) {
        console.error("fetchTasks:", err)
        state.tasks = []
        renderTaskList()
      })
  }

  function fetchProjects() {
    return fetch(apiPathWithToken("/api/projects"), { headers: authHeaders() })
      .then(function (r) {
        if (!r.ok) throw new Error("projects fetch failed: " + r.status)
        return r.json()
      })
      .then(function (data) {
        state.projects = data.projects || []
        renderFilterBar()
      })
      .catch(function (err) {
        console.error("fetchProjects:", err)
      })
  }

  function findTask(id) {
    for (var i = 0; i < state.tasks.length; i++) {
      if (state.tasks[i].id === id) return state.tasks[i]
    }
    return null
  }

  // ── SSE ───────────────────────────────────────────────────────────
  function connectSSE() {
    if (state.menuEvents) {
      state.menuEvents.close()
      state.menuEvents = null
    }

    setConnectionState("connecting")
    var url = apiPathWithToken("/events/menu")
    var es = new EventSource(url)
    state.menuEvents = es

    es.addEventListener("menu", function () {
      setConnectionState("connected")
      fetchTasks()
    })

    es.addEventListener("tasks", function (e) {
      try {
        var data = JSON.parse(e.data)
        state.tasks = data.tasks || []
        renderTaskList()
        updateAgentCount()
        if (state.selectedTaskId) {
          var task = findTask(state.selectedTaskId)
          if (task) {
            renderRightPanel(task)
            renderChatBar()
            renderAskBanner()
          }
        }
      } catch (err) {
        console.error("tasks SSE parse error:", err)
      }
    })

    es.onopen = function () {
      setConnectionState("connected")
    }

    es.onerror = function () {
      if (es.readyState === EventSource.CLOSED) {
        setConnectionState("closed")
        setTimeout(connectSSE, 5000)
      } else {
        setConnectionState("reconnecting")
      }
    }
  }

  function setConnectionState(s) {
    var dot = document.getElementById("sidebar-status-dot")
    if (dot) {
      dot.className = "sidebar-status-dot"
      if (s === "connected") dot.classList.add("connected")
      else if (s === "connecting" || s === "reconnecting") dot.classList.add("connecting")
      else if (s === "error" || s === "closed") dot.classList.add("error")
    }
  }

  function updateAgentCount() {
    var countEl = document.getElementById("sidebar-agent-count")
    if (!countEl) return
    var active = 0
    for (var i = 0; i < state.tasks.length; i++) {
      if (state.tasks[i].status !== "done") active++
    }
    countEl.textContent = active
  }

  // ── Sidebar ───────────────────────────────────────────────────────
  function renderSidebar() {
    var icons = document.querySelectorAll(".sidebar-icon[data-view]")
    for (var i = 0; i < icons.length; i++) {
      if (icons[i].dataset.view === state.activeView) {
        icons[i].classList.add("sidebar-icon--active")
      } else {
        icons[i].classList.remove("sidebar-icon--active")
      }
    }
  }

  function handleSidebarClick(e) {
    var btn = e.currentTarget
    var view = btn.dataset.view
    if (!view) return
    state.activeView = view
    state.chatModeOverride = null
    renderSidebar()
    renderChatBar()
  }

  // ── Filter bar ────────────────────────────────────────────────────
  function renderFilterBar() {
    var filterBar = document.getElementById("filter-bar")
    if (!filterBar) return

    clearChildren(filterBar)

    // "All" pill
    var allPill = el("button", "filter-pill" + (state.projectFilter === "" ? " filter-pill--active" : ""), "All")
    allPill.dataset.project = ""
    allPill.addEventListener("click", handleFilterClick)
    filterBar.appendChild(allPill)

    // Project pills
    for (var i = 0; i < state.projects.length; i++) {
      var name = state.projects[i].name
      var active = state.projectFilter === name
      var pill = el("button", "filter-pill" + (active ? " filter-pill--active" : ""), name)
      pill.dataset.project = name
      pill.addEventListener("click", handleFilterClick)
      filterBar.appendChild(pill)
    }
  }

  function handleFilterClick(e) {
    state.projectFilter = e.currentTarget.dataset.project || ""
    state.chatModeOverride = null
    renderFilterBar()
    renderTaskList()
    renderChatBar()
  }

  // ── Task list ─────────────────────────────────────────────────────
  function renderTaskList() {
    var taskList = document.getElementById("task-list")
    var emptyEl = document.getElementById("task-list-empty")
    if (!taskList) return

    // Filter tasks
    var visible = state.tasks.filter(function (t) {
      if (state.projectFilter && t.project !== state.projectFilter) return false
      return true
    })

    // Remove existing cards and section headers
    var existing = taskList.querySelectorAll(".agent-card, .task-section-header")
    for (var i = 0; i < existing.length; i++) {
      existing[i].remove()
    }

    if (visible.length === 0) {
      if (emptyEl) {
        emptyEl.style.display = ""
        emptyEl.textContent = state.tasks.length === 0
          ? "No agents yet."
          : "No agents match the current filter."
      }
      return
    }

    if (emptyEl) emptyEl.style.display = "none"

    // Split into active and completed
    var active = []
    var completed = []
    for (var j = 0; j < visible.length; j++) {
      if (visible[j].status === "done") {
        completed.push(visible[j])
      } else {
        active.push(visible[j])
      }
    }

    // Active section
    if (active.length > 0) {
      var activeHeader = el("div", "task-section-header")
      activeHeader.appendChild(el("span", null, "Active"))
      activeHeader.appendChild(el("span", "task-section-count", active.length.toString()))
      taskList.appendChild(activeHeader)

      for (var a = 0; a < active.length; a++) {
        taskList.appendChild(createAgentCard(active[a]))
      }
    }

    // Completed section
    if (completed.length > 0) {
      var completedHeader = el("div", "task-section-header")
      completedHeader.appendChild(el("span", null, "Completed"))
      completedHeader.appendChild(el("span", "task-section-count", completed.length.toString()))
      taskList.appendChild(completedHeader)

      for (var c = 0; c < completed.length; c++) {
        taskList.appendChild(createAgentCard(completed[c]))
      }
    }
  }

  // ── Agent card ────────────────────────────────────────────────────
  function createAgentCard(task) {
    var isSelected = state.selectedTaskId === task.id
    var card = el("div", "agent-card" + (isSelected ? " agent-card--selected" : ""))
    card.dataset.taskId = task.id
    card.setAttribute("role", "button")
    card.setAttribute("tabindex", "0")

    // Left border color from task status
    var borderColor = TASK_STATUS_COLORS[task.status] || "var(--text-dim)"
    card.style.borderLeftColor = borderColor

    // Top row: project name + task id
    var top = el("div", "agent-card-top")
    top.appendChild(el("span", "agent-card-project", task.project || "\u2014"))
    top.appendChild(el("span", "agent-card-id", task.id))
    card.appendChild(top)

    // Description
    if (task.description) {
      card.appendChild(el("div", "agent-card-desc", task.description))
    }

    // Footer: status badge + mini session chain
    var footer = el("div", "agent-card-footer")
    footer.appendChild(createAgentStatusBadge(task.agentStatus))

    // Ask badge if waiting with question
    if (task.agentStatus === "waiting" && task.askQuestion) {
      var askBadge = el("span", "ask-badge", "\u25D0 INPUT")
      footer.appendChild(askBadge)
    }

    // Mini session chain
    footer.appendChild(createMiniSessionChain(task))
    card.appendChild(footer)

    // Click handler
    card.addEventListener("click", function () {
      selectTask(task.id)
    })
    card.addEventListener("keydown", function (e) {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault()
        selectTask(task.id)
      }
    })

    return card
  }

  // ── Agent status badge ────────────────────────────────────────────
  function createAgentStatusBadge(agentStatus) {
    var meta = AGENT_STATUS_META[agentStatus] || AGENT_STATUS_META.idle
    var badge = el("span", "agent-status-badge")
    var icon = el("span", "agent-status-badge-icon", meta.icon)
    icon.style.color = meta.color
    badge.appendChild(icon)
    badge.appendChild(document.createTextNode(" " + meta.label))
    return badge
  }

  // ── Mini session chain (in card footer) ───────────────────────────
  function createMiniSessionChain(task) {
    var chain = el("div", "mini-session-chain")

    // Use sessions array if available, otherwise fall back to phase
    var phases = []
    if (task.sessions && task.sessions.length > 0) {
      for (var i = 0; i < task.sessions.length; i++) {
        phases.push({
          phase: task.sessions[i].phase,
          status: task.sessions[i].status,
        })
      }
    } else if (task.phase) {
      var currentIdx = PHASES.indexOf(task.phase)
      for (var j = 0; j < PHASES.length; j++) {
        phases.push({
          phase: PHASES[j],
          status: j < currentIdx ? "complete" : (j === currentIdx ? "active" : "pending"),
        })
      }
    }

    for (var k = 0; k < phases.length; k++) {
      if (k > 0) {
        var conn = el("span", "mini-connector" + (phases[k - 1].status === "complete" ? " done" : ""))
        chain.appendChild(conn)
      }
      var pipClass = "mini-pip"
      if (phases[k].status === "complete") pipClass += " done"
      else if (phases[k].status === "active") pipClass += " active"
      var pip = el("span", pipClass)
      pip.title = phases[k].phase
      chain.appendChild(pip)
    }

    return chain
  }

  // ── Task selection ────────────────────────────────────────────────
  function selectTask(id) {
    state.selectedTaskId = id
    state.chatModeOverride = null
    var task = findTask(id)

    // Update card selection styles
    var cards = document.querySelectorAll(".agent-card")
    for (var i = 0; i < cards.length; i++) {
      if (cards[i].dataset.taskId === id) {
        cards[i].classList.add("agent-card--selected")
        // Update border color
        if (task) {
          cards[i].style.borderLeftColor = TASK_STATUS_COLORS[task.status] || "var(--text-dim)"
        }
      } else {
        cards[i].classList.remove("agent-card--selected")
        // Reset border to task's own color
        var otherTask = findTask(cards[i].dataset.taskId)
        if (otherTask) {
          cards[i].style.borderLeftColor = TASK_STATUS_COLORS[otherTask.status] || "var(--text-dim)"
        }
      }
    }

    if (task) {
      renderRightPanel(task)
      connectTerminal(task)
    }

    // Mobile: show detail panel
    var panels = document.getElementById("panels")
    if (panels) panels.classList.add("detail-active")

    renderChatBar()
    renderAskBanner()
  }

  // ── Right panel ───────────────────────────────────────────────────
  function renderRightPanel(task) {
    var emptyState = document.getElementById("empty-state")
    var detailView = document.getElementById("detail-view")

    if (!task) {
      if (emptyState) emptyState.style.display = ""
      if (detailView) detailView.style.display = "none"
      return
    }

    if (emptyState) emptyState.style.display = "none"
    if (detailView) detailView.style.display = ""

    renderDetailHeader(task)
    renderSessionChain(task)
    renderPreviewHeader(task)
  }

  // ── Detail header ─────────────────────────────────────────────────
  function renderDetailHeader(task) {
    var header = document.getElementById("detail-header")
    if (!header) return

    clearChildren(header)

    // Top row: back button + title + actions
    var top = el("div", "detail-header-top")

    var backBtn = el("button", "detail-back-btn", "\u2190 Back")
    backBtn.addEventListener("click", handleMobileBack)
    top.appendChild(backBtn)

    top.appendChild(el("span", "detail-title", (task.project || "\u2014") + " \u00B7 " + task.id))

    var actions = el("div", "detail-actions")
    actions.appendChild(createAgentStatusBadge(task.agentStatus))
    top.appendChild(actions)

    header.appendChild(top)

    // Meta row: description + branch
    var meta = el("div", "detail-meta")
    if (task.description) {
      meta.appendChild(document.createTextNode(task.description))
    }
    if (task.branch) {
      var sep = el("span", null, "\u00B7")
      sep.style.color = "var(--text-dim)"
      meta.appendChild(sep)
      meta.appendChild(el("span", null, task.branch))
    }
    header.appendChild(meta)
  }

  // ── Session chain (detail panel) ──────────────────────────────────
  function renderSessionChain(task) {
    var container = document.getElementById("session-chain")
    if (!container) return

    clearChildren(container)

    // Use sessions array if available, otherwise fall back to phase pips
    var phases = []
    if (task.sessions && task.sessions.length > 0) {
      for (var i = 0; i < task.sessions.length; i++) {
        var s = task.sessions[i]
        phases.push({
          label: s.phase,
          dotLabel: (s.phase || "?").charAt(0).toUpperCase(),
          status: s.status === "complete" ? "done" : (s.status === "active" ? "active" : ""),
          duration: s.duration || "",
          artifact: s.artifact || "",
        })
      }
    } else if (task.phase) {
      var currentIdx = PHASES.indexOf(task.phase)
      for (var j = 0; j < PHASES.length; j++) {
        var st = ""
        if (j < currentIdx) st = "done"
        else if (j === currentIdx) st = "active"
        phases.push({
          label: PHASES[j],
          dotLabel: PHASE_DOT_LABELS[PHASES[j]],
          status: st,
          duration: "",
          artifact: "",
        })
      }
    }

    for (var k = 0; k < phases.length; k++) {
      // Connector
      if (k > 0) {
        var connClass = "session-chain-connector"
        if (phases[k - 1].status === "done") connClass += " done"
        container.appendChild(el("div", connClass))
      }

      // Pip
      var pip = el("div", "session-chain-pip")

      var dotClass = "session-chain-dot"
      if (phases[k].status === "done") dotClass += " done"
      else if (phases[k].status === "active") dotClass += " active"
      pip.appendChild(el("div", dotClass, phases[k].dotLabel))

      var lblClass = "session-chain-label"
      if (phases[k].status === "active") lblClass += " active"
      var lblText = phases[k].label
      if (phases[k].duration) lblText += " " + phases[k].duration
      pip.appendChild(el("div", lblClass, lblText))

      container.appendChild(pip)
    }
  }

  // ── Preview header ────────────────────────────────────────────────
  function renderPreviewHeader(task) {
    var container = document.getElementById("preview-header")
    if (!container) return

    clearChildren(container)

    var projLabel = el("span", "preview-header-project", task.project || "\u2014")
    container.appendChild(projLabel)

    var agentMeta = AGENT_STATUS_META[task.agentStatus] || AGENT_STATUS_META.idle
    var statusSpan = el("span", "preview-header-status")
    statusSpan.textContent = agentMeta.icon + " " + agentMeta.label
    statusSpan.style.color = agentMeta.color
    container.appendChild(statusSpan)
  }

  // ── Terminal management ───────────────────────────────────────────
  function connectTerminal(task) {
    disconnectTerminal()
    var container = document.getElementById("terminal-container")
    if (!container) return
    clearChildren(container)

    if (!task.tmuxSession) {
      var placeholder = el("div", "terminal-placeholder", "No session attached.")
      container.appendChild(placeholder)
      return
    }

    // Check if Terminal (xterm.js) is available
    if (typeof Terminal === "undefined") {
      var fallback = el("div", "terminal-placeholder", "Terminal emulator not available. Check xterm.js assets.")
      container.appendChild(fallback)
      return
    }

    var term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: "var(--font-mono)",
      theme: {
        background: "#080a0e",
        foreground: "#c8d0dc",
        cursor: "#e8a932",
      },
    })
    var fitAddon = new FitAddon.FitAddon()
    term.loadAddon(fitAddon)
    term.open(container)
    fitAddon.fit()

    state.terminal = term
    state.fitAddon = fitAddon

    var protocol = window.location.protocol === "https:" ? "wss:" : "ws:"
    var wsUrl = protocol + "//" + window.location.host + "/ws/session/" + encodeURIComponent(task.tmuxSession)
    if (state.authToken) wsUrl += "?token=" + encodeURIComponent(state.authToken)
    var ws = new WebSocket(wsUrl)
    state.terminalWs = ws

    ws.binaryType = "arraybuffer"
    ws.onmessage = function (e) {
      if (e.data instanceof ArrayBuffer) {
        term.write(new Uint8Array(e.data))
      } else {
        term.write(e.data)
      }
    }
    ws.onclose = function () { state.terminalWs = null }
    term.onData(function (data) {
      if (ws.readyState === WebSocket.OPEN) ws.send(data)
    })
  }

  function disconnectTerminal() {
    if (state.terminalWs) {
      state.terminalWs.close()
      state.terminalWs = null
    }
    if (state.terminal) {
      state.terminal.dispose()
      state.terminal = null
    }
    state.fitAddon = null
  }

  // ── Resize handler ────────────────────────────────────────────────
  window.addEventListener("resize", function () {
    if (state.fitAddon) state.fitAddon.fit()
  })

  // ── Mobile back ───────────────────────────────────────────────────
  function handleMobileBack() {
    state.selectedTaskId = null
    disconnectTerminal()
    var panels = document.getElementById("panels")
    if (panels) panels.classList.remove("detail-active")

    // Reset right panel to empty state
    var emptyState = document.getElementById("empty-state")
    var detailView = document.getElementById("detail-view")
    if (emptyState) emptyState.style.display = ""
    if (detailView) detailView.style.display = "none"

    renderTaskList()
    renderChatBar()
    renderAskBanner()
  }

  // ── Chat mode detection ────────────────────────────────────────────
  function detectChatMode() {
    if (state.chatModeOverride) return state.chatModeOverride

    var task = state.selectedTaskId ? findTask(state.selectedTaskId) : null

    if (state.activeView === "agents" && task && task.agentStatus !== "complete" && task.agentStatus !== "idle") {
      return {
        mode: "reply",
        label: "\u21A9 " + task.id + "/" + task.phase,
        icon: "\u21A9",
        color: "var(--accent)",
      }
    }

    var project = ""
    if (task) project = task.project
    else if (state.projectFilter) project = state.projectFilter

    return {
      mode: "new",
      label: project ? "+ " + project : "+ auto-route",
      icon: "+",
      color: "var(--blue)",
    }
  }

  function renderChatBar() {
    var mode = detectChatMode()
    state.chatMode = mode

    var modeBtn = document.getElementById("chat-mode-btn")
    var modeIcon = document.getElementById("chat-mode-icon")
    var modeLabel = document.getElementById("chat-mode-label")
    var input = document.getElementById("chat-input")

    if (modeBtn) modeBtn.style.borderColor = mode.color
    if (modeIcon) { modeIcon.textContent = mode.icon; modeIcon.style.color = mode.color }
    if (modeLabel) modeLabel.textContent = mode.label

    if (mode.mode === "reply") {
      if (input) input.placeholder = "Reply to " + state.selectedTaskId + "..."
    } else {
      if (input) input.placeholder = "Describe a new task..."
    }
  }

  // ── AskUserQuestion banner ─────────────────────────────────────────
  function renderAskBanner() {
    var existing = document.querySelector(".ask-banner")
    if (existing) existing.remove()

    var task = state.selectedTaskId ? findTask(state.selectedTaskId) : null
    if (!task || task.agentStatus !== "waiting" || !task.askQuestion) return

    var banner = el("div", "ask-banner")
    var icon = el("span", "ask-banner-icon", "\u25D0")
    banner.appendChild(icon)
    banner.appendChild(document.createTextNode("Agent is asking: " + task.askQuestion))

    var chatBar = document.getElementById("chat-bar")
    if (chatBar && chatBar.parentNode) {
      chatBar.parentNode.insertBefore(banner, chatBar)
    }

    // Update placeholder to reflect the question
    var input = document.getElementById("chat-input")
    if (input) input.placeholder = "Answer: " + task.askQuestion
  }

  // ── Chat mode override menu ────────────────────────────────────────
  function renderModeMenu() {
    var existing = document.querySelector(".chat-mode-menu")
    if (existing) existing.remove()

    var menu = el("div", "chat-mode-menu open")

    // "New in {project}" options for each project
    for (var i = 0; i < state.projects.length; i++) {
      var proj = state.projects[i]
      var opt = el("button", "chat-mode-option")
      var icon = el("span", "chat-mode-option-icon", "+")
      icon.style.color = "var(--blue)"
      opt.appendChild(icon)
      opt.appendChild(document.createTextNode("New in " + proj.name))
      opt.dataset.mode = "new"
      opt.dataset.project = proj.name
      opt.addEventListener("click", handleModeSelect)
      menu.appendChild(opt)
    }

    // "New (auto-route)"
    var autoOpt = el("button", "chat-mode-option")
    var autoIcon = el("span", "chat-mode-option-icon", "+")
    autoIcon.style.color = "var(--blue)"
    autoOpt.appendChild(autoIcon)
    autoOpt.appendChild(document.createTextNode("New (auto-route)"))
    autoOpt.dataset.mode = "new"
    autoOpt.dataset.project = ""
    autoOpt.addEventListener("click", handleModeSelect)
    menu.appendChild(autoOpt)

    // "Back to auto" if overridden
    if (state.chatModeOverride) {
      var backOpt = el("button", "chat-mode-option")
      var backIcon = el("span", "chat-mode-option-icon", "\u2190")
      backOpt.appendChild(backIcon)
      backOpt.appendChild(document.createTextNode("Back to: auto"))
      backOpt.dataset.mode = "auto"
      backOpt.addEventListener("click", handleModeSelect)
      menu.appendChild(backOpt)
    }

    var chatBar = document.getElementById("chat-bar")
    if (chatBar) {
      chatBar.appendChild(menu)
    }

    // Close on outside click (deferred to avoid immediate close)
    setTimeout(function () {
      document.addEventListener("click", closeModeMenu)
    }, 0)
  }

  function handleModeSelect(e) {
    var btn = e.currentTarget
    if (btn.dataset.mode === "auto") {
      state.chatModeOverride = null
    } else {
      state.chatModeOverride = {
        mode: btn.dataset.mode,
        label: btn.dataset.project ? "+ " + btn.dataset.project : "+ auto-route",
        icon: "+",
        color: "var(--blue)",
        project: btn.dataset.project,
      }
    }
    closeModeMenu()
    renderChatBar()
  }

  function closeModeMenu() {
    var menu = document.querySelector(".chat-mode-menu")
    if (menu) menu.remove()
    document.removeEventListener("click", closeModeMenu)
  }

  function sendChatMessage() {
    var input = document.getElementById("chat-input")
    if (!input) return
    var text = input.value.trim()
    if (!text) return

    var mode = state.chatMode || detectChatMode()

    if (mode.mode === "reply" && state.selectedTaskId) {
      sendTaskInput(state.selectedTaskId, text)
    } else {
      openNewTaskModalWithDescription(text)
    }
    input.value = ""
  }

  function sendTaskInput(taskId, text) {
    var headers = authHeaders()
    headers["Content-Type"] = "application/json"

    fetch(apiPathWithToken("/api/tasks/" + taskId + "/input"), {
      method: "POST",
      headers: headers,
      body: JSON.stringify({ input: text }),
    })
      .then(function (r) {
        if (!r.ok) throw new Error("send failed: " + r.status)
      })
      .catch(function (err) {
        console.error("sendTaskInput:", err)
      })
  }

  // ── Utilities ─────────────────────────────────────────────────────
  function formatDuration(isoDate) {
    if (!isoDate) return "\u2014"
    var created = new Date(isoDate)
    if (isNaN(created.getTime())) return "\u2014"

    var ms = Date.now() - created.getTime()
    if (ms < 0) ms = 0

    var seconds = Math.floor(ms / 1000)
    if (seconds < 60) return seconds + "s"

    var minutes = Math.floor(seconds / 60)
    if (minutes < 60) return minutes + "m"

    var hours = Math.floor(minutes / 60)
    var remainMinutes = minutes % 60
    if (hours < 24) return hours + "h " + remainMinutes + "m"

    var days = Math.floor(hours / 24)
    return days + "d " + (hours % 24) + "h"
  }

  // ── New Task modal ────────────────────────────────────────────────
  var newTaskModal = document.getElementById("new-task-modal")
  var newTaskBackdrop = document.getElementById("new-task-backdrop")
  var newTaskProject = document.getElementById("new-task-project")
  var newTaskDesc = document.getElementById("new-task-desc")
  var newTaskPhase = document.getElementById("new-task-phase")
  var newTaskSubmit = document.getElementById("new-task-submit")
  var routeSuggestion = document.getElementById("route-suggestion")

  function openNewTaskModal() {
    clearChildren(newTaskProject)
    var hasProjects = state.projects.length > 0
    for (var i = 0; i < state.projects.length; i++) {
      var opt = document.createElement("option")
      opt.value = state.projects[i].name
      opt.textContent = state.projects[i].name
      newTaskProject.appendChild(opt)
    }
    if (!hasProjects) {
      var placeholder = document.createElement("option")
      placeholder.value = ""
      placeholder.textContent = "No projects configured"
      placeholder.disabled = true
      placeholder.selected = true
      newTaskProject.appendChild(placeholder)
    }
    if (newTaskDesc) newTaskDesc.value = ""
    if (newTaskPhase) newTaskPhase.value = "execute"
    if (newTaskSubmit) newTaskSubmit.disabled = !hasProjects
    if (routeSuggestion) routeSuggestion.textContent = ""

    if (newTaskModal) newTaskModal.classList.add("open")
    if (newTaskBackdrop) newTaskBackdrop.classList.add("open")
    if (newTaskModal) newTaskModal.setAttribute("aria-hidden", "false")
    if (newTaskDesc) newTaskDesc.focus()
  }

  function openNewTaskModalWithDescription(text) {
    openNewTaskModal()
    if (newTaskDesc) newTaskDesc.value = text
    suggestProject(text)
  }

  function closeNewTaskModal() {
    if (routeTimer) { clearTimeout(routeTimer); routeTimer = null }
    if (newTaskModal) newTaskModal.classList.remove("open")
    if (newTaskBackdrop) newTaskBackdrop.classList.remove("open")
    if (newTaskModal) newTaskModal.setAttribute("aria-hidden", "true")
  }

  function submitNewTask() {
    var project = newTaskProject ? newTaskProject.value : ""
    var description = newTaskDesc ? newTaskDesc.value.trim() : ""
    var phase = newTaskPhase ? newTaskPhase.value : "execute"

    if (!project || !description) return

    var body = JSON.stringify({ project: project, description: description, phase: phase })
    var headers = authHeaders()
    headers["Content-Type"] = "application/json"

    fetch(apiPathWithToken("/api/tasks"), {
      method: "POST",
      headers: headers,
      body: body,
    })
      .then(function (r) {
        if (!r.ok) throw new Error("create failed: " + r.status)
        return r.json()
      })
      .then(function (data) {
        closeNewTaskModal()
        fetchTasks()
        if (data.task && data.task.id) selectTask(data.task.id)
      })
      .catch(function (err) {
        console.error("submitNewTask:", err)
      })
  }

  // ── Auto-suggest project via routing ──────────────────────────────
  var routeTimer = null

  function suggestProject(message) {
    if (routeTimer) clearTimeout(routeTimer)
    if (!message || message.length < 5) {
      if (routeSuggestion) routeSuggestion.textContent = ""
      return
    }

    routeTimer = setTimeout(function () {
      routeTimer = null
      var headers = authHeaders()
      headers["Content-Type"] = "application/json"

      fetch(apiPathWithToken("/api/route"), {
        method: "POST",
        headers: headers,
        body: JSON.stringify({ message: message }),
      })
        .then(function (r) {
          if (!r.ok) return null
          return r.json()
        })
        .then(function (data) {
          if (!data || !data.project) {
            if (routeSuggestion) {
              routeSuggestion.textContent = "No matching project"
              routeSuggestion.className = "route-suggestion route-suggestion-muted"
            }
            return
          }
          if (routeSuggestion) {
            routeSuggestion.textContent =
              "Suggested: " + data.project +
              " (" + Math.round(data.confidence * 100) + "% match)"
            routeSuggestion.className = "route-suggestion"
          }
          if (newTaskProject) {
            for (var i = 0; i < newTaskProject.options.length; i++) {
              if (newTaskProject.options[i].value === data.project) {
                newTaskProject.selectedIndex = i
                break
              }
            }
          }
        })
        .catch(function () {
          if (routeSuggestion) routeSuggestion.textContent = ""
        })
    }, 300)
  }

  // ── Event listeners ───────────────────────────────────────────────

  // Sidebar view icons
  var sidebarIcons = document.querySelectorAll(".sidebar-icon[data-view]")
  for (var si = 0; si < sidebarIcons.length; si++) {
    sidebarIcons[si].addEventListener("click", handleSidebarClick)
  }

  // New task button (sidebar +)
  var newTaskBtn = document.getElementById("new-task-btn")
  if (newTaskBtn) newTaskBtn.addEventListener("click", openNewTaskModal)

  // Modal controls
  var newTaskClose = document.getElementById("new-task-close")
  var newTaskCancel = document.getElementById("new-task-cancel")
  if (newTaskClose) newTaskClose.addEventListener("click", closeNewTaskModal)
  if (newTaskCancel) newTaskCancel.addEventListener("click", closeNewTaskModal)
  if (newTaskBackdrop) newTaskBackdrop.addEventListener("click", closeNewTaskModal)
  if (newTaskSubmit) newTaskSubmit.addEventListener("click", submitNewTask)
  if (newTaskDesc) {
    newTaskDesc.addEventListener("input", function () {
      suggestProject(newTaskDesc.value.trim())
    })
  }

  // Chat bar
  var chatModeBtn = document.getElementById("chat-mode-btn")
  if (chatModeBtn) {
    chatModeBtn.addEventListener("click", function (e) {
      e.stopPropagation()
      var existing = document.querySelector(".chat-mode-menu")
      if (existing) { closeModeMenu(); return }
      renderModeMenu()
    })
  }
  var chatSendBtn = document.getElementById("chat-send-btn")
  var chatInput = document.getElementById("chat-input")
  if (chatSendBtn) chatSendBtn.addEventListener("click", sendChatMessage)
  if (chatInput) {
    chatInput.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        e.preventDefault()
        sendChatMessage()
      }
    })
  }

  // Mobile bottom nav
  var mobileNavItems = document.querySelectorAll(".mobile-nav-item[data-view]")
  for (var mi = 0; mi < mobileNavItems.length; mi++) {
    mobileNavItems[mi].addEventListener("click", function (e) {
      e.preventDefault()
      var view = e.currentTarget.dataset.view
      if (!view) return
      state.activeView = view

      // Update active state on mobile nav
      var items = document.querySelectorAll(".mobile-nav-item[data-view]")
      for (var n = 0; n < items.length; n++) {
        if (items[n].dataset.view === view) {
          items[n].classList.add("mobile-nav-item--active")
        } else {
          items[n].classList.remove("mobile-nav-item--active")
        }
      }

      renderSidebar()
    })
  }

  // Escape key
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") {
      if (newTaskModal && newTaskModal.classList.contains("open")) {
        closeNewTaskModal()
      } else if (state.selectedTaskId) {
        handleMobileBack()
      }
    }
  })

  // ── Init ──────────────────────────────────────────────────────────
  renderSidebar()
  renderChatBar()
  fetchTasks()
  fetchProjects()
  connectSSE()
})()
