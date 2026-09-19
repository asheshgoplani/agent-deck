package session

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// samplingPipe answers PipeAlive from a per-session list of readings, one
// per call, repeating the last one.
type samplingPipe struct {
	readings map[string][]bool
	calls    map[string]int
}

func (p *samplingPipe) alive(name string) bool {
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	seq := p.readings[name]
	i := p.calls[name]
	p.calls[name]++
	if len(seq) == 0 {
		return false
	}
	if i >= len(seq) {
		i = len(seq) - 1
	}
	return seq[i]
}

// v1.16.11 rollout: right after tui_restarted the reviver read every pipe
// as dead (the new image had not attached them yet) and respawned the
// maintainer's live session twice. A pipe-dead verdict is now confirmed by
// a second reading ConfirmAfter later: transient → alive, untouched;
// persistent → errored, revived; a stored error status never waits.
func TestReviver_PipeDeadIsConfirmedBySecondSample(t *testing.T) {
	transient := newReviverTestInstance("transient", StatusIdle)
	persistent := newReviverTestInstance("persistent", StatusIdle)
	stored := newReviverTestInstance("stored-error", StatusError)
	pipes := &samplingPipe{readings: map[string][]bool{
		instanceTmuxName(transient):  {false, true},
		instanceTmuxName(persistent): {false, false},
		instanceTmuxName(stored):     {false, true}, // status decides; no second read
	}}
	var logs bytes.Buffer
	var slept []time.Duration
	revived := map[string]int{}
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    pipes.alive,
		ReviveAction: func(i *Instance) error { revived[i.Title]++; return nil },
		Log:          slog.New(slog.NewJSONHandler(&logs, nil)),
		ConfirmAfter: 3 * time.Second,
		sleep:        func(d time.Duration) { slept = append(slept, d) },
	}

	outcomes := r.ReviveAll([]*Instance{transient, persistent, stored})

	if len(slept) != 1 || slept[0] != 3*time.Second {
		t.Fatalf("slept %v, want exactly one ConfirmAfter wait for the whole sweep", slept)
	}
	byTitle := map[string]ReviveOutcome{}
	for _, o := range outcomes {
		byTitle[o.Title] = o
	}
	if o := byTitle["transient"]; o.Class != ClassAlive || o.Revived {
		t.Fatalf("transient pipe-dead: %+v, want alive and untouched", o)
	}
	if o := byTitle["persistent"]; o.Class != ClassErrored || !o.Revived {
		t.Fatalf("persistent pipe-dead: %+v, want errored and revived", o)
	}
	if o := byTitle["stored-error"]; o.Class != ClassErrored || !o.Revived {
		t.Fatalf("stored error: %+v, want errored and revived without waiting", o)
	}
	if revived["transient"] != 0 || revived["persistent"] != 1 || revived["stored-error"] != 1 {
		t.Fatalf("revive calls = %v", revived)
	}
	if pipes.calls[instanceTmuxName(stored)] != 1 {
		t.Fatalf("a stored error status must not be re-sampled, read %d times", pipes.calls[instanceTmuxName(stored)])
	}
	if !strings.Contains(logs.String(), `"msg":"reviver_pipe_dead_transient"`) || !strings.Contains(logs.String(), `"title":"transient"`) {
		t.Fatalf("log = %s, want reviver_pipe_dead_transient for the transient session", logs.String())
	}
}

// With ConfirmAfter zero (every unit test that builds Reviver{} directly,
// and the CLI one-shot where no pipe can come up) a single reading decides
// and nothing sleeps.
func TestReviver_ConfirmDisabledKeepsSingleSample(t *testing.T) {
	inst := newReviverTestInstance("single", StatusIdle)
	pipes := &samplingPipe{readings: map[string][]bool{instanceTmuxName(inst): {false, true}}}
	revived := 0
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    pipes.alive,
		ReviveAction: func(*Instance) error { revived++; return nil },
		sleep:        func(time.Duration) { t.Fatal("must not sleep with ConfirmAfter = 0") },
	}
	out := r.ReviveAll([]*Instance{inst})
	if out[0].Class != ClassErrored || revived != 1 || pipes.calls[instanceTmuxName(inst)] != 1 {
		t.Fatalf("outcome %+v revived=%d reads=%d, want the legacy single-sample revive", out[0], revived, pipes.calls[instanceTmuxName(inst)])
	}
}

// ReviveOne confirms the same way as a sweep.
func TestReviver_ReviveOneConfirmsPipeDead(t *testing.T) {
	inst := newReviverTestInstance("one", StatusIdle)
	pipes := &samplingPipe{readings: map[string][]bool{instanceTmuxName(inst): {false, true}}}
	r := &Reviver{
		TmuxExists:   func(string, string) bool { return true },
		PipeAlive:    pipes.alive,
		ReviveAction: func(*Instance) error { t.Fatal("transient pipe-dead must not be revived"); return nil },
		ConfirmAfter: time.Second,
		sleep:        func(time.Duration) {},
	}
	if out := r.ReviveOne(inst); out.Class != ClassAlive || out.Revived {
		t.Fatalf("outcome = %+v, want alive", out)
	}
}

// The default reviver confirms; the sweep would otherwise act on the boot
// reading.
func TestNewReviver_ConfirmsByDefault(t *testing.T) {
	if got := NewReviver().ConfirmAfter; got != DefaultReviveConfirmAfter || got <= 0 {
		t.Fatalf("NewReviver().ConfirmAfter = %v, want %v", got, DefaultReviveConfirmAfter)
	}
}
