package send

import (
	"errors"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Unit tests for ObserveComposer: one look at the input box after a send
// typed, never a key.

type observePane struct {
	text     string
	capErr   error
	hidden   bool
	captures int
}

func (p *observePane) CapturePaneFresh() (string, error) {
	p.captures++
	if p.capErr != nil {
		return "", p.capErr
	}
	if p.hidden {
		return "no composer here\n", nil
	}
	return renderComposer(p.text), nil
}

func observe(p *observePane, msg, chip string, emptyBefore bool) ComposerObservation {
	return ObserveComposer(p, msg, chip, ClaudeComposerFullBody, nil, emptyBefore)
}

func TestObserveComposer_States(t *testing.T) {
	const msg = "[lane timer] wake: check the inbox and report"
	for _, tc := range []struct {
		name        string
		pane        *observePane
		chip        string
		emptyBefore bool
		state       ComposerState
		verified    bool
		reason      string
	}{
		{"empty", &observePane{}, "", true, ComposerEmpty, false, ""},
		{"idle placeholder reads empty", &observePane{text: `Try "write a test for <filepath>"`}, "", true, ComposerEmpty, false, ""},
		{"our whole text", &observePane{text: msg}, "", true, ComposerLeft, true, ""},
		{"a prefix of our text", &observePane{text: msg[:20]}, "", true, ComposerLeft, true, ""},
		{"not empty before", &observePane{text: msg}, "", false, ComposerLeft, false, ObserveReasonNotEmpty},
		{"other text", &observePane{text: "HUMAN WIP"}, "", true, ComposerLeft, false, ObserveReasonForeign},
		{"our text plus a human paragraph", &observePane{text: msg + "\n\nHUMAN NOTE"}, "", true, ComposerLeft, false, ObserveReasonForeign},
		{"owned chip", &observePane{text: "[Pasted text #1 +3 lines]"}, "[Pasted text #1 +3 lines]", true, ComposerLeft, true, ""},
		{"someone else's chip", &observePane{text: "[Pasted text #2 +5 lines]"}, "[Pasted text #1 +3 lines]", true, ComposerLeft, false, ObserveReasonForeign},
		{"image placeholders", &observePane{text: "[Image #8] [Image #9][Pasted text #11 +5 lines]"}, "", true, ComposerLeft, false, ObserveReasonAttachments},
		{"capture error", &observePane{capErr: errors.New("pane gone")}, "", true, ComposerUnreadable, false, ObserveReasonUnreadable},
		{"no composer", &observePane{hidden: true}, "", true, ComposerUnreadable, false, ObserveReasonUnreadable},
	} {
		o := observe(tc.pane, msg, tc.chip, tc.emptyBefore)
		if o.State != tc.state || o.Verified != tc.verified || o.Reason != tc.reason {
			t.Errorf("%s: got %+v, want state=%v verified=%v reason=%q", tc.name, o, tc.state, tc.verified, tc.reason)
		}
		if tc.pane.captures != 1 {
			t.Errorf("%s: %d captures, want exactly one look", tc.name, tc.pane.captures)
		}
	}
}

// A message shaped like Claude's idle placeholder is still our text: it is
// reported left, never mistaken for an empty input box.
func TestObserveComposer_PlaceholderShapedMessageIsLeft(t *testing.T) {
	msg := `Try "make lint" then "make test" and paste the summary "ok"`
	o := observe(&observePane{text: msg}, msg, "", true)
	if o.State != ComposerLeft || !o.Verified || o.Draft != msg {
		t.Fatalf("o=%+v, want left, verified, with the draft", o)
	}
}

func TestObserveComposer_NilReaderIsUnreadableWithoutCapture(t *testing.T) {
	p := &observePane{text: "x"}
	o := ObserveComposer(p, "x", "", nil, nil, true)
	if o.State != ComposerUnreadable || o.Reason != ObserveReasonNoFullBody || p.captures != 0 {
		t.Fatalf("o=%+v captures=%d", o, p.captures)
	}
}

// Every state but empty carries the plain still-typed notice; unverified
// adds "it may be mixed with other text".
func TestComposerObservation_Describe(t *testing.T) {
	if d := (ComposerObservation{State: ComposerLeft, Verified: true}).Describe(); d != StillTypedNotice {
		t.Fatalf("verified: %q", d)
	}
	d := (ComposerObservation{State: ComposerLeft, Reason: ObserveReasonForeign}).Describe()
	if !strings.Contains(d, StillTypedNotice+MixedNotice) || !strings.HasPrefix(d, ObserveReasonForeign) {
		t.Fatalf("unverified: %q", d)
	}
	if d := (ComposerObservation{State: ComposerUnreadable, Reason: ObserveReasonUnreadable}).Describe(); !strings.Contains(d, StillTypedNotice) {
		t.Fatalf("unreadable: %q", d)
	}
	if d := (ComposerObservation{State: ComposerEmpty}).Describe(); strings.Contains(d, "still typed") || !strings.Contains(d, "may have been submitted") {
		t.Fatalf("empty: %q", d)
	}
}

func TestClaudeComposerFullBody(t *testing.T) {
	div := strings.Repeat("─", 40)
	frame := func(rows ...string) string {
		return "out\n" + div + "\n" + strings.Join(rows, "\n") + "\n" + div + "\n  ? for shortcuts\n"
	}
	for _, tc := range []struct {
		name, raw, want string
		ok              bool
	}{
		{"empty", frame("❯ "), "", true},
		{"one row", frame("❯ hello"), "hello", true},
		{"paragraphs", frame("❯ first", "", "  second", "    indented"), "first\n\nsecond\n  indented", true},
		{"trailing blank rows", frame("❯ x", "", ""), "x", true},
		{"placeholder", frame(`❯ Try "write a test"`), `Try "write a test"`, true},
		{"no frame", "❯ loose prompt\n", "", false},
		{"no glyph", frame("something else"), "", false},
	} {
		got, ok := ClaudeComposerFullBody(tc.raw, nil)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// ProbeComposer carries the composer-empty provenance the launch paths pass
// to TypeAndSubmitReport.
func TestProbeComposer_ComposerEmpty(t *testing.T) {
	pane := func(s string) func() (string, error) { return func() (string, error) { return s, nil } }
	if p := ProbeComposer(pane(renderComposer("")), true); !p.ComposerEmpty || p.FullBody == nil || !p.PasteMarkerFree {
		t.Fatalf("empty Claude composer: %+v", p)
	}
	if p := ProbeComposer(pane(renderComposer("a draft")), true); p.ComposerEmpty {
		t.Fatalf("composer holding a draft read as empty: %+v", p)
	}
	if p := ProbeComposer(pane(renderComposer("")), false); p.ComposerEmpty || p.FullBody != nil {
		t.Fatalf("non-Claude target: %+v", p)
	}
	if p := ProbeComposer(func() (string, error) { return "", errors.New("gone") }, true); p.ComposerEmpty {
		t.Fatalf("capture failure read as empty: %+v", p)
	}
}

// TypeAndSubmitReport records the chip its own post-paste, pre-Enter frame
// showed, exactly, and only with empty-before provenance.
func TestTypeAndSubmitReport_RecordsOwnedChipFromPrePasteFrame(t *testing.T) {
	msg := "one\ntwo\nthree\nfour"
	pane := &chipSender{label: "[Pasted text #3 +3 lines]"}
	rep, err := TypeAndSubmitReport(pane, msg, ExpectedPasteMarkerLineBreaks(msg), false, true, ClaudeComposerFullBody)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if rep.OwnedChip != "[Pasted text #3 +3 lines]" {
		t.Fatalf("OwnedChip = %q", rep.OwnedChip)
	}
	pane = &chipSender{label: "[Pasted text #3 +3 lines]"}
	if rep, _ := TypeAndSubmitReport(pane, msg, ExpectedPasteMarkerLineBreaks(msg), false, false, ClaudeComposerFullBody); rep.OwnedChip != "" {
		t.Fatalf("OwnedChip = %q without composerEmptyBefore", rep.OwnedChip)
	}
}

// A pre-Enter refusal reports what the input box holds and leaves it as it
// is: the truncated chip is reported left (verified only when it is the
// exact owned label, which a truncated chip is).
func TestTypeAndSubmitReport_PreEnterRefusalReportsLeft(t *testing.T) {
	msg := "one\ntwo\nthree\nfour"
	pane := &chipSender{label: "[Pasted text #1 +2 lines]"}
	_, err := TypeAndSubmitReport(pane, msg, ExpectedPasteMarkerLineBreaks(msg), false, true, ClaudeComposerFullBody)
	var ns *NotSubmittedError
	if !errors.As(err, &ns) || !errors.Is(err, ErrPasteTruncated) {
		t.Fatalf("err = %v, want a NotSubmittedError wrapping ErrPasteTruncated", err)
	}
	if ns.Observed.State != ComposerLeft || !pane.typed {
		t.Fatalf("observed = %+v typed=%v, want left and the chip still there", ns.Observed, pane.typed)
	}
}

// chipSender shows label in the input box once typed; Enter empties it.
type chipSender struct {
	label string
	typed bool
}

func (c *chipSender) CapturePaneFresh() (string, error) {
	if c.typed {
		return renderComposer(c.label), nil
	}
	return renderComposer(""), nil
}
func (c *chipSender) SendEnter() error              { c.typed = false; return nil }
func (c *chipSender) SendKeysAndEnter(string) error { return nil }
func (c *chipSender) SendKeysAndEnterChecked(_ string, capture func() (string, error), check tmux.PostPasteCheck) error {
	c.typed = true
	pane, capErr := capture()
	if ok, err := check(pane, capErr); !ok {
		return err
	}
	return c.SendEnter()
}
