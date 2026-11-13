package main

import (
	"strings"
	"testing"
)

// The leftover record is a hash match, not proof of authorship. A refusal
// must never tell a person that text which may be theirs is "agent-deck's
// own" message: a person (or a conductor) told that may clear it.

// The incident's shape: our typing was replaced, so the input box holds only
// a person's draft. The record of that failed send is unverified. The next
// refusal must not call the box agent-deck's own message, and its
// composer_holds must not be agent_deck_residue.
func TestResidueLabel_UnverifiedMatchIsNeverCalledAgentDecksOwn(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			store := newMemResidueStore()
			pane := &orphanPaneBS{orphanPane: &orphanPane{suggestion: true, replaceWith: "HUMAN WIP: draft the release notes"}}
			if res, err := executeSend(pane, "claude", orphanSingleLine, mode.noWait, residueTuning(mode.noWait, store)); err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("setup: delivery=%q err=%v", res.delivery, err)
			}
			before := pane.composer()
			_, entersBefore, bsBefore := pane.counts()
			res, err := executeSend(pane, "claude", "[inbox] next", mode.noWait, residueTuning(mode.noWait, store))
			if err == nil || res.delivery != "composer_blocked" {
				t.Fatalf("delivery=%q err=%v, want composer_blocked", res.delivery, err)
			}
			if strings.Contains(err.Error(), "agent-deck's own") {
				t.Fatalf("a person's draft is called agent-deck's own message: %q", err)
			}
			f := res.jsonFields()
			if f["composer_holds"] != composerHoldsAgentDeckResidueUnverified {
				t.Fatalf("composer_holds = %v, want agent_deck_residue_unverified", f["composer_holds"])
			}
			for _, want := range []string{"may be partly or wholly a person's", "check it before clearing", "nothing was typed"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q lacks %q", err, want)
				}
			}
			requireUntouched(t, pane.orphanPane, entersBefore, bsBefore, before)
		})
	}
}

// A verified match is still only a hash match: a person may have typed or
// recalled the same text since. The refusal says so.
func TestResidueLabel_VerifiedMatchSaysCheckBeforeClearing(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			store := newMemResidueStore()
			inner := &orphanPane{}
			stuck := &orphanPaneBS{orphanPane: inner}
			if res, err := executeSend(stuck, "claude", orphanSingleLine, mode.noWait, residueTuning(mode.noWait, store)); err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("setup: delivery=%q err=%v", res.delivery, err)
			}
			res, err := executeSend(stuck, "claude", "[inbox] next", mode.noWait, residueTuning(mode.noWait, store))
			if err == nil || res.delivery != "composer_blocked" {
				t.Fatalf("delivery=%q err=%v, want composer_blocked", res.delivery, err)
			}
			if res.jsonFields()["composer_holds"] != "agent_deck_residue" {
				t.Fatalf("fields = %v", res.jsonFields())
			}
			for _, want := range []string{"matches the unsent message", "if a person typed or recalled the same text since, it is theirs", "check it before clearing"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

// A queue worker's child inherits the worker's AGENTDECK_INSTANCE_ID, which
// is not the queued message's sender: a leftover must never name it.
func TestResidueLabel_QueueWorkerNeverNamesInheritedSender(t *testing.T) {
	cases := []struct {
		name               string
		noTag, queueWorker bool
		want               string
	}{
		{"direct send names its sender", false, false, "sender-7"},
		{"--no-tag names no one", true, false, ""},
		{"queue worker child names no one", false, true, ""},
	}
	for _, c := range cases {
		if got := residueSenderFor("sender-7", c.noTag, c.queueWorker); got != c.want {
			t.Errorf("%s: residueSenderFor = %q, want %q", c.name, got, c.want)
		}
	}
}
