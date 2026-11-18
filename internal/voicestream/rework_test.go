package voicestream

// Rework round 1 (review of 1020576613b9). R6-R10 are the reviewer's cases
// (review-stream/harness), adapted; the rest are new.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

func liveMirrorFrom(from string) voicestate.Mirror {
	return voicestate.Mirror{Schema: voicestate.MirrorSchema, Relative: true, On: true, Mode: "listening", Phase: "listening",
		HeartbeatMs: 10000, From: from, UpdatedAt: time.Now().UnixMilli(),
		Target: &voicestate.MirrorTarget{Title: "t", SessionID: "sid-t", Profile: "default"}}
}

func writeCurrent(t *testing.T, dir string, on bool, sid string) {
	t.Helper()
	st := map[string]any{"schema": "claude-code-voice.state/v1", "on": on, "mode": "listening", "phase": "listening",
		"phaseSince": time.Now().UnixMilli(), "heartbeatMs": 10000, "daemon": map[string]any{"pid": os.Getpid()},
		"target": map[string]any{"title": "alpha", "sessionId": sid, "profile": "default", "remote": nil}, "updatedAt": time.Now().UnixMilli()}
	raw, _ := json.Marshal(st)
	tmp := filepath.Join(dir, ".c.tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "current.json")); err != nil {
		t.Fatal(err)
	}
}

// sinkRec records what a relay hands one sink.
type sinkRec struct {
	mu     sync.Mutex
	sent   []voicestate.Mirror
	pings  int
	notify chan struct{}
}

func newSinkRec() *sinkRec { return &sinkRec{notify: make(chan struct{}, 64)} }

func (s *sinkRec) sink(dest string) Sink {
	return Sink{Dest: dest,
		Send: func(m voicestate.Mirror) {
			s.mu.Lock()
			s.sent = append(s.sent, m)
			s.mu.Unlock()
			select {
			case s.notify <- struct{}{}:
			default:
			}
		},
		Ping: func() { s.mu.Lock(); s.pings++; s.mu.Unlock() },
	}
}

func (s *sinkRec) snapshot() ([]voicestate.Mirror, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]voicestate.Mirror(nil), s.sent...), s.pings
}

// S3 (reviewer's R6): with no daemon here and another host's live mirror in
// the dir, the relay must not forward that mirror as its own state.
func TestRelay_DoesNotForwardAMirror(t *testing.T) {
	dir := t.TempDir()
	if err := voicestate.WriteMirror(dir, liveMirrorFrom("box")); err != nil {
		t.Fatal(err)
	}
	rec := newSinkRec()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &Relay{Reader: voicestate.NewReader(dir), Self: "mac", Interval: 10 * time.Millisecond, Sinks: []Sink{rec.sink("box")}}
	go r.Run(ctx)
	select {
	case <-rec.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("relay sent nothing")
	}
	sent, _ := rec.snapshot()
	if sent[0].On {
		t.Fatalf("relay with no daemon forwarded another host's mirror as its own live state: %+v", sent[0])
	}
}

// S3 (reviewer's R7): two hosts relaying to each other both go off after
// the daemon turns off.
func TestRelay_MutualRelaysExpireAfterDaemonOff(t *testing.T) {
	dirM, dirB := t.TempDir(), t.TempDir()
	writeCurrent(t, dirM, true, "sid-a")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mk := func(dir, self, dest, destDir string) *Relay {
		return &Relay{Reader: voicestate.NewReader(dir), Self: self, Interval: 10 * time.Millisecond, Refresh: 50 * time.Millisecond,
			Sinks: []Sink{{Dest: dest, Send: func(m voicestate.Mirror) { _ = voicestate.WriteMirror(destDir, m) }, Ping: func() {}}}}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); mk(dirM, "mac", "box", dirB).Run(ctx) }()
	go func() { defer wg.Done(); mk(dirB, "box", "mac", dirM).Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	if s := voicestate.NewReader(dirB).Poll(); s == nil {
		t.Fatal("box never saw the voice on")
	}
	writeCurrent(t, dirM, false, "sid-a")
	time.Sleep(1500 * time.Millisecond)
	sb, sm := voicestate.NewReader(dirB).Poll(), voicestate.NewReader(dirM).Poll()
	cancel()
	wg.Wait()
	if sb != nil || sm != nil {
		t.Fatalf("1.5 s after the daemon turned off: box live=%v mac live=%v (want both off)", sb != nil, sm != nil)
	}
}

