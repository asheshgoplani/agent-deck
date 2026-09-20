package session

import (
	"context"
	"errors"
	"testing"
)

func TestSupportsNudge(t *testing.T) {
	cases := []struct {
		name  string
		state RemoteVersionState
		want  bool
	}{
		{"new enough", RemoteVersionState{Found: true, Version: "1.16.17"}, true},
		{"newer than min", RemoteVersionState{Found: true, Version: "1.17.0"}, true},
		{"too old", RemoteVersionState{Found: true, Version: "1.16.15"}, false},
		{"never found", RemoteVersionState{Found: false, Version: "1.16.17"}, false},
		{"unparsable version", RemoteVersionState{Found: true, Version: "development build"}, false},
		{"empty version", RemoteVersionState{Found: true, Version: ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SupportsNudge(tc.state); got != tc.want {
				t.Fatalf("SupportsNudge(%+v) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

// fakeNudger is the test double for RemoteNudger.
type fakeNudger struct {
	nudgeErr    error
	fallbackErr error
	nudged      bool
	fellBack    bool
}

func (f *fakeNudger) NudgeCheckNow(ctx context.Context) error {
	f.nudged = true
	return f.nudgeErr
}

func (f *fakeNudger) FallbackUpdate(ctx context.Context) ([]byte, error) {
	f.fellBack = true
	return nil, f.fallbackErr
}

func TestNudgeRemotes_NewRemoteGetsNudged(t *testing.T) {
	remotes := map[string]RemoteConfig{"a": {Host: "a.example"}}
	fake := &fakeNudger{}
	results := NudgeRemotes(context.Background(), remotes, nil, NudgeRemoteOptions{
		NewRunner: func(name string, rc RemoteConfig) RemoteNudger { return fake },
		Versions:  map[string]RemoteVersionState{"a": {Found: true, Version: "1.16.17"}},
	})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if !fake.nudged || fake.fellBack {
		t.Fatalf("expected a nudge, not a fallback: nudged=%v fellBack=%v", fake.nudged, fake.fellBack)
	}
	if results[0].Outcome != NudgeOutcomeSent {
		t.Fatalf("Outcome = %v, want NudgeOutcomeSent", results[0].Outcome)
	}
}

func TestNudgeRemotes_OldRemoteGetsFallback(t *testing.T) {
	remotes := map[string]RemoteConfig{"b": {Host: "b.example"}}
	fake := &fakeNudger{}
	results := NudgeRemotes(context.Background(), remotes, nil, NudgeRemoteOptions{
		NewRunner: func(name string, rc RemoteConfig) RemoteNudger { return fake },
		Versions:  map[string]RemoteVersionState{"b": {Found: true, Version: "1.16.15"}},
	})
	if fake.nudged || !fake.fellBack {
		t.Fatalf("expected a fallback, not a nudge: nudged=%v fellBack=%v", fake.nudged, fake.fellBack)
	}
	if results[0].Outcome != NudgeOutcomeFallback {
		t.Fatalf("Outcome = %v, want NudgeOutcomeFallback", results[0].Outcome)
	}
}

func TestNudgeRemotes_UnknownRemoteGetsFallback(t *testing.T) {
	remotes := map[string]RemoteConfig{"c": {Host: "c.example"}}
	fake := &fakeNudger{}
	results := NudgeRemotes(context.Background(), remotes, nil, NudgeRemoteOptions{
		NewRunner: func(name string, rc RemoteConfig) RemoteNudger { return fake },
		Versions:  map[string]RemoteVersionState{}, // never probed
	})
	if fake.nudged || !fake.fellBack {
		t.Fatalf("a never-probed remote must get the safe fallback, not an optimistic nudge")
	}
	if results[0].Outcome != NudgeOutcomeFallback {
		t.Fatalf("Outcome = %v, want NudgeOutcomeFallback", results[0].Outcome)
	}
}

func TestNudgeRemotes_NudgeFailureIsReportedNotFatal(t *testing.T) {
	remotes := map[string]RemoteConfig{"a": {Host: "a"}, "b": {Host: "b"}}
	failing := &fakeNudger{nudgeErr: errors.New("connection refused")}
	ok := &fakeNudger{}
	results := NudgeRemotes(context.Background(), remotes, nil, NudgeRemoteOptions{
		NewRunner: func(name string, rc RemoteConfig) RemoteNudger {
			if name == "a" {
				return failing
			}
			return ok
		},
		Versions: map[string]RemoteVersionState{
			"a": {Found: true, Version: "1.16.17"},
			"b": {Found: true, Version: "1.16.17"},
		},
	})
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2 (one remote failing must not stop the rest)", len(results))
	}
	if results[0].Outcome != NudgeOutcomeFailed || results[0].Err == nil {
		t.Fatalf("results[0] = %+v, want NudgeOutcomeFailed with an error", results[0])
	}
	if results[1].Outcome != NudgeOutcomeSent {
		t.Fatalf("results[1] = %+v, want NudgeOutcomeSent (remote b must still be nudged)", results[1])
	}
}

func TestNudgeResult_String(t *testing.T) {
	cases := []struct {
		result NudgeResult
		want   string
	}{
		{NudgeResult{Name: "a", Outcome: NudgeOutcomeSent}, "a: nudged (check now)"},
		{NudgeResult{Name: "b", Outcome: NudgeOutcomeFallback}, "b: fallback pull (does not understand check-now)"},
		{NudgeResult{Name: "c", Outcome: NudgeOutcomeFailed, Err: errors.New("boom")}, "c: nudge failed: boom"},
	}
	for _, tc := range cases {
		if got := tc.result.String(); got != tc.want {
			t.Fatalf("String() = %q, want %q", got, tc.want)
		}
	}
}
