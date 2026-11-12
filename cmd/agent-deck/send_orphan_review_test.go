package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Regression tests for the adversarial review of the orphaned-composer fix.
// They use only the API executeSend already had (delivery literals,
// jsonFields keys, error substrings, pane contents), so each one fails on the
// reviewed tree for the defect it names and passes once it is fixed.

// reviewPane is an orphanPaneBS with the extra pane behaviours the review
// cases need. It renders the composer like Claude: a hard line break in the
// draft continues on a row indented by two spaces.
type reviewPane struct {
	*orphanPaneBS
	clock          bool // a clock row above the composer changes on every capture
	tick           int
	menuOnEnterCap int // > 0: exactly the n-th capture after the first Enter shows a selection menu
	postEnterCaps  int
	menuAfterCaps  int // > 0: from this many captures after typing on, a menu covers the pane
	postTypeCaps   int
	enterErr       bool // the final Enter's send-keys fails (nothing is submitted)
	emptyOnRefusal bool // when the pre-Enter check refuses, something else empties the composer
}

func newReviewPane(inner *orphanPane) *reviewPane {
	return &reviewPane{orphanPaneBS: &orphanPaneBS{orphanPane: inner}}
}

func (r *reviewPane) CapturePaneFresh() (string, error) {
	p := r.orphanPane
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.typed > 0 {
		r.postTypeCaps++
	}
	if p.enters > 0 {
		r.postEnterCaps++
	}
	if (r.menuOnEnterCap > 0 && r.postEnterCaps == r.menuOnEnterCap) || (r.menuAfterCaps > 0 && r.postTypeCaps >= r.menuAfterCaps) {
		return guardedMenu, nil
	}
	var pane string
	if len(p.segs) == 0 && p.suggestion {
		pane = ghostPane(ghostRows["focused"])
	} else {
		pane = claudeComposer(strings.ReplaceAll(p.text(), "\n", "\n  "))
	}
	if r.clock {
		r.tick++
		pane = fmt.Sprintf("12:00:%02d elapsed\n", r.tick%60) + pane
	}
	return pane, nil
}

func (r *reviewPane) SendEnter() error {
	if r.enterErr {
		return errors.New("tmux send-keys Enter failed")
	}
	return r.orphanPane.SendEnter()
}

func (r *reviewPane) SendKeysAndEnter(body string) error {
	r.typeBody(body)
	return r.SendEnter()
}

func (r *reviewPane) SendKeysAndEnterChecked(body string, capture func() (string, error), check tmux.PostPasteCheck) error {
	r.typeBody(body)
	pane, capErr := capture()
	if ok, err := check(pane, capErr); !ok {
		if err == nil {
			err = errors.New("withheld")
		}
		if r.emptyOnRefusal {
			r.configure(func(p *orphanPane) { p.segs = nil })
		}
		return err
	}
	return r.SendEnter()
}

// Finding 1: ownership was checked on the composer's FIRST paragraph only,
// so a human's paragraph after our text was deleted unchecked.
func TestOrphanReview_HumanParagraphAfterOurTextIsNeverDeleted(t *testing.T) {
	const note = "HUMAN NOTE keep this paragraph"
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := newReviewPane(&orphanPane{humanAfter: "\n\n" + note})
			res, err := orphanSend(t, pane, orphanSingleLine, mode.noWait)
			if err == nil {
				t.Fatalf("want an error, delivery=%q", res.delivery)
			}
			if got := pane.composer(); !strings.Contains(got, note) {
				t.Fatalf("the human's paragraph was deleted: composer = %q (backspace calls %v)", got, pane.bsCalls)
			}
			if _, _, bs := pane.counts(); bs != 0 {
				t.Fatalf("sent %d Backspace call(s) into a composer holding a human paragraph: %v", bs, pane.bsCalls)
			}
			if res.delivery != "typed_not_submitted" {
				t.Fatalf("delivery = %q, want typed_not_submitted; err=%v", res.delivery, err)
			}
			if f := requireOrphanResidue(t, res, "left"); f["composer_residue_verified"] != false {
				t.Fatalf("composer_residue_verified = %v, want false", f["composer_residue_verified"])
			}
		})
	}
}

