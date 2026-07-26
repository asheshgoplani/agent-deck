package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/fleet"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func fleetTestCandidate(title, group string, st session.Status) fleet.Candidate {
	return fleet.Candidate{
		Instance: &session.Instance{ID: "id-" + title, Title: title, GroupPath: group, Status: st},
		Health:   fleet.HealthDown,
		Status:   string(st),
	}
}

func TestFormatFleetStatus_MassDeath(t *testing.T) {
	as := fleet.Assessment{
		Total:     10,
		Alive:     1,
		Down:      2,
		Skipped:   7,
		MassDeath: true,
		Candidates: []fleet.Candidate{
			fleetTestCandidate("conductor-one", "conductor", session.StatusError),
			fleetTestCandidate("worker-a", "agent-deck", session.StatusRunning),
		},
	}

	got := formatFleetStatus(as)

	for _, want := range []string{
		"10 sessions", "1 alive", "2 down", "7 not running",
		"MASS DEATH detected", "conductor-one", "worker-a",
		"status=error", "group=agent-deck", "fleet recover",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status output missing %q:\n%s", want, got)
		}
	}
}

func TestFormatFleetStatus_HealthyFleetSaysNothingToDo(t *testing.T) {
	got := formatFleetStatus(fleet.Assessment{Total: 5, Alive: 5})
	if !strings.Contains(got, "Nothing to recover.") {
		t.Errorf("healthy output = %q", got)
	}
	if strings.Contains(got, "MASS DEATH") {
		t.Errorf("healthy fleet must not claim a mass death:\n%s", got)
	}
}

// The default (no --yes) path must make it unmistakable that nothing happened
// and must say how to actually run the sweep.
func TestFormatFleetRecover_DryRunIsLabelledAndActionable(t *testing.T) {
	as := fleet.Assessment{Total: 3, Down: 2, MassDeath: true,
		Candidates: []fleet.Candidate{
			fleetTestCandidate("a", "g", session.StatusError),
			fleetTestCandidate("b", "g", session.StatusError),
		}}
	sum := fleet.Summary{
		Assessment:  as,
		DryRun:      true,
		Attempted:   2,
		TotalWaited: 5 * time.Second,
		Results: []fleet.Result{
			{Title: "a", Outcome: fleet.OutcomePlanned},
			{Title: "b", Outcome: fleet.OutcomePlanned, WaitedBefore: 5 * time.Second},
		},
	}

	got := formatFleetRecover(sum, true)

	for _, want := range []string{
		"DRY RUN — nothing was restarted", "wait=5s",
		"dry-run down=2 attempted=2", "Estimated sequential runtime", "Re-run with --yes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, got)
		}
	}
}

// A plan must not number sessions it is not going to touch. With --limit the
// rest of the down set is still listed (the operator needs to know it is down)
// but visibly outside this run — printing all 65 as numbered plan lines would
// misreport what --yes is about to do.
func TestFormatFleetRecover_PlanSeparatesSessionsOutsideTheLimit(t *testing.T) {
	sum := fleet.Summary{
		Assessment:  fleet.Assessment{Total: 3, Down: 3},
		DryRun:      true,
		Attempted:   1,
		Skipped:     2,
		TotalWaited: 0,
		Results: []fleet.Result{
			{Title: "in-plan", Outcome: fleet.OutcomePlanned},
			{Title: "out-one", Outcome: fleet.OutcomeSkipped, Reason: "beyond --limit 1"},
			{Title: "out-two", Outcome: fleet.OutcomeSkipped, Reason: "beyond --limit 1"},
		},
	}

	got := formatFleetRecover(sum, true)

	if !strings.Contains(got, " 1. in-plan") {
		t.Errorf("planned session is not numbered:\n%s", got)
	}
	for _, title := range []string{"out-one", "out-two"} {
		line := fleetLineContaining(got, title)
		if !strings.Contains(line, "not in this run: beyond --limit 1") {
			t.Errorf("session outside the limit reported as %q", line)
		}
		if strings.Contains(line, "wait=") {
			t.Errorf("session outside the limit shown with a planned wait: %q", line)
		}
	}
}

