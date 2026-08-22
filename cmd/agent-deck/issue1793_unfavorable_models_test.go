package main

import (
	"strings"
	"sync/atomic"
	"testing"
)

// TestIssue1793SubmitConfirmUnfavorableMatrix exercises the three outcomes a
// favorable swallowed-Enter fixture cannot model. Both supported composer
// glyphs use the same observer and must either prove exactly one turn started
// or fail without pressing a recovery Enter into ambiguous UI.
func TestIssue1793SubmitConfirmUnfavorableMatrix(t *testing.T) {
	for _, tc := range []struct{ name, glyph string }{{"claude", "❯"}, {"codex", "›"}} {
		t.Run(tc.name, func(t *testing.T) {
			msg := "REVIEW-NONCE-" + tc.name

			t.Run("late_acceptance", func(t *testing.T) {
				m := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{
					"BUSY\n" + tc.glyph + " \n",                  // pre-send baseline
					"BUSY\n" + tc.glyph + " " + msg,              // stale frame: initial Enter queued
					"BUSY\nGOT " + msg + "\n" + tc.glyph + " \n", // re-check: accepted
				}}
				delivery, err := sendWithRetryTarget(m, msg, true, sendRetryOptions{maxRetries: 3})
				if err != nil || delivery != deliverySubmitted {
					t.Fatalf("delivery=%q err=%v", delivery, err)
				}
				if got := atomic.LoadInt32(&m.sendEnterCalls); got != 0 {
					t.Fatalf("late initial acceptance must not receive recovery Enter; got %d", got)
				}
			})

			t.Run("dialog_open", func(t *testing.T) {
				dialog := "BUSY\nSelect permission [Allow] [Deny]\n" + msg + "\n"
				if tc.name == "codex" {
					dialog = "BUSY\nWould you like to run this command?\n› 1. Yes, proceed\n  2. No, cancel\n" + msg + "\n"
				}
				m := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{"BUSY\n" + tc.glyph + " \n", dialog}}
				delivery, err := sendWithRetryTarget(m, msg, true, sendRetryOptions{maxRetries: 2})
				if err == nil || delivery != deliveryTyped || !strings.Contains(err.Error(), "dialog open, not submitted") {
					t.Fatalf("delivery=%q err=%v", delivery, err)
				}
				if got := atomic.LoadInt32(&m.sendEnterCalls); got != 0 {
					t.Fatalf("dialog received %d recovery Enters", got)
				}
			})

			t.Run("unconfirmable", func(t *testing.T) {
				m := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{"BUSY\n" + tc.glyph + " \n", "BUSY; no composer and no nonce\n"}}
				delivery, err := sendWithRetryTarget(m, msg, true, sendRetryOptions{maxRetries: 2})
				if err == nil || delivery != deliveryNoEvidence {
					t.Fatalf("delivery=%q err=%v", delivery, err)
				}
				if got := atomic.LoadInt32(&m.sendEnterCalls); got != 0 {
					t.Fatalf("unconfirmable send received %d recovery Enters", got)
				}
			})
		})
	}
}

// These transcripts are the two round-2 review counterexamples.  Keep them
// here verbatim enough to ensure the observer cannot again use pane status as
// per-send evidence, or turn a stale composer frame into a duplicate submit.
func TestIssue1793Round2ReviewTranscripts(t *testing.T) {
	t.Run("late_initial_enter_never_gets_a_second_enter", func(t *testing.T) {
		const msg = "REVIEW-NONCE-late-enter"
		m := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{
			"❯ \n", // baseline
			"❯ " + msg + "\n",
			"❯ " + msg + "\n", // independently fresh, but still stale
			"GOT " + msg + "\n❯ \n",
		}}
		delivery, err := sendWithRetryTarget(m, msg, true, sendRetryOptions{maxRetries: 3})
		if err != nil || delivery != deliverySubmitted {
			t.Fatalf("delivery=%q err=%v", delivery, err)
		}
		if got := atomic.LoadInt32(&m.sendEnterCalls); got != 0 {
			t.Fatalf("late initial acceptance received %d recovery Enters", got)
		}
	})

	t.Run("already_busy_status_is_not_this_send", func(t *testing.T) {
		const msg = "REVIEW-NONCE-busy-pane"
		m := &mockSendRetryTarget{statuses: []string{"waiting", "active"}, panes: []string{
			"BUSY old turn\n› \n", // baseline
			"BUSY old turn\n› " + msg + "\n",
		}}
		delivery, err := sendWithRetryTarget(m, msg, true, sendRetryOptions{maxRetries: 1})
		if err == nil || delivery != deliveryTyped {
			t.Fatalf("status false-positive: delivery=%q err=%v", delivery, err)
		}
	})
}
