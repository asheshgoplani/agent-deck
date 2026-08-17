package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/selfheal"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestBuildSelfHealCandidate_FoldsSignals(t *testing.T) {
	inst := &Instance{
		ID:        "s1-1780000000",
		Title:     "exec-fix",
		GroupPath: "agent-deck",
		Account:   "personal",
		Status:    StatusError,
	}
	// hook freshness is compared against the wall clock (time.Since), so use a
	// fresh "now" here, not a fixed-epoch timestamp.
	hs := &HookStatus{Status: "running", UpdatedAt: time.Now()}

	c := buildSelfHealCandidate(inst, "error", hs, time.Unix(1779999000, 0), true)
	if c.SessionID != "s1-1780000000" || c.Title != "exec-fix" || c.Group != "agent-deck" {
		t.Fatalf("identity not folded: %+v", c)
	}
	if !c.HookRunningFresh {
		t.Error("fresh hook-running must set HookRunningFresh (mid-turn signal)")
	}
	if !c.OptedOut {
		t.Error("optedOut must be carried into the candidate")
	}
	if c.LastSentAt.IsZero() {
		t.Error("LastSentAt must be carried")
	}
}

func TestBuildSelfHealCandidate_StaleHookNotFresh(t *testing.T) {
	inst := &Instance{ID: "s1", Title: "t", Status: StatusError}
	stale := time.Now().Add(-10 * time.Minute)
	c := buildSelfHealCandidate(inst, "error", &HookStatus{Status: "running", UpdatedAt: stale}, time.Time{}, false)
	if c.HookRunningFresh {
		t.Error("a stale hook-running (past freshness window) must NOT count as mid-turn")
	}
}

func TestBuildSelfHealCandidate_StoppedDetected(t *testing.T) {
	inst := &Instance{ID: "s1", Title: "t", Status: StatusStopped}
	c := buildSelfHealCandidate(inst, "stopped", nil, time.Time{}, false)
	if !c.Stopped {
		t.Error("stopped status must set Stopped (highest-precedence disqualifier)")
	}
}

// A live TUI makes the transition daemon consume coarse status from SQLite.
// The freshly loaded daemon-side Instance therefore has no process-local
// substate cache, so Codex capacity detection must fall back to the live pane.
func TestBuildSelfHealCandidate_CodexCapacityWithoutCachedSubstate(t *testing.T) {
	inst := NewInstanceWithTool("selfheal-codex-capacity", t.TempDir(), "codex")
	inst.tmuxSession.SetCustomPatterns("codex", nil, nil, nil)
	if err := inst.tmuxSession.Start("printf '\\n⚠ Selected model is at capacity. Please try a different model.\\n'; sleep 3600"); err != nil {
		t.Fatalf("start capacity pane: %v", err)
	}
	t.Cleanup(func() { _ = inst.tmuxSession.Kill() })

	if got := inst.CachedSubstate(); got != SubstateNone {
		t.Fatalf("precondition: daemon-side cache must start empty, got %q", got)
	}
	c := buildSelfHealCandidate(inst, "idle", nil, time.Time{}, false)
	if c.Substate != SubstateModelUnavailable {
		t.Fatalf("candidate substate = %q, want %q", c.Substate, SubstateModelUnavailable)
	}
	if c.ComposerDraft == nil {
		t.Fatal("capacity resume candidate must carry the deferred composer guard")
	}
}

func TestCapsFromSettings_DefaultsAndOverrides(t *testing.T) {
	def := capsFromSettings(SelfHealSettings{})
	if def != selfheal.DefaultCaps() {
		t.Fatalf("empty settings must yield default caps, got %+v", def)
	}
	over := capsFromSettings(SelfHealSettings{PerSessionPerWindow: 9, GlobalPerHour: 11})
	if over.PerSession6h != 9 || over.GlobalPerHour != 11 {
		t.Fatalf("overrides not applied: %+v", over)
	}
	// auth401 cap stays at the safe default even when per-session is widened.
	if over.PerSessionAuth401 != 1 {
		t.Fatalf("auth401 cap must stay 1, got %d", over.PerSessionAuth401)
	}
}