// Finding 1 (progress): a clock or spinner elsewhere in the pane is not
// Backspace progress; only the composer body changing is.
func TestOrphanReview_ClockTickIsNotBackspaceProgress(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := newReviewPane(&orphanPane{})
			pane.noop = true
			pane.clock = true
			res, err := orphanSend(t, pane, orphanSingleLine, mode.noWait)
			if err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("delivery=%q err=%v, want typed_not_submitted", res.delivery, err)
			}
			if !strings.Contains(err.Error(), "backspace made no progress") {
				t.Fatalf("error %q, want reason backspace made no progress (backspace calls %v)", err, pane.bsCalls)
			}
			if _, _, bs := pane.counts(); bs != 1 {
				t.Fatalf("backspace calls = %v, want exactly one batch before giving up", pane.bsCalls)
			}
		})
	}
}

// Finding 4: a composer that is empty when withdrawal starts was emptied by
// something other than this send (it may have been submitted): never
// typed_withdrawn, which a queue retries.
func TestOrphanReview_ComposerEmptiedByOtherIsNotWithdrawn(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			inner := &orphanPane{truncateBy: 1}
			pane := newReviewPane(inner)
			pane.emptyOnRefusal = true
			res, err := orphanSend(t, pane, orphanMultiLine, mode.noWait)
			if err == nil {
				t.Fatalf("want an error, delivery=%q", res.delivery)
			}
			if res.delivery != "typed_not_submitted" {
				t.Fatalf("delivery = %q, want typed_not_submitted (agent-deck removed nothing); err=%v", res.delivery, err)
			}
			requireOrphanResidue(t, res, "none")
			if !strings.Contains(err.Error(), "may have been submitted") {
				t.Fatalf("error %q must say the message may have been submitted", err)
			}
		})
	}
}

// Finding 3: a pre-Enter refusal whose withdrawal is then stopped by the
// input-prompt guard (a menu opened) must still report the residue.
func TestOrphanReview_GuardRefusalDuringWithdrawKeepsResidue(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := newReviewPane(&orphanPane{truncateBy: 1})
			// capture 1 after typing: the pre-Enter check; 2: withdrawal's
			// first frame; 3: the guard's check before the Backspace.
			pane.menuAfterCaps = 3
			tun := issue2077Tuning(mode.noWait)
			tun.requireInputPrompt = true
			res, err := executeSend(pane, "claude", orphanMultiLine, mode.noWait, tun)
			if err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("delivery=%q err=%v, want typed_not_submitted", res.delivery, err)
			}
			requireOrphanResidue(t, res, "left")
			if _, enters, _ := pane.counts(); enters != 0 {
				t.Fatalf("pressed Enter %d times", enters)
			}
		})
	}
}

// Finding 3: the guard refusing after the body was typed (a menu flashed up
// when the verify loop nudged Enter) reports the typed text as left,
// unverified.
func TestOrphanReview_GuardRefusalAfterTypingReportsLeft(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := newReviewPane(&orphanPane{})
			// capture 1 after the swallowed Enter: the verify loop's frame;
			// 2: the guard's check before the nudge Enter.
			pane.menuOnEnterCap = 2
			tun := issue2077Tuning(mode.noWait)
			tun.requireInputPrompt = true
			res, err := executeSend(pane, "claude", orphanSingleLine, mode.noWait, tun)
			if err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("delivery=%q err=%v, want typed_not_submitted", res.delivery, err)
			}
			if f := requireOrphanResidue(t, res, "left"); f["composer_residue_verified"] != false {
				t.Fatalf("composer_residue_verified = %v, want false", f["composer_residue_verified"])
			}
		})
	}
}

// Finding 3: the final Enter's transport failing after the whole body was
// typed (and passed the pre-Enter check) leaves the text in the composer.
func TestOrphanReview_EnterTransportFailureAfterFullTypeReportsLeft(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := newReviewPane(&orphanPane{})
			pane.enterErr = true
			res, err := orphanSend(t, pane, orphanMultiLine, mode.noWait)
			if err == nil {
				t.Fatalf("want an error, delivery=%q", res.delivery)
			}
			if f := requireOrphanResidue(t, res, "left"); f["composer_residue_verified"] != false {
				t.Fatalf("composer_residue_verified = %v, want false", f["composer_residue_verified"])
			}
			if res.delivery != "typed_not_submitted" {
				t.Fatalf("delivery = %q, want typed_not_submitted; err=%v", res.delivery, err)
			}
			if pane.composer() == "" {
				t.Fatal("setup: the composer should still hold our chip")
			}
		})
	}
}
