package query

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall/enrich"
)

// The sweep drains the cheap classifiers, so every session has its three
// artifacts; a session whose content then moves shows them stale in every
// tier (card, brief, excerpt, and show) until the next drain.
func TestContext_TiersBudgetAndStaleMarkedInEveryTier(t *testing.T) {
	f := newFixture(t)
	f.sweep(t)
	s := New(f.st, f.stateDB)
	ctx := context.Background()

	card, err := s.Context(ctx, sessA, TierCard, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(card.Text, "title: auth fix") || !strings.Contains(card.Text, "hints: ticket=SB-412") || !strings.Contains(card.Text, "agent-deck session deck-a") {
		t.Fatalf("card:\n%s", card.Text)
	}
	if strings.Contains(card.Text, "Derived:") || strings.Contains(card.Text, "BEGIN RECALLED") || card.Included != 0 {
		t.Fatalf("the card tier must carry no brief or excerpt:\n%s", card.Text)
	}
	if card.Chars > 60*charsPerToken*3 {
		t.Fatalf("card is %d chars; the design says about 60 tokens", card.Chars)
	}

	brief, err := s.Context(ctx, sessA, TierBrief, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(brief.Artifacts) != len(enrich.CheapKinds) {
		t.Fatalf("the sweep must have drained the cheap classifiers: %+v", brief.Artifacts)
	}
	for _, a := range brief.Artifacts {
		if a.Stale {
			t.Fatalf("fresh artifact marked stale: %+v", a)
		}
		if !strings.Contains(brief.Text, a.Kind+": ") {
			t.Fatalf("brief lacks %s:\n%s", a.Kind, brief.Text)
		}
	}
	if !strings.Contains(brief.Text, "session_kind: interactive") || !strings.Contains(brief.Text, "outcome: unknown") || strings.Contains(brief.Text, "BEGIN RECALLED") {
		t.Fatalf("brief:\n%s", brief.Text)
	}

	full, err := s.Context(ctx, sessA, TierExcerpt, 0)
	if err != nil {
		t.Fatal(err)
	}
	if full.Included != 4 || full.Truncated || !strings.Contains(full.Text, "[USER]\nfix the flaky auth test SB-412") || !strings.Contains(full.Text, "[ASSISTANT]\nPinned the clock") {
		t.Fatalf("excerpt: included %d truncated %v\n%s", full.Included, full.Truncated, full.Text)
	}
	// A budget too small for every turn keeps the newest ones and says so.
	small, err := s.Context(ctx, sessA, TierExcerpt, (len(card.Text)+200+120)/charsPerToken)
	if err != nil {
		t.Fatal(err)
	}
	if !small.Truncated || small.Included >= 4 || !strings.Contains(small.Text, "older ones left out") || !strings.Contains(small.Text, "Pinned the clock") {
		t.Fatalf("budgeted excerpt: included %d truncated %v\n%s", small.Included, small.Truncated, small.Text)
	}
	if _, err := s.Context(ctx, sessA, "raw", 0); !errors.Is(err, ErrTier) {
		t.Fatalf("unknown tier: %v", err)
	}

	// The content moves: derived_rev bumps, the artifacts are stale in
	// every tier and in show, and the queue holds them for the next drain.
	path := f.roots[0].Dir + "/projects/-p/" + sessA + ".jsonl"
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString(`{"type":"user","message":{"role":"user","content":"one more turn"},"uuid":"x-9","timestamp":"2026-09-11T00:00:00Z","sessionId":"` + sessA + `","cwd":"/Users/x/app"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	if _, err := f.st.W.Exec(`UPDATE session SET derived_rev=derived_rev+1 WHERE native_id=?`, sessA); err != nil {
		t.Fatal(err)
	}
	for _, tier := range []string{TierCard, TierBrief, TierExcerpt} {
		res, err := s.Context(ctx, sessA, tier, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range res.Artifacts {
			if !a.Stale {
				t.Fatalf("%s tier: artifact %s not marked stale after derived_rev moved", tier, a.Kind)
			}
		}
		if tier != TierCard && !strings.Contains(res.Text, "[stale: session changed since") {
			t.Fatalf("%s tier text lacks the stale marker:\n%s", tier, res.Text)
		}
	}
	d, err := s.Show(ctx, sessA, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Artifacts) != len(enrich.CheapKinds) || !d.Artifacts[0].Stale || !strings.Contains(d.Artifacts[0].Line(), "[stale") {
		t.Fatalf("show card tier must carry the stale artifacts: %+v", d.Artifacts)
	}
	// The next sweep parses the appended turn, re-queues and re-drains.
	f.sweep(t)
	after, err := s.Context(ctx, sessA, TierBrief, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range after.Artifacts {
		if a.Stale {
			t.Fatalf("after the sweep's drain the artifact must be fresh: %+v", a)
		}
	}
}
