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

// The per-target leftover record (hash only) makes later refusals
// informative: "matches the unsent message an agent-deck send from <sender>
// left there <age> ago", the same text "never verified as agent-deck's",
// "unknown text" or "other text (a person's draft?)". It never authorises a
// key press: every test here also asserts that the follow-up send touched
// nothing (PR 3123 second review, finding 4: a self-heal deleted a draft a
// person had deliberately put back).

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

const (
	residueTarget = "inst-residue-1"
	residueSender = "sender-1"
)

func residueTuning(noWait bool, store sendResidueStore) sendExecTuning {
	tun := issue2077Tuning(noWait)
	tun.residue = store
	tun.targetID = residueTarget
	tun.residueSender = residueSender
	return tun
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// requireUntouched fails when the follow-up pressed any key or changed the
// input box.
func requireUntouched(t *testing.T, p *orphanPane, entersBefore, bsBefore int, before string) {
	t.Helper()
	if _, enters, bs := p.counts(); enters != entersBefore || bs != bsBefore || p.composer() != before {
		t.Fatalf("input box touched: enters %d->%d backspaces %d->%d composer %q->%q", entersBefore, enters, bsBefore, bs, before, p.composer())
	}
}

func TestResidue_LeftSendWritesRecordWithoutText(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			store := newMemResidueStore()
			pane := &orphanPaneBS{orphanPane: &orphanPane{}}
			res, err := executeSend(pane, "claude", orphanSingleLine, mode.noWait, residueTuning(mode.noWait, store))
			if err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("delivery=%q err=%v", res.delivery, err)
			}
			rec, ok, _ := store.Load(residueTarget)
			if !ok || !rec.Verified || rec.Sender != residueSender {
				t.Fatalf("record = %+v ok=%v, want a verified record from %s", rec, ok, residueSender)
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

// The follow-up names agent-deck's own leftover, its sender and age, and
// touches nothing (it used to be removed by a "self-heal").
func TestResidue_OwnLeftoverIsNamedNeverCleared(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			store := newMemResidueStore()
			inner := &orphanPane{}
			stuck := &orphanPaneBS{orphanPane: inner}
			if res, err := executeSend(stuck, "claude", orphanSingleLine, mode.noWait, residueTuning(mode.noWait, store)); err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("setup: delivery=%q err=%v", res.delivery, err)
			}
			inner.configure(func(p *orphanPane) { p.submitOnEnter = true })
			before := inner.composer()
			_, entersBefore, bsBefore := inner.counts()
			res, err := executeSend(stuck, "claude", "[inbox] a new transition notification is waiting", mode.noWait, residueTuning(mode.noWait, store))
			if err == nil || res.delivery != "composer_blocked" {
				t.Fatalf("delivery=%q err=%v, want composer_blocked", res.delivery, err)
			}
			f := res.jsonFields()
			if f["composer_holds"] != "agent_deck_residue" || f["residue_sender"] != residueSender || f["composer_residue"] != "none" {
				t.Fatalf("fields = %v, want composer_holds=agent_deck_residue, residue_sender, composer_residue=none", f)
			}
			if _, ok := f["residue_age_seconds"]; !ok {
				t.Fatalf("fields = %v, want residue_age_seconds", f)
			}
			for _, want := range []string{"the input box matches the unsent message an agent-deck send from " + residueSender + " left there ", " ago (if a person typed or recalled the same text since, it is theirs); nothing was typed; check it before clearing, then submit or clear it"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q lacks %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "mixed with other text") {
				t.Fatalf("a verified leftover must not say mixed: %q", err)
			}
			if len(inner.submitted) != 0 {
				t.Fatalf("submitted = %q, want nothing", inner.submitted)
			}
			requireUntouched(t, inner, entersBefore, bsBefore, before)
			if _, ok, _ := store.Load(residueTarget); !ok {
				t.Fatal("record dropped while the leftover is still there")
			}
		})
	}
}

