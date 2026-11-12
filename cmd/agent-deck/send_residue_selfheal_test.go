package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Self-heal tests for the per-target residue record. They need new API
// (sendResidueStore, sendExecTuning.residue/targetID, session.SendResidue) and
// do not compile on the base commit. The in-memory store below fixes the
// interface shape the implementation must satisfy:
//
//	type sendResidueStore interface {
//	    Load(instanceID string) (session.SendResidue, bool, error)
//	    Save(instanceID string, r session.SendResidue) error
//	    Clear(instanceID string) error
//	}
//
// If the implementation names things differently, adapt this one file.

type memResidueStore struct {
	mu   sync.Mutex
	recs map[string]session.SendResidue
}

func newMemResidueStore() *memResidueStore {
	return &memResidueStore{recs: map[string]session.SendResidue{}}
}

func (s *memResidueStore) Load(id string) (session.SendResidue, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[id]
	return r, ok, nil
}

func (s *memResidueStore) Save(id string, r session.SendResidue) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[id] = r
	return nil
}

func (s *memResidueStore) Clear(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.recs, id)
	return nil
}

const residueTarget = "inst-residue-1"

func residueTuning(noWait bool, store sendResidueStore) sendExecTuning {
	tun := issue2077Tuning(noWait)
	tun.residue = store
	tun.targetID = residueTarget
	return tun
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestResidue_LeftSendWritesRecordWithoutText(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			store := newMemResidueStore()
			// A swallowed Enter on a single-line message: the composer holds
			// exactly our text and Backspace does nothing.
			pane := &orphanPaneBS{orphanPane: &orphanPane{}, noop: true}
			res, err := executeSend(pane, "claude", orphanSingleLine, mode.noWait, residueTuning(mode.noWait, store))
			if err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("delivery=%q err=%v", res.delivery, err)
			}
			rec, ok, _ := store.Load(residueTarget)
			if !ok || !rec.Verified {
				t.Fatalf("record = %+v ok=%v, want a verified record", rec, ok)
			}
			if rec.DraftSHA256 != sha(send.NormalizePromptText(pane.composer())) {
				t.Fatalf("DraftSHA256 = %q does not match the normalized composer", rec.DraftSHA256)
			}
			if time.Since(rec.CreatedAt) > time.Minute {
				t.Fatalf("CreatedAt = %v", rec.CreatedAt)
			}
		})
	}
}

func TestResidue_VerifiedResidueIsClearedNotSubmitted(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			store := newMemResidueStore()
			inner := &orphanPane{}
			stuck := &orphanPaneBS{orphanPane: inner, noop: true}
			if res, err := executeSend(stuck, "claude", orphanSingleLine, mode.noWait, residueTuning(mode.noWait, store)); err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("setup: delivery=%q err=%v", res.delivery, err)
			}
			// The composer now accepts Backspace and Enter works.
			stuck.noop = false
			inner.configure(func(p *orphanPane) { p.submitOnEnter = true })
			_, entersBefore, _ := inner.counts()

			const next = "[inbox] a new transition notification is waiting"
			res, err := executeSend(stuck, "claude", next, mode.noWait, residueTuning(mode.noWait, store))
			if err != nil {
				t.Fatalf("follow-up refused: delivery=%q err=%v", res.delivery, err)
			}
			if res.jsonFields()["cleared_residue"] != true {
				t.Fatalf("fields = %v, want cleared_residue=true", res.jsonFields())
			}
			if len(inner.submitted) != 1 || inner.submitted[0] != next {
				t.Fatalf("submitted = %q, want only the new message (never the stale residue)", inner.submitted)
			}
			if _, enters, _ := inner.counts(); enters-entersBefore != 1 {
				t.Fatalf("Enter pressed %d times since the residue, want exactly 1 (for the new message)", enters-entersBefore)
			}
			if _, ok, _ := store.Load(residueTarget); ok {
				t.Fatal("record not cleared after the residue was removed")
			}
		})
	}
}

