package session

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Background work (issue #2473): a running workflow means a running session.
//
// A Claude session can end its foreground turn — empty prompt, Stop hook
// written as "waiting" — while work it started keeps running: a Workflow,
// background agents and run_in_background shells. Until that finite work
// reports back the session is RUNNING with substate background-work. Armed
// Monitors and proven sleep-only until loops are watching, not running. Two
// sources prove the work is in flight:
//
//   - the pane (tmux.ParseClaudeBackgroundWork): the workflow progress row
//     under the footer ("◯ name ▰▰▱ 3/5 · 18m32s"), "Waiting for N background
//     agents / dynamic workflows to finish" as the turn's last line, and the
//     live "· 2 shells, 1 monitor ·" counter on the footer;
//   - the transcript (scanTranscriptBackground): a Workflow / background
//     Agent / background Bash / Monitor launch whose terminal
//     <task-notification> has not arrived, and the pending counts Claude Code
//     stamps on its turn_duration records (pendingWorkflowCount,
//     pendingBackgroundAgentCount).
//
// Merge rule (mergeBackgroundWork): pane OR transcript finite work => running +
// background-work; neither => today's logic. The transcript is what keeps a
// redraw, a resize or a capture that misses the footer from flipping the light,
// so transcript-only evidence holds for backgroundTranscriptHold after the
// newest sighting (pane or transcript record) and no longer: background tasks
// can die without a transcript marker (stopped from the /tasks UI, a killed
// Claude process), and a held light must never turn into a stuck one. The
// transcript also VETOES a stale pane row: a workflow row whose task has
// already reported a terminal status is history.
//
// Precedence with the hook (documented in docs/status-detection.md): the
// Stop hook writes "waiting" at the end of EVERY foreground turn, including the
// one that launched the background work. In the hook fast path a "waiting"
// hook therefore never overrides pane or transcript evidence of work in flight;
// the session stays running until that evidence is gone. The status the
// transition daemon and the turn journal see is this merged status, so no
// running -> waiting record is written while the work runs, and exactly one is
// written when it ends (through emitTurn, trigger "task").

// backgroundTranscriptHold bounds how long transcript-only evidence keeps a
// session running after the newest sighting of the work (the pane showing it,
// or the launch / turn-end record in the transcript).
const backgroundTranscriptHold = 3 * time.Minute

// transcriptBackgroundTask is one background launch found in the transcript.
type transcriptBackgroundTask struct {
	ID   string
	Kind string
	Name string
	At   time.Time
}

// transcriptBackgroundScan is what the transcript tail says about background
// work. Built by scanTranscriptBackground; cached with the turn classifier's
// tail read (turnFactsCache).
type transcriptBackgroundScan struct {
	// Pending holds launches with no terminal notification since, oldest first.
	Pending []transcriptBackgroundTask
	// FinishedWorkflows names workflows whose task reported a terminal status.
	FinishedWorkflows map[string]bool
	// TurnWorkflows / TurnAgents are the pending counts on the last main-chain
	// turn_duration record, less terminal notifications that arrived since.
	TurnWorkflows int
	TurnAgents    int
	TurnAt        time.Time
	// LastWorkflow / LastAgent name the newest launch of each kind, used to
	// label a count that outlived its launch record in the tail window.
	LastWorkflow string
	LastAgent    string
}

// bgTranscriptRecord is the subset of a transcript record the scan reads.
type bgTranscriptRecord struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	IsSidechain bool   `json:"isSidechain"`
	Timestamp   string `json:"timestamp"`
	Attachment  struct {
		Type     string `json:"type"`
		HookName string `json:"hookName"`
	} `json:"attachment"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	ToolUseResult               json.RawMessage `json:"toolUseResult"`
	PendingWorkflowCount        int             `json:"pendingWorkflowCount"`
	PendingBackgroundAgentCount int             `json:"pendingBackgroundAgentCount"`
}

// bgToolUseResult is the launch receipt Claude Code stores on the tool_result
// record of a background launch.
type bgToolUseResult struct {
	Status           string `json:"status"`
	TaskID           string `json:"taskId"`
	TaskType         string `json:"taskType"`
	WorkflowName     string `json:"workflowName"`
	IsAsync          bool   `json:"isAsync"`
	AgentID          string `json:"agentId"`
	Description      string `json:"description"`
	BackgroundTaskID string `json:"backgroundTaskId"`
}

// bgToolUse is the tool name and description of a tool_use block, keyed by
// its id so the launch receipt (a later tool_result) can be named.
type bgToolUse struct{ name, desc, command string }

type bgContentBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	Input     struct {
		Description string `json:"description"`
		Command     string `json:"command"`
	} `json:"input"`
}

var (
	// Deliberately a full, narrow grammar: filesystem predicate + sleep only.
	// Unknown shell programs remain running, including substitutions and trailing work.
	passiveUntilWaitRe = regexp.MustCompile(`^\s*until\s+\[\s+-(?:e|f|d)\s+[a-zA-Z0-9_./-]+\s+\]\s*;\s*do\s+sleep\s+[0-9]+(?:\.[0-9]+)?\s*;\s*done\s*;?\s*$`)
	taskIDRe           = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)
	taskStatusRe       = regexp.MustCompile(`<status>([^<]+)</status>`)
)

// taskNotificationBlocks splits a raw transcript line into the bodies of the
// <task-notification> blocks it carries (one line can hold several, and a
// block cut short by a size cap still yields its leading tags).
func taskNotificationBlocks(line string) []string {
	parts := strings.Split(line, "<task-notification>")
	blocks := make([]string, 0, len(parts)-1)
	for _, p := range parts[1:] {
		if end := strings.Index(p, "</task-notification>"); end >= 0 {
			p = p[:end]
		}
		blocks = append(blocks, p)
	}
	return blocks
}

// scanTranscriptBackground reads background launches, their terminal
// notifications and the turn-end pending counts from transcript lines (oldest
// first, as TranscriptTailLines returns them). Sidechain records (a
// sub-agent's own tools) are skipped: only the session's own launches count.
func scanTranscriptBackground(lines []string) transcriptBackgroundScan {
	sc := transcriptBackgroundScan{FinishedWorkflows: map[string]bool{}}
	uses := map[string]bgToolUse{}
	pending := map[string]transcriptBackgroundTask{}
	var order []string
	terminated := map[string]bool{}     // ids already counted against the turn counts
	launchedKind := map[string]string{} // id -> kind, for notifications

	for _, line := range lines {
		var rec bgTranscriptRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil || rec.IsSidechain {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, rec.Timestamp)
		switch rec.Type {
		case "assistant":
			var blocks []bgContentBlock
			if json.Unmarshal(rec.Message.Content, &blocks) == nil {
				for _, b := range blocks {
					if b.Type == "tool_use" && b.ID != "" {
						desc := b.Input.Description
						if desc == "" {
							desc = b.Input.Command
						}
						uses[b.ID] = bgToolUse{name: b.Name, desc: desc, command: b.Input.Command}
					}
				}
			}
			continue // an assistant record never carries a notification of its own
		case "system":
			if rec.Subtype == "turn_duration" {
				sc.TurnWorkflows = rec.PendingWorkflowCount
				sc.TurnAgents = rec.PendingBackgroundAgentCount
				sc.TurnAt = at
				terminated = map[string]bool{}
			}
			continue
		case "attachment":
			// A new Claude process (startup / resume) starts with no background
			// tasks: whatever the old process launched died with it.
			if hook := rec.Attachment.HookName; hook == "SessionStart:startup" || hook == "SessionStart:resume" {
				pending = map[string]transcriptBackgroundTask{}
				order = nil
				sc.TurnWorkflows, sc.TurnAgents = 0, 0
			}
		case "user":
			if task, ok := backgroundLaunch(rec, uses, at); ok {
				pending[task.ID] = task
				order = append(order, task.ID)
				launchedKind[task.ID] = task.Kind
				switch task.Kind {
				case tmux.BackgroundKindWorkflow:
					sc.LastWorkflow = task.Name
				case tmux.BackgroundKindAgent:
					sc.LastAgent = task.Name
				}
				continue
			}
		}
		// user / attachment / queue-operation records may carry notifications.
		if !strings.Contains(line, "<task-notification>") {
			continue
		}
		for _, block := range taskNotificationBlocks(line) {
			idm := taskIDRe.FindStringSubmatch(block)
			if idm == nil {
				continue
			}
			id := strings.TrimSpace(idm[1])
			terminal := taskStatusRe.MatchString(block) || strings.Contains(block, "[Monitor expired")
			if !terminal {
				continue
			}
			if task, ok := pending[id]; ok {
				if task.Kind == tmux.BackgroundKindWorkflow && task.Name != "" {
					sc.FinishedWorkflows[task.Name] = true
				}
				delete(pending, id)
			}
			if terminated[id] {
				continue
			}
			terminated[id] = true
			switch launchedKind[id] {
			case tmux.BackgroundKindWorkflow:
				if sc.TurnWorkflows > 0 {
					sc.TurnWorkflows--
				}
			case tmux.BackgroundKindAgent:
				if sc.TurnAgents > 0 {
					sc.TurnAgents--
				}
			}
		}
	}
	for _, id := range order {
		if task, ok := pending[id]; ok {
			sc.Pending = append(sc.Pending, task)
			delete(pending, id) // a relaunch of the same id is listed once
		}
	}
	return sc
}

// backgroundLaunch recognises the tool_result record of a background launch.
func backgroundLaunch(rec bgTranscriptRecord, uses map[string]bgToolUse, at time.Time) (transcriptBackgroundTask, bool) {
	if len(rec.ToolUseResult) == 0 || rec.ToolUseResult[0] != '{' {
		return transcriptBackgroundTask{}, false
	}
	var r bgToolUseResult
	if json.Unmarshal(rec.ToolUseResult, &r) != nil {
		return transcriptBackgroundTask{}, false
	}
	var use bgToolUse
	var blocks []bgContentBlock
	if json.Unmarshal(rec.Message.Content, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "tool_result" {
				use = uses[b.ToolUseID]
				break
			}
		}
	}
	switch {
	case r.TaskType == "local_workflow" || (use.name == "Workflow" && r.TaskID != ""):
		name := r.WorkflowName
		if name == "" {
			name = use.desc
		}
		return transcriptBackgroundTask{ID: r.TaskID, Kind: tmux.BackgroundKindWorkflow, Name: name, At: at}, r.TaskID != ""
	case r.AgentID != "" && (r.IsAsync || r.Status == "async_launched"):
		name := r.Description
		if name == "" {
			name = use.desc
		}
		return transcriptBackgroundTask{ID: r.AgentID, Kind: tmux.BackgroundKindAgent, Name: name, At: at}, true
	case use.name == "Monitor" && (r.TaskID != "" || r.BackgroundTaskID != ""):
		id := r.TaskID
		if id == "" {
			id = r.BackgroundTaskID
		}
		return transcriptBackgroundTask{ID: id, Kind: tmux.BackgroundKindMonitor, Name: use.desc, At: at}, true
	case r.BackgroundTaskID != "":
		kind := tmux.BackgroundKindBash
		if use.name == "Bash" && passiveUntilWaitRe.MatchString(use.command) {
			kind = tmux.BackgroundKindWatcher
		}
		return transcriptBackgroundTask{ID: r.BackgroundTaskID, Kind: kind, Name: use.desc, At: at}, true
	}
	return transcriptBackgroundTask{}, false
}

// inFlight returns the most relevant in-flight item the transcript proves
// (workflow, then agent, then bash, then monitor) and the time of its newest
// evidence (launch record or the turn_duration that counted it).
func (sc transcriptBackgroundScan) inFlight() (tmux.BackgroundWork, time.Time) {
	for _, kind := range []string{tmux.BackgroundKindWorkflow, tmux.BackgroundKindAgent, tmux.BackgroundKindBash, tmux.BackgroundKindMonitor, tmux.BackgroundKindWatcher} {
		var newest *transcriptBackgroundTask
		for idx := range sc.Pending {
			if sc.Pending[idx].Kind == kind {
				newest = &sc.Pending[idx]
			}
		}
		count, last := 0, ""
		switch kind {
		case tmux.BackgroundKindWorkflow:
			count, last = sc.TurnWorkflows, sc.LastWorkflow
		case tmux.BackgroundKindAgent:
			count, last = sc.TurnAgents, sc.LastAgent
		}
		if newest == nil && count == 0 {
			continue
		}
		work := tmux.BackgroundWork{Kind: kind, Source: "transcript"}
		if work.Watching() {
			work.Count = sc.passiveCount()
		}
		var at time.Time
		if newest != nil {
			work.Task, at = newest.Name, newest.At
		} else {
			work.Task = last
		}
		if count > 0 && sc.TurnAt.After(at) {
			at = sc.TurnAt
		}
		return work, at
	}
	return tmux.BackgroundWork{}, time.Time{}
}

// mergeBackgroundWork applies the merge rule (see the file comment). pane is
// the pane's in-flight item (zero when none); sc/scOK the transcript scan.
// paneShown reports whether the pane itself proved the work (the caller
// stamps that sighting for the transcript hold).
func mergeBackgroundWork(pane tmux.BackgroundWork, sc transcriptBackgroundScan, scOK bool, lastPaneSeen, now time.Time) (work tmux.BackgroundWork, paneShown bool) {
	if pane.InFlight() && scOK && pane.Kind == tmux.BackgroundKindWorkflow && pane.Steps > 0 &&
		sc.FinishedWorkflows[pane.Task] && !sc.pendingNamed(tmux.BackgroundKindWorkflow, pane.Task) {
		// The row outlived its workflow: the task already reported back.
		pane = tmux.BackgroundWork{}
	}
	if scOK {
		tx, at := sc.inFlight()
		if pane.Watching() && tx.Running() && now.Sub(at) <= backgroundTranscriptHold {
			return tx, false
		}
		if count := sc.passiveShellCount(pane.Task, now); pane.Kind == tmux.BackgroundKindBash && count > 0 {
			pane.Kind = tmux.BackgroundKindWatcher
			pane.Count = count
			pane.Source = "pane+transcript"
		}
	}
	if pane.InFlight() {
		if scOK && pane.Steps == 0 {
			// Counter or Waiting line: name the task from the transcript.
			if tx, _ := sc.inFlight(); tx.Task != "" && tx.Kind == pane.Kind {
				pane.Task = tx.Task
				pane.Source = "pane+transcript"
			}
		}
		return pane, true
	}
	if !scOK {
		return tmux.BackgroundWork{}, false
	}
	tx, at := sc.inFlight()
	if !tx.InFlight() {
		return tmux.BackgroundWork{}, false
	}
	if lastPaneSeen.After(at) {
		at = lastPaneSeen
	}
	if now.Sub(at) > backgroundTranscriptHold {
		return tmux.BackgroundWork{}, false
	}
	return tx, false
}

func (sc transcriptBackgroundScan) passiveCount() int {
	count := 0
	for _, task := range sc.Pending {
		if task.Kind == tmux.BackgroundKindMonitor || task.Kind == tmux.BackgroundKindWatcher {
			count++
		}
	}
	return count
}

var shellCountRe = regexp.MustCompile(`^(\d+) shells?(?:, (\d+) monitors?)?$`)

// passiveShellCount requires fresh receipts accounting for every displayed
// shell. Old or partial transcript tails cannot reclassify an unknown shell.
func (sc transcriptBackgroundScan) passiveShellCount(summary string, now time.Time) int {
	match := shellCountRe.FindStringSubmatch(summary)
	if match == nil || sc.TurnWorkflows > 0 || sc.TurnAgents > 0 {
		return 0
	}
	shells, _ := strconv.Atoi(match[1])
	monitors, _ := strconv.Atoi(match[2])
	watchers := 0
	for _, task := range sc.Pending {
		switch task.Kind {
		case tmux.BackgroundKindWatcher:
			if task.At.IsZero() || now.Sub(task.At) > backgroundTranscriptHold {
				return 0
			}
			watchers++
		case tmux.BackgroundKindMonitor:
		default:
			return 0
		}
	}
	if shells > 0 && shells == watchers {
		return shells + monitors
	}
	return 0
}

func (sc transcriptBackgroundScan) pendingNamed(kind, name string) bool {
	for _, t := range sc.Pending {
		if t.Kind == kind && t.Name == name {
			return true
		}
	}
	return false
}

// transcriptBackgroundScanFor returns the cached transcript scan for inst.
// ok=false when the tool has no readable local transcript.
func transcriptBackgroundScanFor(inst *Instance) (transcriptBackgroundScan, bool) {
	if inst == nil {
		return transcriptBackgroundScan{}, false
	}
	path := inst.backgroundTranscriptPath()
	if path == "" {
		return transcriptBackgroundScan{}, false
	}
	sc, err := turnFacts.Background(path)
	if err != nil {
		return transcriptBackgroundScan{}, false
	}
	return sc, true
}

// backgroundTranscriptRetry bounds how often an unresolvable transcript path
// is looked up again (the lookup can glob the projects directory).
const backgroundTranscriptRetry = 30 * time.Second

// backgroundTranscriptPath resolves the instance's Claude transcript, cached
// per Claude session id so the per-poll probe does not repeat the lookup.
func (i *Instance) backgroundTranscriptPath() string {
	i.mu.RLock()
	sid, cachedSID, cached, at := i.ClaudeSessionID, i.bgTranscriptSID, i.bgTranscriptPath, i.bgTranscriptAt
	i.mu.RUnlock()
	if sid == "" {
		return ""
	}
	if sid == cachedSID && (cached != "" || time.Since(at) < backgroundTranscriptRetry) {
		return cached
	}
	path := i.GetJSONLPath()
	i.mu.Lock()
	i.bgTranscriptSID, i.bgTranscriptPath, i.bgTranscriptAt = sid, path, time.Now()
	i.mu.Unlock()
	return path
}

// probeBackgroundWork merges the pane's in-flight item with the transcript
// and records the verdict on the instance. Called WITHOUT i.mu held.
func (i *Instance) probeBackgroundWork(pane tmux.BackgroundWork) tmux.BackgroundWork {
	sc, scOK := transcriptBackgroundScanFor(i)
	now := time.Now()
	i.mu.RLock()
	seen := i.bgWorkPaneSeenAt
	i.mu.RUnlock()
	work, paneShown := mergeBackgroundWork(pane, sc, scOK, seen, now)
	i.mu.Lock()
	if paneShown && work.Running() {
		i.bgWorkPaneSeenAt = now
	}
	i.bgWork = work
	i.mu.Unlock()
	return work
}

// BackgroundWork returns finite work holding a turn open, or passive watchers
// while the session is waiting/idle. Never captures the pane.
func (i *Instance) BackgroundWork() tmux.BackgroundWork {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if (i.bgWorkActive && i.Status == StatusRunning) || (i.bgWork.Watching() && (i.Status == StatusWaiting || i.Status == StatusIdle)) {
		return i.bgWork
	}
	return tmux.BackgroundWork{}
}

// BackgroundWorkJSON is BackgroundWork for JSON surfaces: nil (key omitted)
// when nothing is in flight, so existing consumers see byte-identical output.
func (i *Instance) BackgroundWorkJSON() *tmux.BackgroundWork {
	if work := i.BackgroundWork(); work.InFlight() {
		return &work
	}
	return nil
}

// backgroundWorkHoldsTurn reports whether a running session's last Stop hook
// handed off to background work rather than finishing: the instance's own
// verdict when this process probed it, else the transcript under the same
// bounded hold as the status merge (a TUI-owned row the daemon did not probe;
// a hook candidate is only fresh for hookFreshWindow, well inside the hold).
func backgroundWorkHoldsTurn(inst *Instance) bool {
	if inst == nil {
		return false
	}
	if backgroundWorkOutrankedBySubstate(inst.GetTmuxSession()) {
		return false
	}
	if inst.BackgroundWork().Running() {
		return true
	}
	sc, ok := transcriptBackgroundScanFor(inst)
	if !ok {
		return false
	}
	inst.mu.RLock()
	seen := inst.bgWorkPaneSeenAt
	inst.mu.RUnlock()
	work, _ := mergeBackgroundWork(tmux.BackgroundWork{}, sc, true, seen, time.Now())
	return work.Running()
}

// reconcileBackgroundSubstate names background work in the substate: a
// session running because of it reads background-work unless a newer frame
// shows something more specific (live foreground work, an error, a menu). A
// background-work frame beside a non-running status is a vetoed stale row
// and reads as the idle prompt it is.
func reconcileBackgroundSubstate(sub Substate, status Status, bgActive bool) Substate {
	if bgActive && status == StatusRunning {
		switch sub {
		case SubstateNone, SubstateIdleAtEmptyPrompt, SubstateBackgroundWork, SubstateWatching, SubstateHookLag:
			return SubstateBackgroundWork
		}
		return sub
	}
	if sub == SubstateBackgroundWork && status != StatusRunning {
		return SubstateIdleAtEmptyPrompt
	}
	return sub
}

// hookEventBlocksTurn reports whether a waiting hook event is a menu the
// operator must answer (a PermissionRequest, or a Notification the hook
// handler only writes for permission_prompt / elicitation_dialog) rather than
// a Stop. The daemon always emits such an event (issue #2473): the child is
// blocked on input and its parent must hear about it. The status merge holds
// it at waiting only while the frame shows the menu or for blockingHookGrace.
func hookEventBlocksTurn(event string) bool {
	switch strings.ToLower(strings.TrimSpace(event)) {
	case "permissionrequest", "notification":
		return true
	}
	return false
}

// blockingHookGrace is how long a blocking hook event (hookEventBlocksTurn)
// holds a session at waiting without looking at the pane. The synchronous
// PermissionRequest hook fires just before Claude draws the dialog, so the
// first frame after it can still show the bare prompt and the workflow row;
// past the grace the frame shows the menu (blocked, waiting) or it was
// dismissed (Esc fires no Stop), and the background-work probe decides.
const blockingHookGrace = 5 * time.Second

// blockingHookInGrace reports whether a blocking hook event is young enough
// to hold the session at waiting without probing the pane (issue #2473).
func blockingHookInGrace(event string, at, now time.Time) bool {
	return hookEventBlocksTurn(event) && now.Sub(at) < blockingHookGrace
}

// backgroundWorkOutrankedBySubstate reports whether the session's last pane
// frame shows something background work must not override: an open menu, an
// error banner or the model-unavailable no-op (tmux backgroundWorkOutranked),
// or a cached substate naming one. Never captures.
func backgroundWorkOutrankedBySubstate(ts *tmux.Session) bool {
	if ts == nil {
		return false
	}
	if ts.CachedBackgroundWorkBlocked() {
		return true
	}
	switch ts.CachedSubstate() {
	case tmux.SubstateInteractiveMenu, tmux.SubstateAuth401, tmux.SubstateModelUnavailable, tmux.SubstateUsageLimit:
		return true
	}
	return false
}
