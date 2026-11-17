package voicestate

import (
	"testing"
	"time"
)

func TestMapPhase(t *testing.T) {
	cases := map[string]string{
		"listening": PhaseListening, "hearing": PhaseHearing, "transcribing": PhaseHearing,
		"thinking": PhaseThinking, "speaking": PhaseSpeaking, "resting": PhaseResting,
		"off": PhaseOff, "muted": PhaseResting, "paused": PhaseResting, "meeting": PhaseResting,
		"": PhaseOff, "bogus": PhaseResting,
	}
	for in, want := range cases {
		if got := MapPhase(in); got != want {
			t.Errorf("MapPhase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPhaseWord(t *testing.T) {
	var nilState *State
	if got := nilState.PhaseWord(); got != PhaseOff {
		t.Errorf("nil = %q", got)
	}
	if got := (&State{On: false, Phase: "listening"}).PhaseWord(); got != PhaseOff {
		t.Errorf("not on = %q", got)
	}
	if got := (&State{On: true, Phase: "transcribing"}).PhaseWord(); got != PhaseHearing {
		t.Errorf("transcribing = %q", got)
	}
	// older daemon without phase: derived from mode
	if got := (&State{On: true, Mode: "pending"}).PhaseWord(); got != PhaseThinking {
		t.Errorf("mode pending = %q", got)
	}
	if got := (&State{On: true, Mode: "hibernate"}).PhaseWord(); got != PhaseResting {
		t.Errorf("hibernate = %q", got)
	}
}

func TestNewViewOff(t *testing.T) {
	now := time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC)
	v := NewView(nil, now)
	if v.On || v.Phase != PhaseOff || v.From != "none" || v.Target != nil || v.Source != "" {
		t.Fatalf("off view = %+v", v)
	}
	if v.UpdatedAt != "2026-11-17T23:00:00Z" {
		t.Errorf("updatedAt = %q", v.UpdatedAt)
	}
}

func TestNewViewLiveLocalAndMirror(t *testing.T) {
	now := time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC)
	since := now.Add(-5 * time.Second)
	s := &State{On: true, Phase: "thinking", PhaseSince: since.UnixMilli(), Muted: true,
		UpdatedAt: now.UnixMilli(), Source: SourceCurrent,
		Target: Target{Title: "api", SessionID: "abc", Profile: "work", Remote: "box1"}}
	v := NewView(s, now)
	if !v.On || v.Phase != PhaseThinking || !v.Muted || v.From != "local" {
		t.Fatalf("view = %+v", v)
	}
	if v.PhaseSince != since.UTC().Format(time.RFC3339) || v.UpdatedAt != now.UTC().Format(time.RFC3339) {
		t.Errorf("times = %q %q", v.PhaseSince, v.UpdatedAt)
	}
	if v.Target == nil || *v.Target != (ViewTarget{ID: "abc", Title: "api", Host: "box1", Profile: "work"}) {
		t.Errorf("target = %+v", v.Target)
	}
	s.Source = SourceMirror
	if got := NewView(s, now).From; got != "mirror" {
		t.Errorf("from = %q", got)
	}
	s.Target = Target{}
	if NewView(s, now).Target != nil {
		t.Errorf("empty target must be null")
	}
}