func TestSelfHealSettings_OptOut(t *testing.T) {
	s := SelfHealSettings{
		OptOutGroups:   []string{"stream-leads"},
		OptOutSessions: []string{"keep-warm", "id-123"},
	}
	if !s.IsGroupOptedOut("stream-leads") {
		t.Error("group opt-out not honored")
	}
	if s.IsGroupOptedOut("agent-deck") {
		t.Error("unrelated group must not be opted out")
	}
	if !s.IsSessionOptedOut("id-123", "any") || !s.IsSessionOptedOut("x", "keep-warm") {
		t.Error("session opt-out (by id or title) not honored")
	}
	if s.IsSessionOptedOut("nope", "nope") {
		t.Error("unrelated session must not be opted out")
	}
}

func TestSelfHealMode_Normalizes(t *testing.T) {
	cases := map[string]string{
		"":              "observe",
		"observe":       "observe",
		"garbage":       "observe",
		"single_action": "single_action",
		"full":          "full",
	}
	for in, want := range cases {
		if got := (SelfHealSettings{Mode: in}).SelfHealMode(); got != want {
			t.Errorf("SelfHealMode(%q) = %q, want %q", in, got, want)
		}
	}
}

// The registry must BUILD the right engine for the mode — constructed through
// the real engineFor path, one fresh registry per mode so every call is a cache
// MISS. A pre-injected engine would only re-assert the injected value.
func TestSelfHealRegistry_NonResumeMode_BuildsObserveEngine(t *testing.T) {
	for _, mode := range []string{"observe", "single_action", "full", "", "nonsense"} {
		r := newSelfHealRegistry()
		e, exec := r.engineFor("p", selfheal.DefaultCaps(), mode)
		if e == nil {
			t.Fatalf("mode %q: engineFor returned nil — the audit sink could not be opened. "+
				"internal/session's TestMain calls testutil.IsolateHome(); if that stopped "+
				"happening this test would write to the real data dir", mode)
		}
		if e.Mode() != selfheal.ModeObserve {
			t.Fatalf("mode %q: registry must build an observe engine, got %q", mode, e.Mode())
		}
		if exec != nil {
			t.Fatalf("mode %q: a non-resume engine must hold no executor", mode)
		}
	}
}

// Acceptance criterion 2: mode "resume" BUILDS the acting engine, with an
// executor. Fresh registry, so this exercises the construction branch.
func TestSelfHealRegistry_ResumeMode_BuildsActingEngine(t *testing.T) {
	r := newSelfHealRegistry()
	e, exec := r.engineFor("p", selfheal.DefaultCaps(), "resume")
	if e == nil {
		t.Fatal("engineFor returned nil — the audit sink could not be opened")
	}
	if e.Mode() != selfheal.ModeResume {
		t.Fatalf("want %q, got %q", selfheal.ModeResume, e.Mode())
	}
	if exec == nil {
		t.Fatal("the resume engine must hand back its executor so the pass can refresh the view")
	}
	if r.execs["p"] != exec {
		t.Fatal("the executor must be retained in the registry, not rebuilt per pass")
	}
}

// Pins the documented caching caveat rather than leaving it as a surprise: the
// engine is built once per profile and the mode is read only on that miss, so
// flipping [selfheal] mode in config has NO effect until the transition daemon
// restarts. The engine must outlive the poll — the confirm and cap/breaker
// windows accumulate inside it — so this is the accepted trade, documented for
// operators in docs/self-heal.md. If this test starts failing because the engine
// is rebuilt on a mode change, delete the test AND the doc paragraph together.
func TestSelfHealRegistry_ModeChangeNeedsRestart(t *testing.T) {
	r := newSelfHealRegistry()
	first, _ := r.engineFor("p", selfheal.DefaultCaps(), "observe")
	if first == nil {
		t.Fatal("engineFor returned nil — the audit sink could not be opened")
	}
	second, exec := r.engineFor("p", selfheal.DefaultCaps(), "resume")
	if second != first {
		t.Fatal("the engine must be cached per profile — rebuilding it resets the confirm and cap windows")
	}
	if second.Mode() != selfheal.ModeObserve {
		t.Fatalf("a cached engine keeps its original mode, got %q", second.Mode())
	}
	if exec != nil {
		t.Fatal("no executor appears without a restart either")
	}
}

