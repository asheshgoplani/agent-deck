package main

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A title matched ignoring case is matched as strings.EqualFold matches it,
// not by lowercasing both sides: lowercasing maps "Σ" to "σ" but leaves the
// final form "ς" alone, and leaves "ſ" (long s) apart from "s", while
// EqualFold treats each set as one letter. The pre-#3193 switch matcher used
// EqualFold, so these names resolved before the shared rule.

func foldInstances() []*session.Instance {
	return []*session.Instance{
		{ID: "a1a1a1a1-1700000001", Title: "ΟΔΟΣ", ProjectPath: "/work/odos"},
		{ID: "b2b2b2b2-1700000002", Title: micMarker + "ſync worker", ProjectPath: "/work/sync"},
		{ID: "c3c3c3c3-1700000003", Title: "phone build", ProjectPath: "/work/phone"},
	}
}

func TestMatchSessionRef_TitleFoldMatchesLikeEqualFold(t *testing.T) {
	for ref, wantID := range map[string]string{
		"οδος":                    "a1a1a1a1-1700000001", // final sigma against capital sigma
		"οδοσ":                    "a1a1a1a1-1700000001",
		"  ΟΔΟς ":                 "a1a1a1a1-1700000001",
		"sync worker":             "b2b2b2b2-1700000002", // "s" against a decorated "ſ"
		"SYNC WORKER":             "b2b2b2b2-1700000002",
		micMarker + "Sync Worker": "b2b2b2b2-1700000002",
	} {
		inst, errMsg, _ := ResolveSession(ref, foldInstances())
		if inst == nil || inst.ID != wantID {
			t.Errorf("ResolveSession(%q) = %v (%s), want %s", ref, inst, errMsg, wantID)
		}
		m := matchSwitchTarget(ref, refSwitchTargets(foldInstances()))
		if m.Kind != switchMatchOne || m.Target.ID != wantID {
			t.Errorf("switch %q = %s %q, want %s", ref, m.Kind, m.Target.ID, wantID)
		}
	}
	// The reverse direction: a stored final sigma, a capital ref.
	instances := []*session.Instance{{ID: "d4d4d4d4-1700000004", Title: "οδος"}}
	if inst, errMsg, _ := ResolveSession("ΟΔΟΣ", instances); inst == nil {
		t.Errorf("ResolveSession(ΟΔΟΣ) against title οδος: %s", errMsg)
	}
	// Letters that only lowercase alike but are not one letter to EqualFold
	// stay apart: dotted capital I is not "i".
	if inst, _, _ := ResolveSession("i", []*session.Instance{{ID: "e5e5e5e5-1700000005", Title: "İ"}}); inst != nil {
		t.Errorf("ResolveSession(i) matched the title İ; EqualFold keeps them apart")
	}
}

// The switch-only tie (a stored-spelling hit while another title differs
// from it only in case) uses the same comparison.
func TestSwitchCaseOnlyTieUsesEqualFold(t *testing.T) {
	targets := []switchTarget{
		{ID: "a1a1a1a1-1700000001", Title: "οδος"},
		{ID: "b2b2b2b2-1700000002", Title: "ΟΔΟΣ"},
	}
	for _, ref := range []string{"οδος", "ΟΔΟΣ"} {
		if m := matchSwitchTarget(ref, targets); m.Kind != switchMatchAmbiguous || len(m.Candidates) != 2 {
			t.Errorf("switch %q = %s with %d candidates, want ambiguous between οδος and ΟΔΟΣ", ref, m.Kind, len(m.Candidates))
		}
	}
}

// The fuzzy pass of switch compares words in the same folded form, so
// "sync" reads the same as "ſync" there too.
func TestSwitchFuzzyWordsFoldLikeEqualFold(t *testing.T) {
	if got := switchTitleScore("sync", "ſync"); got != 1 {
		t.Errorf("switchTitleScore(sync, ſync) = %v, want 1", got)
	}
	if got := switchTitleScore("SYNC", "sync"); got != 1 {
		t.Errorf("switchTitleScore(SYNC, sync) = %v, want 1", got)
	}
	if got := switchTitleScore("i", "İ"); got == 1 {
		t.Errorf("switchTitleScore(i, İ) = 1, want them apart as EqualFold keeps them")
	}
}

// foldSessionRef is a key for strings.EqualFold: two runes get the same key
// exactly when EqualFold calls them equal.
func TestFoldSessionRefAgreesWithEqualFold(t *testing.T) {
	runes := []rune("aAzZsSſkKKiIİıσςΣµμΜßẞåÅÅ09-🎙")
	for _, a := range runes {
		for _, b := range runes {
			x, y := string(a), string(b)
			if got, want := foldSessionRef(x) == foldSessionRef(y), strings.EqualFold(x, y); got != want {
				t.Errorf("foldSessionRef(%q) == foldSessionRef(%q) is %v, EqualFold is %v", x, y, got, want)
			}
		}
	}
	if got := foldSessionRef("The Session"); got != "the session" {
		t.Errorf("foldSessionRef(The Session) = %q, want ASCII lowercased", got)
	}
}
