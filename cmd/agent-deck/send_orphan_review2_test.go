package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Regression tests for the second adversarial review of the
// orphaned-composer fix. Each names the finding it pins.

// humanChipPane: the first Enter submits our message (the composer empties)
// and a human immediately pastes their own text, which Claude collapses to
// "[Pasted text #2 +4 lines]". No later Enter submits anything, and the
// status never goes active, so the verify budget ends typed_not_submitted
// with the human's chip in the composer.
type humanChipPane struct {
	*orphanPaneBS
	chip string
}

func (h *humanChipPane) SendEnter() error {
	p := h.orphanPane
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enters++
	if p.enters == 1 && len(p.segs) > 0 {
		p.submitted = append(p.submitted, p.text())
		p.segs = []orphanSeg{{lit: h.chip}}
	}
	return nil
}

func (h *humanChipPane) SendKeysAndEnter(body string) error {
	h.typeBody(body)
	return h.SendEnter()
}

func (h *humanChipPane) SendKeysAndEnterChecked(body string, capture func() (string, error), check tmux.PostPasteCheck) error {
	h.typeBody(body)
	pane, capErr := capture()
	if ok, err := check(pane, capErr); !ok {
		if err == nil {
			err = errors.New("withheld")
		}
		return err
	}
	return h.SendEnter()
}

// H1: a paste chip in the composer at the end of the verify budget is ours
// only when it is exactly the label our own paste showed before Enter. A
// human's chip (#2) is never deleted and never reported typed_withdrawn (our
// message was in fact submitted: a retry would duplicate it).
func TestOrphanReview2_HumanPasteChipAtEndOfBudgetIsNeverDeleted(t *testing.T) {
	const chip = "[Pasted text #2 +4 lines]"
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := &humanChipPane{orphanPaneBS: &orphanPaneBS{orphanPane: &orphanPane{}}, chip: chip}
			res, err := orphanSend(t, pane, orphanMultiLine, mode.noWait)
			if got := pane.composer(); got != chip {
				t.Fatalf("the human's chip was touched: composer = %q (backspace calls %v); delivery=%q err=%v", got, pane.bsCalls, res.delivery, err)
			}
			if _, _, bs := pane.counts(); bs != 0 {
				t.Fatalf("sent Backspace into a human's paste chip: %v", pane.bsCalls)
			}
			if res.delivery == "typed_withdrawn" {
				t.Fatalf("delivery = typed_withdrawn for a message that was submitted; err=%v", err)
			}
			if res.jsonFields()["composer_residue"] == "removed" {
				t.Fatalf("composer_residue = removed; fields=%v", res.jsonFields())
			}
		})
	}
}

// M1: a message shaped like Claude's idle placeholder (Try "..." ending in a
// quote) that stays in the composer is reported left, not none.
func TestOrphanReview2_PlaceholderShapedMessageIsReportedLeft(t *testing.T) {
	const msg = `Try "make lint" then "make test" and paste the summary "ok"`
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := &orphanPaneBS{orphanPane: &orphanPane{}}
			res, err := orphanSend(t, pane, msg, mode.noWait)
			if pane.composer() != msg {
				t.Fatalf("setup: composer = %q", pane.composer())
			}
			if err == nil {
				t.Fatalf("a message still typed in the composer reported success: delivery=%q", res.delivery)
			}
			if f := requireOrphanResidue(t, res, "left"); f["composer_residue_verified"] != true {
				t.Fatalf("composer_residue_verified = %v, want true", f["composer_residue_verified"])
			}
		})
	}
}

// transportFailPane: the body transport fails. typedRunes > 0 types that
// much of the body first (a chunk failed mid-paste); enterFails types the
// whole body and then fails the Enter.
type transportFailPane struct {
	*orphanPaneBS
	typedRunes int
	enterFails bool
}

func (f *transportFailPane) send(body string) error {
	if f.enterFails {
		f.typeBody(body)
		return errors.New("tmux send-keys Enter failed")
	}
	if f.typedRunes > 0 {
		f.typeBody(string([]rune(body)[:f.typedRunes]))
	}
	return errors.New("failed to send chunk 2/2: tmux send-keys failed")
}

func (f *transportFailPane) SendKeysAndEnter(body string) error { return f.send(body) }

func (f *transportFailPane) SendKeysAndEnterChecked(body string, _ func() (string, error), _ tmux.PostPasteCheck) error {
	return f.send(body)
}

