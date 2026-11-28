package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/voicestate"
	"github.com/asheshgoplani/agent-deck/internal/voicestream"
)

type stateSSEEvent struct {
	id, event string
	data      json.RawMessage
	at        time.Time
}

// sseClient follows GET /events/state and forwards every event.
func sseClient(t *testing.T, ctx context.Context, url, lastID string, out chan<- stateSSEEvent) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events/state: %d", resp.StatusCode)
	}
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		var ev stateSSEEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				ev.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				ev.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.data = json.RawMessage(strings.TrimPrefix(line, "data: "))
			case line == "":
				if ev.event != "" {
					ev.at = time.Now()
					select {
					case out <- ev:
					case <-ctx.Done():
						return
					}
				}
				ev = stateSSEEvent{}
			}
		}
	}()
}

type voiceFrame struct {
	Cursor    uint64                 `json:"cursor"`
	Kind      string                 `json:"kind"`
	SessionID string                 `json:"session_id"`
	Data      voicestream.StateEvent `json:"data"`
}

// The lane-4 seed (plan section 5): the daemon's file on one host, the
// relay, the receiver on the stream host, its bus and the SSE door, driven
// by a scripted run: 10 target switches (local and remote), 20 phase
// changes, 6 status changes, one daemon kill, one 5 s cut with cursor
// resume. Every change must reach the SSE client within 1 s with the right
// session, and after the cut no frame may be missing or repeated.
func TestStateEvents_RelayToSSE_ScriptedRun(t *testing.T) {
	if testing.Short() {
		t.Skip("timed integration run")
	}
	const profile = "_test"
	macDir, boxDir := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_VOICE_STATE_DIR", boxDir)

	daemon := exec.Command("sleep", "300")
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	daemonDone := make(chan struct{})
	go func() { _ = daemon.Wait(); close(daemonDone) }()
	t.Cleanup(func() { _ = daemon.Process.Kill(); <-daemonDone })

	targets := map[string]map[string]any{
		"alpha": {"title": "alpha", "sessionId": "aaaa1111-1794950000", "profile": "default", "remote": nil},  // on the daemon's machine
		"beta":  {"title": "beta", "sessionId": "bbbb2222-1794950000", "profile": "default", "remote": "box"}, // on the stream host
		"gamma": {"title": "gamma", "sessionId": "cccc3333-1794950000", "profile": "default", "remote": nil},
	}
	// From the stream host (box), a target on the daemon's machine is named
	// by the relay's self name; its own session has host "".
	wantHost := map[string]string{"alpha": "laptop", "beta": "", "gamma": "laptop"}
	var phaseSince int64
	write := func(target, phase string) {
		t.Helper()
		phaseSince = time.Now().UnixMilli()
		raw, _ := json.Marshal(map[string]any{"schema": "claude-code-voice.state/v1", "on": true, "mode": "listening",
			"phase": phase, "phaseSince": phaseSince, "heartbeatMs": 10000, "daemon": map[string]any{"pid": daemon.Process.Pid},
			"heard": "HEARD-TEXT", "spoken": "SPOKEN-TEXT", "target": targets[target], "updatedAt": time.Now().UnixMilli()})
		tmp := filepath.Join(macDir, ".current.json.tmp")
		if err := os.WriteFile(tmp, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(macDir, "current.json")); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha", "listening")

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })

	// relay (daemon side) -> pipe (stands in for ssh) -> receiver (box side)
	pr, pw := io.Pipe()
	var pwMu sync.Mutex
	relay := &voicestream.Relay{Reader: voicestate.NewReader(macDir), Self: "laptop", Interval: 20 * time.Millisecond,
		Sinks: []voicestream.Sink{{Dest: "box", Send: func(m voicestate.Mirror) {
			raw, _ := json.Marshal(m)
			pwMu.Lock()
			_, _ = pw.Write(append(raw, '\n'))
			pwMu.Unlock()
		}}}}
	wg.Add(2)
	go func() { defer wg.Done(); relay.Run(ctx) }()
	go func() {
		defer wg.Done()
		_ = voicestream.MirrorIn(ctx, profile, boxDir, pr, io.Discard)
	}()
	t.Cleanup(func() { _ = pw.Close() })

	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: profile})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	// As deployed: the web server's own producer contends with the
	// receiver's for the profile's producer lock (one publishes).
	srv.startVoiceProducer()
	t.Cleanup(srv.stopVoiceProducer)

	evs := make(chan stateSSEEvent, 1024)
	connCtx, closeConn := context.WithCancel(ctx)
	defer closeConn()
	sseClient(t, connCtx, ts.URL+"/events/state", "", evs)

	var seen []stateSSEEvent // every voice/status event the client got, in order
	lastID := ""
	await := func(what string, match func(stateSSEEvent) bool) stateSSEEvent {
		t.Helper()
		start := time.Now()
		deadline := time.After(time.Second)
		for {
			select {
			case ev := <-evs:
				if ev.event == "hello" {
					continue
				}
				seen = append(seen, ev)
				if ev.id != "" {
					lastID = ev.id
				}
				if match(ev) {
					return ev
				}
			case <-deadline:
				t.Fatalf("%s: not on the SSE stream within 1 s (waited %s)", what, time.Since(start))
			}
		}
	}
	isVoice := func(target, phase string) func(stateSSEEvent) bool {
		return func(ev stateSSEEvent) bool {
			if ev.event != voicestream.KindState {
				return false
			}
			var f voiceFrame
			if json.Unmarshal(ev.data, &f) != nil || f.Data.Target == nil {
				return false
			}
			return f.Data.Target.Title == target && f.Data.Phase == phase && f.Data.Target.Host == wantHost[target]
		}
	}
	// hello first, carrying the newest cursor
	select {
	case ev := <-evs:
		if ev.event != "hello" {
			t.Fatalf("first event %q, want hello", ev.event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no hello")
	}
	await("initial state", isVoice("alpha", "listening"))

	statuses := 0
	publishStatus := func(id, to string) {
		statuses++
		events.PublishProfile(profile, "session.status", id, session.StatusBusEvent{From: "idle", To: to, Tool: "claude", ChangedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	}
	order := []string{"beta", "alpha", "gamma", "beta", "alpha", "beta", "gamma", "alpha", "beta", "alpha"}
	phases := []string{"hearing", "thinking", "speaking", "listening"}
	pi := 0
	for i, target := range order {
		if i == 5 {
			// 5 s network cut on the client side; the run goes on meanwhile.
			closeConn()
			cutEnd := time.Now().Add(5 * time.Second)
			write(target, "listening")
			for k := 0; k < 2; k++ {
				time.Sleep(300 * time.Millisecond)
				write(target, phases[pi%4])
				pi++
			}
			publishStatus("bbbb2222-1794950000", "running")
			time.Sleep(time.Until(cutEnd))
			// drain what the old connection delivered before the cut
			for drained := false; !drained; {
				select {
				case ev := <-evs:
					if ev.event != "hello" {
						seen = append(seen, ev)
						if ev.id != "" {
							lastID = ev.id
						}
					}
				case <-time.After(200 * time.Millisecond):
					drained = true
				}
			}
			resumeCtx, closeResume := context.WithCancel(ctx)
			t.Cleanup(closeResume)
			sseClient(t, resumeCtx, ts.URL+"/events/state", lastID, evs)
			await("state written during the cut, after resume", isVoice(target, phases[(pi-1)%4]))
			continue
		}
		write(target, "listening")
		await("switch "+target, isVoice(target, "listening"))
		for k := 0; k < 2; k++ {
			ph := phases[pi%4]
			pi++
			write(target, ph)
			await("phase "+ph+" on "+target, isVoice(target, ph))
		}
		if i%2 == 1 && statuses < 5 {
			id := targets[target]["sessionId"].(string)
			publishStatus(id, "waiting")
			await("status of "+target, func(ev stateSSEEvent) bool {
				return ev.event == "session.status" && strings.Contains(string(ev.data), id)
			})
		}
	}
	for statuses < 6 {
		publishStatus("aaaa1111-1794950000", "running")
		await("status", func(ev stateSSEEvent) bool {
			return ev.event == "session.status" && strings.Contains(string(ev.data), `"to":"running"`)
		})
	}

	// daemon kill -> "off" (reason expired) everywhere within 1 s
	_ = daemon.Process.Kill()
	<-daemonDone
	await("off after the daemon was killed", func(ev stateSSEEvent) bool {
		var f voiceFrame
		return ev.event == voicestream.KindState && json.Unmarshal(ev.data, &f) == nil && !f.Data.On && f.Data.Phase == "off" && f.Data.Reason == voicestream.ReasonExpired
	})

	// No frame missed or repeated, cut included: the client's cursors are
	// exactly the bus's voice/status frames after the first one it saw.
	var got []uint64
	for _, ev := range seen {
		if strings.Contains(string(ev.data), "HEARD-TEXT") || strings.Contains(string(ev.data), "SPOKEN-TEXT") {
			t.Fatalf("heard/spoken text crossed hosts: %s", ev.data)
		}
		var f voiceFrame
		if err := json.Unmarshal(ev.data, &f); err != nil {
			t.Fatalf("bad frame %s: %v", ev.data, err)
		}
		got = append(got, f.Cursor)
	}
	bus, err := srv.stateBus()
	if err != nil {
		t.Fatal(err)
	}
	sctx, scancel := context.WithTimeout(ctx, 3*time.Second)
	defer scancel()
	sub, err := bus.Subscribe(sctx, events.Cursor(got[0]-1))
	if err != nil {
		t.Fatal(err)
	}
	var want []uint64
	last := got[len(got)-1]
	for f := range sub.Frames() {
		if f.Kind == voicestream.KindState || f.Kind == "session.status" {
			want = append(want, uint64(f.Cursor))
		}
		if uint64(f.Cursor) >= last {
			break
		}
	}
	scancel()
	if len(got) != len(want) {
		t.Fatalf("client got %d frames, bus has %d\n got %v\nwant %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("frame %d: client cursor %d, bus cursor %d (missed or repeated)\n got %v\nwant %v", i, got[i], want[i], got, want)
		}
	}
	switches, phaseChanges := len(order), pi
	if switches != 10 || phaseChanges != 20 || statuses != 6 {
		t.Fatalf("script ran %d switches, %d phase changes, %d status changes; want 10, 20, 6", switches, phaseChanges, statuses)
	}
	t.Logf("SSE frames %d (cursors %d..%d), 10 switches, 20 phase changes, 6 status changes, 1 kill, 1 cut of 5 s", len(got), got[0], last)
}

func TestStateEvents_KindAndCursorValidation(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "_test"})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer srv.stopVoiceProducer()
	for _, c := range []struct {
		q    string
		code int
	}{
		{"?kind=tmux.output", http.StatusBadRequest},
		{"?kind=macapp.ask", http.StatusBadRequest},
		{"?after=abc", http.StatusBadRequest},
	} {
		resp, err := http.Get(ts.URL + "/events/state" + c.q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.code {
			t.Errorf("%s: %d, want %d", c.q, resp.StatusCode, c.code)
		}
	}
	resp, err := http.Post(ts.URL+"/events/state", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("POST /events/state answered 200")
	}
}

func TestStateEvents_RequiresToken(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "_test", Token: "s3cret-token"})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer srv.stopVoiceProducer()
	resp, err := http.Get(ts.URL + "/events/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events/state?token=s3cret-token", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("query token on a stream: %d, want 200", resp.StatusCode)
	}
}

