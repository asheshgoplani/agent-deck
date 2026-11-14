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

// First review finding 1: a person's paragraph after our text must survive
// and make the leftover unverified (every paragraph is read).
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

// An input box that reads empty after the refusal was emptied by something
// other than this send (it may have been submitted): composer_residue
// "absent", typed_not_submitted, which no queue retries.
func TestOrphanReview_ComposerEmptiedByOtherIsAbsent(t *testing.T) {
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
			requireOrphanResidue(t, res, "absent")
			if !strings.Contains(err.Error(), "may have been submitted") {
				t.Fatalf("error %q must say the message may have been submitted", err)
			}
		})
	}
}

// A pre-Enter refusal after which a menu covers the pane (the one capture
// that reports cannot read the input box) still reports the text left.
func TestOrphanReview_MenuAfterRefusalKeepsResidue(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := newReviewPane(&orphanPane{truncateBy: 1})
			// capture 1 after typing: the pre-Enter check; 2: the report's
			// one look, which a menu now covers.
			pane.menuAfterCaps = 2
			tun := issue2077Tuning(mode.noWait)
			tun.requireInputPrompt = true
			res, err := executeSend(pane, "claude", orphanMultiLine, mode.noWait, tun)
			if err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("delivery=%q err=%v, want typed_not_submitted", res.delivery, err)
			}
			if f := requireOrphanResidue(t, res, "left"); f["composer_residue_verified"] != false {
				t.Fatalf("composer_residue_verified = %v, want false (unreadable)", f["composer_residue_verified"])
			}
			if _, enters, bs := pane.counts(); enters != 0 || bs != 0 {
				t.Fatalf("pressed Enter %d times, Backspace %d times", enters, bs)
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

// guardedReviewPane adds the tmux transport's guarded send: the prompt
// guard's before() runs ahead of the final Enter, after the post-paste check
// already passed (internal/tmux sendKeysAndEnterCheckedToTarget).
type guardedReviewPane struct{ *reviewPane }

func (g *guardedReviewPane) SendKeysAndEnterCheckedGuarded(keys string, capture func() (string, error), check tmux.PostPasteCheck, before func() error) error {
	// before() runs ahead of every keystroke batch: the body, then Enter.
	if err := before(); err != nil {
		return err
	}
	g.typeBody(keys)
	pane, capErr := capture()
	if ok, err := check(pane, capErr); !ok {
		if err == nil {
			err = errors.New("withheld")
		}
		return err
	}
	if err := before(); err != nil {
		return err
	}
	return g.SendEnter()
}

// Third review, finding 1: a menu that opens after the post-paste check
// passed makes the prompt guard withhold the final Enter. That is the
// guard's refusal (its "typed, not submitted" evidence), not a failed Enter.
func TestOrphanReview_GuardWithholdsFinalEnterIsGuardRefusal(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := &guardedReviewPane{newReviewPane(&orphanPane{})}
			// capture 1 after typing: the post-paste check (clean); 2: the
			// guard's look before the final Enter, which a menu now covers.
			pane.menuAfterCaps = 2
			tun := issue2077Tuning(mode.noWait)
			tun.requireInputPrompt = true
			res, err := executeSend(pane, "claude", orphanMultiLine, mode.noWait, tun)
			if err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("delivery=%q err=%v, want typed_not_submitted", res.delivery, err)
			}
			if strings.Contains(err.Error(), "enter could not be sent") {
				t.Fatalf("error %q calls the guard's refusal a failed Enter", err)
			}
			if !strings.Contains(err.Error(), "typed, not submitted") {
				t.Fatalf("error %q lost the guard's typed-not-submitted evidence", err)
			}
			if _, enters, bs := pane.counts(); enters != 0 || bs != 0 {
				t.Fatalf("pressed Enter %d times, Backspace %d times", enters, bs)
			}
		})
	}
}
