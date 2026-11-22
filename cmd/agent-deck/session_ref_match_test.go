package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// One way to name a session works on every command: the full id, the title
// (also while the voice daemon shows its microphone marker in front of it,
// and whatever its case) or a short id prefix.

const micMarker = "🎙 "

func refInstances() []*session.Instance {
	return []*session.Instance{
		{ID: "7c15fc21-1700000001", Title: micMarker + "voice-test", ProjectPath: "/work/voice"},
		{ID: "9a0b33d4-1700000002", Title: "Germany Immigration", ProjectPath: "/work/germany"},
		{ID: "9a0b77e5-1700000003", Title: "phone build", ProjectPath: "/work/phone"},
		{ID: "e4d1c0aa-1700000004", Title: "delta", ProjectPath: "/work/delta-lower"},
		{ID: "f5e2d1bb-1700000005", Title: "Delta", ProjectPath: "/work/delta-upper"},
	}
}

func refSwitchTargets(instances []*session.Instance) []switchTarget {
	targets := make([]switchTarget, 0, len(instances))
	for _, inst := range instances {
		targets = append(targets, switchTarget{ID: inst.ID, Title: inst.Title, Path: inst.ProjectPath})
	}
	return targets
}

// TestResolveSession_TitleWithMicMarkerStillResolvesByPlainTitle: while the
// owner talks to a session its title is shown as "🎙 <title>"; scripts and
// conductors that hold the plain title must still reach it.
func TestResolveSession_TitleWithMicMarkerStillResolvesByPlainTitle(t *testing.T) {
	for _, ref := range []string{"voice-test", "Voice-Test", micMarker + "voice-test", "🎙voice-test"} {
		inst, errMsg, _ := ResolveSession(ref, refInstances())
		if inst == nil || inst.ID != "7c15fc21-1700000001" {
			t.Errorf("ResolveSession(%q) = %v (%s), want the session titled %q", ref, inst, errMsg, micMarker+"voice-test")
		}
	}
}

// A caller that still holds the decorated title after the marker is gone
// reaches the session too.
func TestResolveSession_DecoratedRefFindsPlainTitle(t *testing.T) {
	inst, errMsg, _ := ResolveSession(micMarker+"phone build", refInstances())
	if inst == nil || inst.ID != "9a0b77e5-1700000003" {
		t.Fatalf("ResolveSession(decorated ref) = %v (%s), want phone build", inst, errMsg)
	}
}

func TestResolveSession_TitleIgnoresCaseAndShortIDPrefixWorks(t *testing.T) {
	for ref, wantID := range map[string]string{
		"germany immigration": "9a0b33d4-1700000002",
		"Phone Build":         "9a0b77e5-1700000003",
		"7c15":                "7c15fc21-1700000001",
		"7c15fc":              "7c15fc21-1700000001",
		"9a0b33d4-1700000002": "9a0b33d4-1700000002",
	} {
		inst, errMsg, _ := ResolveSession(ref, refInstances())
		if inst == nil || inst.ID != wantID {
			t.Errorf("ResolveSession(%q) = %v (%s), want %s", ref, inst, errMsg, wantID)
		}
	}
}

// The stored spelling wins over the case-insensitive pass, so a deck that
// holds both "delta" and "Delta" keeps both addressable; a spelling neither
// has is a tie that names both.
func TestResolveSession_ExactTitleWinsOverCaseFold(t *testing.T) {
	instances := refInstances()
	for ref, wantID := range map[string]string{"delta": "e4d1c0aa-1700000004", "Delta": "f5e2d1bb-1700000005"} {
		inst, errMsg, _ := ResolveSession(ref, instances)
		if inst == nil || inst.ID != wantID {
			t.Errorf("ResolveSession(%q) = %v (%s), want %s", ref, inst, errMsg, wantID)
		}
	}
	inst, errMsg, code := ResolveSession("DELTA", instances)
	if inst != nil || code != ErrCodeAmbiguous {
		t.Fatalf("ResolveSession(DELTA) = %v code %q, want AMBIGUOUS", inst, code)
	}
	for _, want := range []string{"title 'DELTA' is held by multiple sessions", "e4d1c0aa-170", "f5e2d1bb-170", "/work/delta-lower"} {
		if !strings.Contains(errMsg, want) {
			t.Errorf("ambiguity message lacks %q:\n%s", want, errMsg)
		}
	}
}

