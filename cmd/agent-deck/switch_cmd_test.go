package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func switchFixture() []switchTarget {
	return []switchTarget{
		{ID: "a1b2c3d4-1700000001", Title: "api-server", Group: "work/backend", Path: "/src/api", Status: "running"},
		{ID: "a1b2ffff-1700000002", Title: "Voice Test 0955", Group: "lab", Path: "/src/voice", Status: "idle"},
		{ID: "c0ffee00-1700000003", Title: "voice test 0956", Group: "lab", Path: "/src/voice", Status: "waiting"},
		{ID: "d00d0000-1700000004", Title: "conductor-agent-deck", Group: "conductors", Path: "/src/deck", Status: "running"},
		{ID: "e1e1e1e1-1700000005", Title: "web frontend", Group: "work/frontend", Path: "/src/web", Status: "idle"},
		{ID: "f2f2f2f2-1700000006", Title: "docs", Group: "work/backend", Path: "/src/docs", Status: "stopped"},
	}
}

func candidateIDs(cs []switchCandidate) []string {
	ids := make([]string, 0, len(cs))
	for _, c := range cs {
		ids = append(ids, c.ID)
	}
	return ids
}

func TestSwitchMatchTable(t *testing.T) {
	scratch := []switchTarget{}
	for i := 1; i <= 7; i++ {
		scratch = append(scratch, switchTarget{ID: fmt.Sprintf("%08x-17000000%02d", i, i), Title: fmt.Sprintf("scratch %d", i), Status: "idle"})
	}
	dupes := append(switchFixture(), switchTarget{ID: "99999999-1700000009", Title: "API-Server", Group: "other", Status: "idle"})
	tests := []struct {
		name      string
		query     string
		targets   []switchTarget
		wantKind  string
		wantID    string
		wantCands []string
	}{
		{"exact id", "c0ffee00-1700000003", nil, switchMatchOne, "c0ffee00-1700000003", nil},
		{"unique id prefix", "d00d", nil, switchMatchOne, "d00d0000-1700000004", nil},
		{"short id prefix is not an id", "d00", nil, switchMatchNone, "", nil},
		{"shared id prefix lists both", "a1b2", nil, switchMatchAmbiguous, "", []string{"a1b2c3d4-1700000001", "a1b2ffff-1700000002"}},
		{"exact title ignores case", "API-SERVER", nil, switchMatchOne, "a1b2c3d4-1700000001", nil},
		{"same exact title twice is ambiguous", "api-server", dupes, switchMatchAmbiguous, "", []string{"a1b2c3d4-1700000001", "99999999-1700000009"}},
		{"fuzzy typo", "api servr", nil, switchMatchOne, "a1b2c3d4-1700000001", nil},
		{"fuzzy reordered words", "agent deck conductor", nil, switchMatchOne, "d00d0000-1700000004", nil},
		{"group path words count", "backend api", nil, switchMatchOne, "a1b2c3d4-1700000001", nil},
		{"number must match exactly", "voice 0956", nil, switchMatchOne, "c0ffee00-1700000003", nil},
		{"near number is no match", "voice 0957", nil, switchMatchNone, "", nil},
		{"close scores are ambiguous, live first", "voice test", nil, switchMatchAmbiguous, "", []string{"c0ffee00-1700000003", "a1b2ffff-1700000002"}},
		{"ambiguous lists at most five", "scratch", scratch, switchMatchAmbiguous, "", []string{"00000001-1700000001", "00000002-1700000002", "00000003-1700000003", "00000004-1700000004", "00000005-1700000005"}},
		{"none", "kubernetes", nil, switchMatchNone, "", nil},
		{"empty query", "  ", nil, switchMatchNone, "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			targets := tc.targets
			if targets == nil {
				targets = switchFixture()
			}
			got := matchSwitchTarget(tc.query, targets)
			if got.Kind != tc.wantKind {
				t.Fatalf("kind = %q, want %q (%+v)", got.Kind, tc.wantKind, got)
			}
			if tc.wantID != "" && got.Target.ID != tc.wantID {
				t.Fatalf("target = %q (%s), want %q", got.Target.ID, got.Target.Title, tc.wantID)
			}
			if tc.wantCands != nil && !reflect.DeepEqual(candidateIDs(got.Candidates), tc.wantCands) {
				t.Fatalf("candidates = %v, want %v", candidateIDs(got.Candidates), tc.wantCands)
			}
		})
	}
}

