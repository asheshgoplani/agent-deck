package voicestream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

type published struct {
	kind, sid string
	ev        StateEvent
}

type rig struct {
	t     *testing.T
	dir   string
	now   time.Time
	alive map[int]bool
	pubs  []published
	p     *Producer
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, dir: t.TempDir(), now: time.Unix(1_794_950_000, 0), alive: map[int]bool{77: true}}
	rd := voicestate.NewReader(r.dir)
	rd.Now = func() time.Time { return r.now }
	rd.PIDAlive = func(pid int) bool { return r.alive[pid] }
	r.p = &Producer{Reader: rd, Origin: "laptop", Now: func() time.Time { return r.now },
		Publish: func(kind, sid string, data any) {
			r.pubs = append(r.pubs, published{kind, sid, data.(StateEvent)})
		}}
	return r
}

// write puts a daemon-shaped current.json in place (tmp + rename) stamped now.
func (r *rig) write(fields map[string]any) {
	r.t.Helper()
	st := map[string]any{"schema": "claude-code-voice.state/v1", "on": true, "mode": "listening", "phase": "listening",
		"phaseSince": r.now.UnixMilli(), "heartbeatMs": 10000, "daemon": map[string]any{"pid": 77},
		"heard": "words he said", "spoken": "words it said",
		"target":    map[string]any{"title": "alpha", "sessionId": "aaaa1111-1794950000", "profile": "default", "remote": nil},
		"updatedAt": r.now.UnixMilli()}
	for k, v := range fields {
		st[k] = v
	}
	raw, _ := json.Marshal(st)
	tmp := filepath.Join(r.dir, ".c.tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		r.t.Fatal(err)
	}
	_ = os.Chtimes(tmp, r.now, r.now)
	if err := os.Rename(tmp, filepath.Join(r.dir, "current.json")); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) step() (StateEvent, bool) { return r.p.Step() }

// The six-word mapping plus extras, as frames carry them.
func TestFromState_PhaseTable(t *testing.T) {
	cases := []struct {
		raw, mode    string
		muted        bool
		phase, extra string
	}{
		{"listening", "", false, "listening", ""},
		{"hearing", "", false, "hearing", ""},
		{"transcribing", "", false, "hearing", ""},
		{"thinking", "", false, "thinking", ""},
		{"speaking", "", false, "speaking", ""},
		{"resting", "", false, "resting", ""},
		{"muted", "", true, "resting", "muted"},
		{"paused", "", false, "resting", "call"},
		{"meeting", "", false, "resting", "meeting"},
		{"", "hibernate", false, "resting", ""},
		{"", "pending", false, "thinking", ""},
		{"", "call", false, "resting", "call"},
		{"", "muted", true, "resting", "muted"},
		{"off", "", false, "off", ""},
	}
	for _, c := range cases {
		s := &voicestate.State{On: true, Phase: c.raw, Mode: c.mode, Muted: c.muted}
		ev := FromState(s, "laptop", ReasonChange, time.Unix(0, 0))
		if ev.Phase != c.phase || ev.Extra != c.extra {
			t.Errorf("raw %q mode %q: phase %q extra %q, want %q %q", c.raw, c.mode, ev.Phase, ev.Extra, c.phase, c.extra)
		}
	}
	if ev := FromState(nil, "laptop", ReasonExpired, time.Unix(0, 0)); ev.On || ev.Phase != "off" || ev.Target != nil {
		t.Errorf("nil state: %+v", ev)
	}
}

// Target host and session_id: a local target is a session of this bus's
// host; a remote one keeps the host name and no session_id.
func TestFromState_TargetAndSessionID(t *testing.T) {
	local := FromState(&voicestate.State{On: true, Phase: "listening", Target: voicestate.Target{SessionID: "a1", Title: "alpha"}}, "laptop", ReasonChange, time.Unix(0, 0))
	if local.SessionID() != "a1" || local.Target.Host != "" {
		t.Errorf("local target: sid %q host %q", local.SessionID(), local.Target.Host)
	}
	remote := FromState(&voicestate.State{On: true, Phase: "listening", Target: voicestate.Target{SessionID: "b2", Title: "beta", Remote: "box"}}, "laptop", ReasonChange, time.Unix(0, 0))
	if remote.SessionID() != "" || remote.Target.Host != "box" {
		t.Errorf("remote target: sid %q host %q", remote.SessionID(), remote.Target.Host)
	}
	mirror := FromState(&voicestate.State{On: true, Phase: "listening", From: "laptop", Source: voicestate.SourceMirror}, "box", ReasonChange, time.Unix(0, 0))
	if mirror.From != "mirror" || mirror.Origin != "laptop" {
		t.Errorf("mirror: from %q origin %q", mirror.From, mirror.Origin)
	}
}