// "resume" is a recognised mode; the shipped default is still observe.
func TestSelfHealMode_AcceptsResume(t *testing.T) {
	if got := (SelfHealSettings{Mode: "resume"}).SelfHealMode(); got != "resume" {
		t.Fatalf("want resume, got %q", got)
	}
	if got := (SelfHealSettings{}).SelfHealMode(); got != "observe" {
		t.Fatalf("the shipped default must stay observe, got %q", got)
	}
	if (SelfHealSettings{}).Enabled {
		t.Fatal("the shipped default must stay disabled")
	}
}

// The composer read is scoped to the substates that can produce a resume.
func TestIsSelfHealResumeSubstate(t *testing.T) {
	for _, s := range []Substate{SubstateAPIError, SubstateUsageLimit, SubstateModelUnavailable} {
		if !isSelfHealResumeSubstate(s) {
			t.Fatalf("%q must be a resume substate", s)
		}
	}
	for _, s := range []Substate{SubstateNone, SubstateRunning, SubstateIdleAtEmptyPrompt, SubstateAuth401, SubstateStalled} {
		if isSelfHealResumeSubstate(s) {
			t.Fatalf("%q must NOT trigger a composer capture", s)
		}
	}
}

// A candidate with no tmux session takes neither extra read and reports no draft.
func TestBuildSelfHealCandidate_NoPane_NoComposerDraft(t *testing.T) {
	inst := &Instance{ID: "s1", Title: "worker-3"}
	c := buildSelfHealCandidate(inst, "idle", nil, time.Time{}, false)
	if c.ComposerDraft != nil {
		t.Fatal("no pane means nothing to protect")
	}
	if !c.NotBefore.IsZero() {
		t.Fatalf("no usage-limit verdict means no schedule, got %s", c.NotBefore)
	}
}

// A session with no pane has nothing to protect and nothing to send to, so the
// lookup reports no draft rather than taking the fail-safe branch.
func TestInstanceComposerHasDraft_NoPane_ReportsNoDraft(t *testing.T) {
	if instanceComposerHasDraft(&Instance{ID: "s1"}) {
		t.Fatal("a nil tmux session has no composer to hold a draft")
	}
}

// The fail-safe: a capture that ERRORS must read as "there might be a draft",
// never as "the composer is empty". It is the only thing protecting an
// operator's typed text when tmux is unreachable, and it is a different branch
// from the no-pane case above.
func TestInstanceComposerHasDraft_CaptureError_FailsSafe(t *testing.T) {
	// A tmux session bound to a name no server has: CapturePaneFresh's subprocess
	// exits non-zero, which is the branch under test.
	inst := &Instance{ID: "s1"}
	inst.SetTmuxSessionForTest(&tmux.Session{Name: "agentdeck-no-such-session-selfheal-failsafe"})
	if !instanceComposerHasDraft(inst) {
		t.Fatal("a capture that fails must read as 'there might be a draft'")
	}
}

// buildSelfHealCandidate must NOT resolve the draft: each resolution forks a
// tmux capture-pane with a 3s timeout, and the daemon builds a candidate for
// every wedged session on every 1-3s poll inside one serial loop. Under a
// correlated outage that is the multi-second-freeze class this repo has hit
// before, and most of those reads can only return skip_dwell / skip_confirm.
func TestBuildSelfHealCandidate_ComposerDraftIsDeferred(t *testing.T) {
	calls := 0
	prev := composerDraftLookup
	composerDraftLookup = func(*Instance) bool { calls++; return true }
	t.Cleanup(func() { composerDraftLookup = prev })

	inst := usageLimitedInstance(t, "selfheal-deferred-draft", time.Now().Add(90*time.Minute))

	c := buildSelfHealCandidate(inst, "waiting", nil, time.Time{}, false)
	if calls != 0 {
		t.Fatalf("building a candidate must capture nothing, got %d captures", calls)
	}
	if c.ComposerDraft == nil {
		t.Fatal("a resume substate must carry a draft lookup for the engine to resolve")
	}
	if !c.ComposerDraft() {
		t.Fatal("the lookup must return what the underlying check says")
	}
	if calls != 1 {
		t.Fatalf("resolving the lookup captures exactly once, got %d", calls)
	}
}

