(function () {
  "use strict"

  // ── DOM references ──────────────────────────────────────────────
  var metaState = document.getElementById("meta-state")
  var taskGrid = document.getElementById("task-grid")
  var taskEmpty = document.getElementById("task-empty")
  var filterStatus = document.getElementById("filter-status")
  var filterProject = document.getElementById("filter-project")
  var detailPanel = document.getElementById("detail-panel")
  var detailBackdrop = document.getElementById("detail-backdrop")
  var detailBack = document.getElementById("detail-back")
  var detailBody = document.getElementById("detail-body")
  var chatInput = document.getElementById("detail-chat-input")
  var chatSend = document.getElementById("detail-chat-send")
  var newTaskBtn = document.getElementById("new-task-btn")
  var newTaskModal = document.getElementById("new-task-modal")
  var newTaskBackdrop = document.getElementById("new-task-backdrop")
  var newTaskClose = document.getElementById("new-task-close")
  var newTaskCancel = document.getElementById("new-task-cancel")
  var newTaskSubmit = document.getElementById("new-task-submit")
  var newTaskProject = document.getElementById("new-task-project")
  var newTaskDesc = document.getElementById("new-task-desc")
  var newTaskPhase = document.getElementById("new-task-phase")
  var routeSuggestion = document.getElementById("route-suggestion")

  // ── State ───────────────────────────────────────────────────────
  var state = {
    tasks: [],
    projects: [],
    selectedTaskId: null,
    authToken: readAuthTokenFromURL(),
    menuEvents: null,
  }

  // ── Status metadata ─────────────────────────────────────────────
  var STATUS_META = {
    thinking: { icon: "\u25CF", label: "Thinking" },
    waiting:  { icon: "\u25D0", label: "Waiting" },
    running:  { icon: "\u27F3", label: "Running" },
    idle:     { icon: "\u25CB", label: "Idle" },
    error:    { icon: "\u2715", label: "Error" },
    complete: { icon: "\u2713", label: "Complete" },
  }

  var PHASES = ["brainstorm", "plan", "execute", "review"]
  var PHASE_DOT_LABELS = { brainstorm: "B", plan: "P", execute: "E", review: "R" }
  var PHASE_LABELS = { brainstorm: "Brainstorm", plan: "Plan", execute: "Execute", review: "Review" }

  // ── Auth ────────────────────────────────────────────────────────
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

  // ── Helpers: safe DOM construction ──────────────────────────────
  function el(tag, className, textContent) {
    var node = document.createElement(tag)
    if (className) node.className = className
    if (textContent != null) node.textContent = textContent
    return node
  }

  function clearChildren(parent) {
    while (parent.firstChild) parent.removeChild(parent.firstChild)
  }

  // ── Data fetching ───────────────────────────────────────────────
  function fetchTasks() {
    return fetch(apiPathWithToken("/api/tasks"), { headers: authHeaders() })
      .then(function (r) {
        if (!r.ok) throw new Error("tasks fetch failed: " + r.status)
        return r.json()
      })
      .then(function (data) {
        state.tasks = data.tasks || []
        renderTasks()
        // Re-render open detail if task data changed
        if (state.selectedTaskId) {
          var task = findTask(state.selectedTaskId)
          if (task) renderDetail(task)
        }
      })
      .catch(function (err) {
        console.error("fetchTasks:", err)
        state.tasks = []
        renderTasks()
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
        renderProjectFilter()
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

  // ── SSE ─────────────────────────────────────────────────────────
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
        renderTasks()
        if (state.selectedTaskId) {
          var task = findTask(state.selectedTaskId)
          if (task) renderDetail(task)
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
    if (!metaState) return
    metaState.textContent = s
    metaState.className = "meta state-" + s
  }

  // ── Rendering: task cards ───────────────────────────────────────
  function renderTasks() {
    if (!taskGrid) return

    var statusVal = filterStatus ? filterStatus.value : ""
    var projectVal = filterProject ? filterProject.value : ""

    var visible = state.tasks.filter(function (t) {
      if (statusVal && t.status !== statusVal) return false
      if (projectVal && t.project !== projectVal) return false
      return true
    })

    // Remove existing cards
    var cards = taskGrid.querySelectorAll(".task-card")
    for (var i = 0; i < cards.length; i++) {
      cards[i].remove()
    }

    if (visible.length === 0) {
      if (taskEmpty) {
        taskEmpty.style.display = ""
        taskEmpty.textContent =
          state.tasks.length === 0
            ? "No tasks yet."
            : "No tasks match the current filters."
      }
      return
    }

    if (taskEmpty) taskEmpty.style.display = "none"

    for (var j = 0; j < visible.length; j++) {
      taskGrid.appendChild(createTaskCard(visible[j]))
    }
  }

  function createTaskCard(task) {
    var card = el("div", "task-card")
    card.setAttribute("data-task-id", task.id)
    card.setAttribute("role", "button")
    card.setAttribute("tabindex", "0")

    var sm = STATUS_META[task.status] || STATUS_META.idle

    // Top row: status dot + project name
    var top = el("div", "task-card-top")

    var dot = el("span", "task-status-dot task-status--" + task.status)
    dot.title = sm.label
    top.appendChild(dot)

    top.appendChild(el("span", "task-project", task.project || "\u2014"))
    card.appendChild(top)

    // ID + phase row
    card.appendChild(
      el("div", "task-id-phase", task.id + " \u00B7 " + (task.phase || "\u2014"))
    )

    // Description
    if (task.description) {
      card.appendChild(el("div", "task-description", task.description))
    }

    // Footer: duration + branch
    var footer = el("div", "task-footer")
    footer.appendChild(el("span", "task-duration", formatDuration(task.createdAt)))
    if (task.branch) {
      footer.appendChild(el("span", "task-branch", task.branch))
    }
    card.appendChild(footer)

    // Click handler
    card.addEventListener("click", function () {
      openDetail(task.id)
    })
    card.addEventListener("keydown", function (e) {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault()
        openDetail(task.id)
      }
    })

    return card
  }

  // ── Rendering: project filter ───────────────────────────────────
  function renderProjectFilter() {
    if (!filterProject) return

    while (filterProject.options.length > 1) {
      filterProject.remove(1)
    }

    for (var i = 0; i < state.projects.length; i++) {
      var opt = document.createElement("option")
      opt.value = state.projects[i].name
      opt.textContent = state.projects[i].name
      filterProject.appendChild(opt)
    }
  }

  // ── Detail panel ────────────────────────────────────────────────
  function openDetail(taskId) {
    var task = findTask(taskId)
    if (!task) return

    state.selectedTaskId = taskId
    renderDetail(task)

    if (detailPanel) detailPanel.classList.add("open")
    if (detailBackdrop) detailBackdrop.classList.add("open")
    if (detailPanel) detailPanel.setAttribute("aria-hidden", "false")
  }

  function closeDetail() {
    state.selectedTaskId = null
    if (detailPanel) detailPanel.classList.remove("open")
    if (detailBackdrop) detailBackdrop.classList.remove("open")
    if (detailPanel) detailPanel.setAttribute("aria-hidden", "true")
  }

  function renderDetail(task) {
    if (!detailBody) return

    var sm = STATUS_META[task.status] || STATUS_META.idle

    clearChildren(detailBody)

    // Title row
    var title = el("div", "detail-title")
    var titleDot = el("span", "task-status-dot task-status--" + task.status)
    titleDot.style.display = "inline-block"
    titleDot.style.verticalAlign = "middle"
    titleDot.style.marginRight = "8px"
    title.appendChild(titleDot)
    title.appendChild(document.createTextNode(
      (task.project || "\u2014") + " \u00B7 " + task.id
    ))
    detailBody.appendChild(title)

    // Description
    detailBody.appendChild(
      el("div", "detail-meta", task.description || "No description")
    )

    // Phase progress
    var phaseSection = el("div", "detail-section")
    phaseSection.appendChild(el("div", "detail-section-label", "Phase"))
    phaseSection.appendChild(buildPhaseTrack(task.phase))
    detailBody.appendChild(phaseSection)

    // Details section
    var infoSection = el("div", "detail-section")
    infoSection.appendChild(el("div", "detail-section-label", "Details"))

    var infoGrid = el("div")
    infoGrid.style.fontSize = "0.88rem"
    infoGrid.style.lineHeight = "1.8"

    appendInfoRow(infoGrid, "Status", sm.label)
    if (task.branch) appendInfoRow(infoGrid, "Branch", task.branch)
    appendInfoRow(infoGrid, "Duration", formatDuration(task.createdAt))
    if (task.sessionId) appendInfoRow(infoGrid, "Session", task.sessionId)
    if (task.parentTaskId) appendInfoRow(infoGrid, "Parent", task.parentTaskId)

    infoSection.appendChild(infoGrid)
    detailBody.appendChild(infoSection)

    // Terminal preview placeholder
    var termSection = el("div", "detail-section")
    termSection.appendChild(el("div", "detail-section-label", "Terminal"))

    var termBox = el("div", "detail-terminal")
    var termPlaceholder = el("div", "detail-terminal-placeholder")
    termPlaceholder.textContent = task.sessionId
      ? "Terminal preview for session " + task.sessionId + "..."
      : "No session attached."
    termBox.appendChild(termPlaceholder)
    termSection.appendChild(termBox)
    detailBody.appendChild(termSection)
  }

  function appendInfoRow(parent, label, value) {
    var strong = document.createElement("strong")
    strong.textContent = label + ": "
    parent.appendChild(strong)
    parent.appendChild(document.createTextNode(value))
    parent.appendChild(document.createElement("br"))
  }

  function buildPhaseTrack(currentPhase) {
    var currentIdx = PHASES.indexOf(currentPhase)
    if (currentIdx < 0) currentIdx = -1

    var track = el("div", "phase-track")

    for (var i = 0; i < PHASES.length; i++) {
      // Connector between phases
      if (i > 0) {
        var conn = el("div", i <= currentIdx ? "phase-connector done" : "phase-connector")
        track.appendChild(conn)
      }

      // Phase pip
      var pip = el("div", "phase-pip")

      var dotClass = "phase-dot"
      if (i < currentIdx) dotClass += " done"
      else if (i === currentIdx) dotClass += " active"

      var phaseDot = el("div", dotClass, PHASE_DOT_LABELS[PHASES[i]])
      pip.appendChild(phaseDot)
      track.appendChild(pip)
    }

    // Labels row
    var labelsRow = el("div")
    labelsRow.style.display = "flex"
    labelsRow.style.justifyContent = "space-between"

    for (var j = 0; j < PHASES.length; j++) {
      var lblClass = j === currentIdx ? "phase-label active" : "phase-label"
      var lbl = el("div", lblClass, PHASE_LABELS[PHASES[j]])
      labelsRow.appendChild(lbl)
    }

    var container = el("div")
    container.appendChild(track)
    container.appendChild(labelsRow)
    return container
  }

  // ── Utilities ───────────────────────────────────────────────────
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

  // ── Chat input ──────────────────────────────────────────────────
  function sendChatInput() {
    if (!state.selectedTaskId || !chatInput) return
    var input = chatInput.value.trim()
    if (!input) return

    var headers = authHeaders()
    headers["Content-Type"] = "application/json"

    fetch(apiPathWithToken("/api/tasks/" + state.selectedTaskId + "/input"), {
      method: "POST",
      headers: headers,
      body: JSON.stringify({ input: input }),
    })
      .then(function (r) {
        if (!r.ok) throw new Error("send failed: " + r.status)
        chatInput.value = ""
      })
      .catch(function (err) {
        console.error("sendChatInput:", err)
      })
  }

  // ── New Task modal ───────────────────────────────────────────────
  function openNewTaskModal() {
    // Populate project selector from loaded projects.
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
        if (data.task && data.task.id) openDetail(data.task.id)
      })
      .catch(function (err) {
        console.error("submitNewTask:", err)
      })
  }

  // ── Auto-suggest project via routing ─────────────────────────────
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
          // Auto-select the suggested project in the dropdown.
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

  // ── Event listeners ─────────────────────────────────────────────
  if (filterStatus) {
    filterStatus.addEventListener("change", renderTasks)
  }
  if (filterProject) {
    filterProject.addEventListener("change", renderTasks)
  }
  if (detailBack) {
    detailBack.addEventListener("click", closeDetail)
  }
  if (detailBackdrop) {
    detailBackdrop.addEventListener("click", closeDetail)
  }

  if (chatSend) {
    chatSend.addEventListener("click", sendChatInput)
  }
  if (chatInput) {
    chatInput.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        e.preventDefault()
        sendChatInput()
      }
    })
  }
  if (newTaskBtn) {
    newTaskBtn.addEventListener("click", openNewTaskModal)
  }
  if (newTaskClose) {
    newTaskClose.addEventListener("click", closeNewTaskModal)
  }
  if (newTaskCancel) {
    newTaskCancel.addEventListener("click", closeNewTaskModal)
  }
  if (newTaskBackdrop) {
    newTaskBackdrop.addEventListener("click", closeNewTaskModal)
  }
  if (newTaskSubmit) {
    newTaskSubmit.addEventListener("click", submitNewTask)
  }
  if (newTaskDesc) {
    newTaskDesc.addEventListener("input", function () {
      suggestProject(newTaskDesc.value.trim())
    })
  }

  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") {
      if (newTaskModal && newTaskModal.classList.contains("open")) {
        closeNewTaskModal()
      } else if (state.selectedTaskId) {
        closeDetail()
      }
    }
  })

  // ── Init ────────────────────────────────────────────────────────
  fetchTasks()
  fetchProjects()
  connectSSE()
})()
