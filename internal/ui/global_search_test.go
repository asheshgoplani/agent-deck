package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/ingest"
	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// stubRecall is a RecallSource with canned answers: the overlay is tested
// (and its frames pinned) without a recall.db.
type stubRecall struct {
	hits      []query.Hit
	detail    map[int64]query.Detail
	status    query.Status
	refresh   []ingest.Result // one per Refresh call, in order
	refreshOK bool
	searchErr error
	searches  []string
	refreshes []bool
	closed    bool
}

func (s *stubRecall) Search(_ context.Context, q string, limit int) (query.SearchResult, error) {
	s.searches = append(s.searches, q)
	if s.searchErr != nil {
		return query.SearchResult{}, s.searchErr
	}
	hits := s.hits
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return query.SearchResult{Query: q, Hits: hits, Candidates: 7, ElapsedMS: 3}, nil
}

func (s *stubRecall) Show(_ context.Context, sessID int64, _ int) (query.Detail, error) {
	d, ok := s.detail[sessID]
	if !ok {
		return query.Detail{}, query.ErrNotFound
	}
	return d, nil
}

func (s *stubRecall) Status(context.Context) (query.Status, error) { return s.status, nil }

func (s *stubRecall) Refresh(_ context.Context, gated bool) (ingest.Result, error) {
	s.refreshes = append(s.refreshes, gated)
	if len(s.refresh) == 0 {
		return ingest.Result{}, nil
	}
	r := s.refresh[0]
	s.refresh = s.refresh[1:]
	return r, nil
}

func (s *stubRecall) Close() { s.closed = true }

var fixedEnd = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC).Unix()

func newStubRecall() *stubRecall {
	return &stubRecall{
		hits: []query.Hit{
			{SessID: 41, Harness: "claude", Profile: "personal", NativeID: "3fec37ee-811e-48c4-ba90-552abd93d9c7", DeckID: "auth-fix", Title: "Auth fix review",
				CWD: "/Users/x/proj", EndedAt: fixedEnd, BodyHits: 3, CardHit: true, Snippet: "the root cause was clock skew, not the retry budget"},
			{SessID: 42, Harness: "codex", NativeID: "01a0b956-e4be-7301-8f3c-3f1e39ab75b0", Title: "Fix flaky auth", CWD: "/Users/x/proj", EndedAt: fixedEnd - 86400, BodyHits: 1},
			{SessID: 43, Harness: "claude", Profile: "work", NativeID: "aaaaaaaa-0000-4000-8000-000000000001", CWD: "/Users/x/other", EndedAt: fixedEnd - 7*86400, BodyHits: 2, Missing: true},
		},
		detail: map[int64]query.Detail{
			41: {Session: query.SessionRow{SessID: 41, Turns: 12, ToolCalls: 30, Errors: 1, Model: "claude-opus-5", Hints: "ticket=SB-412", Tags: "auth"},
				Messages: []query.Message{
					{Seq: 1, Role: "user", Text: "Review PR 2308 for the auth fix"},
					{Seq: 2, Role: "assistant", Text: "Reading the diff. The root cause was clock skew."},
				}, Truncated: 10},
		},
		status: query.Status{Sessions: 1793, Messages: 151524, LastSweep: time.Now().Add(-3 * time.Minute).Unix()},
	}
}

// drain runs a tea.Cmd chain synchronously until it yields nothing.
func drain(gs *GlobalSearch, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		cmd = nil
		switch m := msg.(type) {
		case nil:
		case tea.BatchMsg:
			for _, c := range m {
				drain(gs, c)
			}
		default:
			gs, cmd = gs.Update(m)
		}
	}
}

func TestGlobalSearchVisibility(t *testing.T) {
	gs := NewGlobalSearch()
	if gs.IsVisible() {
		t.Error("GlobalSearch should not be visible initially")
	}
	if cmd := gs.Show(); cmd != nil {
		t.Error("without a source Show schedules nothing")
	}
	if !gs.IsVisible() {
		t.Error("GlobalSearch should be visible after Show()")
	}
	gs.Hide()
	if gs.IsVisible() {
		t.Error("GlobalSearch should not be visible after Hide()")
	}
}

func TestGlobalSearchKeyHandling(t *testing.T) {
	gs := NewGlobalSearch()
	gs.Show()
	gs.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if gs.IsVisible() {
		t.Error("Escape should hide GlobalSearch")
	}
}