func TestResolveSession_SharedIDPrefixIsAmbiguousAndShortRefIsNotAPrefix(t *testing.T) {
	inst, errMsg, code := ResolveSession("9a0b", refInstances())
	if inst != nil || code != ErrCodeAmbiguous || !strings.Contains(errMsg, "'9a0b' matches multiple sessions") {
		t.Fatalf("ResolveSession(9a0b) = %v code %q msg %q, want AMBIGUOUS naming both", inst, code, errMsg)
	}
	// Three characters are too few to name a session by id.
	inst, errMsg, code = ResolveSession("7c1", refInstances())
	if inst != nil || code != ErrCodeNotFound || errMsg != "session '7c1' not found" {
		t.Fatalf("ResolveSession(7c1) = %v code %q msg %q, want NOT_FOUND", inst, code, errMsg)
	}
}

// A full id is never shadowed by another session that carries it as title.
func TestResolveSession_FullIDWinsOverATitleSpelledLikeIt(t *testing.T) {
	instances := append(refInstances(), &session.Instance{ID: "0001aaaa-1700000009", Title: "9a0b33d4-1700000002"})
	inst, errMsg, _ := ResolveSession("9a0b33d4-1700000002", instances)
	if inst == nil || inst.ID != "9a0b33d4-1700000002" {
		t.Fatalf("ResolveSession(full id) = %v (%s), want the session with that id", inst, errMsg)
	}
}

// "conductor-<name>" keeps naming the conductor (heartbeat script, bridge)
// when another session's title only looks like it after folding.
func TestResolveSession_FoldedTitleDoesNotTakeAConductorsName(t *testing.T) {
	conductor := &session.Instance{ID: "cccccccccccc3333", Title: "my assistant", IsConductor: true, ConductorName: "ops"}
	worker := &session.Instance{ID: "dddddddddddd4444", Title: "Conductor-ops"}
	decorated := &session.Instance{ID: "eeeeeeeeeeee5555", Title: micMarker + "conductor-ops"}
	instances := []*session.Instance{worker, decorated, conductor}
	if got, errMsg, _ := ResolveSession("conductor-ops", instances); got != conductor {
		t.Fatalf("ResolveSession(conductor-ops) = %v (%s), want the conductor", got, errMsg)
	}
	// The stored spelling still reaches the worker.
	if got, errMsg, _ := ResolveSession("Conductor-ops", instances); got != worker {
		t.Fatalf("ResolveSession(Conductor-ops) = %v (%s), want the worker with that title", got, errMsg)
	}
}

// TestSwitchNamesSessionsLikeEveryOtherCommand: `switch` and the `session`
// verbs give the same answer for an id, an id prefix and a title; fuzzy
// search stays an extra of switch.
func TestSwitchNamesSessionsLikeEveryOtherCommand(t *testing.T) {
	instances := refInstances()
	targets := refSwitchTargets(instances)
	for _, ref := range []string{
		"voice-test", "Voice-Test", micMarker + "voice-test", "germany immigration", "Phone Build",
		"7c15", "7c15fc", "9a0b33d4-1700000002",
	} {
		inst, errMsg, _ := ResolveSession(ref, instances)
		if inst == nil {
			t.Errorf("ResolveSession(%q): %s", ref, errMsg)
			continue
		}
		m := matchSwitchTarget(ref, targets)
		if m.Kind != switchMatchOne || m.Target.ID != inst.ID {
			t.Errorf("switch %q = %s %q, session verbs resolve it to %q", ref, m.Kind, m.Target.ID, inst.ID)
		}
	}
	// Both report the same tie.
	m := matchSwitchTarget("DELTA", targets)
	if _, _, code := ResolveSession("DELTA", instances); code != ErrCodeAmbiguous || m.Kind != switchMatchAmbiguous || len(m.Candidates) != 2 {
		t.Errorf("DELTA: session verbs code %q, switch %s with %d candidates; want ambiguous on both", code, m.Kind, len(m.Candidates))
	}
	// A title that reads like another session's id prefix goes to the same
	// session on both: the stored title first, the id prefix before a title
	// that only matches after folding.
	lookalikes := append(refInstances(),
		&session.Instance{ID: "0001aaaa-1700000008", Title: "7c15"},
		&session.Instance{ID: "0002bbbb-1700000009", Title: "9A0B3"})
	for ref, wantID := range map[string]string{"7c15": "0001aaaa-1700000008", "9a0b3": "9a0b33d4-1700000002", "9A0B3": "0002bbbb-1700000009"} {
		inst, _, _ := ResolveSession(ref, lookalikes)
		m := matchSwitchTarget(ref, refSwitchTargets(lookalikes))
		if inst == nil || inst.ID != wantID || m.Kind != switchMatchOne || m.Target.ID != wantID {
			t.Errorf("%q: session verbs -> %v, switch -> %s %q; want %s on both", ref, inst, m.Kind, m.Target.ID, wantID)
		}
	}
	// The one difference: a switch query may be spoken, so letter case never
	// settles it; two titles that differ only in case stay a tie there.
	for _, ref := range []string{"delta", "Delta"} {
		if m := matchSwitchTarget(ref, targets); m.Kind != switchMatchAmbiguous || len(m.Candidates) != 2 {
			t.Errorf("switch %q = %s with %d candidates, want ambiguous between delta and Delta", ref, m.Kind, len(m.Candidates))
		}
	}
	// Fuzzy is switch only.
	if m := matchSwitchTarget("germany", targets); m.Kind != switchMatchOne || m.Target.ID != "9a0b33d4-1700000002" {
		t.Errorf("switch germany = %s %q, want the fuzzy match", m.Kind, m.Target.ID)
	}
	if inst, _, code := ResolveSession("germany", instances); inst != nil || code != ErrCodeNotFound {
		t.Errorf("ResolveSession(germany) = %v code %q, want NOT_FOUND (no fuzzy outside switch)", inst, code)
	}
}