// The lookup is attached only for the substates that can produce a resume, so
// every other session pays nothing at all — not even a deferred one.
func TestBuildSelfHealCandidate_NonResumeSubstate_NoDraftLookup(t *testing.T) {
	inst := NewInstanceWithTool("selfheal-no-draft-lookup", t.TempDir(), "claude")
	if c := buildSelfHealCandidate(inst, "waiting", nil, time.Time{}, false); c.ComposerDraft != nil {
		t.Fatal("a non-resume substate must carry no draft lookup")
	}
}

// usageLimitedInstance seeds a live usage-limit verdict with a schedule, exactly
// as a completed scan would publish it, and claims the throttle window so the
// next read is answered from the memo instead of rescanning.
func usageLimitedInstance(t *testing.T, title string, notBefore time.Time) *Instance {
	t.Helper()
	inst := NewInstanceWithTool(title, t.TempDir(), "claude")
	inst.ClaudeSessionID = "session-A"
	inst.mu.Lock()
	inst.usageLimitSessionID = "session-A"
	inst.usageLimitedCached = true
	inst.usageLimitNotBeforeCached = notBefore
	inst.lastUsageLimitScanAt = time.Now()
	inst.mu.Unlock()
	return inst
}

// Task 06 AC 3 / task 04 AC 4-5: a usage-limited session is LIFTED to
// SubstateUsageLimit and carries the parsed schedule. CachedSubstate is
// deliberately usage-limit-blind (it is the TUI render hot path), so without
// this lift a quota-rejected session reaches self-heal labelled
// idle_at_empty_prompt and is never scheduled at all.
func TestBuildSelfHealCandidate_UsageLimited_LiftsSubstateAndSchedule(t *testing.T) {
	notBefore := time.Now().Add(90 * time.Minute).UTC()
	inst := usageLimitedInstance(t, "selfheal-usage-limit-lift", notBefore)

	c := buildSelfHealCandidate(inst, "waiting", nil, time.Time{}, false)
	if c.Substate != SubstateUsageLimit {
		t.Fatalf("a usage-limited session must reach self-heal as %q, got %q", SubstateUsageLimit, c.Substate)
	}
	if !c.NotBefore.Equal(notBefore) {
		t.Fatalf("the schedule must ride along with the verdict: want %s, got %s", notBefore, c.NotBefore)
	}
	if c.ComposerDraft == nil {
		t.Fatal("usage-limit is a resume substate and must carry a draft lookup")
	}
}

// The atomic accessor reports the live memo, and a rebind must clear it: the memo
// is keyed by the Claude session id, so an A→B rebind discards both the verdict
// and its schedule rather than handing B the schedule formed for A.
//
// A schedule is only ever legible alongside the verdict it belongs to, so there
// is deliberately no separate NotBefore accessor to assert against — reading the
// two apart is the torn read usageLimitedWithSchedule exists to prevent.
func TestUsageLimitedWithSchedule_ReportsMemo_RebindClearsIt(t *testing.T) {
	notBefore := time.Now().Add(90 * time.Minute).UTC()
	inst := usageLimitedInstance(t, "selfheal-notbefore-rebind", notBefore)

	if limited, nb := inst.usageLimitedWithSchedule(); !limited || !nb.Equal(notBefore) {
		t.Fatalf("verdict and schedule must arrive together, got limited=%v notBefore=%s", limited, nb)
	}

	// Normal rebind onto the same Instance.
	inst.ClaudeSessionID = "session-B"

	// Both halves, in one read: no verdict must mean no schedule. Asserting the
	// zero NotBefore here is what the deleted accessor's test covered — a
	// surviving A-schedule on a B-verdict would be a resume with no gate at all.
	if limited, nb := inst.usageLimitedWithSchedule(); limited || !nb.IsZero() {
		t.Fatalf("a rebind must discard A's verdict and schedule, got limited=%v notBefore=%s", limited, nb)
	}
}