func TestResidue_UnverifiedResidueIsRefusedAndNamed(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			store := newMemResidueStore()
			pane := &orphanPaneBS{orphanPane: &orphanPane{suggestion: true, replaceWith: "HUMAN WIP: draft the release notes"}}
			if res, err := executeSend(pane, "claude", orphanSingleLine, mode.noWait, residueTuning(mode.noWait, store)); err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("setup: delivery=%q err=%v", res.delivery, err)
			}
			rec, ok, _ := store.Load(residueTarget)
			if !ok || rec.Verified {
				t.Fatalf("record = %+v ok=%v, want an unverified record", rec, ok)
			}
			before := pane.composer()
			_, entersBefore, bsBefore := pane.counts()
			res, err := executeSend(pane, "claude", "[inbox] next", mode.noWait, residueTuning(mode.noWait, store))
			if err == nil || res.delivery != "composer_blocked" {
				t.Fatalf("delivery=%q err=%v, want composer_blocked", res.delivery, err)
			}
			if !strings.Contains(err.Error(), "left by a failed agent-deck send") || !strings.Contains(err.Error(), "ago") {
				t.Fatalf("error %q must name the agent-deck origin and the age", err)
			}
			f := res.jsonFields()
			if f["composer_holds"] != "agent_deck_residue" {
				t.Fatalf("fields = %v, want composer_holds=agent_deck_residue", f)
			}
			if _, ok := f["residue_age_seconds"]; !ok {
				t.Fatalf("fields = %v, want residue_age_seconds", f)
			}
			if _, enters, bs := pane.counts(); enters != entersBefore || bs != bsBefore || pane.composer() != before {
				t.Fatalf("pane touched: enters %d->%d backspaces %d->%d composer %q->%q", entersBefore, enters, bsBefore, bs, before, pane.composer())
			}
		})
	}
}

func TestResidue_EditedResidueFallsBackToGenericRefusal(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			store := newMemResidueStore()
			inner := &orphanPane{truncateBy: 1}
			pane := &orphanPaneBS{orphanPane: inner, noop: true}
			if _, err := executeSend(pane, "claude", orphanMultiLine, mode.noWait, residueTuning(mode.noWait, store)); err == nil {
				t.Fatal("setup: want an error")
			}
			// A human edits the residue: it is a human draft now.
			inner.configure(func(p *orphanPane) { p.segs = append(p.segs, orphanSeg{lit: " plus my own words"}) })
			pane.noop = false
			before := inner.composer()
			_, entersBefore, bsBefore := inner.counts()
			res, err := executeSend(pane, "claude", "[inbox] next", mode.noWait, residueTuning(mode.noWait, store))
			if err == nil || res.delivery != "composer_blocked" {
				t.Fatalf("delivery=%q err=%v, want composer_blocked", res.delivery, err)
			}
			if strings.Contains(err.Error(), "left by a failed agent-deck send") || res.jsonFields()["composer_holds"] != nil {
				t.Fatalf("an edited draft must not be attributed to agent-deck: %v / %v", err, res.jsonFields())
			}
			if _, enters, bs := inner.counts(); enters != entersBefore || bs != bsBefore || inner.composer() != before {
				t.Fatalf("human-edited draft touched: enters %d->%d backspaces %d->%d composer %q->%q", entersBefore, enters, bsBefore, bs, before, inner.composer())
			}
		})
	}
}

