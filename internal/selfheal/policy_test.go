package selfheal

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func cand(id string, s tmux.Substate) Candidate {
	return Candidate{SessionID: id, Substate: s}
}

func TestGate_PerSessionCap_TwoThenBlocked(t *testing.T) {
	p := NewPolicyMachine(DefaultCaps()) // PerSession6h=2
	now := time.Unix(1780000000, 0).UTC()
	c := cand("s1", tmux.SubstateModelUnavailable)

	// 1st and 2nd attempts allowed.
	for i := 0; i < 2; i++ {
		if d, _ := p.Gate(c, now); d != DecisionAct {
			t.Fatalf("attempt %d: want act, got %s", i+1, d)
		}
		p.RecordAttempt(c, now)
		now = now.Add(time.Minute)
	}
	// 3rd within the 6h window is capped.
	if d, st := p.Gate(c, now); d != DecisionCapHit {
		t.Fatalf("3rd attempt: want cap_hit, got %s (session6h=%d)", d, st.Session6h)
	}
}

func TestGate_PerSessionCap_RollsOffAfterWindow(t *testing.T) {
	p := NewPolicyMachine(DefaultCaps())
	now := time.Unix(1780000000, 0).UTC()
	c := cand("s1", tmux.SubstateModelUnavailable)
	p.RecordAttempt(c, now)
	p.RecordAttempt(c, now.Add(time.Minute))
	// 7h later both attempts have rolled off the 6h window.
	later := now.Add(7 * time.Hour)
	if d, st := p.Gate(c, later); d != DecisionAct || st.Session6h != 0 {
		t.Fatalf("after window: want act/0, got %s/%d", d, st.Session6h)
	}
}

func TestGate_Auth401_CapIsOne(t *testing.T) {
	p := NewPolicyMachine(DefaultCaps()) // PerSessionAuth401=1
	now := time.Unix(1780000000, 0).UTC()
	c := cand("s1", tmux.SubstateAuth401)
	if d, _ := p.Gate(c, now); d != DecisionAct {
		t.Fatalf("1st 401: want act, got %s", d)
	}
	p.RecordAttempt(c, now)
	if d, _ := p.Gate(c, now.Add(time.Minute)); d != DecisionCapHit {
		t.Fatalf("2nd 401: want cap_hit (K=1), got %s", d)
	}
}

func TestGate_GlobalFleetCap(t *testing.T) {
	p := NewPolicyMachine(DefaultCaps()) // GlobalPerHour=5
	now := time.Unix(1780000000, 0).UTC()
	// 5 distinct sessions each record one attempt → global window full.
	for i := 0; i < 5; i++ {
		c := cand(string(rune('a'+i)), tmux.SubstateModelUnavailable)
		if d, _ := p.Gate(c, now); d != DecisionAct {
			t.Fatalf("session %d: want act, got %s", i, d)
		}
		p.RecordAttempt(c, now)
	}
	// 6th distinct session is blocked by the GLOBAL cap even though its own
	// per-session count is 0.
	c6 := cand("z", tmux.SubstateModelUnavailable)
	if d, st := p.Gate(c6, now); d != DecisionCapHit {
		t.Fatalf("6th session: want global cap_hit, got %s (global=%d)", d, st.GlobalHour)
	}
}

func TestBreaker_OpensAfterKFails(t *testing.T) {
	p := NewPolicyMachine(DefaultCaps()) // BreakerK=2
	c := cand("s1", tmux.SubstateModelUnavailable)
	now := time.Unix(1780000000, 0).UTC()
	p.RecordOutcome(c, false) // 1st fail
	if p.IsQuarantined("s1") {
		t.Fatal("breaker opened too early (after 1 fail, K=2)")
	}
	p.RecordOutcome(c, false) // 2nd fail → open
	if !p.IsQuarantined("s1") {
		t.Fatal("breaker must open after K=2 consecutive fails")
	}
	if d, st := p.Gate(c, now); d != DecisionBreakerOpen || !st.BreakerOpen {
		t.Fatalf("quarantined session: want breaker_open, got %s", d)
	}
}

func TestBreaker_Auth401_OpensAfterOneFail(t *testing.T) {
	p := NewPolicyMachine(DefaultCaps()) // BreakerKAuth401=1
	c := cand("s1", tmux.SubstateAuth401)
	p.RecordOutcome(c, false)
	if !p.IsQuarantined("s1") {
		t.Fatal("401 breaker must open after a single fail (K=1)")
	}
}

func TestBreaker_HealthyResetsFails(t *testing.T) {
	p := NewPolicyMachine(DefaultCaps())
	c := cand("s1", tmux.SubstateModelUnavailable)
	p.RecordOutcome(c, false)
	p.RecordOutcome(c, true) // healthy resets
	p.RecordOutcome(c, false)
	if p.IsQuarantined("s1") {
		t.Fatal("healthy outcome must reset the consecutive-fail counter")
	}
}

func TestFlicker_QuarantineEquivalent(t *testing.T) {
	p := NewPolicyMachine(DefaultCaps())
	c := cand("s1", tmux.SubstateModelUnavailable)
	now := time.Unix(1780000000, 0).UTC()
	p.SetFlickering("s1", true)
	if d, st := p.Gate(c, now); d != DecisionBreakerOpen || !st.BreakerOpen {
		t.Fatalf("flapping session must be breaker_open (never restart a flap), got %s", d)
	}
	// clearing the flicker re-allows.
	p.SetFlickering("s1", false)
	if d, _ := p.Gate(c, now); d != DecisionAct {
		t.Fatalf("after flicker cleared: want act, got %s", d)
	}
}

func TestClearQuarantine(t *testing.T) {
	p := NewPolicyMachine(DefaultCaps())
	c := cand("s1", tmux.SubstateAuth401)
	p.RecordOutcome(c, false) // opens (K=1)
	if !p.IsQuarantined("s1") {
		t.Fatal("expected quarantine")
	}
	p.ClearQuarantine("s1")
	if p.IsQuarantined("s1") {
		t.Fatal("ClearQuarantine must release the breaker")
	}
}
