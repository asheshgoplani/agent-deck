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

  var PHASE_COLORS_HEX = {
    brainstorm: "#c084fc",
    plan: "#8b8cf8",
    execute: "#e8a932",
    review: "#4ca8e8",
    done: "#2dd4a0",
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
        renderTopBar()
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

  function getCardBorderColor(task) {
    if (task.agentStatus === "waiting") return "var(--orange)"
    return TASK_STATUS_COLORS[task.status] || "var(--text-dim)"
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
    var active = 0
    for (var i = 0; i < state.tasks.length; i++) {
      if (state.tasks[i].status !== "done") active++
    }
    if (countEl) countEl.textContent = active + " agent" + (active !== 1 ? "s" : "")
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
    renderTopBar()
    renderView()
    renderChatBar()
  }

  // ── Top bar ──────────────────────────────────────────────────────
  function renderTopBar() {
    var leftEl = document.getElementById("top-bar-left")
    var rightEl = document.getElementById("top-bar-right")
    if (!leftEl || !rightEl) return

    clearChildren(leftEl)
    clearChildren(rightEl)

    // View label
    leftEl.appendChild(el("span", "top-bar-view", state.activeView))

    // Breadcrumb for selected task
    var task = state.selectedTaskId ? findTask(state.selectedTaskId) : null
    if (task && (state.activeView === "agents" || state.activeView === "kanban")) {
      leftEl.appendChild(el("span", "top-bar-sep", "/"))
      leftEl.appendChild(el("span", "top-bar-project", task.project || "\u2014"))
      leftEl.appendChild(el("span", "top-bar-sep", "/"))
      leftEl.appendChild(el("span", "top-bar-task-id", task.id))
      leftEl.appendChild(el("span", "top-bar-sep", "/"))

      var phasePill = el("span", "top-bar-phase", task.phase || "\u2014")
      var phaseColor = PHASE_COLORS_HEX[task.phase] || "#4a5368"
      phasePill.style.background = phaseColor + "20"
      phasePill.style.color = phaseColor
      leftEl.appendChild(phasePill)
    }

    // Right side: action buttons when task with tmux is selected
    var isMobile = window.innerWidth < 768
    if (task && task.tmuxSession && !isMobile) {
      var attachBtn = el("button", "top-bar-action top-bar-action--attach", "\u25B6 Attach")
      attachBtn.title = "Attach to " + task.tmuxSession
      attachBtn.addEventListener("click", function () {
        window.open("/terminal?session=" + encodeURIComponent(task.tmuxSession), "_blank")
      })
      rightEl.appendChild(attachBtn)

      var sshBtn = el("button", "top-bar-action", "\u229E SSH")
      sshBtn.title = "SSH — coming soon"
      sshBtn.disabled = true
      sshBtn.style.opacity = "0.4"
      sshBtn.style.cursor = "default"
      rightEl.appendChild(sshBtn)

      var ideBtn = el("button", "top-bar-action", "\u27E8\u27E9 IDE")
      ideBtn.title = "IDE — coming soon"
      ideBtn.disabled = true
      ideBtn.style.opacity = "0.4"
      ideBtn.style.cursor = "default"
      rightEl.appendChild(ideBtn)
    }

    // Agent count indicator
    var activeCount = 0
    for (var i = 0; i < state.tasks.length; i++) {
      var t = state.tasks[i]
      if (t.tmuxSession && t.status !== "done" && t.status !== "backlog") {
        activeCount++
      }
    }
    var countSpan = el("span", "top-bar-agent-indicator")
    var dot = el("span", "top-bar-agent-dot")
    countSpan.appendChild(dot)
    countSpan.appendChild(document.createTextNode(" " + activeCount))
    rightEl.appendChild(countSpan)
  }

  // ── View switching ─────────────────────────────────────────────────
  var VIEW_PLACEHOLDERS = {
    kanban: { icon: "\u25A6", title: "Kanban Board", desc: "Board view with columns \u2014 coming soon." },
    workspaces: { icon: "\u25A3", title: "Workspaces", desc: "Dev environment management \u2014 coming soon." },
    brainstorm: { icon: "\u25C7", title: "Brainstorm", desc: "Pre-project ideation \u2014 coming soon." },
  }

  // ── Conductor log type styles ────────────────────────────────────
  var CONDUCTOR_LOG_STYLES = {
    check:  { icon: "\u2713", color: "var(--green)" },
    action: { icon: "\u2192", color: "var(--accent)" },
    alert:  { icon: "\u26A0", color: "var(--red)" },
    route:  { icon: "\u25C8", color: "var(--purple)" },
    spawn:  { icon: "\u2295", color: "var(--blue)" },
    ask:    { icon: "\u25D0", color: "var(--orange)" },
  }

  function renderView() {
    // Clean up any open popups from previous view
    closeSlashPalette()
    closeModeMenu()

    var panels = document.getElementById("panels")
    var placeholder = document.getElementById("view-placeholder")
    var conductorEl = document.getElementById("conductor-view")
    var kanbanEl = document.getElementById("kanban-view")
    var workspacesEl = document.getElementById("workspaces-view")
    var chatBar = document.getElementById("chat-bar")

    // Hide all non-active views
    var hideAll = function () {
      if (panels) panels.style.display = "none"
      if (placeholder) placeholder.style.display = "none"
      if (conductorEl) conductorEl.style.display = "none"
      if (kanbanEl) kanbanEl.style.display = "none"
      if (workspacesEl) workspacesEl.style.display = "none"
    }

    if (state.activeView === "agents") {
      hideAll()
      if (panels) panels.style.display = ""
      return
    }

    hideAll()

    // Conductor view
    if (state.activeView === "conductor") {
      renderConductorView()
      return
    }

    // Kanban view
    if (state.activeView === "kanban") {
      renderKanbanView()
      return
    }

    // Workspaces view
    if (state.activeView === "workspaces") {
      renderWorkspacesView()
      return
    }

    // Create placeholder if needed
    if (!placeholder) {
      placeholder = el("div", "view-placeholder")
      placeholder.id = "view-placeholder"
      var parent = panels ? panels.parentNode : document.querySelector(".main-content")
      if (parent && chatBar) {
        parent.insertBefore(placeholder, chatBar)
      } else if (parent) {
        parent.appendChild(placeholder)
      }
    }

    clearChildren(placeholder)
    placeholder.style.display = ""

    var info = VIEW_PLACEHOLDERS[state.activeView] || { icon: "?", title: state.activeView, desc: "Coming soon." }
    placeholder.appendChild(el("div", "view-placeholder-icon", info.icon))
    placeholder.appendChild(el("div", "view-placeholder-title", info.title))
    placeholder.appendChild(el("div", "view-placeholder-text", info.desc))
  }

  // ── Conductor view ───────────────────────────────────────────────
  function renderConductorView() {
    var chatBar = document.getElementById("chat-bar")
    var conductorEl = document.getElementById("conductor-view")

    if (!conductorEl) {
      conductorEl = el("div", "conductor-view")
      conductorEl.id = "conductor-view"
      var parent = document.querySelector(".main-content")
      if (parent && chatBar) {
        parent.insertBefore(conductorEl, chatBar)
      } else if (parent) {
        parent.appendChild(conductorEl)
      }
    }

    clearChildren(conductorEl)
    conductorEl.style.display = ""

    // Fetch conductor status from API (graceful fallback)
    fetch(apiPathWithToken("/api/conductor"), { headers: authHeaders() })
      .then(function (r) {
        if (!r.ok) throw new Error("conductor fetch failed: " + r.status)
        return r.json()
      })
      .then(function (data) {
        buildConductorContent(conductorEl, data.conductor || null, data.log || [])
      })
      .catch(function () {
        // No conductor API yet — render empty state
        buildConductorContent(conductorEl, null, [])
      })
  }

  function buildConductorContent(container, conductor, log) {
    clearChildren(container)

    if (!conductor) {
      // Empty / not configured state
      var emptyCard = el("div", "conductor-identity")

      var avatar = el("div", "conductor-avatar", "\u25CE")
      emptyCard.appendChild(avatar)

      var info = el("div", "conductor-info")
      var nameRow = el("div", "conductor-name", "No conductor running")
      info.appendChild(nameRow)

      var desc = el("div", "conductor-desc", "Start a conductor session to orchestrate agents")
      info.appendChild(desc)

      emptyCard.appendChild(info)
      container.appendChild(emptyCard)

      // Log header even when empty
      container.appendChild(el("div", "conductor-log-header", "Activity Log"))

      var emptyLog = el("div", "conductor-log-entry")
      var emptyMsg = el("span", "conductor-log-msg", "No activity yet")
      emptyMsg.style.color = "var(--text-dim)"
      emptyLog.appendChild(emptyMsg)
      container.appendChild(emptyLog)
      return
    }

    // ── Identity card ──────────────────────────────────────────────
    var card = el("div", "conductor-identity")

    var avatar = el("div", "conductor-avatar", "\u25CE")
    card.appendChild(avatar)

    var infoDiv = el("div", "conductor-info")

    // Name + status
    var nameRow = el("div", "conductor-name")
    nameRow.textContent = conductor.name || "conductor"
    var statusSpan = el("span",
      "conductor-status conductor-status--" + (conductor.status === "connected" ? "connected" : "disconnected"),
      "\u25CF " + (conductor.status || "disconnected")
    )
    nameRow.appendChild(document.createTextNode(" "))
    nameRow.appendChild(statusSpan)
    infoDiv.appendChild(nameRow)

    // Description line
    var descLine = el("div", "conductor-desc")
    descLine.textContent = "Separate Claude instance \u00B7 tmux: " + (conductor.tmuxSession || "n/a")
    infoDiv.appendChild(descLine)

    // Stats row
    var stats = el("div", "conductor-stats")
    stats.appendChild(el("span", "", "heartbeat: " + (conductor.heartbeatInterval || "n/a")))
    var autoApprove = conductor.autoApprove
    if (autoApprove && autoApprove.length) {
      stats.appendChild(el("span", "", "auto-approve: " + autoApprove.join(", ")))
    }
    if (conductor.monitoredSessions != null) {
      stats.appendChild(el("span", "", "monitoring: " + conductor.monitoredSessions + " sessions"))
    }
    infoDiv.appendChild(stats)

    card.appendChild(infoDiv)
    container.appendChild(card)

    // ── Activity log ───────────────────────────────────────────────
    container.appendChild(el("div", "conductor-log-header", "Activity Log"))

    if (!log || !log.length) {
      var noLog = el("div", "conductor-log-entry")
      var noMsg = el("span", "conductor-log-msg", "No recent activity")
      noMsg.style.color = "var(--text-dim)"
      noLog.appendChild(noMsg)
      container.appendChild(noLog)
      return
    }

    for (var i = 0; i < log.length; i++) {
      var entry = log[i]
      var style = CONDUCTOR_LOG_STYLES[entry.type] || CONDUCTOR_LOG_STYLES.check

      var row = el("div", "conductor-log-entry")

      var time = el("span", "conductor-log-time", entry.time || "")
      row.appendChild(time)

      var icon = el("span", "conductor-log-icon", style.icon)
      icon.style.color = style.color
      row.appendChild(icon)

      var msg = el("span", "conductor-log-msg", entry.msg || "")
      row.appendChild(msg)

      container.appendChild(row)
    }
  }

  // ── Kanban view ──────────────────────────────────────────────────
  var KANBAN_COLUMNS = ["backlog", "planning", "running", "review", "done"]
  var KANBAN_STATUS_META = {
    backlog:  { icon: "\u25CB", color: "var(--text-dim)" },
    planning: { icon: "\u25C8", color: "var(--purple)" },
    running:  { icon: "\u27F3", color: "var(--accent)" },
    review:   { icon: "\u25CE", color: "var(--blue)" },
    done:     { icon: "\u2713", color: "var(--green)" },
  }

  var kanbanGroupByProject = true

  function renderKanbanView() {
    var chatBar = document.getElementById("chat-bar")
    var kanbanEl = document.getElementById("kanban-view")

    if (!kanbanEl) {
      kanbanEl = el("div", "kanban-view")
      kanbanEl.id = "kanban-view"
      var parent = document.querySelector(".main-content")
      if (parent && chatBar) {
        parent.insertBefore(kanbanEl, chatBar)
      } else if (parent) {
        parent.appendChild(kanbanEl)
      }
    }

    clearChildren(kanbanEl)
    kanbanEl.style.display = ""

    // ── Filter bar ──────────────────────────────────────────────
    var filterBar = el("div", "kanban-filter-bar")

    // "All" button
    var allBtn = el("button",
      "kanban-filter-btn" + (state.projectFilter === "" ? " kanban-filter-btn--active" : ""),
      "All (" + state.tasks.length + ")"
    )
    allBtn.addEventListener("click", function () {
      state.projectFilter = ""
      renderKanbanView()
      renderTopBar()
    })
    filterBar.appendChild(allBtn)

    // Per-project buttons
    var projects = state.projects || []
    for (var p = 0; p < projects.length; p++) {
      ;(function (proj) {
        var active = state.projectFilter === proj.name
        var btn = el("button",
          "kanban-filter-btn" + (active ? " kanban-filter-btn--active" : ""),
          proj.name
        )
        btn.addEventListener("click", function () {
          state.projectFilter = active ? "" : proj.name
          renderKanbanView()
          renderTopBar()
        })
        filterBar.appendChild(btn)
      })(projects[p])
    }

    // Spacer
    filterBar.appendChild(el("div", "kanban-filter-spacer"))

    // Group toggle
    var groupBtn = el("button",
      "kanban-group-btn" + (kanbanGroupByProject ? " kanban-group-btn--active" : "")
    )
    groupBtn.appendChild(document.createTextNode((kanbanGroupByProject ? "\u25A4 " : "\u25A5 ") + "Group"))
    groupBtn.addEventListener("click", function () {
      kanbanGroupByProject = !kanbanGroupByProject
      renderKanbanView()
    })
    filterBar.appendChild(groupBtn)

    kanbanEl.appendChild(filterBar)

    // ── Columns ─────────────────────────────────────────────────
    var columnsContainer = el("div", "kanban-columns")

    // Filter tasks
    var tasks = state.tasks || []
    if (state.projectFilter) {
      tasks = tasks.filter(function (t) { return t.project === state.projectFilter })
    }

    for (var c = 0; c < KANBAN_COLUMNS.length; c++) {
      var colName = KANBAN_COLUMNS[c]
      var colMeta = KANBAN_STATUS_META[colName] || KANBAN_STATUS_META.backlog

      // Map task status to kanban column
      var colTasks = tasks.filter(function (t) {
        return mapTaskToKanbanColumn(t) === colName
      })

      var column = el("div", "kanban-column")

      // Column header
      var header = el("div", "kanban-column-header")
      header.style.color = colMeta.color
      var headerIcon = el("span", "", colMeta.icon)
      header.appendChild(headerIcon)
      header.appendChild(document.createTextNode(" " + colName + " "))
      var countSpan = el("span", "kanban-column-count", String(colTasks.length))
      header.appendChild(countSpan)
      column.appendChild(header)

      // Column body
      var body = el("div", "kanban-column-body")

      if (colTasks.length === 0) {
        body.appendChild(el("div", "kanban-empty", "\u2014"))
      } else if (kanbanGroupByProject) {
        // Group by project
        var projectSet = []
        for (var i = 0; i < colTasks.length; i++) {
          if (projectSet.indexOf(colTasks[i].project) === -1) {
            projectSet.push(colTasks[i].project)
          }
        }
        for (var gi = 0; gi < projectSet.length; gi++) {
          var projName = projectSet[gi]
          body.appendChild(el("div", "kanban-project-group", "\u25A3 " + projName))
          for (var ti = 0; ti < colTasks.length; ti++) {
            if (colTasks[ti].project === projName) {
              body.appendChild(createKanbanCard(colTasks[ti], colMeta))
            }
          }
        }
      } else {
        for (var j = 0; j < colTasks.length; j++) {
          body.appendChild(createKanbanCard(colTasks[j], colMeta))
        }
      }

      column.appendChild(body)
      columnsContainer.appendChild(column)
    }

    kanbanEl.appendChild(columnsContainer)
  }

  function mapTaskToKanbanColumn(task) {
    var status = task.status || ""
    if (status === "done") return "done"
    if (status === "review") return "review"
    if (status === "running") return "running"
    if (status === "planning") return "planning"
    // Map phase-based tasks
    var phase = task.phase || ""
    if (phase === "done") return "done"
    if (phase === "review") return "review"
    if (phase === "execute") return "running"
    if (phase === "plan") return "planning"
    if (phase === "brainstorm") return "planning"
    return "backlog"
  }

  function createKanbanCard(task, colMeta) {
    var card = el("div", "kanban-card" + (state.selectedTaskId === task.id ? " kanban-card--selected" : ""))
    card.style.borderLeftColor = colMeta.color

    card.addEventListener("click", function () {
      state.selectedTaskId = task.id
      renderKanbanView()
    })

    // Project name
    card.appendChild(el("div", "kanban-card-project", task.project || ""))

    // Description
    card.appendChild(el("div", "kanban-card-desc", task.description || ""))

    // Footer: id + agent status
    var footer = el("div", "kanban-card-footer")
    footer.appendChild(el("span", "kanban-card-id", task.id || ""))

    if (task.agentStatus) {
      var meta = AGENT_STATUS_META[task.agentStatus]
      if (meta) {
        var statusEl = el("span", "kanban-card-status")
        statusEl.style.color = meta.color
        statusEl.textContent = meta.icon
        footer.appendChild(statusEl)
      }
    }

    card.appendChild(footer)
    return card
  }

  // ── Workspaces view ──────────────────────────────────────────────
  function renderWorkspacesView() {
    var chatBar = document.getElementById("chat-bar")
    var wsEl = document.getElementById("workspaces-view")

    if (!wsEl) {
      wsEl = el("div", "workspaces-view")
      wsEl.id = "workspaces-view"
      var parent = document.querySelector(".main-content")
      if (parent && chatBar) {
        parent.insertBefore(wsEl, chatBar)
      } else if (parent) {
        parent.appendChild(wsEl)
      }
    }

    clearChildren(wsEl)
    wsEl.style.display = ""

    // Fetch workspaces from API (graceful fallback to projects)
    fetch(apiPathWithToken("/api/workspaces"), { headers: authHeaders() })
      .then(function (r) {
        if (!r.ok) throw new Error("workspaces fetch failed: " + r.status)
        return r.json()
      })
      .then(function (data) {
        buildWorkspacesContent(wsEl, data.workspaces || [])
      })
      .catch(function () {
        // No workspaces API — derive from projects
        var workspaces = (state.projects || []).map(function (p) {
          return {
            name: p.name,
            desc: p.description || "",
            template: p.template || "claude-sandbox",
            status: p.containerStatus || "running",
            path: p.path || "/workspace/" + p.name,
            cpu: p.cpu || "2.0",
            mem: p.mem || "2GB",
            container: p.container || "coder-" + p.name,
          }
        })
        buildWorkspacesContent(wsEl, workspaces)
      })
  }

  function buildWorkspacesContent(container, workspaces) {
    clearChildren(container)

    // Section header
    container.appendChild(el("div", "workspaces-header", "Workspaces \u00B7 OpenTofu"))

    if (!workspaces || !workspaces.length) {
      var emptyCard = el("div", "workspace-card")
      emptyCard.appendChild(el("div", "workspace-card-name", "No workspaces configured"))
      emptyCard.appendChild(el("div", "workspace-card-desc", "Provision a workspace to get started"))
      container.appendChild(emptyCard)
    } else {
      for (var i = 0; i < workspaces.length; i++) {
        container.appendChild(createWorkspaceCard(workspaces[i]))
      }
    }

    // Provision button
    var provBtn = el("button", "workspace-provision-btn", "+ Provision new workspace (tofu apply)")
    container.appendChild(provBtn)
  }

  function createWorkspaceCard(ws) {
    var card = el("div", "workspace-card")

    // Top row: name + status badge + start/stop button
    var top = el("div", "workspace-card-top")

    top.appendChild(el("span", "workspace-card-name", ws.name))

    var actions = el("div", "workspace-card-actions")

    // Status badge
    var isRunning = ws.status === "running"
    var badge = el("span", "workspace-badge")
    var badgeIcon = el("span", "")
    badgeIcon.style.color = isRunning ? "var(--accent)" : "var(--text-dim)"
    badgeIcon.textContent = isRunning ? "\u27F3" : "\u25CB"
    badge.appendChild(badgeIcon)
    badge.appendChild(document.createTextNode(" " + (ws.status || "stopped")))
    badge.style.color = isRunning ? "var(--accent)" : "var(--text-dim)"
    actions.appendChild(badge)

    // Start / Stop button
    if (isRunning) {
      var stopBtn = el("button", "workspace-btn-stop", "Stop")
      stopBtn.title = "Stop — coming soon"
      stopBtn.disabled = true
      stopBtn.style.opacity = "0.5"
      stopBtn.style.cursor = "default"
      actions.appendChild(stopBtn)
    } else {
      var startBtn = el("button", "workspace-btn-start", "Start")
      startBtn.title = "Start — coming soon"
      startBtn.disabled = true
      startBtn.style.opacity = "0.5"
      startBtn.style.cursor = "default"
      actions.appendChild(startBtn)
    }

    top.appendChild(actions)
    card.appendChild(top)

    // Description
    if (ws.desc) {
      card.appendChild(el("div", "workspace-card-desc", ws.desc))
    }

    // Stats row
    var stats = el("div", "workspace-card-stats")
    stats.appendChild(el("span", "", "template: " + (ws.template || "n/a")))
    stats.appendChild(el("span", "", "cpu: " + (ws.cpu || "n/a")))
    stats.appendChild(el("span", "", "mem: " + (ws.mem || "n/a")))
    card.appendChild(stats)

    // Container + path
    var pathLine = el("div", "workspace-card-path")
    pathLine.textContent = "container: " + (ws.container || "n/a") + " \u00B7 path: " + (ws.path || "n/a")
    card.appendChild(pathLine)

    // Active agents count
    var tasks = state.tasks || []
    var agentCount = 0
    for (var j = 0; j < tasks.length; j++) {
      var t = tasks[j]
      if (t.project === ws.name && t.tmuxSession && t.status !== "done" && t.status !== "backlog") {
        agentCount++
      }
    }
    if (agentCount > 0) {
      card.appendChild(el("div", "workspace-card-agents",
        agentCount + " active tmux session" + (agentCount > 1 ? "s" : "")
      ))
    }

    return card
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

    // Left border color: orange for waiting agents, otherwise from task status
    var borderColor = getCardBorderColor(task)
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

    // Footer row 1: status badge + INPUT badge
    var footer = el("div", "agent-card-footer")

    var leftBadges = el("div", null)
    leftBadges.style.display = "flex"
    leftBadges.style.alignItems = "center"
    leftBadges.style.gap = "6px"

    // INPUT badge if waiting with question (shows before status badge per design)
    if (task.agentStatus === "waiting" && task.askQuestion) {
      var askBadge = el("span", "ask-badge", "\u25D0 INPUT")
      leftBadges.appendChild(askBadge)
    }

    leftBadges.appendChild(createAgentStatusBadge(task.agentStatus))
    footer.appendChild(leftBadges)
    card.appendChild(footer)

    // Footer row 2: task ID + time | mini session chain bars
    var footer2 = el("div", "agent-card-footer")
    footer2.style.justifyContent = "space-between"
    var idTime = el("span", "agent-card-time", task.id + " \u00B7 " + (task.time || formatDuration(task.createdAt)))
    footer2.appendChild(idTime)
    footer2.appendChild(createMiniSessionChain(task))
    card.appendChild(footer2)

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

  // ── Mini session chain (colored bars in card footer) ──────────────
  function createMiniSessionChain(task) {
    var chain = el("div", "mini-session-chain")

    // Use sessions array if available, otherwise fall back to phase
    var segments = []
    if (task.sessions && task.sessions.length > 0) {
      for (var i = 0; i < task.sessions.length; i++) {
        segments.push({
          phase: task.sessions[i].phase,
          status: task.sessions[i].status,
        })
      }
    } else if (task.phase) {
      var currentIdx = PHASES.indexOf(task.phase)
      for (var j = 0; j < PHASES.length; j++) {
        segments.push({
          phase: PHASES[j],
          status: j < currentIdx ? "complete" : (j === currentIdx ? "active" : "pending"),
        })
      }
    }

    for (var k = 0; k < segments.length; k++) {
      var bar = el("div", "mini-bar")
      if (segments[k].status === "complete") {
        bar.style.background = "var(--green)"
      } else if (segments[k].status === "active") {
        bar.style.background = PHASE_COLORS_HEX[segments[k].phase] || "var(--accent)"
      }
      bar.title = segments[k].phase
      chain.appendChild(bar)
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
        if (task) {
          cards[i].style.borderLeftColor = getCardBorderColor(task)
        }
      } else {
        cards[i].classList.remove("agent-card--selected")
        var otherTask = findTask(cards[i].dataset.taskId)
        if (otherTask) {
          cards[i].style.borderLeftColor = getCardBorderColor(otherTask)
        }
      }
    }

    if (task) {
      renderRightPanel(task)
      connectTerminal(task)
    }

    renderTopBar()

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
    renderClaudeMeta(task)
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

  // ── Preview header (rich) ──────────────────────────────────────────
  function renderPreviewHeader(task) {
    var container = document.getElementById("preview-header")
    if (!container) return

    clearChildren(container)

    // "PREVIEW" label
    container.appendChild(el("div", "preview-header-label", "Preview"))

    // Row: project + agent status | NEEDS INPUT badge
    var row = el("div", "preview-header-row")

    var leftGroup = el("div", null)
    leftGroup.appendChild(el("span", "preview-header-project", task.project || "\u2014"))

    var agentMeta = AGENT_STATUS_META[task.agentStatus] || AGENT_STATUS_META.idle
    var statusSpan = el("span", "preview-header-agent-status")
    statusSpan.textContent = agentMeta.icon + " " + agentMeta.label
    statusSpan.style.color = agentMeta.color
    if (agentMeta === AGENT_STATUS_META.waiting || agentMeta === AGENT_STATUS_META.thinking) {
      statusSpan.style.animation = "pulse-dot 1.5s ease-in-out infinite"
    }
    leftGroup.appendChild(statusSpan)
    row.appendChild(leftGroup)

    // NEEDS INPUT badge
    if (task.agentStatus === "waiting" && task.askQuestion) {
      var needsInput = el("span", "preview-header-needs-input", "NEEDS INPUT")
      needsInput.style.background = "rgba(245, 158, 11, 0.12)"
      needsInput.style.border = "1px solid rgba(245, 158, 11, 0.25)"
      needsInput.style.color = "var(--orange)"
      row.appendChild(needsInput)
    }
    container.appendChild(row)

    // Meta: workspace path + active time
    var meta = el("div", "preview-header-meta")
    var path = task.workspacePath || "/workspace/" + (task.project || "unknown")
    meta.appendChild(document.createTextNode("\uD83D\uDCC1 " + path))
    if (task.createdAt || task.time) {
      var timeSpan = el("span", "preview-header-meta-time")
      timeSpan.textContent = "\u23F1 active " + (task.time || formatDuration(task.createdAt))
      meta.appendChild(timeSpan)
    }
    container.appendChild(meta)

    // Skills badges
    var skills = task.skills || []
    if (skills.length > 0) {
      var skillsRow = el("div", "preview-header-skills")
      for (var i = 0; i < skills.length; i++) {
        skillsRow.appendChild(el("span", "preview-header-skill", skills[i]))
      }
      container.appendChild(skillsRow)
    }
  }

  // ── Claude metadata section ──────────────────────────────────────
  function renderClaudeMeta(task) {
    var container = document.getElementById("claude-meta")
    if (!container) return

    clearChildren(container)

    // Row 1: connection status + session ID
    var row1 = el("div", "claude-meta-row")

    var statusLabel = el("span", null)
    statusLabel.appendChild(document.createTextNode("Status: "))
    if (task.tmuxSession) {
      var connSpan = el("span", "claude-meta-connected", "\u25CF Connected")
      statusLabel.appendChild(connSpan)
    } else {
      var discSpan = el("span", "claude-meta-disconnected", "\u25CB Disconnected")
      statusLabel.appendChild(discSpan)
    }
    row1.appendChild(statusLabel)

    // Session ID (from active session in chain)
    var activeSession = null
    if (task.sessions) {
      for (var i = 0; i < task.sessions.length; i++) {
        if (task.sessions[i].status === "active") {
          activeSession = task.sessions[i]
          break
        }
      }
    }
    if (activeSession && activeSession.claudeSessionId) {
      var sidLabel = el("span", null)
      sidLabel.appendChild(document.createTextNode("Session: "))
      sidLabel.appendChild(el("span", "claude-meta-session-id", activeSession.claudeSessionId))
      row1.appendChild(sidLabel)
    }
    container.appendChild(row1)

    // MCPs
    var mcps = task.mcps || []
    if (mcps.length > 0) {
      var mcpRow = el("div", "claude-meta-mcps")
      mcpRow.appendChild(document.createTextNode("MCPs: "))
      for (var m = 0; m < mcps.length; m++) {
        mcpRow.appendChild(el("span", "claude-meta-mcp-name", mcps[m] + " \u00D7"))
      }
      container.appendChild(mcpRow)
    }

    // Fork hints
    var hints = el("div", "claude-meta-hints")
    hints.appendChild(document.createTextNode("Fork: "))
    var keyHint = el("span", "claude-meta-hint-key", "f quick fork, F fork with options")
    hints.appendChild(keyHint)
    container.appendChild(hints)
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
    renderTopBar()
    renderChatBar()
    renderAskBanner()
  }

  // ── Chat mode colors ────────────────────────────────────────────────
  var CHAT_MODES = {
    reply:     { icon: "\u21A9", color: "#e8a932", label: "Reply" },
    new:       { icon: "+", color: "#4ca8e8", label: "New task" },
    conductor: { icon: "\u25CE", color: "#8b8cf8", label: "Conductor" },
  }

  // ── Slash command definitions ─────────────────────────────────────
  var HUB_COMMANDS = [
    { cmd: "/new", desc: "Create new task (override reply)", group: "Hub" },
    { cmd: "/fork", desc: "Fork \u2192 new sibling task", group: "Hub" },
    { cmd: "/diff", desc: "View git diff for task", group: "Hub" },
    { cmd: "/approve", desc: "Approve and merge", group: "Hub" },
    { cmd: "/reject", desc: "Reject task changes", group: "Hub" },
    { cmd: "/status", desc: "All agent statuses", group: "Hub" },
    { cmd: "/sessions", desc: "List sessions for task", group: "Hub" },
    { cmd: "/conductor", desc: "Message conductor", group: "Hub" },
  ]
  var CLAUDE_COMMANDS = [
    { cmd: "/compact", desc: "Compact conversation context", group: "Claude Code" },
    { cmd: "/permissions", desc: "Toggle bypass mode", group: "Claude Code" },
    { cmd: "/memory", desc: "View/edit CLAUDE.md", group: "Claude Code" },
    { cmd: "/cost", desc: "Token usage this session", group: "Claude Code" },
    { cmd: "/clear", desc: "Clear conversation", group: "Claude Code" },
  ]
  var SKILL_COMMANDS = [
    { cmd: "/test", desc: "Run test suite", group: "Skills" },
    { cmd: "/lint", desc: "Run linter", group: "Skills" },
    { cmd: "/deploy", desc: "Deploy to staging", group: "Skills" },
  ]

  var GROUP_COLORS = {
    Hub: "var(--accent)",
    "Claude Code": "var(--purple)",
    Skills: "var(--green)",
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
        color: CHAT_MODES.reply.color,
        tmuxSession: task.tmuxSession,
        taskId: task.id,
        askQuestion: task.askQuestion,
      }
    }

    if (state.activeView === "conductor") {
      return {
        mode: "conductor",
        label: "\u25CE Conductor",
        icon: "\u25CE",
        color: CHAT_MODES.conductor.color,
      }
    }

    var project = ""
    if (task) project = task.project
    else if (state.projectFilter) project = state.projectFilter

    return {
      mode: "new",
      label: project ? "+ " + project : "+ auto-route",
      icon: "+",
      color: CHAT_MODES.new.color,
      target: project,
    }
  }

  function renderChatBar() {
    var mode = detectChatMode()
    state.chatMode = mode

    var modeBtn = document.getElementById("chat-mode-btn")
    var modeIcon = document.getElementById("chat-mode-icon")
    var modeLabel = document.getElementById("chat-mode-label")
    var input = document.getElementById("chat-input")
    var hint = document.getElementById("chat-bar-hint")
    var sendBtn = document.getElementById("chat-send-btn")

    // Style mode button with color tint
    if (modeBtn) {
      modeBtn.style.background = mode.color + "12"
      modeBtn.style.borderColor = mode.color + "30"
      modeBtn.style.color = mode.color
    }
    if (modeIcon) { modeIcon.textContent = mode.icon; modeIcon.style.color = mode.color }
    if (modeLabel) { modeLabel.textContent = mode.label; modeLabel.style.color = mode.color }

    // Placeholder
    if (input) {
      if (mode.mode === "reply" && mode.askQuestion) {
        input.placeholder = "Answer: " + mode.askQuestion
      } else if (mode.mode === "reply") {
        input.placeholder = "Reply to " + (mode.taskId || "agent") + " / " + (mode.sessionPhase || "session") + "..."
      } else if (mode.mode === "new" && mode.target) {
        input.placeholder = "New task in " + mode.target + "..."
      } else if (mode.mode === "new") {
        input.placeholder = "Describe a task (conductor will route)..."
      } else if (mode.mode === "conductor") {
        input.placeholder = "Message conductor..."
      } else {
        input.placeholder = "Type a message..."
      }
    }

    // "via tmux send-keys" hint
    if (hint) {
      var tmuxTarget = mode.tmuxSession || "new session"
      hint.textContent = "via tmux send-keys \u2192 " + tmuxTarget
    }

    // Send button state
    updateSendButton()
  }

  function updateSendButton() {
    var input = document.getElementById("chat-input")
    var sendBtn = document.getElementById("chat-send-btn")
    if (!input || !sendBtn) return

    if (input.value.trim()) {
      sendBtn.classList.add("chat-send-btn--active")
    } else {
      sendBtn.classList.remove("chat-send-btn--active")
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

    var msgSpan = el("span", null, "Agent is asking: ")
    var qSpan = el("span", null, task.askQuestion)
    qSpan.style.color = "var(--text)"
    banner.appendChild(msgSpan)
    banner.appendChild(qSpan)

    var chatBar = document.getElementById("chat-bar")
    if (chatBar && chatBar.parentNode) {
      chatBar.parentNode.insertBefore(banner, chatBar)
    }
  }

  // ── Slash command palette ──────────────────────────────────────────
  function renderSlashPalette() {
    var existing = document.querySelector(".slash-palette")
    if (existing) existing.remove()

    var input = document.getElementById("chat-input")
    var value = input ? input.value : ""
    if (!value.startsWith("/")) return

    var mode = state.chatMode || detectChatMode()
    var isProjectMode = mode.mode === "reply" || (mode.mode === "new" && mode.target)

    var allCommands = HUB_COMMANDS.slice()
    if (isProjectMode) {
      allCommands = allCommands.concat(CLAUDE_COMMANDS)
      allCommands = allCommands.concat(SKILL_COMMANDS)
    }

    // Filter by typed text
    var filter = value.toLowerCase()
    var filtered = []
    for (var i = 0; i < allCommands.length; i++) {
      if (allCommands[i].cmd.includes(filter)) filtered.push(allCommands[i])
    }
    if (filtered.length === 0) return

    var palette = el("div", "slash-palette open")

    // Group commands
    var groups = []
    var groupMap = {}
    for (var j = 0; j < filtered.length; j++) {
      var g = filtered[j].group
      if (!groupMap[g]) {
        groupMap[g] = []
        groups.push(g)
      }
      groupMap[g].push(filtered[j])
    }

    for (var k = 0; k < groups.length; k++) {
      var groupName = groups[k]
      palette.appendChild(el("div", "slash-group-header", groupName))

      var cmds = groupMap[groupName]
      for (var c = 0; c < cmds.length; c++) {
        var cmdBtn = el("button", "slash-command")
        var nameSpan = el("span", "slash-command-name", cmds[c].cmd)
        nameSpan.style.color = GROUP_COLORS[groupName] || "var(--text)"
        cmdBtn.appendChild(nameSpan)
        cmdBtn.appendChild(el("span", "slash-command-desc", cmds[c].desc))
        cmdBtn.dataset.cmd = cmds[c].cmd
        cmdBtn.addEventListener("click", function (e) {
          var cmdVal = e.currentTarget.dataset.cmd
          if (input) { input.value = cmdVal + " "; input.focus() }
          closeSlashPalette()
        })
        palette.appendChild(cmdBtn)
      }
    }

    var chatBar = document.getElementById("chat-bar")
    if (chatBar) chatBar.appendChild(palette)
  }

  function closeSlashPalette() {
    var p = document.querySelector(".slash-palette")
    if (p) p.remove()
  }

  // ── Chat mode override menu ────────────────────────────────────────
  function renderModeMenu() {
    var existing = document.querySelector(".chat-mode-menu")
    if (existing) existing.remove()

    var menu = el("div", "chat-mode-menu open")
    var mode = state.chatMode || detectChatMode()

    // Header
    menu.appendChild(el("div", "chat-mode-menu-header", "Switch context"))

    // "Back to auto" if overridden (show first)
    if (state.chatModeOverride) {
      var backOpt = createModeOption("\u2190", "var(--text-dim)", "\u2190 Back to: auto", "Use auto-detected context")
      backOpt.dataset.mode = "auto"
      backOpt.addEventListener("click", handleModeSelect)
      menu.appendChild(backOpt)
    }

    // "New in {project}" for each project (skip current if reply mode)
    for (var i = 0; i < state.projects.length; i++) {
      var proj = state.projects[i]
      if (mode.mode === "reply" && proj.name === mode.target) continue
      var opt = createModeOption("+", CHAT_MODES.new.color, "+ New in " + proj.name, "New task in " + proj.name)
      opt.dataset.mode = "new"
      opt.dataset.project = proj.name
      opt.addEventListener("click", handleModeSelect)
      menu.appendChild(opt)
    }

    // "New (auto-route)"
    var autoOpt = createModeOption("+", CHAT_MODES.new.color, "+ New (auto-route)", "Conductor picks project")
    autoOpt.dataset.mode = "new"
    autoOpt.dataset.project = ""
    autoOpt.addEventListener("click", handleModeSelect)
    menu.appendChild(autoOpt)

    // "Message conductor"
    var condOpt = createModeOption("\u25CE", CHAT_MODES.conductor.color, "\u25CE Message conductor", "Orchestration commands")
    condOpt.dataset.mode = "conductor"
    condOpt.dataset.project = ""
    condOpt.addEventListener("click", handleModeSelect)
    menu.appendChild(condOpt)

    var chatBar = document.getElementById("chat-bar")
    if (chatBar) chatBar.appendChild(menu)

    setTimeout(function () {
      document.addEventListener("click", closeModeMenu)
    }, 0)
  }

  function createModeOption(iconText, iconColor, label, desc) {
    var opt = el("button", "chat-mode-option")
    var icon = el("span", "chat-mode-option-icon", iconText)
    icon.style.color = iconColor
    opt.appendChild(icon)
    var textWrap = el("div", "chat-mode-option-text")
    textWrap.appendChild(el("div", "chat-mode-option-label", label))
    if (desc) textWrap.appendChild(el("div", "chat-mode-option-desc", desc))
    opt.appendChild(textWrap)
    return opt
  }

  function handleModeSelect(e) {
    var btn = e.currentTarget
    if (btn.dataset.mode === "auto") {
      state.chatModeOverride = null
    } else if (btn.dataset.mode === "conductor") {
      state.chatModeOverride = {
        mode: "conductor",
        label: "\u25CE Conductor",
        icon: "\u25CE",
        color: CHAT_MODES.conductor.color,
      }
    } else {
      state.chatModeOverride = {
        mode: btn.dataset.mode,
        label: btn.dataset.project ? "+ " + btn.dataset.project : "+ auto-route",
        icon: "+",
        color: CHAT_MODES.new.color,
        target: btn.dataset.project,
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
    } else if (mode.mode === "conductor") {
      sendConductorMessage(text)
    } else {
      openNewTaskModalWithDescription(text)
    }
    input.value = ""
    updateSendButton()
    closeSlashPalette()
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

  function sendConductorMessage(text) {
    var headers = authHeaders()
    headers["Content-Type"] = "application/json"

    fetch(apiPathWithToken("/api/conductor/input"), {
      method: "POST",
      headers: headers,
      body: JSON.stringify({ input: text }),
    })
      .then(function (r) {
        if (!r.ok) throw new Error("conductor send failed: " + r.status)
      })
      .catch(function (err) {
        console.error("sendConductorMessage:", err)
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
      if (e.key === "Escape") {
        closeSlashPalette()
      }
    })
    chatInput.addEventListener("input", function () {
      updateSendButton()
      if (chatInput.value.startsWith("/")) {
        renderSlashPalette()
      } else {
        closeSlashPalette()
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
      renderTopBar()
      renderView()
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
  renderTopBar()
  renderView()
  renderChatBar()
  fetchTasks()
  fetchProjects()
  connectSSE()
})()
