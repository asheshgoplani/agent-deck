package selfheal

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// base returns a candidate that, with a dwelled anchor, IS a candidate. Tests
// flip one field to assert each disqualifier in isolation.
func base(now time.Time) Candidate {
	return Candidate{
		SessionID:       "s1-1780000000",
		Title:           "exec-fix",
		Group:           "agent-deck",
		Profile:         "personal",
		Status:          "error",
		Substate:        tmux.SubstateModelUnavailable,
		Busy:            false,
		HookRunningFresh: false,
		OutputMoved:     false,
		Stopped:         false,
		OptedOut:        false,
		StatusChangedAt: now.Add(-5 * time.Minute), // well past 90s
	}
}

func TestEvaluate_ModelUnavailable_Dwelled_IsCandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	res := Evaluate(base(now), now)
	if !res.Candidate || res.Decision != DecisionAct {
		t.Fatalf("want candidate/act, got candidate=%v decision=%s", res.Candidate, res.Decision)
	}
}

func TestEvaluate_Busy_NeverCandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := base(now)
	c.Busy = true // authoritative disqualifier (§3.1)
	res := Evaluate(c, now)
	if res.Candidate || res.Decision != DecisionSkipBusy {
		t.Fatalf("busy must never be a candidate, got candidate=%v decision=%s", res.Candidate, res.Decision)
	}
}

func TestEvaluate_MidTurn_HookRunning_NeverCandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := base(now)
	c.HookRunningFresh = true
	res := Evaluate(c, now)
	if res.Candidate || res.Decision != DecisionSkipMidTurn {
		t.Fatalf("mid-turn (hook running) must never be a candidate, got %v/%s", res.Candidate, res.Decision)
	}
}

func TestEvaluate_MidTurn_OutputMoved_NeverCandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := base(now)
	c.OutputMoved = true
	res := Evaluate(c, now)
	if res.Candidate || res.Decision != DecisionSkipMidTurn {
		t.Fatalf("mid-turn (output moved) must never be a candidate, got %v/%s", res.Candidate, res.Decision)
	}
}

func TestEvaluate_Healthy_NeverCandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	for _, s := range []tmux.Substate{tmux.SubstateNone, tmux.SubstateRunning} {
		c := base(now)
		c.Substate = s
		res := Evaluate(c, now)
		if res.Candidate || res.Decision != DecisionSkipHealthy {
			t.Fatalf("substate %q must never be a candidate, got %v/%s", s, res.Candidate, res.Decision)
		}
	}
}

func TestEvaluate_NotDwelled_NeverCandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := base(now)
	c.StatusChangedAt = now.Add(-30 * time.Second) // < 90s threshold
	res := Evaluate(c, now)
	if res.Candidate || res.Decision != DecisionSkipDwell {
		t.Fatalf("not dwelled must skip_dwell, got %v/%s", res.Candidate, res.Decision)
	}
}

func TestEvaluate_Stopped_NeverCandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := base(now)
	c.Stopped = true
	res := Evaluate(c, now)
	if res.Candidate || res.Decision != DecisionSkipStopped {
		t.Fatalf("stopped must never be a candidate, got %v/%s", res.Candidate, res.Decision)
	}
}

func TestEvaluate_OptedOut_NeverCandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := base(now)
	c.OptedOut = true
	res := Evaluate(c, now)
	if res.Candidate || res.Decision != DecisionSkipOptOut {
		t.Fatalf("opted-out must never be a candidate, got %v/%s", res.Candidate, res.Decision)
	}
}

// §1.4: a long-waiting session with NO send is deliberate idle, never a
// candidate — idle_at_empty_prompt is only stuck AFTER a send.
func TestEvaluate_IdleNoSend_NeverCandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := base(now)
	c.Substate = tmux.SubstateIdleAtEmptyPrompt
	c.Status = "waiting"
	c.StatusChangedAt = now.Add(-3 * time.Hour) // waiting for ages
	c.LastSentAt = time.Time{}                   // but we never sent anything
	res := Evaluate(c, now)
	if res.Candidate || res.Decision != DecisionSkipDwell {
		t.Fatalf("deliberate idle (no send) must never be a candidate, got %v/%s", res.Candidate, res.Decision)
	}
}

// idle_at_empty_prompt IS a candidate only once it has dwelled past 5m AFTER a
// send.
func TestEvaluate_IdleAfterSend_Dwelled_IsCandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := base(now)
	c.Substate = tmux.SubstateIdleAtEmptyPrompt
	c.Status = "waiting"
	c.LastSentAt = now.Add(-6 * time.Minute) // sent, no response for 6m > 5m
	res := Evaluate(c, now)
	if !res.Candidate || res.Decision != DecisionAct {
		t.Fatalf("idle 6m after send must be a candidate, got %v/%s", res.Candidate, res.Decision)
	}
}

func TestEvaluate_IdleAfterSend_NotDwelled_Skips(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := base(now)
	c.Substate = tmux.SubstateIdleAtEmptyPrompt
	c.LastSentAt = now.Add(-2 * time.Minute) // < 5m
	res := Evaluate(c, now)
	if res.Candidate || res.Decision != DecisionSkipDwell {
		t.Fatalf("idle 2m after send must skip_dwell, got %v/%s", res.Candidate, res.Decision)
	}
}

// Each stuck substate maps to the correct would_have action (§2.4).
func TestWouldHaveAction_PerSubstate(t *testing.T) {
	cases := map[tmux.Substate]Action{
		tmux.SubstateModelUnavailable:  ActionRestartModelSwitch,
		tmux.SubstateAuth401:           ActionRestartReassertCreds,
		tmux.SubstateIdleAtEmptyPrompt: ActionResend,
	}
	for s, want := range cases {
		if got := WouldHaveAction(s); got != want {
			t.Errorf("WouldHaveAction(%q) = %q, want %q", s, got, want)
		}
	}
	// a non-stuck class falls back to escalate (defensive).
	if got := WouldHaveAction(tmux.SubstateNone); got != ActionEscalate {
		t.Errorf("WouldHaveAction(none) = %q, want escalate", got)
	}
}

func TestDwellThresholds(t *testing.T) {
	if d, ok := DwellThreshold(tmux.SubstateModelUnavailable); !ok || d != 90*time.Second {
		t.Errorf("model_unavailable dwell = %v ok=%v, want 90s", d, ok)
	}
	if d, ok := DwellThreshold(tmux.SubstateAuth401); !ok || d != 60*time.Second {
		t.Errorf("auth_401 dwell = %v ok=%v, want 60s", d, ok)
	}
	// usage_limit / running / none are not stuck classes.
	if _, ok := DwellThreshold(tmux.SubstateRunning); ok {
		t.Errorf("running must not be a stuck class")
	}
}