func TestResidue_UnverifiedLeftoverIsNamedMixed(t *testing.T) {
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
			if strings.Contains(err.Error(), "agent-deck's own") || !strings.Contains(err.Error(), "never verified as agent-deck's and may be partly or wholly a person's draft") {
				t.Fatalf("error %q must name the failed send without calling the text agent-deck's own", err)
			}
			if res.jsonFields()["composer_holds"] != "agent_deck_residue_unverified" {
				t.Fatalf("fields = %v", res.jsonFields())
			}
			requireUntouched(t, pane.orphanPane, entersBefore, bsBefore, before)
		})
	}
}

// A person edited the leftover: the hash no longer matches, so it is
// "unknown text", never attributed to agent-deck.
func TestResidue_EditedLeftoverIsUnknownText(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			store := newMemResidueStore()
			inner := &orphanPane{truncateBy: 1}
			pane := &orphanPaneBS{orphanPane: inner}
			if _, err := executeSend(pane, "claude", orphanMultiLine, mode.noWait, residueTuning(mode.noWait, store)); err == nil {
				t.Fatal("setup: want an error")
			}
			inner.configure(func(p *orphanPane) { p.segs = append(p.segs, orphanSeg{lit: " plus my own words"}) })
			before := inner.composer()
			_, entersBefore, bsBefore := inner.counts()
			res, err := executeSend(pane, "claude", "[inbox] next", mode.noWait, residueTuning(mode.noWait, store))
			if err == nil || res.delivery != "composer_blocked" {
				t.Fatalf("delivery=%q err=%v, want composer_blocked", res.delivery, err)
			}
			if strings.Contains(err.Error(), "agent-deck's own unsent message") || res.jsonFields()["composer_holds"] != "unknown_text" {
				t.Fatalf("an edited draft must be unknown text: %v / %v", err, res.jsonFields())
			}
			for _, want := range []string{"unknown text", "nothing was typed", "existing draft preserved"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q lacks %q", err, want)
				}
			}
			requireUntouched(t, inner, entersBefore, bsBefore, before)
		})
	}
}

// A record past the 7-day keep age is ignored (and dropped): the box holds
// "other text".
func TestResidue_ExpiredRecordIsOtherTextAndDropped(t *testing.T) {
	const human = "a draft a human typed"
	store := newMemResidueStore()
	_ = store.Save(residueTarget, session.SendResidue{
		Version: 1, InstanceID: residueTarget, DraftSHA256: sha(human), DraftLen: len(human),
		Verified: true, CreatedAt: time.Now().Add(-8 * 24 * time.Hour),
	})
	inner := &orphanPane{segs: []orphanSeg{{lit: human}}}
	pane := &orphanPaneBS{orphanPane: inner}
	res, err := executeSend(pane, "claude", "[inbox] next", false, residueTuning(false, store))
	if err == nil || res.delivery != "composer_blocked" || res.jsonFields()["composer_holds"] != "other_text" {
		t.Fatalf("delivery=%q err=%v fields=%v, want composer_blocked, other_text", res.delivery, err, res.jsonFields())
	}
	requireUntouched(t, inner, 0, 0, human)
	if _, ok, _ := store.Load(residueTarget); ok {
		t.Fatal("expired record not dropped")
	}
}

func TestResidue_NoRecordIsOtherText(t *testing.T) {
	const human = "run `go build` then report"
	inner := &orphanPane{segs: []orphanSeg{{lit: human}}}
	pane := &orphanPaneBS{orphanPane: inner}
	res, err := executeSend(pane, "claude", "[inbox] next", false, residueTuning(false, newMemResidueStore()))
	if err == nil || res.delivery != "composer_blocked" {
		t.Fatalf("delivery=%q err=%v", res.delivery, err)
	}
	if res.jsonFields()["composer_holds"] != "other_text" || !strings.Contains(err.Error(), "other text (a person's draft?)") ||
		!strings.Contains(err.Error(), "composer is occupied or unreadable; existing draft preserved") {
		t.Fatalf("err=%v fields=%v", err, res.jsonFields())
	}
	requireUntouched(t, inner, 0, 0, human)
}