func TestResidue_StaleRecordIsIgnoredAndHumanDraftNeverDeleted(t *testing.T) {
	const human = "a draft a human typed"
	store := newMemResidueStore()
	_ = store.Save(residueTarget, session.SendResidue{
		Version:     1,
		InstanceID:  residueTarget,
		DraftSHA256: sha(human),
		DraftLen:    len(human),
		Verified:    true,
		CreatedAt:   time.Now().Add(-25 * time.Hour),
	})
	inner := &orphanPane{segs: []orphanSeg{{lit: human}}}
	pane := &orphanPaneBS{orphanPane: inner}
	res, err := executeSend(pane, "claude", "[inbox] next", false, residueTuning(false, store))
	if err == nil || res.delivery != "composer_blocked" {
		t.Fatalf("delivery=%q err=%v, want composer_blocked", res.delivery, err)
	}
	if _, enters, bs := inner.counts(); enters != 0 || bs != 0 || inner.composer() != human {
		t.Fatalf("human draft touched: enters=%d backspaces=%d composer=%q", enters, bs, inner.composer())
	}
}

func TestResidue_NoRecordMeansHumanDraftIsNeverDeleted(t *testing.T) {
	const human = "run `go build` then report"
	inner := &orphanPane{segs: []orphanSeg{{lit: human}}}
	pane := &orphanPaneBS{orphanPane: inner}
	res, err := executeSend(pane, "claude", "[inbox] next", false, residueTuning(false, newMemResidueStore()))
	if err == nil || res.delivery != "composer_blocked" {
		t.Fatalf("delivery=%q err=%v", res.delivery, err)
	}
	if _, enters, bs := inner.counts(); enters != 0 || bs != 0 || inner.composer() != human {
		t.Fatalf("human draft touched")
	}
}

// Review finding 9: a residue that is only a paste chip is recorded
// unverified. "[Pasted text #1 +3 lines]" is a generic label, not unique to
// this send's text, so a later send must never remove it on the record's
// say-so.
func TestResidue_PasteChipResidueIsRecordedUnverified(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			store := newMemResidueStore()
			pane := &orphanPaneBS{orphanPane: &orphanPane{truncateBy: 1}, noop: true}
			res, err := executeSend(pane, "claude", orphanMultiLine, mode.noWait, residueTuning(mode.noWait, store))
			if err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("delivery=%q err=%v", res.delivery, err)
			}
			rec, ok, _ := store.Load(residueTarget)
			if !ok {
				t.Fatal("no residue record written")
			}
			if rec.Verified {
				t.Fatalf("record = %+v, want verified=false for a chip-only residue %q", rec, pane.composer())
			}
		})
	}
}

// Review finding 7: a record older than the 24h clear limit still names the
// residue's origin and age (records are kept up to 7 days), but never
// authorises removing it.
func TestResidue_DayOldVerifiedResidueIsNamedNotCleared(t *testing.T) {
	const ours = "[lane timer] wake: check the inbox and report"
	store := newMemResidueStore()
	_ = store.Save(residueTarget, session.SendResidue{
		Version:     1,
		InstanceID:  residueTarget,
		DraftSHA256: sha(ours),
		DraftLen:    len(ours),
		Verified:    true,
		Sender:      "sender-7",
		CreatedAt:   time.Now().Add(-30 * time.Hour),
	})
	inner := &orphanPane{segs: []orphanSeg{{lit: ours}}, submitOnEnter: true}
	pane := &orphanPaneBS{orphanPane: inner}
	res, err := executeSend(pane, "claude", "[inbox] next", false, residueTuning(false, store))
	if err == nil || res.delivery != "composer_blocked" {
		t.Fatalf("delivery=%q err=%v, want composer_blocked", res.delivery, err)
	}
	if !strings.Contains(err.Error(), "left by a failed agent-deck send") || !strings.Contains(err.Error(), "sender-7") {
		t.Fatalf("error %q must name the agent-deck origin, sender and age", err)
	}
	if res.jsonFields()["composer_holds"] != "agent_deck_residue" {
		t.Fatalf("fields = %v, want composer_holds=agent_deck_residue", res.jsonFields())
	}
	if _, enters, bs := inner.counts(); enters != 0 || bs != 0 || inner.composer() != ours {
		t.Fatalf("a day-old residue was touched: enters=%d backspaces=%d composer=%q", enters, bs, inner.composer())
	}
}
