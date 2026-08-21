package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/verify"
)

// parityTestView returns the minimal view the parity renderer needs.
func parityTestView() contextView {
	rep := &ctxinspect.Report{
		Harness: "claude",
		Adapter: "claude",
		Model:   "claude-opus-4-7",
		Window:  ctxinspect.WindowInfo{Tokens: 200000, Source: ctxinspect.WindowModelDefault, Detail: "test"},
		Basis:   ctxinspect.BasisObserved,
	}
	rep.Reconcile()
	return contextView{Ref: "demo", Title: "demo", Profile: "_test", Tool: "claude", Report: rep}
}

func parityTestSpec(t *testing.T) verify.Spec {
	t.Helper()
	spec, ok := verify.SpecForAdapter("claude")
	if !ok {
		t.Fatal("the claude adapter must have a verification spec")
	}
	return spec
}

// TestConfirmContextVerifyNamesTheMutation: "verify the context" does not sound
// like "send a message to my agent", and the user has to be agreeing to the
// second thing.
func TestConfirmContextVerifyNamesTheMutation(t *testing.T) {
	var out bytes.Buffer
	confirmContextVerify(strings.NewReader("n\n"), &out, parityTestView(), parityTestSpec(t))

	text := out.String()
	for _, want := range []string{"/context", "demo", "types into the agent's composer"} {
		if !strings.Contains(text, want) {
			t.Errorf("the prompt does not mention %q:\n%s", want, text)
		}
	}
}

// TestConfirmContextVerifySaysItIsALiveSession: the guard makes a mid-turn
// landing unlikely, not impossible, and it clears an operator draft to do it.
// A consent prompt that advertises only the happy path is asking for agreement
// to something other than what happens.
func TestConfirmContextVerifySaysItIsALiveSession(t *testing.T) {
	var out bytes.Buffer
	confirmContextVerify(strings.NewReader("n\n"), &out, parityTestView(), parityTestSpec(t))

	text := out.String()
	for _, want := range []string{"live session", "mid-turn", "cannot be taken back"} {
		if !strings.Contains(text, want) {
			t.Errorf("the prompt does not warn about %q:\n%s", want, text)
		}
	}
}

func TestConfirmContextVerifyAnswers(t *testing.T) {
	tests := map[string]bool{
		"y\n":   true,
		"Y\n":   true,
		"yes\n": true,
		"n\n":   false,
		"\n":    false,
		"":      false, // EOF, i.e. a non-terminal stdin
		"sure":  false,
	}
	for input, want := range tests {
		var out bytes.Buffer
		got := confirmContextVerify(strings.NewReader(input), &out, parityTestView(), parityTestSpec(t))
		if got != want {
			t.Errorf("input %q → %v, want %v", input, got, want)
		}
	}
}

// TestConfirmContextVerifyDefaultsToNo is the safety property: anything that is
// not an explicit yes must not type into a live session. In particular an
// unattended run reads EOF, and EOF is not consent.
func TestConfirmContextVerifyDefaultsToNo(t *testing.T) {
	var out bytes.Buffer
	if confirmContextVerify(strings.NewReader(""), &out, parityTestView(), parityTestSpec(t)) {
		t.Fatal("EOF must not be read as consent")
	}
}

func TestContextVerifyFailureMessageExplainsWhatToDo(t *testing.T) {
	msg := contextVerifyFailureMessage("/context", verify.ErrNoAccounting)
	if !strings.Contains(msg, "will not report a comparison it could not make") {
		t.Errorf("an unreadable panel must refuse rather than degrade:\n%s", msg)
	}
	if !strings.Contains(msg, "/context") {
		t.Errorf("the message must name the command:\n%s", msg)
	}

	timeout := contextVerifyFailureMessage("/context", verify.ErrPanelTimeout)
	if !strings.Contains(timeout, "busy") {
		t.Errorf("a timeout must suggest the likely cause:\n%s", timeout)
	}
}

// TestContextLivePaneReturnsAnUntypedNil guards the classic interface trap: a
// nil *tmux.Session stored in an interface is not nil, and the caller's "is
// there a pane" check would pass before dereferencing it.
func TestContextLivePaneReturnsAnUntypedNil(t *testing.T) {
	if got := contextLivePane(nil); got != nil {
		t.Fatalf("contextLivePane(nil) = %#v, want a nil interface", got)
	}
}

// buildParityForRender assembles a parity result with one row of each verdict,
// so the renderer is exercised on every branch at once.
func buildParityForRender() *verify.Parity {
	return &verify.Parity{
		Harness: &verify.HarnessReport{
			Harness: "claude",
			Command: verify.ClaudeCommand,
			Window:  200000,
			Used:    116000,
			Figures: []verify.Figure{{Label: "memory files", Raw: "Memory files", Tokens: 4500, Slack: 50}},
			Unrecognized: []string{
				"Background tasks: 2.0k tokens (1.0%) (label not part of the /context panel)",
			},
		},
		Tolerance: verify.DefaultTolerance(),
		Rows: []verify.Row{
			{Group: "memory files", Harness: 4500, HarnessKnown: true, Ours: 4460, OursKnown: true, OursComplete: true,
				Delta: -40, DeltaPct: -0.9, Allowed: 550, Verdict: verify.VerdictMatch, Note: "the CLAUDE.md hierarchy"},
			{Group: "agents", Harness: 1600, HarnessKnown: true, Ours: 3900, OursKnown: true, OursComplete: true,
				Delta: 2300, DeltaPct: 143.8, Allowed: 500, Verdict: verify.VerdictDrift, Note: "the startup agent catalogue"},
			{Group: "skills", Verdict: verify.VerdictHarnessSilent, Note: "the panel printed no row for this group"},
			{Group: "harness internals", Harness: 17100, HarnessKnown: true, Ours: 9000, OursKnown: true,
				Verdict: verify.VerdictOursUnknown, Note: "an item in this group has no token count"},
			{Group: "messages", Harness: 93000, HarnessKnown: true, Ours: 93000, OursKnown: true, OursComplete: true,
				Verdict: verify.VerdictInformational, Note: "conversation history"},
		},
		Status:       verify.StatusDrift,
		Unmapped:     []string{"Background tasks (2000 tokens)"},
		WindowAgrees: true,
		WindowNote:   "both sides use a 200000-token window (agent-deck source: model-default)",
	}
}