// Review finding 4: a person recalls (Up arrow) or retypes agent-deck's
// leftover within hours. It hashes to the record, but it is the person's
// draft now: it is named, never deleted, never submitted.
func TestResidue_RecalledDraftWithinHoursIsNeverDeleted(t *testing.T) {
	const recalled = "Please continue."
	store := newMemResidueStore()
	_ = store.Save(residueTarget, session.SendResidue{
		Version: 1, InstanceID: residueTarget, DraftSHA256: sha(recalled), DraftLen: len(recalled),
		Verified: true, Sender: "sender-7", CreatedAt: time.Now().Add(-1 * time.Hour),
	})
	inner := &orphanPane{segs: []orphanSeg{{lit: recalled}}, submitOnEnter: true}
	pane := &orphanPaneBS{orphanPane: inner}
	res, err := executeSend(pane, "claude", "[inbox] next", false, residueTuning(false, store))
	if err == nil || res.delivery != "composer_blocked" {
		t.Fatalf("delivery=%q err=%v, want composer_blocked", res.delivery, err)
	}
	if !strings.Contains(err.Error(), "from sender-7 left there 1h0m") || !strings.Contains(err.Error(), "if a person typed or recalled the same text since, it is theirs") {
		t.Fatalf("error %q must name the sender and age, and say a recalled draft is the person's", err)
	}
	requireUntouched(t, inner, 0, 0, recalled)
	if len(inner.submitted) != 0 {
		t.Fatalf("submitted %q", inner.submitted)
	}
}

// A leftover that is only a paste chip is recorded unverified: the label is
// generic, not unique to this send's text.
func TestResidue_PasteChipResidueIsRecordedUnverified(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			store := newMemResidueStore()
			pane := &orphanPaneBS{orphanPane: &orphanPane{truncateBy: 1}}
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

// A days-old record still names the leftover (records are kept 7 days) and
// never touches it.
func TestResidue_DaysOldLeftoverIsNamedNotCleared(t *testing.T) {
	const ours = "[lane timer] wake: check the inbox and report"
	store := newMemResidueStore()
	_ = store.Save(residueTarget, session.SendResidue{
		Version: 1, InstanceID: residueTarget, DraftSHA256: sha(ours), DraftLen: len(ours),
		Verified: true, Sender: "sender-7", CreatedAt: time.Now().Add(-30 * time.Hour),
	})
	inner := &orphanPane{segs: []orphanSeg{{lit: ours}}, submitOnEnter: true}
	pane := &orphanPaneBS{orphanPane: inner}
	res, err := executeSend(pane, "claude", "[inbox] next", false, residueTuning(false, store))
	if err == nil || res.delivery != "composer_blocked" {
		t.Fatalf("delivery=%q err=%v, want composer_blocked", res.delivery, err)
	}
	if !strings.Contains(err.Error(), "matches the unsent message an agent-deck send from sender-7 left there 30h0m") {
		t.Fatalf("error %q must name the agent-deck origin, sender and age", err)
	}
	if res.jsonFields()["composer_holds"] != "agent_deck_residue" {
		t.Fatalf("fields = %v, want composer_holds=agent_deck_residue", res.jsonFields())
	}
	requireUntouched(t, inner, 0, 0, ours)
}

// A later send whose guard sees the input box empty expires the record.
func TestResidue_EmptyBoxExpiresRecord(t *testing.T) {
	store := newMemResidueStore()
	inner := &orphanPane{}
	pane := &orphanPaneBS{orphanPane: inner}
	if res, err := executeSend(pane, "claude", orphanSingleLine, false, residueTuning(false, store)); err == nil || res.delivery != "typed_not_submitted" {
		t.Fatalf("setup: delivery=%q err=%v", res.delivery, err)
	}
	if _, ok, _ := store.Load(residueTarget); !ok {
		t.Fatal("setup: no record")
	}
	inner.configure(func(p *orphanPane) { p.segs = nil; p.submitOnEnter = true })
	if res, err := executeSend(pane, "claude", "[inbox] next", false, residueTuning(false, store)); err != nil {
		t.Fatalf("follow-up: delivery=%q err=%v", res.delivery, err)
	}
	if _, ok, _ := store.Load(residueTarget); ok {
		t.Fatal("record survived a send that saw the input box empty")
	}
}