// One frame per change, none for a heartbeat that changes nothing, "off"
// (reason expired) for each way the voice goes away.
func TestProducer_ChangesAndExpiry(t *testing.T) {
	t.Run("start, change, heartbeat, switch", func(t *testing.T) {
		r := newRig(t)
		r.write(nil)
		if ev, ok := r.step(); !ok || ev.Reason != ReasonStart || ev.Phase != "listening" {
			t.Fatalf("start: %v %+v", ok, ev)
		}
		r.now = r.now.Add(time.Second)
		r.write(map[string]any{"phaseSince": r.now.UnixMilli() - 1000}) // a beat: only updatedAt moves
		if _, ok := r.step(); ok {
			t.Fatal("a heartbeat that changes nothing published a frame")
		}
		r.write(map[string]any{"phase": "hearing", "phaseSince": r.now.UnixMilli()})
		if ev, ok := r.step(); !ok || ev.Phase != "hearing" || ev.Reason != ReasonChange {
			t.Fatalf("phase change: %v %+v", ok, ev)
		}
		r.write(map[string]any{"phase": "hearing", "target": map[string]any{"title": "beta", "sessionId": "bbbb2222-1794950000", "remote": "box"}})
		ev, ok := r.step()
		if !ok || ev.Target == nil || ev.Target.ID != "bbbb2222-1794950000" || ev.Target.Host != "box" {
			t.Fatalf("switch: %v %+v", ok, ev.Target)
		}
		if got := r.pubs[len(r.pubs)-1]; got.kind != KindState || got.sid != "" {
			t.Fatalf("remote target frame kind %q sid %q", got.kind, got.sid)
		}
	})
	expiries := []struct {
		name string
		do   func(r *rig)
	}{
		{"daemon killed", func(r *rig) { r.alive[77] = false }},
		{"on false", func(r *rig) { r.write(map[string]any{"on": false}) }},
		{"hung daemon past 3 beats + 5 s", func(r *rig) { r.now = r.now.Add(36 * time.Second) }},
		{"file removed", func(r *rig) { _ = os.Remove(filepath.Join(r.dir, "current.json")) }},
	}
	for _, c := range expiries {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.write(nil)
			r.step()
			r.now = r.now.Add(100 * time.Millisecond)
			c.do(r)
			ev, ok := r.step()
			if !ok || ev.On || ev.Phase != "off" || ev.Reason != ReasonExpired {
				t.Fatalf("got %v %+v, want an off frame with reason expired", ok, ev)
			}
			if _, ok := r.step(); ok {
				t.Fatal("off published twice")
			}
		})
	}
	t.Run("hung daemon inside the window stays live", func(t *testing.T) {
		r := newRig(t)
		r.write(nil)
		r.step()
		r.now = r.now.Add(34 * time.Second)
		if _, ok := r.step(); ok {
			t.Fatal("a quiet daemon inside its heartbeat window went off")
		}
	})
}

// No heard or spoken text in any frame, whatever the file holds.
func TestProducer_NoHeardText(t *testing.T) {
	r := newRig(t)
	r.write(nil)
	r.step()
	raw, _ := json.Marshal(r.pubs)
	for _, p := range r.pubs {
		b, _ := json.Marshal(p.ev)
		raw = append(raw, b...)
	}
	if strings.Contains(string(raw), "words he said") || strings.Contains(string(raw), "words it said") {
		t.Fatalf("heard/spoken text in a frame: %s", raw)
	}
}

// Two producers for one profile: only the lock holder publishes; the other
// takes over when the holder stops.
func TestRunExclusive_OneProducerPerProfile(t *testing.T) {
	profile := "_test"
	path, err := lockPath(profile)
	if err != nil {
		t.Fatal(err)
	}
	release, err := tryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tryLock(path); err != errLocked {
		t.Fatalf("second lock: %v, want errLocked", err)
	}
	release()
	release2, err := tryLock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release2()
}