func TestRenderContextParityShowsEveryVerdict(t *testing.T) {
	out := renderContextParity(parityTestView(), parityTestSpec(t), buildParityForRender())

	for _, want := range []string{
		"live parity",
		"memory files",
		"DRIFT",
		"harness silent",
		"ours unknown",
		"informational",
		"Background tasks",
		"tolerance:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the parity table is missing %q:\n%s", want, out)
		}
	}
}

// TestRenderContextParityNeverPrintsZeroForAnAbsentFigure: a group the harness
// was silent about must render as absent, because a zero there reads as
// agreement about nothing.
func TestRenderContextParityNeverPrintsZeroForAnAbsentFigure(t *testing.T) {
	out := renderContextParity(parityTestView(), parityTestSpec(t), buildParityForRender())

	var skillsRow string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "skills") && strings.Contains(line, "harness silent") {
			skillsRow = line
		}
	}
	if skillsRow == "" {
		t.Fatalf("no harness-silent row was rendered:\n%s", out)
	}
	if strings.Contains(skillsRow, " 0 ") {
		t.Errorf("an absent figure was rendered as zero: %q", skillsRow)
	}
	if !strings.Contains(skillsRow, "—") {
		t.Errorf("an absent figure must render as a dash: %q", skillsRow)
	}
}

// TestRenderContextParityMarksALowerBound: an agent-deck side that could not
// price everything must be shown as a lower bound, not as a total.
func TestRenderContextParityMarksALowerBound(t *testing.T) {
	out := renderContextParity(parityTestView(), parityTestSpec(t), buildParityForRender())
	if !strings.Contains(out, "≥") {
		t.Errorf("an incomplete side must be marked as a lower bound:\n%s", out)
	}
}

func TestParityVerdictLineNeverReadsLikeAgreementWhenNothingWasGraded(t *testing.T) {
	p := &verify.Parity{Status: verify.StatusIndeterminate}
	line := parityVerdictLine(p)
	if !strings.Contains(line, "INDETERMINATE") || !strings.Contains(line, "no agreement is claimed") {
		t.Errorf("an ungraded comparison must say so plainly: %q", line)
	}
}

// TestContextExitCodesAreDistinct: CI has to be able to tell "we could not
// check" from "we checked and we were wrong".
func TestContextExitCodesAreDistinct(t *testing.T) {
	codes := map[string]int{
		"ok":           contextExitOK,
		"error":        contextExitError,
		"not-found":    contextExitNotFound,
		"unreconciled": contextExitUnreconciled,
		"drift":        contextExitDrift,
	}
	seen := make(map[int]string, len(codes))
	for name, code := range codes {
		if other, ok := seen[code]; ok {
			t.Errorf("%s and %s share exit code %d", name, other, code)
		}
		seen[code] = name
	}
}

// TestRestoreContextVerifyDraftNeverSilentlyDropsOperatorText: the guard cleared
// text the operator wrote. If it cannot be typed back, the only honest thing to
// do is print it rather than let it vanish.
func TestRestoreContextVerifyDraftNeverSilentlyDropsOperatorText(t *testing.T) {
	failing := &fakeContextLiveSession{chunkedErr: errors.New("pane went away")}

	if got := restoreContextVerifyDraft(failing, ""); got != nil {
		t.Fatalf("no draft was saved, so there is nothing to warn about; got %v", got)
	}

	warnings := restoreContextVerifyDraft(failing, "half-written thought")
	if len(warnings) != 1 {
		t.Fatalf("a failed restore must produce exactly one warning, got %v", warnings)
	}
	if !strings.Contains(warnings[0], "half-written thought") {
		t.Errorf("the warning must carry the lost text back to the user: %q", warnings[0])
	}

	ok := &fakeContextLiveSession{}
	if got := restoreContextVerifyDraft(ok, "half-written thought"); got != nil {
		t.Fatalf("a successful restore warns about nothing, got %v", got)
	}
	if ok.chunked != "half-written thought" {
		t.Errorf("the draft was typed back as %q", ok.chunked)
	}
}

// fakeContextLiveSession is a scripted pane. It exists to prove the interface
// carries the whole pre-send surface: if contextLiveSession ever narrows back
// to "capture and type", this stops compiling.
type fakeContextLiveSession struct {
	pane       string
	status     string
	chunked    string
	chunkedErr error
	sentEnter  string
	ctrlC      int
}

func (f *fakeContextLiveSession) CapturePaneFresh() (string, error) { return f.pane, nil }
func (f *fakeContextLiveSession) GetStatus() (string, error)        { return f.status, nil }
func (f *fakeContextLiveSession) SendCtrlC() error                  { f.ctrlC++; return nil }
func (f *fakeContextLiveSession) SendKeysAndEnter(keys string) error {
	f.sentEnter = keys
	return nil
}
func (f *fakeContextLiveSession) SendKeysChunked(keys string) error {
	if f.chunkedErr != nil {
		return f.chunkedErr
	}
	f.chunked = keys
	return nil
}

var _ contextLiveSession = (*fakeContextLiveSession)(nil)