// B2: a relay that stops sends "off" down every link before it closes, so
// a remote does not keep the old session until the mirror expires.
func TestRelay_StopSendsOff(t *testing.T) {
	dir := t.TempDir()
	writeCurrent(t, dir, true, "sid-a")
	rec := newSinkRec()
	ctx, cancel := context.WithCancel(context.Background())
	r := &Relay{Reader: voicestate.NewReader(dir), Self: "mac", Interval: 10 * time.Millisecond, Sinks: []Sink{rec.sink("box")}}
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	select {
	case <-rec.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("relay sent nothing")
	}
	cancel()
	<-done
	sent, _ := rec.snapshot()
	if last := sent[len(sent)-1]; last.On {
		t.Fatalf("relay stopped with the voice on and sent no off: last mirror %+v", last)
	}
}

// S2: a relay with no daemon sends one off at start and then only
// keepalive pings, so an idle second relay cannot make a remote blink.
func TestRelay_IdleSendsOneOffThenPings(t *testing.T) {
	dir := t.TempDir()
	rec := newSinkRec()
	ctx, cancel := context.WithCancel(context.Background())
	r := &Relay{Reader: voicestate.NewReader(dir), Self: "studio", Interval: 5 * time.Millisecond, Refresh: 30 * time.Millisecond,
		Sinks: []Sink{rec.sink("box")}}
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	time.Sleep(400 * time.Millisecond)
	cancel()
	<-done
	sent, pings := rec.snapshot()
	if len(sent) > 2 { // the start off, plus at most the stop off
		t.Fatalf("idle relay sent %d mirrors in 400 ms, want one off (then pings): %+v", len(sent), sent)
	}
	for _, m := range sent {
		if m.On {
			t.Fatalf("idle relay sent an on mirror: %+v", m)
		}
	}
	if pings < 3 {
		t.Fatalf("idle relay sent %d keepalive pings in 400 ms at a 30 ms refresh", pings)
	}
}

// S5: a frame the bus did not take is published again on the next step, so
// a dropped "off" cannot leave "listening" as the newest frame for good.
func TestProducer_RetriesARejectedFrame(t *testing.T) {
	r := newRig(t)
	var accepted []StateEvent
	reject := false
	r.p.Publish = func(_, _ string, d any) bool {
		if reject {
			return false // what a full queue or a disabled bus does
		}
		accepted = append(accepted, d.(StateEvent))
		return true
	}
	r.write(nil)
	r.step()
	reject = true
	r.alive[77] = false // the daemon dies: "off" is due
	r.step()
	reject = false
	r.now = r.now.Add(100 * time.Millisecond)
	r.step()
	if last := accepted[len(accepted)-1]; last.On || last.Reason != ReasonExpired {
		t.Fatalf("rejected off frame not published again: newest accepted %+v", last)
	}
}

// S6 (reviewer's R9): a receiver whose link dies before its first line,
// on a host with a fresh mirror from a relay that is gone, must not leave
// "on" as the newest frame when it exits.
func TestMirrorIn_EmptyInputDoesNotLeaveOn(t *testing.T) {
	dir := t.TempDir()
	if err := voicestate.WriteMirror(dir, liveMirrorFrom("mac")); err != nil {
		t.Fatal(err)
	}
	profile := "_rev_mirrorin"
	var mu sync.Mutex
	var frames []StateEvent
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := events.OpenProfile(profile)
	_ = w.Close()
	bus, err := events.OpenReader(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	sub, err := bus.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for f := range sub.Frames() {
			if f.Kind != KindState {
				continue
			}
			var ev StateEvent
			_ = json.Unmarshal(f.Data, &ev)
			mu.Lock()
			frames = append(frames, ev)
			mu.Unlock()
		}
	}()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- MirrorIn(ctx, profile, dir, pr, io.Discard) }()
	time.Sleep(500 * time.Millisecond)
	_ = pw.Close() // the link dies before any line
	select {
	case <-done:
	case <-time.After(voicestate.RelayMirrorWindow + 5*time.Second):
		t.Fatal("mirror-in did not exit after its mirror expired")
	}
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(frames) == 0 {
		t.Fatal("no voice.state frame on the bus")
	}
	if last := frames[len(frames)-1]; last.On {
		t.Fatalf("mirror-in exited and the newest frame is on (reason %s)", last.Reason)
	}
}