// A resume cursor the log never reached gets a reset (with the voice
// state now) instead of a silent wait for the log to catch up.
func TestStateEvents_CursorAheadResets(t *testing.T) {
	t.Setenv("CLAUDE_VOICE_STATE_DIR", t.TempDir())
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "_test"})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer srv.stopVoiceProducer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evs := make(chan stateSSEEvent, 16)
	sseClient(t, ctx, ts.URL+"/events/state", "999999999", evs)
	var names []string
	for len(names) < 2 {
		select {
		case ev := <-evs:
			names = append(names, ev.event)
			if ev.event == "reset" && !strings.Contains(string(ev.data), `"reason":"cursor-ahead"`) {
				t.Fatalf("reset data %s", ev.data)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("events so far %v, want hello then reset", names)
		}
	}
	if names[0] != "hello" || names[1] != "reset" {
		t.Fatalf("events %v, want [hello reset]", names)
	}
}

// B1 (rework round 1): stopping the server while it is the only producer
// publishes "off" (reason stopped), so no follower keeps "listening" after
// agent-deck web stops. main's signal path calls the same stop.
func TestStateEvents_ShutdownPublishesOff(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_VOICE_STATE_DIR", dir)
	raw, _ := json.Marshal(map[string]any{"schema": "claude-code-voice.state/v1", "on": true, "mode": "listening", "phase": "listening",
		"phaseSince": time.Now().UnixMilli(), "heartbeatMs": 10000, "daemon": map[string]any{"pid": os.Getpid()},
		"target": map[string]any{"title": "alpha", "sessionId": "aaaa1111-1794950000"}, "updatedAt": time.Now().UnixMilli()})
	if err := os.WriteFile(filepath.Join(dir, "current.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	const profile = "_test_b1"
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: profile})
	w := events.OpenProfile(profile)
	_ = w.Close()
	bus, err := events.OpenReader(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sub, err := bus.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	next := func(what string, ok func(voicestream.StateEvent) bool) {
		t.Helper()
		for {
			select {
			case f, open := <-sub.Frames():
				if !open {
					t.Fatalf("%s: bus closed", what)
				}
				var ev voicestream.StateEvent
				if f.Kind == voicestream.KindState && json.Unmarshal(f.Data, &ev) == nil && ok(ev) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("%s: no such frame", what)
			}
		}
	}
	srv.startVoiceProducer()
	next("start frame", func(ev voicestream.StateEvent) bool { return ev.On })
	sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	_ = srv.Shutdown(sctx)
	next("off after Shutdown", func(ev voicestream.StateEvent) bool { return !ev.On && ev.Reason == voicestream.ReasonStopped })
}

// S9 (rework round 1): a browser EventSource reconnects with the original
// ?after= in its URL plus Last-Event-ID; the larger cursor wins, so frames
// the client already has are not replayed.
func TestStateEvents_LastEventIDBeatsAStaleAfter(t *testing.T) {
	const profile = "_test_s9"
	t.Setenv("CLAUDE_VOICE_STATE_DIR", t.TempDir())
	for i := 0; i < 5; i++ {
		events.PublishProfile(profile, "session.status", fmt.Sprintf("s%d", i), session.StatusBusEvent{From: "idle", To: "running"})
	}
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: profile})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer srv.stopVoiceProducer()
	var last uint64
	for deadline := time.Now().Add(5 * time.Second); ; {
		b, err := srv.stateBus()
		if err == nil {
			if end, _, _ := b.Ends(); end >= 5 {
				last = uint64(end)
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("frames never reached the bus")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evs := make(chan stateSSEEvent, 16)
	// The URL still says after=1; the client has seen everything up to last-1.
	sseClient(t, ctx, ts.URL+"/events/state?after=1", fmt.Sprint(last-1), evs)
	for {
		select {
		case ev := <-evs:
			if ev.event == "hello" {
				continue
			}
			if ev.id != fmt.Sprint(last) {
				t.Fatalf("first replayed frame id %s, want %d (Last-Event-ID %d beats ?after=1)", ev.id, last, last-1)
			}
			return
		case <-time.After(3 * time.Second):
			t.Fatal("no frame after resume")
		}
	}
}