// The daemon's audit sink must COLLAPSE by default. Wired wrong, the pass goes
// straight back to one record per evaluation — the 480 MB/day this bounds.
func TestNewSelfHealAuditSink_CollapsesByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selfheal-audit.ndjson")
	sink, err := newSelfHealAuditSink(path, SelfHealSettings{})
	if err != nil {
		t.Fatalf("newSelfHealAuditSink: %v", err)
	}
	if _, ok := sink.(*selfheal.CollapsingSink); !ok {
		t.Fatalf("default audit sink is %T, want *selfheal.CollapsingSink", sink)
	}

	// End to end through the real file: identical evaluations write once.
	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 100; i++ {
		ev := selfheal.Event{
			TS:        base.Add(time.Duration(i) * 2 * time.Second).Format(time.RFC3339),
			SessionID: "s1",
			Decision:  selfheal.DecisionSkipHealthy,
			Stage:     selfheal.ModeObserve,
		}
		if err := sink.Append(ev); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	var n int
	if err := selfheal.ForEachAuditEvent(path, func(selfheal.Event) error { n++; return nil }); err != nil {
		t.Fatalf("ForEachAuditEvent: %v", err)
	}
	if n != 1 {
		t.Fatalf("100 identical evaluations wrote %d audit records, want 1", n)
	}
}

// The escape hatch must actually bypass the collapse.
func TestNewSelfHealAuditSink_FullRecordsOptOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selfheal-audit.ndjson")
	sink, err := newSelfHealAuditSink(path, SelfHealSettings{AuditFullRecords: true})
	if err != nil {
		t.Fatalf("newSelfHealAuditSink: %v", err)
	}
	if _, ok := sink.(*selfheal.NDJSONSink); !ok {
		t.Fatalf("audit_full_records sink is %T, want the raw *selfheal.NDJSONSink", sink)
	}
}

// A daemon shutdown must not truncate the last run of each session: the
// collapsing sink owes a closing record with the suppressed count, and
// flushSinks is what pays it. Nil registry is the no-daemon case.
func TestSelfHealRegistry_FlushSinksWritesPendingClosingRecords(t *testing.T) {
	(*selfHealRegistry)(nil).flushSinks() // must not panic

	path := filepath.Join(t.TempDir(), "selfheal-audit.ndjson")
	sink, err := newSelfHealAuditSink(path, SelfHealSettings{})
	if err != nil {
		t.Fatalf("newSelfHealAuditSink: %v", err)
	}
	r := newSelfHealRegistry()
	r.sinks["p"] = sink

	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		if err := sink.Append(selfheal.Event{
			TS:        base.Add(time.Duration(i) * 2 * time.Second).Format(time.RFC3339),
			SessionID: "s1",
			Decision:  selfheal.DecisionSkipHealthy,
			Stage:     selfheal.ModeObserve,
		}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	var before []selfheal.Event
	if err := selfheal.ForEachAuditEvent(path, func(e selfheal.Event) error {
		before = append(before, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("before flush: %d records, want 1 open entry", len(before))
	}

	r.flushSinks()

	var after []selfheal.Event
	if err := selfheal.ForEachAuditEvent(path, func(e selfheal.Event) error {
		after = append(after, e)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatalf("after flush: %d records, want the entry plus its closing record", len(after))
	}
	if after[1].Repeat != 9 {
		t.Fatalf("closing record repeat = %d, want the 9 suppressed reads", after[1].Repeat)
	}
	if want := base.Add(18 * time.Second).Format(time.RFC3339); after[1].TS != want {
		t.Fatalf("closing record TS = %q, want the last suppressed read %q", after[1].TS, want)
	}
}