// TestGlobalSearch_ShowRefreshesThenSearches: opening runs the status
// read and one ungated bounded sweep; a deferral continues gated; typing
// searches after the debounce; moving the cursor loads a preview once.
func TestGlobalSearch_ShowRefreshesThenSearches(t *testing.T) {
	fastCatchUp(t)
	src := newStubRecall()
	src.refresh = []ingest.Result{{Deferred: 2, DeferredBytes: 5 << 20}, {}}
	gs := NewGlobalSearch()
	gs.SetSource(src)
	gs.SetSize(160, 45)
	drain(gs, gs.Show())
	if len(src.refreshes) != 2 || src.refreshes[0] || !src.refreshes[1] {
		t.Fatalf("refresh passes %v: want one ungated pass then a gated continuation", src.refreshes)
	}
	if gs.refresh != "" || !gs.hasStat || gs.status.Sessions != 1793 {
		t.Fatalf("after the passes: refresh %q stat %v", gs.refresh, gs.status)
	}
	// Type a query: the search waits for the debounce, then runs once.
	for _, r := range "clock skew" {
		_, cmd := gs.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if r == 'w' { // the last keystroke's tick is the one that fires
			drain(gs, cmd)
		}
	}
	if len(src.searches) != 1 || src.searches[0] != "clock skew" {
		t.Fatalf("searches %v", src.searches)
	}
	if len(gs.results) != 3 || gs.results[0].Title != "Auth fix review" || gs.results[0].DeckID != "auth-fix" {
		t.Fatalf("results: %+v", gs.results)
	}
	if _, ok := gs.preview[41]; !ok {
		t.Fatal("the selected hit's preview was not loaded")
	}
	_, cmd := gs.Update(tea.KeyMsg{Type: tea.KeyDown})
	drain(gs, cmd)
	if gs.cursor != 1 || gs.Selected().Harness != "codex" {
		t.Fatalf("cursor %d selected %+v", gs.cursor, gs.Selected())
	}
	_, cmd = gs.Update(tea.KeyMsg{Type: tea.KeyDown})
	drain(gs, cmd)
	_, cmd = gs.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd != nil || gs.cursor != 2 {
		t.Fatalf("cursor must stop at the last row: %d", gs.cursor)
	}
	// Enter closes; the parent reads Selected.
	gs.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if gs.IsVisible() || gs.Selected().SessID != 43 {
		t.Fatalf("enter: visible %v selected %+v", gs.IsVisible(), gs.Selected())
	}
}

// fastCatchUp shortens the pause between catch-up passes so drain does not
// sleep; the pacing itself is asserted by TestGlobalSearch_CatchUpIsPaced.
func fastCatchUp(t *testing.T) {
	t.Helper()
	old := recallCatchUpDelay
	recallCatchUpDelay = time.Millisecond
	t.Cleanup(func() { recallCatchUpDelay = old })
}

// TestGlobalSearch_CatchUpIsPaced: a deferred pass does not chain the next
// gated pass immediately; it schedules a tick and the pass runs when the
// tick fires (and only while the overlay is still open and sweeping).
func TestGlobalSearch_CatchUpIsPaced(t *testing.T) {
	src := newStubRecall()
	gs := NewGlobalSearch()
	gs.SetSource(src)
	gs.SetSize(120, 40)
	gs.Show()
	_, cmd := gs.Update(recallRefreshMsg{res: ingest.Result{Deferred: 4, DeferredBytes: 1 << 20}})
	if cmd == nil {
		t.Fatal("a deferred pass must schedule the continuation")
	}
	if len(src.refreshes) != 0 {
		t.Fatalf("the next pass must wait for the tick, got %v", src.refreshes)
	}
	if !gs.sweeping || !strings.Contains(gs.staleLine(), "catching up") {
		t.Fatalf("stale line %q sweeping %v", gs.staleLine(), gs.sweeping)
	}
	// The tick fires: one gated pass.
	_, cmd = gs.Update(recallCatchUpMsg{})
	if cmd == nil {
		t.Fatal("the tick must run the gated pass")
	}
	if msg, ok := cmd().(recallRefreshMsg); !ok || !msg.gated {
		t.Fatalf("expected a gated refresh, got %#v", msg)
	}
	if len(src.refreshes) != 1 || !src.refreshes[0] {
		t.Fatalf("refreshes %v", src.refreshes)
	}
	// A tick that outlives the overlay (closed, or the chain ended) is inert.
	gs.sweeping = false
	if _, cmd := gs.Update(recallCatchUpMsg{}); cmd != nil {
		t.Fatal("no pass after the chain ended")
	}
	gs.Hide()
	if _, cmd := gs.Update(recallCatchUpMsg{}); cmd != nil {
		t.Fatal("no pass after the overlay closed")
	}
}

// TestGlobalSearch_PreviewErrorIsShown: when `recall show` fails for the
// selected hit the pane says so instead of "loading turns..." forever.
func TestGlobalSearch_PreviewErrorIsShown(t *testing.T) {
	src := newStubRecall()
	gs := NewGlobalSearch()
	gs.SetSource(src)
	gs.SetSize(120, 40)
	gs.Show()
	gs.input.SetValue("clock")
	gs.query = "clock"
	res, _ := src.Search(context.Background(), "clock", recallResultLimit)
	gs.Update(globalSearchResultsMsg{query: "clock", res: res})
	if !strings.Contains(ansi.Strip(gs.View()), "loading turns...") {
		t.Fatal("before the preview answers the pane says it is loading")
	}
	gs.Update(recallPreviewMsg{sessID: 41, err: query.ErrNotFound})
	frame := ansi.Strip(gs.View())
	if strings.Contains(frame, "loading turns...") || !strings.Contains(frame, "preview failed: recall: no such session") {
		t.Fatalf("frame:\n%s", frame)
	}
}

