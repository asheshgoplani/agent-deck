package fleet

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// testInstance builds a minimal Instance. No tmux session is attached, so
// TmuxName falls back to the title — which is what the probe stub keys on.
func testInstance(title string, st session.Status) *session.Instance {
	return &session.Instance{
		ID:        "id-" + title,
		Title:     title,
		Status:    st,
		Tool:      "claude",
		GroupPath: "root",
		CreatedAt: time.Unix(1000, 0),
	}
}

// aliveOnly returns a probe stub reporting only the named sessions as present.
func aliveOnly(names ...string) func(string, string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(name, _ string) bool { return set[name] }
}

func newTestDetector(exists func(string, string) bool) *Detector {
	return &Detector{
		TmuxExists:    exists,
		Sleep:         func(time.Duration) {},
		ConfirmProbes: 1,
		MinDead:       DefaultMinDead,
		DeadFraction:  DefaultDeadFraction,
	}
}

func candidateTitles(as Assessment) []string {
	titles := make([]string, 0, len(as.Candidates))
	for _, c := range as.Candidates {
		titles = append(titles, c.Title())
	}
	return titles
}

func TestAssessClassifiesByStatusAndTmuxPresence(t *testing.T) {
	instances := []*session.Instance{
		testInstance("live-running", session.StatusRunning),
		testInstance("dead-running", session.StatusRunning),
		testInstance("dead-error", session.StatusError),
		testInstance("dead-waiting", session.StatusWaiting),
		testInstance("dead-starting", session.StatusStarting),
		testInstance("stopped", session.StatusStopped),
		testInstance("queued", session.StatusQueued),
		testInstance("idle", session.StatusIdle),
	}
	archived := testInstance("archived", session.StatusError)
	archived.ArchivedAt = time.Unix(2000, 0)
	instances = append(instances, archived)

	d := newTestDetector(aliveOnly("live-running"))
	as := d.Assess(instances)

	if as.Total != len(instances) {
		t.Fatalf("Total = %d, want %d", as.Total, len(instances))
	}
	if as.Alive != 1 {
		t.Errorf("Alive = %d, want 1", as.Alive)
	}
	// stopped + queued + idle + archived are all intentionally-not-running.
	if as.Skipped != 4 {
		t.Errorf("Skipped = %d, want 4", as.Skipped)
	}
	want := []string{"dead-error", "dead-running", "dead-starting", "dead-waiting"}
	got := candidateTitles(as)
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Errorf("missing candidate %q (got %v)", w, got)
		}
	}
}

// A dead pane is reported as error/stopped, while `idle` is ALSO the status of a
// session that was added but never started. Recovering idle by default would
// launch sessions the operator never launched, so it must be opt-in.
func TestAssessIdleIsOptIn(t *testing.T) {
	instances := []*session.Instance{testInstance("idle", session.StatusIdle)}

	d := newTestDetector(aliveOnly())
	if as := d.Assess(instances); as.Down != 0 {
		t.Fatalf("Down = %d with idle excluded, want 0", as.Down)
	}

	d.IncludeIdle = true
	as := d.Assess(instances)
	if as.Down != 1 {
		t.Fatalf("Down = %d with --include-idle, want 1", as.Down)
	}
}

// The destructive direction of this tool is restarting sessions that are ALIVE.
// A single has-session miss is not proof of death (a tmux server that just
// restarted answers "no such session" for a moment), so a session that
// reappears on a confirming probe must be counted alive, never restarted.
func TestAssessRequiresConfirmingProbes(t *testing.T) {
	flaky := testInstance("flaky", session.StatusRunning)
	reallyDead := testInstance("really-dead", session.StatusError)

	calls := map[string]int{}
	d := newTestDetector(func(name, _ string) bool {
		calls[name]++
		// "flaky" misses the first probe and answers on the second.
		return name == "flaky" && calls[name] >= 2
	})
	d.ConfirmProbes = 2
	slept := 0
	d.ConfirmDelay = 10 * time.Millisecond
	d.Sleep = func(time.Duration) { slept++ }

	as := d.Assess([]*session.Instance{flaky, reallyDead})

	if got := candidateTitles(as); len(got) != 1 || got[0] != "really-dead" {
		t.Fatalf("candidates = %v, want [really-dead]", got)
	}
	if as.Alive != 1 {
		t.Errorf("Alive = %d, want 1 (the flaky session recovered on probe 2)", as.Alive)
	}
	if slept != 1 {
		t.Errorf("confirm delay slept %d times, want 1", slept)
	}
	if as.Probes != 2 {
		t.Errorf("Probes = %d, want 2", as.Probes)
	}
}

