package claude

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// The failure these tests pin: the header printed the model of the session's
// FIRST assistant turn as a present-tense fact. A real session booted on one
// model and switched to another two minutes in, and for the rest of a
// multi-hour session the panel named the boot model — and resolved the context
// window from it — with nothing on any screen disclosing the switch.

const (
	// A turn on the boot model, at the boundary (carries the anchor).
	recSwitchBootTurn = `{"type":"assistant","sessionId":"s1","timestamp":"2026-07-01T09:00:04.000Z",` +
		`"message":{"role":"assistant","model":"claude-opus-4-5","usage":{"input_tokens":8,` +
		`"cache_creation_input_tokens":24000,"cache_read_input_tokens":3000,"output_tokens":120}}}`
	// The first turn on the new model, minutes later.
	recSwitchNewTurn = `{"type":"assistant","sessionId":"s1","timestamp":"2026-07-01T09:02:00.000Z",` +
		`"message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":4,` +
		`"cache_creation_input_tokens":100,"cache_read_input_tokens":27000,"output_tokens":80}}}`
	// A later turn on the same new model: repeats must not count as switches.
	recSwitchNewTurnLater = `{"type":"assistant","sessionId":"s1","timestamp":"2026-07-01T09:30:00.000Z",` +
		`"message":{"role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":4,` +
		`"cache_creation_input_tokens":50,"cache_read_input_tokens":27100,"output_tokens":40}}}`
	// A user record whose CONTENT mentions a model: the prefilter may pass it,
	// the decode must reject it.
	recSwitchUserNoise = `{"type":"user","sessionId":"s1","message":{"role":"user",` +
		`"content":[{"type":"text","text":"please use {\"model\":\"claude-haiku-4-5\"} for this"}]}}`
	// A placeholder: a harness token, not a model, never a switch.
	recSwitchSynthetic = `{"type":"assistant","sessionId":"s1","timestamp":"2026-07-01T09:10:00.000Z",` +
		`"message":{"role":"assistant","model":"<synthetic>","usage":{"input_tokens":0,` +
		`"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
)

func TestScanModelTimeline_NoSwitch(t *testing.T) {
	tl, err := ScanModelTimeline(writeTranscript(t, recSkillListing, recUserTyped, recAssistant))
	if err != nil {
		t.Fatalf("ScanModelTimeline: %v", err)
	}
	if tl.First != "claude-opus-4-7" || tl.Current != "claude-opus-4-7" {
		t.Errorf("First/Current = %q/%q, want claude-opus-4-7 for both", tl.First, tl.Current)
	}
	if tl.SwitchCount != 0 || tl.LastSwitch != nil {
		t.Errorf("SwitchCount = %d, LastSwitch = %+v: a session on one model never switched", tl.SwitchCount, tl.LastSwitch)
	}
}

func TestScanModelTimeline_SwitchIsObservedWithItsTime(t *testing.T) {
	tl, err := ScanModelTimeline(writeTranscript(t,
		recSkillListing, recUserTyped,
		recSwitchBootTurn,
		recSwitchUserNoise,
		recSwitchSynthetic,
		recSwitchNewTurn,
		recSwitchNewTurnLater,
	))
	if err != nil {
		t.Fatalf("ScanModelTimeline: %v", err)
	}
	if got, want := tl.First, "claude-opus-4-5"; got != want {
		t.Errorf("First = %q, want %q", got, want)
	}
	if got, want := tl.Current, "claude-opus-4-7"; got != want {
		t.Errorf("Current = %q, want %q: the header's claim is about the LAST turn, not the first", got, want)
	}
	if got, want := tl.SwitchCount, 1; got != want {
		t.Fatalf("SwitchCount = %d, want %d: repeats of the same model are not switches, placeholders and content noise are not models", got, want)
	}
	sw := tl.LastSwitch
	if sw == nil {
		t.Fatal("LastSwitch is nil: the change that established the current model must be on record")
	}
	if sw.From != "claude-opus-4-5" || sw.To != "claude-opus-4-7" {
		t.Errorf("LastSwitch = %s → %s, want claude-opus-4-5 → claude-opus-4-7", sw.From, sw.To)
	}
	if want := time.Date(2026, time.July, 1, 9, 2, 0, 0, time.UTC); !sw.At.Equal(want) {
		t.Errorf("LastSwitch.At = %v, want %v: the switch time is the first record naming the new model", sw.At, want)
	}
}

func TestScanModelTimeline_PlaceholdersAloneYieldAnHonestUnknown(t *testing.T) {
	tl, err := ScanModelTimeline(writeTranscript(t, recSkillListing, recSwitchSynthetic))
	if err != nil {
		t.Fatalf("ScanModelTimeline: %v", err)
	}
	if tl.Current != "" || tl.First != "" {
		t.Errorf("First/Current = %q/%q, want empty: \"<synthetic>\" is a harness token, not a model", tl.First, tl.Current)
	}
}

func TestScanModelTimeline_MissingFileIsAnError(t *testing.T) {
	if _, err := ScanModelTimeline("/nonexistent/definitely/not/here.jsonl"); err == nil {
		t.Fatal("want an error for a missing transcript, got nil")
	}
}

// TestInspect_ModelSwitchIsDisclosedAndFollowed is the adapter-level pin for
// the oracle finding: header = current model, window resolved from the current
// model, anchor still the boot turn's, and the switch disclosed on the report.
func TestInspect_ModelSwitchIsDisclosedAndFollowed(t *testing.T) {
	f := newFixture(t, recDeferredTools, recAgentListing, recSkillListing, recMCPInstructions,
		recUserTyped,
		recSwitchBootTurn,
		recSwitchNewTurn,
	)
	rep := inspect(t, f.request())

	if got, want := rep.Model, "claude-opus-4-7"; got != want {
		t.Errorf("Model = %q, want %q: the header must name the model the session is on now", got, want)
	}
	// The window must follow the CURRENT model. The fixture switches from a
	// 200k model (claude-opus-4-5) to a 1M one (claude-opus-4-7) precisely so
	// a header that silently kept the boot model would also fail here.
	if got, want := rep.Window.Tokens, 1000000; got != want {
		t.Errorf("Window.Tokens = %d, want %d: the window must be resolved from the current model, not the boot model", got, want)
	}

	// The anchor stays pinned to the boot turn: that is the request it measures.
	if rep.Anchor == nil {
		t.Fatal("Anchor is nil: the boot turn carries a cold measurement")
	}
	if got, ok := rep.Anchor.Tokens.Value(); !ok || got != 8+24000+3000 {
		t.Errorf("Anchor.Tokens = %d (ok=%v), want %d: the anchor must keep measuring the boot request", got, ok, 8+24000+3000)
	}

	var switched *ctxinspect.Caveat
	for i := range rep.Caveats {
		if rep.Caveats[i].Code == "model-switched" {
			switched = &rep.Caveats[i]
		}
	}
	if switched == nil {
		t.Fatalf("no model-switched caveat: a header that changed its answer without saying why is undisclosed, got %v", caveatCodes(rep))
	}
	if switched.Severity != ctxinspect.SeverityInfo {
		t.Errorf("model-switched severity = %v, want info: the switch is disclosure, not a defect", switched.Severity)
	}
	for _, needle := range []string{"claude-opus-4-5", "claude-opus-4-7", "2026-07-01 09:02:00", "anchor"} {
		if !strings.Contains(switched.Message, needle) {
			t.Errorf("model-switched caveat does not mention %q:\n%s", needle, switched.Message)
		}
	}
}

// TestInspect_NoSwitchMeansNoSwitchCaveat guards the other direction: a session
// that stayed on one model must not be captioned as having switched.
func TestInspect_NoSwitchMeansNoSwitchCaveat(t *testing.T) {
	f := newFixture(t, recDeferredTools, recAgentListing, recSkillListing, recMCPInstructions, recUserTyped, recAssistant)
	rep := inspect(t, f.request())
	if hasCaveat(rep, "model-switched") {
		t.Error("model-switched caveat on a session that never switched")
	}
	if got, want := rep.Model, "claude-opus-4-7"; got != want {
		t.Errorf("Model = %q, want %q", got, want)
	}
}