func TestGlobalSearch_RefreshSkippedAndSearchErrorAreShown(t *testing.T) {
	src := newStubRecall()
	gs := NewGlobalSearch()
	gs.SetSource(src)
	gs.SetSize(120, 40)
	gs.Show()
	gs.Update(recallRefreshMsg{err: errors.New("another sweep is running")})
	if !strings.Contains(gs.staleLine(), "another sweep is running") {
		t.Fatalf("stale line %q", gs.staleLine())
	}
	gs.input.SetValue("x")
	gs.query = "x"
	gs.Update(globalSearchResultsMsg{query: "x", err: errors.New("fts5: syntax error")})
	if !strings.Contains(ansi.Strip(gs.View()), "fts5: syntax error") {
		t.Fatal("a search error must be visible")
	}
}

func TestGlobalSearchMarkInAgentDeck(t *testing.T) {
	src := newStubRecall()
	gs := NewGlobalSearch()
	gs.SetSource(src)
	gs.Show()
	gs.applySearchResults(query.SearchResult{Hits: src.hits})
	gs.MarkInAgentDeck([]*session.Instance{
		{ID: "auth-fix"},
		{ID: "other", ClaudeSessionID: "aaaaaaaa-0000-4000-8000-000000000001"},
	})
	if !gs.results[0].InAgentDeck || gs.results[0].InstanceID != "auth-fix" {
		t.Fatalf("deck id match: %+v", gs.results[0])
	}
	if gs.results[1].InAgentDeck {
		t.Fatalf("codex hit must not match a Claude session id: %+v", gs.results[1])
	}
	if !gs.results[2].InAgentDeck || gs.results[2].InstanceID != "other" {
		t.Fatalf("claude session id match: %+v", gs.results[2])
	}
}

func TestGlobalSearchHighlightMatches(t *testing.T) {
	gs := NewGlobalSearch()
	if got := gs.highlightMatches("Hello World", ""); got != "Hello World" {
		t.Errorf("empty query: %q", got)
	}
	if got := gs.highlightMatches("", "x"); got != "" {
		t.Errorf("empty text: %q", got)
	}
	got := ansi.Strip(gs.highlightMatches("test one TEST two", `"test" AND one`))
	if got != "test one TEST two" {
		t.Errorf("highlighting must keep the text: %q", got)
	}
}

// assertOverlayGolden compares the overlay frame with testdata (UPDATE_GOLDEN=1 rewrites)
// and checks that no line is wider than the terminal.
func assertOverlayGolden(t *testing.T, gs *GlobalSearch, name string) {
	t.Helper()
	got := ansi.Strip(gs.View()) + "\n"
	for _, line := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(line); w > gs.width {
			t.Fatalf("%s: a line is %d cells wide on a %d-column terminal:\n%s", name, w, gs.width, line)
		}
	}
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s golden mismatch\nwant:\n%s\ngot:\n%s", name, want, got)
	}
}

// TestGlobalSearchGolden pins the three states of the Recall screen: just
// opened (index summary, refreshing), results with the preview of the
// selected hit, and the staleness line after a deferred pass.
func TestGlobalSearchGolden(t *testing.T) {
	src := newStubRecall()
	src.status.LastSweep = 0
	gs := NewGlobalSearch()
	gs.SetSource(src)
	gs.SetSize(140, 40)
	gs.Show()
	gs.Update(recallStatusMsg{status: src.status})
	assertOverlayGolden(t, gs, "recall_search_open.golden")

	gs.Update(recallRefreshMsg{res: ingest.Result{}})
	gs.input.SetValue("clock skew")
	gs.query = "clock skew"
	res, _ := src.Search(context.Background(), "clock skew", recallResultLimit)
	gs.Update(globalSearchResultsMsg{query: "clock skew", res: res})
	gs.Update(recallPreviewMsg{sessID: 41, detail: src.detail[41]})
	gs.MarkInAgentDeck([]*session.Instance{{ID: "auth-fix"}})
	for i := range gs.results {
		gs.results[i].EndedAt = time.Time{} // relative dates would drift
	}
	assertOverlayGolden(t, gs, "recall_search_results.golden")

	gs.Update(recallRefreshMsg{res: ingest.Result{Deferred: 3, DeferredBytes: 152 << 20}})
	assertOverlayGolden(t, gs, "recall_search_behind.golden")

	// The same results screen at the three widths the task named: the
	// overlay follows the terminal below its 160-column cap and fits 80.
	gs.Update(recallRefreshMsg{res: ingest.Result{}})
	for _, w := range []int{200, 120, 80} {
		gs.SetSize(w, 40)
		assertOverlayGolden(t, gs, fmt.Sprintf("recall_search_results_%d.golden", w))
	}
}
