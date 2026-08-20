package agents

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SanitizeForDisplay strips control characters from a string before it reaches
// a terminal.
//
// Definition content and remote rows are DATA, not markup. Without this, an
// adopted file or a compromised remote could embed escape sequences in a name
// or a summary and repaint the fleet view — including forging a "link ok"
// line. Tabs become spaces; everything else below 0x20, plus DEL and the C1
// range, is dropped.
func SanitizeForDisplay(value string) string {
	var sb strings.Builder
	sb.Grow(len(value))
	for _, r := range value {
		switch {
		case r == '\t':
			sb.WriteRune(' ')
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			continue
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// LinkState describes how much we know about a machine's data.
type LinkState string

const (
	// LinkLocal is this machine: the data was read directly.
	LinkLocal LinkState = "local"
	// LinkOK means the remote answered and its rows are current.
	LinkOK LinkState = "ok"
	// LinkUnconfirmed means the remote did not answer. Its rows, if any, are
	// the last thing we saw and must be labelled as such — never rendered as
	// if they were current, and never silently dropped, which would make the
	// fleet look smaller than it is.
	LinkUnconfirmed LinkState = "unconfirmed"
	// LinkNotContacted means this machine was never asked. It appears only
	// because a local definition places a post there. Calling that "link ok"
	// claims a round trip that never happened — the same dishonesty as
	// LinkUnconfirmed pointing the other way, and the one a reader acts on.
	LinkNotContacted LinkState = "not contacted"
)

// RunState is what an agent's runtime is doing now.
type RunState string

const (
	RunWorking  RunState = "working"
	RunIdle     RunState = "idle"
	RunNeedsYou RunState = "needs you"
	// RunError is a session that failed, as distinct from one that is
	// waiting for a human. Both need attention; only one of them is waiting
	// on you, and saying the wrong one sends the reader looking for a prompt
	// that does not exist.
	RunError     RunState = "error"
	RunStopped   RunState = "stopped"
	RunNoRuntime RunState = "no runtime"
	RunUnknown   RunState = "unknown"
)

// LedgerEntry is one line of what an agent actually did.
type LedgerEntry struct {
	At      time.Time `json:"at"`
	Summary string    `json:"summary"`
	Status  string    `json:"status,omitempty"`
}

// TriggerRow is a declared trigger rendered for display.
type TriggerRow struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Schedule string `json:"schedule,omitempty"`
	Enabled  bool   `json:"enabled"`
	// External is true when a plist, timer or manager still owns the firing.
	// Every phase-1 trigger is external.
	External       bool   `json:"external"`
	ExternalSource string `json:"external_source,omitempty"`
	// NextDue is computed from the DECLARED schedule. It is what the source
	// automation should do next, not something agent-deck will do.
	NextDue *time.Time `json:"next_due,omitempty"`
	// NextDueText is the short rendering for a list row.
	NextDueText string `json:"next_due_text"`
	// Note carries the honest reason when NextDue could not be computed.
	Note string `json:"note,omitempty"`
}

// AgentRow is one agent in the fleet view.
type AgentRow struct {
	Name        string         `json:"name"`
	PostID      string         `json:"post_id"`
	Role        string         `json:"role"`
	RoleVersion string         `json:"role_version,omitempty"`
	Class       Classification `json:"class"`
	Machine     string         `json:"machine"`
	Harness     string         `json:"harness,omitempty"`
	Account     string         `json:"account,omitempty"`
	SessionID   string         `json:"session_id,omitempty"`
	State       RunState       `json:"state"`
	LastDid     string         `json:"last_did,omitempty"`
	LastDidAt   *time.Time     `json:"last_did_at,omitempty"`
	Triggers    []TriggerRow   `json:"triggers,omitempty"`
	Connectors  []Health       `json:"connectors,omitempty"`
	Recent      []LedgerEntry  `json:"recent,omitempty"`
	Unresolved  []string       `json:"unresolved,omitempty"`
	// Attention is non-empty when this row should be loud. It carries the
	// reason and, where known, since when.
	Attention string `json:"attention,omitempty"`
	// LinkState is inherited from the machine the row came from, so a stale
	// remote row can never be mistaken for a live local one.
	LinkState LinkState `json:"link_state"`
	// LoadError marks a definition directory that could not be read.
	LoadError string `json:"load_error,omitempty"`
	// Violations are phase-1 invariants this record breaks. The registry is a
	// directory a human, a later phase, or a synced dotfile can write into,
	// so what adoption emits is not the same as what the renderer is handed.
	// A row that claims to be armed says so instead of being painted under a
	// blanket "this is disabled" footer.
	Violations []string `json:"violations,omitempty"`
	// ReportsTo is the manager post or human principal.
	ReportsTo string `json:"reports_to,omitempty"`
	// ReportsToIssue is set when this post's escalation chain is broken —
	// a cycle, an unknown parent, or a chain that never reaches a human.
	ReportsToIssue string `json:"reports_to_issue,omitempty"`
}

// NextDue returns the soonest declared next-due across this row's triggers.
func (r AgentRow) NextDue() *TriggerRow {
	var best *TriggerRow
	for i := range r.Triggers {
		t := r.Triggers[i]
		if t.NextDue == nil {
			continue
		}
		if best == nil || t.NextDue.Before(*best.NextDue) {
			best = &r.Triggers[i]
		}
	}
	if best == nil && len(r.Triggers) > 0 {
		return &r.Triggers[0]
	}
	return best
}

// Machine groups agents by where they run.
type Machine struct {
	Name       string     `json:"name"`
	Link       LinkState  `json:"link"`
	LinkDetail string     `json:"link_detail,omitempty"`
	Agents     []AgentRow `json:"agents"`
}

// View is the whole grouped-by-machine fleet view.
type View struct {
	Machines []Machine `json:"machines"`
	// Counts are for the header line.
	TotalAgents   int `json:"total_agents"`
	NeedAttention int `json:"need_attention"`
	// Notices carry view-level honesty, e.g. a registry that could not be
	// read at all.
	Notices []string `json:"notices,omitempty"`
}

// SessionState maps an agent-deck session status onto a run state.
type SessionState struct {
	Status  string
	Present bool
}

// BuildOptions carries everything BuildView needs. The caller supplies live
// data; this package does no I/O beyond the local health checks it is asked
// for, which keeps the view deterministic under test.
type BuildOptions struct {
	Definitions []*Definition
	// SessionStates maps a session id to its current status.
	SessionStates map[string]SessionState
	// Ledger returns the recent work for a session, newest first.
	Ledger func(sessionID string) []LedgerEntry
	// LocalMachine labels rows with no explicit machine.
	LocalMachine string
	// Remotes are machines whose rows the caller fetched (or failed to).
	Remotes []RemoteMachineData
	// RecentLimit bounds ledger entries per row.
	RecentLimit int
	StaleAfter  time.Duration
	Now         time.Time
	// SkipHealth disables filesystem health probing (used by tests and by
	// callers that only need the row skeleton).
	SkipHealth bool
}

// RemoteMachineData is a remote machine's contribution to the view.
type RemoteMachineData struct {
	Name string
	Link LinkState
	// Detail explains the link state, e.g. "drained 2m ago" or the error.
	Detail string
	Agents []AgentRow
}

// BuildView assembles the fleet view from definitions and live state.
func BuildView(opts BuildOptions) View {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	recentLimit := opts.RecentLimit
	if recentLimit <= 0 {
		recentLimit = 3
	}
	localMachine := opts.LocalMachine
	if strings.TrimSpace(localMachine) == "" {
		localMachine = "local"
	}

	view := View{}
	byMachine := map[string][]AgentRow{}

	for _, def := range opts.Definitions {
		row := buildRow(def, opts, now, localMachine, recentLimit)
		machine := row.Machine
		byMachine[machine] = append(byMachine[machine], row)
	}

	machineNames := make([]string, 0, len(byMachine))
	for name := range byMachine {
		machineNames = append(machineNames, name)
	}
	sort.Strings(machineNames)

	// The local machine leads; remotes follow in name order.
	sort.SliceStable(machineNames, func(i, j int) bool {
		if machineNames[i] == localMachine {
			return true
		}
		if machineNames[j] == localMachine {
			return false
		}
		return machineNames[i] < machineNames[j]
	})

	for _, name := range machineNames {
		rows := byMachine[name]
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		// Only the local machine was actually read here; remotes that really
		// answered are appended separately below with LinkOK.
		link := LinkLocal
		if name != localMachine {
			link = LinkNotContacted
		}
		view.Machines = append(view.Machines, Machine{Name: name, Link: link, Agents: rows})
	}

	for _, remote := range opts.Remotes {
		rows := append([]AgentRow(nil), remote.Agents...)
		for i := range rows {
			rows[i].LinkState = remote.Link
			if remote.Link == LinkUnconfirmed && rows[i].Attention == "" {
				rows[i].Attention = "link unconfirmed; this row may be out of date"
			}
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		view.Machines = append(view.Machines, Machine{
			Name: remote.Name, Link: remote.Link, LinkDetail: remote.Detail, Agents: rows,
		})
	}

	// The escalation graph can only be checked across the whole set, so it
	// runs here rather than per row. Without this call the validator existed
	// but nothing ever ran it, and a post reporting to a nonexistent manager
	// rendered with no complaint anywhere.
	applyReportsToFindings(&view, opts.Definitions)

	for _, machine := range view.Machines {
		for _, row := range machine.Agents {
			view.TotalAgents++
			if row.Attention != "" {
				view.NeedAttention++
			}
		}
	}
	return view
}

// applyReportsToFindings walks the reports_to graph and attaches each finding
// to the row it concerns.
func applyReportsToFindings(view *View, defs []*Definition) {
	posts := make([]*Post, 0, len(defs))
	byName := map[string]bool{}
	for _, def := range defs {
		if def != nil && def.Post != nil {
			posts = append(posts, def.Post)
			byName[def.Post.Metadata.Name] = true
		}
	}
	if len(posts) == 0 {
		return
	}

	issues := map[string]string{}
	for _, finding := range ValidateReportsTo(posts) {
		// Fields are of the form "post.<name>.spec.placement.reportsTo".
		field := strings.TrimPrefix(finding.Field, "post.")
		name := strings.TrimSuffix(field, ".spec.placement.reportsTo")
		if name == "" || !byName[name] {
			view.Notices = append(view.Notices, finding.Message)
			continue
		}
		if _, seen := issues[name]; !seen {
			issues[name] = finding.Message
		}
	}
	if len(issues) == 0 {
		return
	}
	for mi := range view.Machines {
		for ri := range view.Machines[mi].Agents {
			row := &view.Machines[mi].Agents[ri]
			issue, ok := issues[row.Name]
			if !ok {
				continue
			}
			row.ReportsToIssue = issue
			// A broken escalation chain is the one error that makes
			// escalation silently unreachable, so it belongs in the row's
			// attention — and therefore in the "N need attention" count —
			// rather than only in --json.
			if row.Attention == "" {
				row.Attention = issue
			} else {
				row.Attention += "; " + issue
			}
		}
	}
}

func buildRow(def *Definition, opts BuildOptions, now time.Time, localMachine string, recentLimit int) AgentRow {
	row := AgentRow{
		Name:      def.Name,
		Machine:   localMachine,
		LinkState: LinkLocal,
		State:     RunUnknown,
	}

	if def.LoadError != "" {
		row.LoadError = def.LoadError
		row.Class = ClassDebris
		row.Role = RoleUnresolved
		row.State = RunUnknown
		row.Attention = "definition could not be read: " + def.LoadError
		return row
	}

	post := def.Post
	row.PostID = post.Metadata.PostID
	row.Role = post.Spec.Role.Name
	row.RoleVersion = post.Spec.Role.Version
	row.Class = post.Spec.Classification
	row.Harness = post.Spec.Runtime.Harness
	row.Account = post.Spec.Runtime.Account
	row.SessionID = post.Spec.Runtime.AdoptedSessionID
	row.Unresolved = post.Spec.Unresolved
	row.ReportsTo = post.Spec.Placement.ReportsTo
	if post.Spec.Placement.Machine != "" {
		row.Machine = post.Spec.Placement.Machine
	}
	if def.Role != nil && def.Role.Metadata.Version != "" {
		row.RoleVersion = def.Role.Metadata.Version
	}

	// Live state comes from the existing session status, which stays
	// authoritative. A post with no runtime says so rather than guessing.
	// The session index is LOCAL. A post placed on another machine cannot be
	// resolved against it — its session may well be running there — so its
	// state is unknown rather than a confident "no runtime".
	switch {
	case row.Machine != localMachine:
		row.State = RunUnknown
	case row.SessionID == "":
		row.State = RunNoRuntime
	default:
		state, ok := opts.SessionStates[row.SessionID]
		if !ok || !state.Present {
			row.State = RunNoRuntime
		} else {
			row.State = mapSessionStatus(state.Status)
		}
	}

	if opts.Ledger != nil && row.SessionID != "" {
		entries := opts.Ledger(row.SessionID)
		if len(entries) > recentLimit {
			entries = entries[:recentLimit]
		}
		row.Recent = entries
		if len(entries) > 0 {
			row.LastDid = entries[0].Summary
			at := entries[0].At
			row.LastDidAt = &at
		}
	}

	for _, t := range post.Spec.Triggers {
		row.Triggers = append(row.Triggers, buildTriggerRow(t, now))
	}

	if !opts.SkipHealth {
		for _, connector := range post.Spec.Connectors {
			health := CheckHealth(connector.Name, connector.Kind, connector.EvidencePath, opts.StaleAfter, now)
			row.Connectors = append(row.Connectors, health)
		}
	}

	// Phase-1 invariants are checked on RENDER, not just on emit.
	for _, finding := range ValidatePost(post) {
		if finding.Severity == SeverityError {
			row.Violations = append(row.Violations, finding.Field+": "+finding.Message)
		}
	}

	row.Attention = attentionFor(row)
	return row
}

// Armed reports whether any trigger on this row claims agent-deck owns its
// firing. Nothing in phase 1 should ever be armed; a row that is says so.
func (r AgentRow) Armed() bool {
	for _, t := range r.Triggers {
		if t.Enabled && !t.External {
			return true
		}
	}
	return false
}

// attentionFor decides whether a row should be loud, and why. Only proven
// problems qualify: an unknown connector is not an alarm, it is an unknown.
func attentionFor(row AgentRow) string {
	// A broken invariant and a dead connector are separate problems. Reporting
	// only the first found would hide a real outage behind a schema complaint,
	// so both are named.
	var reasons []string
	if len(row.Violations) > 0 {
		reasons = append(reasons, "definition breaks a phase-1 invariant: "+row.Violations[0])
	}
	for _, c := range row.Connectors {
		switch c.State {
		case HealthDown, HealthStale:
			reasons = append(reasons, fmt.Sprintf("connector %s: %s", c.Name, c.Detail))
		}
	}
	if len(reasons) > 0 {
		return strings.Join(reasons, "; ")
	}
	switch row.State {
	case RunNeedsYou:
		return "session is waiting on you"
	case RunError:
		return "session is in error"
	}
	return ""
}

func buildTriggerRow(t Trigger, now time.Time) TriggerRow {
	row := TriggerRow{
		Name:           t.Name,
		Kind:           t.Type,
		Schedule:       t.Schedule,
		Enabled:        t.Enabled,
		External:       t.External,
		ExternalSource: t.ExternalSource,
	}

	switch {
	case t.Type == TriggerCron && t.Schedule != "":
		loc := time.Local
		if t.Timezone != "" {
			if parsed, err := time.LoadLocation(t.Timezone); err == nil {
				loc = parsed
			} else {
				row.Note = "unknown timezone " + t.Timezone + "; shown in local time"
			}
		}
		schedule, err := ParseCron(t.Schedule, loc)
		if err != nil {
			row.NextDueText = t.Schedule
			row.Note = "schedule not understood: " + err.Error()
			break
		}
		next, ok := schedule.Next(now)
		if !ok {
			row.NextDueText = DescribeCron(t.Schedule)
			row.Note = "this schedule does not recur within four years"
			break
		}
		row.NextDue = &next
		row.NextDueText = DescribeCron(t.Schedule)
	case t.IntervalSeconds > 0:
		// An interval trigger fired by a launcher has no last-fire we can
		// see, so the cadence is all we can honestly show.
		row.NextDueText = DescribeInterval(t.IntervalSeconds)
		row.Note = "cadence only; the launcher owns the phase of this cycle"
	case t.Type == TriggerMailDoorbell:
		row.NextDueText = "on mail"
	case t.Type == TriggerFileWatch:
		row.NextDueText = "on file"
	case t.Type == TriggerWebhook:
		row.NextDueText = "on request"
	case t.Type == TriggerSessionTransition:
		row.NextDueText = "on session event"
	case t.Schedule != "":
		// An opaque trigger that still carries the source's own spelling of
		// its schedule shows that, rather than "unknown". The terms are
		// knowable even when the next wall-clock time is not.
		row.NextDueText = t.Schedule
		row.Note = "this schedule has no exact cron equivalent, so no next-due time is computed"
	default:
		row.NextDueText = "unknown"
		row.Note = "adoption could see that this fires but not on what terms"
	}

	return row
}

func mapSessionStatus(status string) RunState {
	// These are agent-deck's own session.Status values. A status this
	// function does not recognize becomes RunUnknown rather than being
	// bucketed into a plausible neighbour.
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "starting":
		return RunWorking
	case "idle", "queued":
		return RunIdle
	case "waiting":
		return RunNeedsYou
	case "error":
		return RunError
	case "stopped":
		return RunStopped
	default:
		return RunUnknown
	}
}

// FormatLastDid renders the "last: ... 4m ago" cell.
func FormatLastDid(row AgentRow, now time.Time) string {
	if row.LastDid == "" {
		return ""
	}
	if row.LastDidAt == nil {
		return row.LastDid
	}
	return fmt.Sprintf("%s %s ago", row.LastDid, RoundDuration(now.Sub(*row.LastDidAt)))
}

// FormatNextDue renders the "next: cron 15m" cell.
func FormatNextDue(row AgentRow) string {
	next := row.NextDue()
	if next == nil {
		return ""
	}
	return next.NextDueText
}
