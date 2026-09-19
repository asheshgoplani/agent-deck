package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall/ingest"
	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// newRealRecallIndex indexes one Claude transcript into a throwaway
// recall.db and returns the RecallSource the TUI would open over it: the
// real query.Searcher, not the stub, so the overlay's Show goes through
// Resolve exactly as `recall show` does.
func newRealRecallIndex(t *testing.T) *recallIndex {
	t.Helper()
	base := t.TempDir()
	claude := filepath.Join(base, ".claude")
	proj := filepath.Join(claude, "projects", "-tmp-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "e2e0e2e0-0000-4000-8000-00000000e2e0"
	transcript := `{"type":"user","message":{"role":"user","content":"please fix the heron migration flake"},"uuid":"u1","timestamp":"2026-09-19T12:00:00Z","sessionId":"` + id + `","cwd":"/tmp/proj"}
{"type":"assistant","message":{"role":"assistant","model":"claude-test","content":[{"type":"text","text":"The heron migration flaked on clock skew; fixed."}],"usage":{"input_tokens":12,"output_tokens":7}},"uuid":"a1","timestamp":"2026-09-19T12:00:01Z","sessionId":"` + id + `"}
`
	if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(base, "data", "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	roots := []reader.Root{{Harness: reader.HarnessClaude, Profile: "personal", Dir: claude}}
	if _, err := ingest.New(st, ingest.Options{Roots: roots}).Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &recallIndex{profile: "personal", cfg: &session.UserConfig{}, st: st, q: query.New(st, ""), roots: roots,
		lock: filepath.Join(base, "data", "recall.lock"), queue: filepath.Join(base, "data", "recall", "queue.jsonl")}
}

// TestRecallIndex_PreviewLoadsOverTheRealIndex pins the P1 of the phase-3
// review: the overlay asks for the selected hit's preview by the listing
// id and the real index answers it (Resolve accepts the `#n` form), so the
// right pane shows the turns rather than "loading turns..." forever.
func TestRecallIndex_PreviewLoadsOverTheRealIndex(t *testing.T) {
	idx := newRealRecallIndex(t)
	ctx := context.Background()
	res, err := idx.Search(ctx, "heron", recallResultLimit)
	if err != nil || len(res.Hits) != 1 {
		t.Fatalf("search: %v %+v", err, res)
	}
	sessID := res.Hits[0].SessID
	d, err := idx.Show(ctx, sessID, recallPreviewTurns)
	if err != nil || len(d.Messages) != 2 || d.Session.SessID != sessID {
		t.Fatalf("show %s: %v %+v", query.Ref(sessID), err, d)
	}
	// The same id, the CLI's way and the TUI's way, is one resolver.
	if _, err := idx.q.Show(ctx, fmt.Sprint(sessID), 0); err != nil {
		t.Fatalf("bare id: %v", err)
	}
	if _, err := idx.q.Show(ctx, query.Ref(sessID), 0); err != nil {
		t.Fatalf("#n id: %v", err)
	}

	gs := NewGlobalSearch()
	gs.SetSource(idx)
	gs.SetSize(120, 40)
	gs.Show()
	gs.Update(recallStatusMsg{})
	for _, r := range "heron" {
		_, cmd := gs.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if r == 'n' {
			drain(gs, cmd)
		}
	}
	if len(gs.results) != 1 || gs.results[0].SessID != sessID {
		t.Fatalf("results %+v", gs.results)
	}
	if _, ok := gs.preview[sessID]; !ok || len(gs.previewE) != 0 {
		t.Fatalf("preview loaded %v, errors %v", ok, gs.previewE)
	}
	frame := ansi.Strip(gs.View())
	if strings.Contains(frame, "loading turns...") || !strings.Contains(frame, "The heron migration flaked on clock skew") {
		t.Fatalf("frame:\n%s", frame)
	}
}