func TestSwitchMatchAmbiguousScores(t *testing.T) {
	got := matchSwitchTarget("voice test", switchFixture())
	if len(got.Candidates) != 2 || got.Candidates[0].Score != 0.92 || got.Candidates[1].Score != 0.9 {
		t.Fatalf("candidates = %+v, want scores 0.92 (live) then 0.9", got.Candidates)
	}
}

func TestSwitchTargetsExcludeArchived(t *testing.T) {
	instances := []*session.InstanceData{
		{ID: "11111111-1", Title: "old work", GroupPath: "g", ProjectPath: "/p", Status: session.StatusStopped, ArchivedAt: time.Unix(1700000000, 0)},
		{ID: "22222222-2", Title: "new work", GroupPath: "g", ProjectPath: "/q", Status: session.StatusRunning},
		nil,
	}
	targets := switchTargets(instances)
	want := []switchTarget{{ID: "22222222-2", Title: "new work", Group: "g", Path: "/q", Status: "running"}}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %+v, want %+v", targets, want)
	}
	if got := matchSwitchTarget("old work", targets); got.Kind != switchMatchNone {
		t.Fatalf("archived session matched: %+v", got)
	}
	if got := matchSwitchTarget("11111111-1", targets); got.Kind != switchMatchNone {
		t.Fatalf("archived session matched by id: %+v", got)
	}
}

func TestSwitchTitleScoreMatchesVoice(t *testing.T) {
	// Values of the voice daemon's titleScore for the same inputs.
	for _, tc := range []struct {
		q, title string
		want     float64
	}{
		{"api server", "api-server", 1},
		{"the api session", "api", 1},
		{"voice", "voice test", 0.9},
		{"", "voice", 0},
	} {
		if got := switchTitleScore(tc.q, tc.title); got != tc.want {
			t.Errorf("switchTitleScore(%q, %q) = %v, want %v", tc.q, tc.title, got, tc.want)
		}
	}
	if levenshtein("कल", "कल") != 0 || levenshtein("claude", "cloud") != 2 {
		t.Fatal("levenshtein by runes")
	}
}

type fakeSwitchDeps struct {
	openErr    error
	focused    bool
	publishErr error
	attachErr  error
	canAttach  bool
	localMac   bool
	calls      []string
}

func (f *fakeSwitchDeps) delivery() switchDelivery {
	return switchDelivery{
		localMac: f.localMac,
		openApp: func(id string) error {
			f.calls = append(f.calls, "open "+macappFocusLink(id))
			return f.openErr
		},
		publish: func(id string, wait time.Duration) (bool, error) {
			f.calls = append(f.calls, fmt.Sprintf("publish %s %s", id, wait))
			return f.focused, f.publishErr
		},
		canAttach: f.canAttach,
		attach: func(id string) error {
			f.calls = append(f.calls, "attach "+id)
			return f.attachErr
		},
	}
}

