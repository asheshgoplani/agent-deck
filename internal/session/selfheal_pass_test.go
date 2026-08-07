package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/selfheal"
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

// The composer read is scoped to the two substates that can produce a resume.
func TestIsSelfHealResumeSubstate(t *testing.T) {
	for _, s := range []Substate{SubstateAPIError, SubstateUsageLimit} {
		if !isSelfHealResumeSubstate(s) {
			t.Fatalf("%q must be a resume substate", s)
		}
	}
	for _, s := range []Substate{SubstateNone, SubstateRunning, SubstateIdleAtEmptyPrompt, SubstateAuth401, SubstateModelUnavailable, SubstateStalled} {
		if isSelfHealResumeSubstate(s) {
			t.Fatalf("%q must NOT trigger a composer capture", s)
		}
	}
}

// A candidate with no tmux session takes neither extra read and reports no draft.
func TestBuildSelfHealCandidate_NoPane_NoComposerDraft(t *testing.T) {
	inst := &Instance{ID: "s1", Title: "worker-3"}
	c := buildSelfHealCandidate(inst, "idle", nil, time.Time{}, false)
	if c.ComposerDraft {
		t.Fatal("no pane means nothing to protect")
	}
	if !c.NotBefore.IsZero() {
		t.Fatalf("no usage-limit verdict means no schedule, got %s", c.NotBefore)
	}
}

// instanceComposerHasDraft fails SAFE: a capture that errors must read as
// "there might be a draft", never as "the composer is empty".
func TestInstanceComposerHasDraft_NoPane(t *testing.T) {
	if instanceComposerHasDraft(&Instance{ID: "s1"}) {
		t.Fatal("a nil tmux session has no composer to hold a draft")
	}
}
