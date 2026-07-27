package ui

import (
	"sort"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Adaptive refresh policy — the "generation skip" half of the refresh-tick v2
// architecture (docs/design/refresh-architecture-v2.md, issue #1753).
//
// The background status sweep used to call Instance.UpdateStatus() on EVERY
// live session every ~2s. That is O(live sessions) per tick, and for the most
// common steady state on a large deck — a Claude session sitting in hook
// "waiting" (done / needs-you) — UpdateStatus takes the hook fast path, which
// calls tmux.Session.BackgroundWorkPending() and therefore captures the pane
// (bgWorkCacheTTL = 3s). Only livePipeLRUCapacity (3) + attached sessions hold
// a control pipe, so for everyone else that capture is a `tmux capture-pane`
// SUBPROCESS, serialized by the tmux server. At ~60 quiescent sessions that is
// tens of subprocesses per sweep spent re-deriving a status that cannot have
// changed.
//
// The gate below skips that poll when there is cheap, positive evidence that
// nothing observable changed. The evidence is a per-session FINGERPRINT built
// entirely from caches the sweep already refreshed once (list-sessions +
// list-panes) plus in-memory watcher maps, so building it is a handful of map
// lookups and spawns nothing:
//
//	window_activity  — tmux bumps it on any pane output
//	pane title       — the OSC-driven spinner/done marker Claude sets
//	pane command      — foreground process
//	pane dead        — process exited under remain-on-exit
//	hook UpdatedAt   — Stop/Notification/PermissionRequest hook file mtime
//	SSE UpdatedAt    — OpenCode /event stream (issue #1614)
//
// Unchanged window_activity as proof of "no new pane output" is NOT a new
// assumption: tmux.Session.GetStatus already gates its CapturePane on exactly
// that comparison (needsBusyCheck), and Instance.UpdateStatus gates its idle
// tier on it too. This gate is strictly more conservative than those, because
// it also bounds how long it may hold: after maxSkips consecutive skips a
// session is polled no matter what, so any time-based transition inside
// UpdateStatus (acknowledged→idle, hook-freshness expiry, debounce
// confirmation) is delayed by at most maxSkips sweeps rather than suppressed.
//
// VISIBLE rows (issue #1753, group-expand diagnostic): with a large group
// expanded, visible-row count approaches fleet size, so a policy that only
// skips off-screen rows degenerates to refreshing everything. Visible rows
// therefore get the same treatment, tuned for what the user can see:
//
//   - a visible row whose fingerprint is provably unchanged is HELD (costs
//     zero) under the same maxSkips ceiling — holdVisible;
//   - visible rows that DO need a poll are budgeted: at most
//     visiblePollBudgetPerSweep of them per sweep, round-robin via a
//     starvation counter so every due row gets its turn — admitVisiblePolls;
//   - the cursor row is exempt from the budget (it drives the preview pane
//     and is what the user is reading);
//   - the gate still fails OPEN (polls everything, no budget) when no fresh
//     viewport snapshot exists, and the kill switch disables budget and hold
//     alike.
//
// Group expand itself never polls: newly visible rows render from the render
// snapshot instantly and fill in through this budget on subsequent sweeps.

const (
	// defaultAdaptiveRefreshMaxSkips is how many consecutive background sweeps
	// a quiescent off-screen session may be skipped before it is polled
	// regardless of its fingerprint. 2 means "poll at least every 3rd sweep"
	// (~6s at baseStatusInterval), which bounds the worst-case staleness of a
	// time-based transition on an off-screen row. The config knob that
	// overrides it is resolved (defaulted, kill-switched, and clamped) by
	// session.UISettings.GetAdaptiveRefreshMaxSkips.
	defaultAdaptiveRefreshMaxSkips = session.DefaultAdaptiveRefreshMaxSkips

	// visibleSessionsMaxAge is how long a published viewport snapshot is
	// trusted. The TUI republishes on every tick (baseStatusInterval), so a
	// snapshot older than this means the UI event loop is wedged or suspended
	// (tea.Exec) — the gate then fails open and polls everything, exactly as
	// it did before this policy existed.
	visibleSessionsMaxAge = 10 * time.Second

	// visiblePollBudgetPerSweep is the per-sweep cap on how many VISIBLE rows
	// may be polled (issue #1753 group-expand case). Matches the sweep's
	// worker-pool width (g.SetLimit(10) in backgroundStatusUpdate) so one
	// sweep's visible polls can never exceed one pool of tmux work; due rows
	// beyond the budget are deferred with a starvation counter and admitted
	// first on later sweeps, so every due visible row is polled within
	// ceil(due/budget) sweeps. The cursor row bypasses the budget entirely.
	visiblePollBudgetPerSweep = 10
)

// sessionFingerprint is the cheap, cache-only evidence of a session's observable
// state. Comparable by ==; ok is false when the caches could not supply an
// answer (stale bulk cache, no tmux session yet), which always forces a poll.
type sessionFingerprint struct {
	activity int64  // tmux window_activity (seconds; 0 = cache miss)
	title    string // tmux pane title
	command  string // tmux pane_current_command
	dead     bool   // tmux pane_dead
	hookAt   int64  // hook status UpdatedAt (UnixNano; 0 = none)
	sseAt    int64  // OpenCode SSE UpdatedAt (UnixNano; 0 = none)
	ok       bool
}

// unchangedFrom reports whether both fingerprints are usable and identical.
func (f sessionFingerprint) unchangedFrom(prev sessionFingerprint) bool {
	return f.ok && prev.ok && f == prev
}

// statusSettledForSkip reports whether a status is one the gate is allowed to
// hold. Only the three steady states qualify. error/stopped carry their own
// recheck timers inside UpdateStatus and must be free to recover promptly;
// starting must converge fast; anything unrecognised is treated as unsettled.
func statusSettledForSkip(s session.Status) bool {
	switch s {
	case session.StatusRunning, session.StatusWaiting, session.StatusIdle:
		return true
	default:
		return false
	}
}

// skipInput is the complete input to the skip decision, kept as a plain struct
// so the policy is unit-testable without tmux, a HOME, or a live Home.
type skipInput struct {
	visible  bool
	status   session.Status
	fp       sessionFingerprint
	prev     sessionFingerprint
	hasPrev  bool
	skips    int
	maxSkips int
}

// shouldSkipStatusPoll decides whether the background sweep may skip
// UpdateStatus for one session this sweep. The returned reason is for debug
// logging and tests; it is never shown to the user.
//
// Every condition is a veto: the gate skips only when all of them agree.
func shouldSkipStatusPoll(in skipInput) (bool, string) {
	switch {
	case in.maxSkips <= 0:
		return false, "policy-disabled"
	case in.visible:
		return false, "visible"
	case !statusSettledForSkip(in.status):
		return false, "status-unsettled"
	case !in.hasPrev:
		return false, "no-baseline"
	case !in.fp.unchangedFrom(in.prev):
		return false, "fingerprint-changed"
	case in.skips >= in.maxSkips:
		return false, "staleness-ceiling"
	default:
		return true, "quiescent"
	}
}

// refreshLedger remembers, per session, the fingerprint of the last sweep that
// actually polled it and how many sweeps have been skipped since. Guarded by a
// mutex: the sweep runs on the status worker goroutine while forget() can be
// called from the Bubble Tea goroutine on attach-return.
//
// All methods are nil-receiver safe so an alternate/test Home constructor that
// never builds a ledger still behaves exactly like the pre-policy code (poll
// everything).
type refreshLedger struct {
	mu      sync.Mutex
	entries map[string]refreshLedgerEntry
}

type refreshLedgerEntry struct {
	fp    sessionFingerprint
	skips int
	// deferrals counts consecutive sweeps a DUE visible row was pushed past
	// by the visible-poll budget. Admission orders by it (most-starved first)
	// so the budget cycles round-robin instead of starving a stable suffix.
	// Zeroed on every actual poll.
	deferrals int
}

func newRefreshLedger() *refreshLedger {
	return &refreshLedger{entries: make(map[string]refreshLedgerEntry)}
}

// decide consults the policy for one session and records the outcome in one
// locked step. On a poll the fingerprint becomes the new baseline and the skip
// counter resets; on a skip the baseline is kept (it is equal by construction)
// and the counter advances toward the ceiling.
func (l *refreshLedger) decide(id string, fp sessionFingerprint, status session.Status, visible bool, maxSkips int) (bool, string) {
	if l == nil || id == "" {
		return false, "policy-disabled"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]refreshLedgerEntry)
	}
	prev, hasPrev := l.entries[id]
	skip, reason := shouldSkipStatusPoll(skipInput{
		visible:  visible,
		status:   status,
		fp:       fp,
		prev:     prev.fp,
		hasPrev:  hasPrev,
		skips:    prev.skips,
		maxSkips: maxSkips,
	})
	if skip {
		l.entries[id] = refreshLedgerEntry{fp: prev.fp, skips: prev.skips + 1}
	} else {
		l.entries[id] = refreshLedgerEntry{fp: fp, skips: 0}
	}
	return skip, reason
}