func TestSwitchDeliveryDecision(t *testing.T) {
	const id = "abc-1"
	open := "open agentdeck://focus?session=abc-1"
	pub := "publish abc-1 2s"
	boom := errors.New("boom")
	tests := []struct {
		name       string
		deps       fakeSwitchDeps
		wantStatus string
		wantVia    string
		wantErr    bool
		wantCalls  []string
	}{
		{"mac: url scheme", fakeSwitchDeps{localMac: true, canAttach: true}, switchStatusSwitched, switchViaApp, false, []string{open}},
		{"mac: open fails, app acks", fakeSwitchDeps{localMac: true, openErr: boom, focused: true}, switchStatusSwitched, switchViaApp, false, []string{open, pub}},
		{"box: app acks", fakeSwitchDeps{focused: true, canAttach: true}, switchStatusSwitched, switchViaApp, false, []string{pub}},
		{"box: no ack, terminal", fakeSwitchDeps{canAttach: true}, switchStatusAttached, switchViaTerminal, false, []string{pub, "attach abc-1"}},
		{"box: no ack, no terminal", fakeSwitchDeps{}, switchStatusQueued, switchViaNone, false, []string{pub}},
		{"mac: open fails, no ack, no terminal", fakeSwitchDeps{localMac: true, openErr: boom}, switchStatusQueued, switchViaNone, false, []string{open, pub}},
		{"bus down, terminal", fakeSwitchDeps{publishErr: boom, canAttach: true}, switchStatusAttached, switchViaTerminal, false, []string{pub, "attach abc-1"}},
		{"bus down, no terminal", fakeSwitchDeps{publishErr: boom}, "", "", true, []string{pub}},
		{"attach fails", fakeSwitchDeps{canAttach: true, attachErr: boom}, "", "", true, []string{pub, "attach abc-1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.deps
			status, via, err := deliverSwitch(f.delivery(), id, 2*time.Second)
			if (err != nil) != tc.wantErr || status != tc.wantStatus || via != tc.wantVia {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, err=%v)", status, via, err, tc.wantStatus, tc.wantVia, tc.wantErr)
			}
			if !reflect.DeepEqual(f.calls, tc.wantCalls) {
				t.Fatalf("calls = %q, want %q", f.calls, tc.wantCalls)
			}
		})
	}
}

func TestSwitchCanAttach(t *testing.T) {
	probed := false
	tty := func() bool { probed = true; return true }
	if switchCanAttach(true, false, tty) || probed {
		t.Fatal("--json must never attach or probe the terminal")
	}
	if switchCanAttach(false, true, tty) {
		t.Fatal("--no-attach attached")
	}
	if !switchCanAttach(false, false, tty) || switchCanAttach(false, false, func() bool { return false }) {
		t.Fatal("attach follows the terminal check")
	}
}

func TestMacappFocusLink(t *testing.T) {
	if got := macappFocusLink("abc-1"); got != "agentdeck://focus?session=abc-1" {
		t.Fatalf("link = %q", got)
	}
	if got := macappFocusLink("-a b&c=d"); got != "agentdeck://focus?session=-a+b%26c%3Dd" {
		t.Fatalf("link = %q", got)
	}
}

func TestSwitchAcknowledged(t *testing.T) {
	frame := func(kind, data string) events.Frame {
		return events.Frame{Kind: kind, SessionID: "abc-1", Data: json.RawMessage(data)}
	}
	for _, tc := range []struct {
		name             string
		f                events.Frame
		matched, focused bool
	}{
		{"focused", frame(events.KindMacappSwitchAck, `{"request_id":"r1","focused":true}`), true, true},
		{"expired", frame(events.KindMacappSwitchAck, `{"request_id":"r1","focused":false,"error":"expired"}`), true, false},
		{"other request", frame(events.KindMacappSwitchAck, `{"request_id":"r2","focused":true}`), false, false},
		{"other kind", frame(events.KindMacappOpenAck, `{"request_id":"r1","focused":true}`), false, false},
		{"bad json", frame(events.KindMacappSwitchAck, `{`), false, false},
	} {
		if m, f := switchAcknowledged(tc.f, "r1"); m != tc.matched || f != tc.focused {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", tc.name, m, f, tc.matched, tc.focused)
		}
	}
}