func TestAssessSingleProbeSkipsConfirmRound(t *testing.T) {
	d := newTestDetector(aliveOnly())
	d.ConfirmProbes = 1
	d.Sleep = func(time.Duration) { t.Fatal("single-probe scan must not sleep") }

	as := d.Assess([]*session.Instance{testInstance("dead", session.StatusError)})
	if as.Down != 1 {
		t.Fatalf("Down = %d, want 1", as.Down)
	}
}

func TestMassDeathThresholds(t *testing.T) {
	mk := func(n int, st session.Status, prefix string) []*session.Instance {
		out := make([]*session.Instance, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, testInstance(prefix+string(rune('a'+i)), st))
		}
		return out
	}

	tests := []struct {
		name     string
		dead     int
		alive    int
		minDead  int
		fraction float64
		want     bool
	}{
		{name: "whole fleet gone", dead: 10, alive: 0, want: true},
		{name: "one crash is not a fleet death", dead: 1, alive: 9, want: false},
		{name: "below absolute floor even at 100%", dead: 2, alive: 0, want: false},
		{name: "at floor and at fraction", dead: 3, alive: 3, want: true},
		{name: "above floor but below fraction", dead: 4, alive: 40, want: false},
		{name: "custom lower floor trips", dead: 2, alive: 0, minDead: 2, want: true},
		{name: "custom fraction trips", dead: 4, alive: 40, minDead: 3, fraction: 0.05, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deadInsts := mk(tc.dead, session.StatusError, "dead-")
			aliveInsts := mk(tc.alive, session.StatusRunning, "alive-")
			aliveNames := make([]string, 0, len(aliveInsts))
			for _, a := range aliveInsts {
				aliveNames = append(aliveNames, a.Title)
			}
			d := newTestDetector(aliveOnly(aliveNames...))
			if tc.minDead != 0 {
				d.MinDead = tc.minDead
			}
			if tc.fraction != 0 {
				d.DeadFraction = tc.fraction
			}
			as := d.Assess(append(deadInsts, aliveInsts...))
			if as.MassDeath != tc.want {
				t.Fatalf("MassDeath = %t (down=%d alive=%d), want %t", as.MassDeath, as.Down, as.Alive, tc.want)
			}
		})
	}
}

// FAIL-SAFE DEFAULT. A Detector with no liveness probe must report every
// session ALIVE, so a misconstructed detector produces a no-op assessment
// instead of a plan to restart the entire live fleet.
func TestAssessWithoutProbeTreatsEverythingAsAlive(t *testing.T) {
	d := &Detector{ConfirmProbes: 1}
	as := d.Assess([]*session.Instance{
		testInstance("one", session.StatusRunning),
		testInstance("two", session.StatusError),
	})
	if as.Down != 0 || as.MassDeath {
		t.Fatalf("assessment = %+v, want nothing down", as)
	}
	if as.Alive != 2 {
		t.Errorf("Alive = %d, want 2", as.Alive)
	}
}

func TestAssessNoSessionsIsNotMassDeath(t *testing.T) {
	d := newTestDetector(aliveOnly())
	as := d.Assess(nil)
	if as.MassDeath || as.Down != 0 || as.Total != 0 {
		t.Fatalf("empty fleet assessed as %+v", as)
	}
}

func TestAssessGroupFilter(t *testing.T) {
	inGroupInst := testInstance("in-group", session.StatusError)
	inGroupInst.GroupPath = "agent-deck/workers"
	sameGroup := testInstance("same-group", session.StatusError)
	sameGroup.GroupPath = "agent-deck"
	other := testInstance("other", session.StatusError)
	other.GroupPath = "other-project"

	d := newTestDetector(aliveOnly())
	d.Group = "agent-deck"
	as := d.Assess([]*session.Instance{inGroupInst, sameGroup, other})

	if as.Down != 2 {
		t.Fatalf("Down = %d, want 2 (group + descendants)", as.Down)
	}
	if as.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", as.Skipped)
	}
}