// holdVisible is the sweep-side gate for a VISIBLE row: it skips (and counts
// the skip toward the ceiling) only when the row's status is settled, the
// fingerprint is usable and identical to the last polled baseline, and fewer
// than maxSkips consecutive skips have happened — the same vetoes as the
// off-screen gate minus visibility itself. On false it mutates NOTHING: the
// row becomes a budget candidate and the ledger is updated by admitPoll or
// deferPoll, whichever the admission pass picks.
func (l *refreshLedger) holdVisible(id string, fp sessionFingerprint, status session.Status, maxSkips int) bool {
	if l == nil || id == "" || maxSkips <= 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	prev, hasPrev := l.entries[id]
	if !hasPrev || !statusSettledForSkip(status) || !fp.unchangedFrom(prev.fp) || prev.skips >= maxSkips {
		return false
	}
	l.entries[id] = refreshLedgerEntry{fp: prev.fp, skips: prev.skips + 1, deferrals: prev.deferrals}
	return true
}

// heldSteady is the READ-ONLY variant used by the incremental visible-first
// path (processStatusUpdate): true when the row is settled and its fingerprint
// matches the last polled baseline. It deliberately has no ceiling and touches
// no counters — the background sweep owns freshness (it polls every row at
// least every maxSkips+1 sweeps), so the incremental path may hold a steady
// row indefinitely without any transition being suppressed.
func (l *refreshLedger) heldSteady(id string, fp sessionFingerprint, status session.Status) bool {
	if l == nil || id == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	prev, hasPrev := l.entries[id]
	return hasPrev && statusSettledForSkip(status) && fp.unchangedFrom(prev.fp)
}