func runSwitchForTest(t *testing.T, deps *fakeSwitchDeps, args ...string) (int, map[string]any, string) {
	t.Helper()
	tick := time.Unix(1700000000, 0)
	env := switchEnv{
		load: func() ([]switchTarget, error) { return switchFixture(), nil },
		delivery: func(jsonOut, noAttach bool) switchDelivery {
			d := deps.delivery()
			d.canAttach = deps.canAttach && !jsonOut && !noAttach
			return d
		},
		now: func() time.Time { tick = tick.Add(7 * time.Millisecond); return tick },
	}
	var out, errOut bytes.Buffer
	code := runSwitch(args, &out, &errOut, env)
	var got map[string]any
	if strings.Contains(strings.Join(args, " "), "--json") {
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("output is not one JSON object: %v\n%s", err, out.String())
		}
	}
	return code, got, out.String() + errOut.String()
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestSwitchJSONShape(t *testing.T) {
	t.Run("switched", func(t *testing.T) {
		code, got, _ := runSwitchForTest(t, &fakeSwitchDeps{focused: true}, "api servr", "--json")
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if keys := sortedKeys(got); !reflect.DeepEqual(keys, []string{"elapsed_ms", "query", "session", "status", "via"}) {
			t.Fatalf("keys = %v", keys)
		}
		if got["status"] != "switched" || got["via"] != "app" || got["query"] != "api servr" || got["elapsed_ms"] != float64(7) {
			t.Fatalf("result = %v", got)
		}
		want := map[string]any{"id": "a1b2c3d4-1700000001", "title": "api-server", "group": "work/backend", "path": "/src/api", "status": "running"}
		if !reflect.DeepEqual(got["session"], want) {
			t.Fatalf("session = %v, want %v", got["session"], want)
		}
	})
	t.Run("queued never attaches under --json", func(t *testing.T) {
		deps := &fakeSwitchDeps{canAttach: true}
		code, got, _ := runSwitchForTest(t, deps, "--json", "docs")
		if code != 0 || got["status"] != "queued" || got["via"] != "none" {
			t.Fatalf("exit %d result %v", code, got)
		}
		for _, c := range deps.calls {
			if strings.HasPrefix(c, "attach") {
				t.Fatalf("attached under --json: %v", deps.calls)
			}
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		code, got, _ := runSwitchForTest(t, &fakeSwitchDeps{}, "voice test", "--json")
		if code != switchExitAmbiguous {
			t.Fatalf("exit %d", code)
		}
		if keys := sortedKeys(got); !reflect.DeepEqual(keys, []string{"candidates", "elapsed_ms", "query", "status", "via"}) {
			t.Fatalf("keys = %v", keys)
		}
		cands := got["candidates"].([]any)
		first := cands[0].(map[string]any)
		if len(cands) != 2 || got["status"] != "ambiguous" || got["via"] != "none" {
			t.Fatalf("result = %v", got)
		}
		if keys := sortedKeys(first); !reflect.DeepEqual(keys, []string{"group", "id", "score", "status", "title"}) {
			t.Fatalf("candidate keys = %v", keys)
		}
	})
	t.Run("not found", func(t *testing.T) {
		code, got, _ := runSwitchForTest(t, &fakeSwitchDeps{}, "kubernetes", "--json")
		if code != switchExitNotFound {
			t.Fatalf("exit %d", code)
		}
		if keys := sortedKeys(got); !reflect.DeepEqual(keys, []string{"elapsed_ms", "query", "status", "via"}) || got["status"] != "not_found" {
			t.Fatalf("result = %v", got)
		}
	})
}

func TestSwitchTextOutput(t *testing.T) {
	code, _, text := runSwitchForTest(t, &fakeSwitchDeps{}, "voice test")
	if code != switchExitAmbiguous || !strings.Contains(text, "which one?") || !strings.Contains(text, "Voice Test 0955 (lab)") {
		t.Fatalf("exit %d text %q", code, text)
	}
	code, _, text = runSwitchForTest(t, &fakeSwitchDeps{canAttach: true}, "docs")
	if code != 0 || !strings.Contains(text, "attaching docs here") {
		t.Fatalf("exit %d text %q", code, text)
	}
	code, _, text = runSwitchForTest(t, &fakeSwitchDeps{canAttach: true}, "--no-attach", "docs")
	if code != 0 || !strings.Contains(text, "Queued for AgentDeck") || !strings.Contains(text, "session attach f2f2f2f2-1700000006") {
		t.Fatalf("exit %d text %q", code, text)
	}
	if code, _, _ = runSwitchForTest(t, &fakeSwitchDeps{}, "docs", "--wait", "3m"); code != 1 {
		t.Fatalf("--wait 3m exit %d", code)
	}
}