func TestAssessOrdersCandidatesOldestFirst(t *testing.T) {
	newer := testInstance("newer", session.StatusError)
	newer.CreatedAt = time.Unix(3000, 0)
	older := testInstance("older", session.StatusError)
	older.CreatedAt = time.Unix(1000, 0)
	tieA := testInstance("a-tie", session.StatusError)
	tieA.CreatedAt = time.Unix(2000, 0)
	tieB := testInstance("b-tie", session.StatusError)
	tieB.CreatedAt = time.Unix(2000, 0)

	d := newTestDetector(aliveOnly())
	as := d.Assess([]*session.Instance{newer, tieB, older, tieA})

	want := []string{"older", "a-tie", "b-tie", "newer"}
	got := candidateTitles(as)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestAssessNeverMutatesInstances(t *testing.T) {
	inst := testInstance("dead", session.StatusError)
	beforeStatus, beforeTitle := inst.Status, inst.Title
	d := newTestDetector(aliveOnly())
	d.Assess([]*session.Instance{inst})
	if inst.Status != beforeStatus || inst.Title != beforeTitle {
		t.Fatalf("Assess mutated the instance: status %q -> %q, title %q -> %q",
			beforeStatus, inst.Status, beforeTitle, inst.Title)
	}
}

func TestAssessTolerantOfNilEntries(t *testing.T) {
	d := newTestDetector(aliveOnly())
	as := d.Assess([]*session.Instance{nil, testInstance("dead", session.StatusError), nil})
	if as.Total != 1 || as.Down != 1 {
		t.Fatalf("assessment = %+v, want total=1 down=1", as)
	}
}

func TestClassifyReportsWhySessionsAreOutOfScope(t *testing.T) {
	archived := testInstance("archived", session.StatusError)
	archived.ArchivedAt = time.Unix(2000, 0)
	outside := testInstance("outside", session.StatusError)
	outside.GroupPath = "elsewhere"

	tests := []struct {
		name       string
		inst       *session.Instance
		group      string
		wantHealth Health
		wantReason string
	}{
		{name: "nil", inst: nil, wantHealth: HealthSkipped, wantReason: "no instance"},
		{name: "archived", inst: archived, wantHealth: HealthSkipped, wantReason: "archived"},
		{name: "outside group", inst: outside, group: "agent-deck", wantHealth: HealthSkipped, wantReason: "outside --group"},
		{name: "stopped", inst: testInstance("stopped", session.StatusStopped), wantHealth: HealthSkipped, wantReason: "status stopped"},
		{name: "queued", inst: testInstance("queued", session.StatusQueued), wantHealth: HealthSkipped, wantReason: "status queued"},
		{name: "down", inst: testInstance("down", session.StatusError), wantHealth: HealthDown},
		{name: "alive", inst: testInstance("live", session.StatusRunning), wantHealth: HealthAlive},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDetector(aliveOnly("live"))
			d.Group = tc.group
			health, reason := d.Classify(tc.inst)
			if health != tc.wantHealth {
				t.Fatalf("health = %s, want %s", health, tc.wantHealth)
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func TestInGroup(t *testing.T) {
	tests := []struct {
		path, want string
		ok         bool
	}{
		{path: "agent-deck", want: "agent-deck", ok: true},
		{path: "agent-deck/workers", want: "agent-deck", ok: true},
		{path: "agent-deckery", want: "agent-deck", ok: false},
		{path: "other-project", want: "agent-deck", ok: false},
		{path: "anything", want: "", ok: true},
		{path: "/agent-deck/", want: "agent-deck", ok: true},
	}
	for _, tc := range tests {
		if got := inGroup(tc.path, tc.want); got != tc.ok {
			t.Errorf("inGroup(%q, %q) = %t, want %t", tc.path, tc.want, got, tc.ok)
		}
	}
}

func TestTmuxNameFallsBackToTitle(t *testing.T) {
	if got := TmuxName(nil); got != "" {
		t.Errorf("TmuxName(nil) = %q, want empty", got)
	}
	inst := testInstance("my-session", session.StatusError)
	if got := TmuxName(inst); got != "my-session" {
		t.Errorf("TmuxName = %q, want the title fallback", got)
	}
}