// fleetLineContaining returns the first line of out containing needle.
func fleetLineContaining(out, needle string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

func TestFormatFleetRecover_DistinguishesEveryOutcome(t *testing.T) {
	sum := fleet.Summary{
		Assessment: fleet.Assessment{Total: 4, Down: 4},
		Attempted:  3,
		Recovered:  1,
		Unverified: 1,
		Failed:     1,
		Skipped:    1,
		Halted:     true,
		HaltReason: "3 consecutive restarts failed",
		Results: []fleet.Result{
			{Title: "ok-one", Outcome: fleet.OutcomeRecovered,
				Report: fleet.VerifyReport{PaneAlive: true, ToolStarted: true, Status: "running", Elapsed: 4 * time.Second}},
			{Title: "half-one", Outcome: fleet.OutcomeUnverified,
				Report: fleet.VerifyReport{PaneAlive: true, Status: "starting", Substate: "auth-401"}},
			{Title: "broken-one", Outcome: fleet.OutcomeFailed, Err: errFleetTest},
			{Title: "untried-one", Outcome: fleet.OutcomeSkipped, Reason: "halted"},
		},
	}

	got := formatFleetRecover(sum, false)

	for _, want := range []string{
		"ok         ok-one", "unverified half-one", "auth-401",
		"FAILED     broken-one", "tmux exploded",
		"skipped    untried-one",
		"halted=true", "HALTED: 3 consecutive restarts failed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("recover output missing %q:\n%s", want, got)
		}
	}
}

func TestFormatFleetRecover_NothingDown(t *testing.T) {
	got := formatFleetRecover(fleet.Summary{Assessment: fleet.Assessment{Total: 4, Alive: 4}}, false)
	if !strings.Contains(got, "Nothing to recover.") {
		t.Errorf("output = %q", got)
	}
}

func TestFleetRecoverJSON_ShapeIsMachineCheckable(t *testing.T) {
	sum := fleet.Summary{
		Assessment: fleet.Assessment{Total: 2, Alive: 0, Down: 2, MassDeath: true},
		Attempted:  2,
		Recovered:  1,
		Unverified: 1,
		Halted:     true,
		HaltReason: "auth circuit open",
		Results: []fleet.Result{
			{ID: "id-a", Title: "a", Status: "error", Outcome: fleet.OutcomeRecovered,
				WaitedBefore: 5 * time.Second,
				Report:       fleet.VerifyReport{PaneAlive: true, ToolStarted: true, Status: "running", Elapsed: 2 * time.Second}},
			{ID: "id-b", Title: "b", Status: "error", Outcome: fleet.OutcomeSkipped, Reason: "halted"},
		},
	}

	payload := fleetRecoverJSON(sum, false)

	if payload["success"] != false {
		t.Error("a halted sweep must not report success=true")
	}
	if payload["mass_death"] != true || payload["halted"] != true {
		t.Errorf("payload = %+v", payload)
	}
	if payload["halt_reason"] != "auth circuit open" {
		t.Errorf("halt_reason = %v", payload["halt_reason"])
	}
	sessions, ok := payload["sessions"].([]map[string]interface{})
	if !ok || len(sessions) != 2 {
		t.Fatalf("sessions = %#v", payload["sessions"])
	}
	first := sessions[0]
	if first["outcome"] != string(fleet.OutcomeRecovered) {
		t.Errorf("outcome = %v", first["outcome"])
	}
	if first["waited_ms"] != int64(5000) || first["verify_ms"] != int64(2000) {
		t.Errorf("timings = %v / %v", first["waited_ms"], first["verify_ms"])
	}
	if first["tool_started"] != true || first["status_after"] != "running" {
		t.Errorf("verification fields = %+v", first)
	}
	// A skipped session carries a reason and NO verification fields — the
	// payload must never imply we looked at a session we never touched.
	second := sessions[1]
	if second["reason"] != "halted" {
		t.Errorf("reason = %v", second["reason"])
	}
	if _, present := second["pane_alive"]; present {
		t.Errorf("skipped session must not carry verification fields: %+v", second)
	}
}

func TestFleetStatusJSON_Shape(t *testing.T) {
	as := fleet.Assessment{Total: 3, Alive: 1, Down: 1, Skipped: 1, MassDeath: false, Probes: 2,
		Candidates: []fleet.Candidate{fleetTestCandidate("a", "", session.StatusError)}}

	payload := fleetStatusJSON(as)

	if payload["total"] != 3 || payload["down"] != 1 || payload["probes"] != 2 {
		t.Errorf("payload = %+v", payload)
	}
	sessions := payload["sessions"].([]map[string]interface{})
	if len(sessions) != 1 || sessions[0]["title"] != "a" {
		t.Fatalf("sessions = %#v", sessions)
	}
	// An empty group must render as the default group, never as "".
	if sessions[0]["group"] != session.DefaultGroupPath {
		t.Errorf("group = %v, want %q", sessions[0]["group"], session.DefaultGroupPath)
	}
}