// S2: on a host where a live mirror from one daemon's relay is in place, an
// off mirror from another sender (an idle second relay) is ignored.
func TestMirrorIn_IgnoresOffFromAnotherSender(t *testing.T) {
	dir := t.TempDir()
	if err := voicestate.WriteMirror(dir, liveMirrorFrom("mac")); err != nil {
		t.Fatal(err)
	}
	off := voicestate.MirrorFor(nil, "box", "studio", time.Now().UnixMilli())
	line, _ := json.Marshal(off)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	in := strings.NewReader(string(line) + "\n")
	go func() { _ = MirrorIn(ctx, "_rev_owner", dir, in, io.Discard) }()
	time.Sleep(300 * time.Millisecond)
	if s := voicestate.NewReader(dir).Poll(); s == nil || s.From != "mac" {
		t.Fatalf("an idle relay's off replaced the live mirror from another daemon: %+v", s)
	}
	// The owner's own off is applied.
	ownOff := voicestate.MirrorFor(nil, "box", "mac", time.Now().UnixMilli())
	line, _ = json.Marshal(ownOff)
	go func() { _ = MirrorIn(ctx, "_rev_owner2", dir, strings.NewReader(string(line)+"\n"), io.Discard) }()
	time.Sleep(300 * time.Millisecond)
	if s := voicestate.NewReader(dir).Poll(); s != nil {
		t.Fatalf("the owner's off was not applied: %+v", s)
	}
}

// S11: a receiver whose link goes silent (no line, no ping) for
// MirrorInIdle stops reading and exits once its mirror is no longer live.
func TestMirrorIn_ExitsWhenTheLinkGoesSilent(t *testing.T) {
	old := MirrorInIdle
	MirrorInIdle = 300 * time.Millisecond
	defer func() { MirrorInIdle = old }()
	dir := t.TempDir()
	pr, pw := io.Pipe()
	defer pw.Close()
	done := make(chan error, 1)
	go func() { done <- MirrorIn(context.Background(), "_rev_idle", dir, pr, io.Discard) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("receiver of a silent link (open, no lines) still running after 3 s")
	}
}

// S10: the frame's confirm card never carries the dictated message: the
// read-back sentence holds his words ("Send to alpha: <message>. Yes?").
func TestResultFromDesk_ConfirmCarriesNoDictation(t *testing.T) {
	raw := `{"handled":true,"intent":"tell","target":{"id":"a1","title":"alpha","host":""},
 "say":"Send to alpha: delete the prod database. Yes?","show":{"kind":"confirm","items":[]},
 "confirm":{"token":"T","kind":"yes","prompt":"Send to alpha: delete the prod database. Yes?","expiresMs":10000},
 "then":null,"refused":null,"dryRun":false}`
	ev, err := ResultFromDesk([]byte(raw), "mac")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(ev)
	if strings.Contains(string(out), "prod database") {
		t.Fatalf("voice.result carries the dictated message: %s", out)
	}
	if ev.Confirm == nil || ev.Confirm.Kind != "yes" || ev.Confirm.ExpiresMs != 10000 || !strings.Contains(ev.Confirm.Prompt, "alpha") {
		t.Fatalf("confirm card lost what screens need: %+v", ev.Confirm)
	}
}

// Reviewer's R10 (control): nothing outside the frame's fields survives.
func TestResultFromDesk_DropsEverythingElse(t *testing.T) {
	raw := `{"handled":true,"intent":"tell","say":"SAY-X","heard":"HEARD-X","sentence":"SENT-X",
 "target":{"id":"a","title":"A","host":"","token":"TOK-X"},
 "show":{"kind":"confirm","heard":"HEARD-X","items":[{"id":"a","title":"A","host":"","status":"waiting","line":"l","token":"TOK-X","say":"SAY-X"}]},
 "confirm":{"token":"TOK-X","kind":"yes","prompt":"Send to A: hi. Yes?","expiresMs":10000,"sentence":"SENT-X"},
 "then":{"action":"switch","token":"TOK-X","target":{"id":"a","title":"A","host":"","say":"SAY-X"}},"refused":null,"dryRun":false}`
	ev, err := ResultFromDesk([]byte(raw), "glasses")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(ev)
	for _, n := range []string{"SAY-X", "HEARD-X", "SENT-X", "TOK-X"} {
		if strings.Contains(string(out), n) {
			t.Fatalf("voice.result carries %s: %s", n, out)
		}
	}
}
