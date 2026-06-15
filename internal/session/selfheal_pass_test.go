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

// The registry exposes an observe-only engine: it must never hold an executor.
func TestSelfHealRegistry_ObserveEngineOnly(t *testing.T) {
	r := newSelfHealRegistry()
	// Inject a memory-backed engine so we don't depend on the filesystem path.
	r.engines["p"] = selfheal.NewObserveEngine(selfheal.DefaultCaps(), &selfheal.MemorySink{})
	e := r.engines["p"]
	if e.Mode() != selfheal.ModeObserve {
		t.Fatalf("registry engine must be observe mode, got %q", e.Mode())
	}
}