func TestTruncateFleetTitle(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "short", want: "short"},
		{in: "0123456789", want: "0123456789"},
		{in: "0123456789a", want: "0123456..."},
	}
	for _, tc := range tests {
		if got := truncateFleetTitle(tc.in, 10); got != tc.want {
			t.Errorf("truncateFleetTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := truncateFleetTitle("abcdef", 2); got != "ab" {
		t.Errorf("tiny max = %q", got)
	}
}

// Titles are user-supplied and often non-ASCII; truncation must cut on rune
// boundaries so a report never prints a mangled half-character.
func TestTruncateFleetTitle_IsRuneSafe(t *testing.T) {
	title := "工作会话-日本語-ほげほげ"
	got := truncateFleetTitle(title, 6)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated title is not valid UTF-8: %q", got)
	}
	if want := "工作会..."; got != want {
		t.Errorf("truncateFleetTitle = %q, want %q", got, want)
	}
	// A title that fits by rune count must survive untouched even though its
	// byte length exceeds the limit.
	if got := truncateFleetTitle("日本語", 3); got != "日本語" {
		t.Errorf("truncateFleetTitle = %q, want the title unchanged", got)
	}
	if got := truncateFleetTitle("日本語です", 2); got != "日本" {
		t.Errorf("tiny max on multibyte = %q, want %q", got, "日本")
	}
}

// SAFETY DEFAULT. `fleet recover` restarts processes. Without an explicit --yes
// it must only ever print a plan — this is the guard that keeps a typo from
// restarting 65 sessions.
func TestFleetRecoverIsDryRunUnlessYes(t *testing.T) {
	tests := []struct {
		name     string
		yes      bool
		dryRun   bool
		wantPlan bool
	}{
		{name: "default is a plan", wantPlan: true},
		{name: "--yes acts", yes: true, wantPlan: false},
		{name: "--dry-run wins over --yes", yes: true, dryRun: true, wantPlan: true},
		{name: "--dry-run alone is a plan", dryRun: true, wantPlan: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fleetRecoverConfig{plan: tc.dryRun || !tc.yes}
			if cfg.plan != tc.wantPlan {
				t.Fatalf("plan = %t, want %t", cfg.plan, tc.wantPlan)
			}
			if got := cfg.recoverer().DryRun; got != tc.wantPlan {
				t.Fatalf("recoverer DryRun = %t, want %t", got, tc.wantPlan)
			}
		})
	}
}

func TestFleetRecoverConfigWiring(t *testing.T) {
	t.Run("spacing 0 is an explicit opt-out, not an unset field", func(t *testing.T) {
		rec := fleetRecoverConfig{spacing: 0}.recoverer()
		if !rec.NoSpacing {
			t.Fatal("NoSpacing = false for --spacing 0")
		}
		rec = fleetRecoverConfig{spacing: 8 * time.Second}.recoverer()
		if rec.NoSpacing || rec.Spacing != 8*time.Second {
			t.Fatalf("Spacing = %s NoSpacing = %t", rec.Spacing, rec.NoSpacing)
		}
	})

	t.Run("auth breaker can be disabled and tuned", func(t *testing.T) {
		if rec := (fleetRecoverConfig{authHaltAfter: 0}).recoverer(); rec.AuthGate != nil {
			t.Error("AuthGate should be nil when --auth-halt-after is 0")
		}
		rec := fleetRecoverConfig{authHaltAfter: 4}.recoverer()
		gate, ok := rec.AuthGate.(*fleet.SubstateAuthGate)
		if !ok {
			t.Fatalf("AuthGate = %T, want *fleet.SubstateAuthGate", rec.AuthGate)
		}
		if gate.HaltAfter != 4 {
			t.Errorf("HaltAfter = %d, want 4", gate.HaltAfter)
		}
	})

	t.Run("limits and brakes are forwarded", func(t *testing.T) {
		rec := fleetRecoverConfig{limit: 7, maxFailures: 2, jitter: 0.35}.recoverer()
		if rec.Limit != 7 || rec.MaxFailures != 2 || rec.Jitter != 0.35 {
			t.Fatalf("recoverer = limit %d, maxFailures %d, jitter %v", rec.Limit, rec.MaxFailures, rec.Jitter)
		}
	})

	// The recoverer reads MaxDeadBoots 0 as "unset → default", so the flag value
	// 0 (meaning "off") has to be translated to the negative opt-out. Passing it
	// through would silently re-enable the brake the operator asked to disable.
	t.Run("dead-boot brake: 0 disables, positive is forwarded", func(t *testing.T) {
		if rec := (fleetRecoverConfig{maxDeadBoots: 0}).recoverer(); rec.MaxDeadBoots >= 0 {
			t.Errorf("MaxDeadBoots = %d for --max-dead-boots 0, want a negative opt-out", rec.MaxDeadBoots)
		}
		if rec := (fleetRecoverConfig{maxDeadBoots: 5}).recoverer(); rec.MaxDeadBoots != 5 {
			t.Errorf("MaxDeadBoots = %d, want 5", rec.MaxDeadBoots)
		}
	})

	t.Run("a real verifier is always wired", func(t *testing.T) {
		if (fleetRecoverConfig{}).recoverer().Verify == nil {
			t.Fatal("Verify is nil — a sweep would report every boot as unverified")
		}
	})
}

var errFleetTest = fleetTestError("tmux exploded")

type fleetTestError string

func (e fleetTestError) Error() string { return string(e) }