// TestSessionRefCLI_OneNameOnEveryCommand drives the binary: the same name
// reaches the session through show, open, switch and focus, and a name that
// matches nothing gets the same words and exit code from each.
func TestSessionRefCLI_OneNameOnEveryCommand(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	stdout, stderr, code := runAgentDeck(t, home, "add", project, "--title", "voice-test", "-c", "bash", "--no-parent", "--json")
	if code != 0 {
		t.Fatalf("add: %d %s %s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("add result: %s %v", stdout, err)
	}
	// What the voice daemon does while the microphone is on this session.
	if stdout, stderr, code = runAgentDeck(t, home, "session", "set", added.ID, "title", micMarker+"voice-test"); code != 0 {
		t.Fatalf("set title: %d %s %s", code, stdout, stderr)
	}
	// SSH also ensures no GUI can be launched if this test runs on a Mac runner.
	env := []string{"SSH_CONNECTION=test"}
	prefix := added.ID[:4]

	for _, ref := range []string{"voice-test", "Voice-Test", micMarker + "voice-test", prefix, added.ID} {
		stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "session", "show", "--json", ref)
		var shown struct {
			ID string `json:"id"`
		}
		if code != 0 || json.Unmarshal([]byte(stdout), &shown) != nil || shown.ID != added.ID {
			t.Errorf("session show %q: exit %d %s %s", ref, code, stdout, stderr)
		}
		stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "open", "https://example.com", "--session", ref, "--wait", "0s")
		if code != 0 || strings.TrimSpace(stdout) != openQueuedMessage {
			t.Errorf("open --session %q: exit %d %s %s", ref, code, stdout, stderr)
		}
		stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "switch", "--json", "--no-attach", "--wait", "0s", ref)
		var switched struct {
			Session struct {
				ID string `json:"id"`
			} `json:"session"`
		}
		if code != 0 || json.Unmarshal([]byte(stdout), &switched) != nil || switched.Session.ID != added.ID {
			t.Errorf("switch %q: exit %d %s %s", ref, code, stdout, stderr)
		}
		if stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "session", "focus", ref); code != 0 {
			t.Errorf("session focus %q: exit %d %s %s", ref, code, stdout, stderr)
		}
	}

	const want = "session 'nope' not found"
	for _, args := range [][]string{
		{"session", "show", "nope"},
		{"session", "send", "nope", "hi"},
		{"session", "send-keys", "nope", "--enter"},
		{"session", "focus", "nope"},
		{"open", "https://example.com", "--session", "nope"},
		{"rename", "nope", "other"},
		{"remove", "nope"},
	} {
		stdout, stderr, code = runAgentDeckEnv(t, home, "", env, args...)
		if code != 2 || !strings.Contains(stdout+stderr, want) {
			t.Errorf("%s: exit %d, want 2 with %q\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, want, stdout, stderr)
		}
	}
	stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "session", "focus", "--json", "nope")
	if code != 2 || !strings.Contains(stdout, `"code": "NOT_FOUND"`) || !strings.Contains(stdout, want) {
		t.Errorf("session focus --json nope: exit %d %s %s", code, stdout, stderr)
	}
}