// M2: a transport failure after typing began observes the input box: text in
// it is reported left (unverified) and recorded; an empty box is absent
// (send_failed as before: typing may have begun, so never "none"). Nothing is
// ever deleted on this path.
func TestOrphanReview2_TransportFailureAfterTypingReportsResidue(t *testing.T) {
	long := orphanLongSingleLine()
	cases := []struct {
		name   string
		pane   func() *transportFailPane
		msg    string
		want   string
		record bool
	}{
		{"chunk failed mid-paste", func() *transportFailPane {
			return &transportFailPane{orphanPaneBS: &orphanPaneBS{orphanPane: &orphanPane{}}, typedRunes: 300}
		}, long, "left", true},
		{"final Enter failed (plain send)", func() *transportFailPane {
			return &transportFailPane{orphanPaneBS: &orphanPaneBS{orphanPane: &orphanPane{}}, enterFails: true}
		}, orphanSingleLine, "left", true},
		{"box reads empty", func() *transportFailPane {
			return &transportFailPane{orphanPaneBS: &orphanPaneBS{orphanPane: &orphanPane{}}}
		}, orphanSingleLine, "absent", false},
	}
	for _, mode := range issue2077Modes() {
		for _, tc := range cases {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				store := newMemResidueStore()
				pane := tc.pane()
				before := ""
				res, err := executeSend(pane, "claude", tc.msg, mode.noWait, residueTuning(mode.noWait, store))
				if err == nil {
					t.Fatalf("want an error, delivery=%q", res.delivery)
				}
				before = pane.composer()
				f := requireOrphanResidue(t, res, tc.want)
				if tc.want == "left" && f["composer_residue_verified"] != false {
					t.Fatalf("composer_residue_verified = %v, want false", f["composer_residue_verified"])
				}
				if _, ok, _ := store.Load(residueTarget); ok != tc.record {
					t.Fatalf("record written = %v, want %v", ok, tc.record)
				}
				if _, _, bs := pane.counts(); bs != 0 || pane.composer() != before {
					t.Fatalf("the transport-failure path deleted text: backspace calls %v", pane.bsCalls)
				}
				if !strings.Contains(err.Error(), "failed to send message") {
					t.Fatalf("error %q lost the failed-to-send wording", err)
				}
				wantDelivery := map[string]string{"left": "typed_not_submitted", "absent": "send_failed"}[tc.want]
				if res.delivery != wantDelivery {
					t.Fatalf("delivery = %q, want %q", res.delivery, wantDelivery)
				}
			})
		}
	}
}

// launchRetryPane shows no composer on its first capture (the prompt was not
// consumed yet), then behaves like the wrapped pane.
type launchRetryPane struct {
	*orphanPaneBS
	caps int
}

func (l *launchRetryPane) CapturePaneFresh() (string, error) {
	l.caps++
	if l.caps == 1 {
		return "Starting Claude Code…\n", nil
	}
	return l.orphanPaneBS.CapturePaneFresh()
}

// M3: the launch prompt retry must not discard TypeAndSubmitReport's verdict:
// the warning says what is left in the composer.
func TestOrphanReview2_LaunchRetrySurfacesComposerResidue(t *testing.T) {
	inner := &orphanPane{suggestion: true, replaceWith: "HUMAN WIP: draft the release notes"}
	pane := &launchRetryPane{orphanPaneBS: &orphanPaneBS{orphanPane: inner}}
	var warn bytes.Buffer
	verifyPromptConsumedAfterLaunchAttributed(pane, "claude", "review the diff and report", true, 0, time.Millisecond, &warn)
	if _, enters, bs := inner.counts(); enters != 0 || bs != 0 {
		t.Fatalf("enters=%d backspace calls=%d, want none", enters, bs)
	}
	if !strings.Contains(warn.String(), "still typed, unsubmitted, in the target's input box") {
		t.Fatalf("warning %q does not say what the retry left in the composer", warn.String())
	}
}

// sameLabelChipPane: the first Enter submits our message and the composer
// shows empty for a couple of frames; then a human pastes text that Claude
// collapses to a chip with the SAME label our paste showed ("#1", same line
// count: Claude may restart paste numbering per prompt). No later Enter
// submits it, so the budget ends typed_not_submitted with the human's chip.
type sameLabelChipPane struct {
	*orphanPaneBS
	chip          string
	emptyCaptures int
}

func (h *sameLabelChipPane) SendEnter() error {
	p := h.orphanPane
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enters++
	if p.enters == 1 && len(p.segs) > 0 {
		p.submitted = append(p.submitted, p.text())
		p.segs = nil
	}
	return nil
}

func (h *sameLabelChipPane) SendKeysAndEnter(body string) error {
	h.typeBody(body)
	return h.SendEnter()
}

func (h *sameLabelChipPane) SendKeysAndEnterChecked(body string, capture func() (string, error), check tmux.PostPasteCheck) error {
	h.typeBody(body)
	pane, capErr := capture()
	if ok, err := check(pane, capErr); !ok {
		if err == nil {
			err = errors.New("withheld")
		}
		return err
	}
	return h.SendEnter()
}

func (h *sameLabelChipPane) CapturePaneFresh() (string, error) {
	p := h.orphanPane
	p.mu.Lock()
	if p.enters >= 1 && len(p.segs) == 0 {
		h.emptyCaptures++
		if h.emptyCaptures > 2 {
			p.segs = []orphanSeg{{lit: h.chip}}
		}
	}
	p.mu.Unlock()
	return h.orphanPaneBS.CapturePaneFresh()
}

// A chip whose label equals the one our own paste showed is still not ours
// once a post-Enter frame showed the composer empty: our chip left the
// composer, so the one there now is a new paste.
func TestOrphanReview3_SameLabelChipAfterComposerEmptiedIsNeverDeleted(t *testing.T) {
	chip := fmt.Sprintf("[Pasted text #1 +%d lines]", strings.Count(orphanMultiLine, "\n"))
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := &sameLabelChipPane{orphanPaneBS: &orphanPaneBS{orphanPane: &orphanPane{}}, chip: chip}
			res, err := orphanSend(t, pane, orphanMultiLine, mode.noWait)
			if got := pane.composer(); got != chip {
				t.Fatalf("the human's same-label chip was touched: composer = %q (backspace calls %v); delivery=%q err=%v", got, pane.bsCalls, res.delivery, err)
			}
			if _, _, bs := pane.counts(); bs != 0 {
				t.Fatalf("sent Backspace into a human's paste chip: %v", pane.bsCalls)
			}
			if res.delivery == "typed_withdrawn" || res.jsonFields()["composer_residue"] == "removed" {
				t.Fatalf("reported removed for a message that was submitted: delivery=%q fields=%v", res.delivery, res.jsonFields())
			}
		})
	}
}
