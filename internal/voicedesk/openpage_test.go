package voicedesk

import (
	"context"
	"testing"
)

type pageFake struct {
	*fakeDeck
	opened []string
	pages  map[string]Page
}

func (f *pageFake) OpenPage(_ context.Context, s Session) (Page, error) {
	if s.Host != "" {
		return Page{}, ErrUnsupported
	}
	pg, ok := f.pages[s.ID]
	if !ok {
		return Page{}, ErrNoPage
	}
	f.opened = append(f.opened, s.ID)
	return pg, nil
}

func pageHarness(t *testing.T) (*harness, *pageFake) {
	h := newHarness(t)
	pf := &pageFake{fakeDeck: h.deck, pages: map[string]Page{
		idAudit:     {Title: "Audit report", Path: "/x/audit.html", Where: "both"},
		idConductor: {Title: "Tonight's plan", Path: "/x/plan.md", Where: "mac"},
	}}
	h.desk.Deck = pf
	return h, pf
}

func TestOpenPage(t *testing.T) {
	rows := []struct {
		sentence, on, lang, target, say, refused string
	}{
		{"open the page from proactive audit", "", LangEN, idAudit, "Opening Audit report from proactive audit.", ""},
		{"open proactive audit's page", "", LangEN, idAudit, "Opening Audit report from proactive audit.", ""},
		{"show me what proactive audit made", "", LangEN, idAudit, "Opening Audit report from proactive audit.", ""},
		{"open the report from deck conductor", "", LangEN, idConductor, "Opening Tonight's plan from conductor deck alex.", ""},
		{"proactive audit ka page kholo", "", LangHI, idAudit, "Opening Audit report from proactive audit.", ""},
		{"deck conductor ne jo banaya woh dikhao", "", LangHI, idConductor, "Opening Tonight's plan from conductor deck alex.", ""},
		{"jo page banaya woh kholo", idAudit, LangHI, idAudit, "Opening Audit report from proactive audit.", ""},
		{"jo page banaya woh kholo", "", LangHI, "", "Which session's page? Say its name.", RefusedNotFound},
		{"open the page from voice test", "", LangEN, idVoiceTest, "voice test has made no page yet.", ""},
		{"open the page from conductor boxone", "", LangEN, idBoxCond, "Pages of conductor boxone on boxone cannot be opened by voice yet.", ""},
		{"open the page from conductor deck sam", "", LangEN, "", "That is Sam's session.", RefusedOtherPerson},
	}
	for _, r := range rows {
		t.Run(r.sentence+"/"+r.on, func(t *testing.T) {
			h, _ := pageHarness(t)
			got := h.do(Request{Sentence: r.sentence, On: r.on})
			if got.Intent != IntentOpenPage || got.Lang != r.lang || targetID(got) != r.target || got.Say != r.say || refusedOf(got) != r.refused || got.Confirm != nil {
				t.Fatalf("got intent=%s lang=%s target=%s refused=%s say=%q", got.Intent, got.Lang, targetID(got), refusedOf(got), got.Say)
			}
		})
	}
}

// The daemon's own page commands stay the daemon's.
func TestOpenPageNotMine(t *testing.T) {
	for _, s := range []string{"open the page", "page kholo", "open browser", "show files", "open the page in the browser"} {
		h, pf := pageHarness(t)
		if got := h.say(s); got.Handled || len(pf.opened) != 0 {
			t.Fatalf("stole %q: %+v", s, got)
		}
	}
}