// admitPoll records that a budget-admitted visible row is being polled this
// sweep: the fingerprint becomes the new baseline and both counters reset,
// exactly like decide()'s poll branch.
func (l *refreshLedger) admitPoll(id string, fp sessionFingerprint) {
	if l == nil || id == "" {
		return
	}
	l.mu.Lock()
	l.entries[id] = refreshLedgerEntry{fp: fp}
	l.mu.Unlock()
}

// deferPoll records that a due visible row was pushed past this sweep by the
// budget. Only the starvation counter moves — baseline and skip count stay, so
// the row remains due and rises in admission priority next sweep.
func (l *refreshLedger) deferPoll(id string) {
	if l == nil || id == "" {
		return
	}
	l.mu.Lock()
	prev := l.entries[id]
	prev.deferrals++
	l.entries[id] = prev
	l.mu.Unlock()
}

// deferralCount reports how many consecutive sweeps a row has been deferred.
func (l *refreshLedger) deferralCount(id string) int {
	if l == nil || id == "" {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.entries[id].deferrals
}

// forget drops one session's baseline so the next sweep polls it. Called after
// any path that refreshes a session out of band (attach-return, forced check),
// so the gate can never hold a session whose state the TUI just invalidated.
func (l *refreshLedger) forget(id string) {
	if l == nil || id == "" {
		return
	}
	l.mu.Lock()
	delete(l.entries, id)
	l.mu.Unlock()
}

// prune drops baselines for sessions that no longer exist, so the ledger cannot
// grow across a long-lived TUI session.
func (l *refreshLedger) prune(live map[string]struct{}) {
	if l == nil {
		return
	}
	l.mu.Lock()
	for id := range l.entries {
		if _, ok := live[id]; !ok {
			delete(l.entries, id)
		}
	}
	l.mu.Unlock()
}

// visiblePollCandidate is a visible row the gate could not hold this sweep —
// it needs a poll and competes for the visible-poll budget.
type visiblePollCandidate struct {
	inst      *session.Instance
	fp        sessionFingerprint
	deferrals int
}

// admitVisiblePolls splits the due visible rows into the prefix polled this
// sweep and the deferred rest. Ordering: the cursor row first (it drives the
// preview pane, so it is effectively budget-exempt — it always lands in the
// admitted prefix), then most-starved first so deferred rows rise until
// admitted and the budget cycles round-robin through every due row.
func admitVisiblePolls(due []visiblePollCandidate, cursorID string, budget int) (admitted, deferred []visiblePollCandidate) {
	sort.SliceStable(due, func(i, j int) bool {
		iCursor := cursorID != "" && due[i].inst != nil && due[i].inst.ID == cursorID
		jCursor := cursorID != "" && due[j].inst != nil && due[j].inst.ID == cursorID
		if iCursor != jCursor {
			return iCursor
		}
		return due[i].deferrals > due[j].deferrals
	})
	if budget < 1 {
		budget = 1
	}
	if len(due) <= budget {
		return due, nil
	}
	return due[:budget], due[budget:]
}

// fingerprintSession builds a session's fingerprint from caches the sweep has
// already refreshed. It spawns no subprocess: GetCachedWindowActivity and
// GetCachedPaneInfo are map reads over the bulk list-sessions / list-panes
// snapshots, and the watcher lookups are in-memory maps.
//
// ok is false — forcing a poll — whenever the evidence is incomplete: no tmux
// session, a stale pane cache, or a missing window_activity sample.
func (h *Home) fingerprintSession(inst *session.Instance) sessionFingerprint {
	if inst == nil {
		return sessionFingerprint{}
	}
	ts := inst.GetTmuxSession()
	if ts == nil {
		return sessionFingerprint{}
	}
	activity := ts.GetCachedWindowActivity()
	if activity == 0 {
		return sessionFingerprint{}
	}
	paneInfo, paneOK := tmux.GetCachedPaneInfo(ts.Name)
	if !paneOK {
		return sessionFingerprint{}
	}
	fp := sessionFingerprint{
		activity: activity,
		title:    paneInfo.Title,
		command:  paneInfo.CurrentCommand,
		dead:     paneInfo.Dead,
		ok:       true,
	}
	if h.hookWatcher != nil {
		if hs := h.hookWatcher.GetHookStatus(inst.ID); hs != nil {
			fp.hookAt = hs.UpdatedAt.UnixNano()
		}
	}
	if h.sseWatcher != nil {
		if ss := h.sseWatcher.GetStatus(inst.ID); ss != nil {
			fp.sseAt = ss.UpdatedAt.UnixNano()
		}
	}
	return fp
}

// visibleSessionSnapshot is the set of session IDs currently on screen, plus
// when it was taken so a stale snapshot can fail open.
type visibleSessionSnapshot struct {
	ids map[string]struct{}
	// cursorID is the session under the cursor (empty when the cursor sits on
	// a group header or nothing). The sweep's visible-poll budget never defers
	// it: it is the row the preview pane shows.
	cursorID string
	at       time.Time
}

// visibleRowBudget is how many list rows the viewport can hold. Shared by the
// viewport publisher and the round-robin status request so the two cannot drift.
func (h *Home) visibleRowBudget() int {
	budget := h.height - 8
	if budget < 5 {
		budget = 5
	}
	return budget
}

// publishVisibleSessions snapshots which session rows are on screen for the
// background sweep. O(visible rows) — it walks only the viewport slice, never
// the whole deck — and is called from the TUI tick, which already owns
// flatItems/cursor/viewOffset on the Bubble Tea goroutine.
//
// The selected row is always included even when the viewport slice math would
// miss it (cursor on a group header keeps its child out; a selected row is what
// the preview pane shows), and so is a session we just returned from attaching.
func (h *Home) publishVisibleSessions(extra ...string) {
	ids := make(map[string]struct{}, h.visibleRowBudget()+len(extra)+1)
	add := func(item session.Item) {
		switch item.Type {
		case session.ItemTypeSession:
			if item.Session != nil {
				ids[item.Session.ID] = struct{}{}
			}
		case session.ItemTypeWindow:
			if item.WindowSessionID != "" {
				ids[item.WindowSessionID] = struct{}{}
			}
		}
	}
	end := h.viewOffset + h.visibleRowBudget()
	if end > len(h.flatItems) {
		end = len(h.flatItems)
	}
	for i := h.viewOffset; i < end; i++ {
		if i < 0 {
			continue
		}
		add(h.flatItems[i])
	}
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		add(h.flatItems[h.cursor])
	}
	for _, id := range extra {
		if id != "" {
			ids[id] = struct{}{}
		}
	}
	snap := visibleSessionSnapshot{ids: ids, at: time.Now()}
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		switch item := h.flatItems[h.cursor]; item.Type {
		case session.ItemTypeSession:
			if item.Session != nil {
				snap.cursorID = item.Session.ID
			}
		case session.ItemTypeWindow:
			snap.cursorID = item.WindowSessionID
		}
	}
	h.visibleSessions.Store(snap)
}

// visibleSessionsForSweep returns the published viewport snapshot and whether
// it is fresh enough to trust. ok=false means the gate must fail open: with no
// recent viewport snapshot (TUI not started yet, event loop suspended by
// tea.Exec, or wedged) every session is treated as visible and polled with no
// budget, which is the exact pre-policy behaviour.
func (h *Home) visibleSessionsForSweep() (visibleSessionSnapshot, bool) {
	raw := h.visibleSessions.Load()
	if raw == nil {
		return visibleSessionSnapshot{}, false
	}
	snap, typed := raw.(visibleSessionSnapshot)
	if !typed || snap.ids == nil {
		return visibleSessionSnapshot{}, false
	}
	if time.Since(snap.at) > visibleSessionsMaxAge {
		return visibleSessionSnapshot{}, false
	}
	return snap, true
}
