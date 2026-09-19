package query

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
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// A tiny hand-written corpus: three sessions in two profiles with known
// words, so ranking assertions are exact.
func writeSession(t *testing.T, dir, id, cwd, title string, turns ...string) string {
	t.Helper()
	proj := filepath.Join(dir, "projects", "-p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(proj, id+".jsonl")
	var sb strings.Builder
	if title != "" {
		fmt.Fprintf(&sb, `{"type":"custom-title","customTitle":%q,"sessionId":%q}`+"\n", title, id)
	}
	ts := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	for i, text := range turns {
		ts = ts.Add(time.Hour)
		role, typ := "user", "user"
		if i%2 == 1 {
			role, typ = "assistant", "assistant"
		}
		fmt.Fprintf(&sb, `{"type":%q,"message":{"role":%q,"content":%q,"usage":{"input_tokens":1,"output_tokens":1}},"uuid":"%s-%d","timestamp":%q,"sessionId":%q,"cwd":%q}`+"\n",
			typ, role, text, id, i, ts.Format(time.RFC3339), id, cwd)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type fixture struct {
	st      *store.Store
	stateDB string
	roots   []reader.Root
	reg     *statedb.StateDB
}

const (
	sessA = "aaaaaaaa-0000-4000-8000-000000000001"
	sessB = "bbbbbbbb-0000-4000-8000-000000000002"
	sessC = "cccccccc-0000-4000-8000-000000000003"
)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	personal := filepath.Join(base, "claude")
	work := filepath.Join(base, "claude-work")
	writeSession(t, personal, sessA, "/Users/x/app", "auth fix",
		"fix the flaky auth test SB-412", "The auth test fails because of clock skew on the runner.",
		"try pinning the clock", "Pinned the clock; the auth test passes now.")
	writeSession(t, personal, sessB, "/Users/x/deck", "",
		"add a telegram bridge to the deck", "Telegram needs a bot token; I added the bridge and the auth test is untouched.",
		"looks good", "Done.")
	writeSession(t, work, sessC, "/Users/x/work", "clock skew on prod",
		"prod clock skew breaks tokens", "Root cause was clock skew; fixed by ntp.")
	st, err := store.Open(filepath.Join(base, "data", "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	reg, err := statedb.Open(filepath.Join(base, "profiles", "personal", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	f := &fixture{st: st, stateDB: filepath.Join(base, "profiles", "personal", "state.db"), reg: reg,
		roots: []reader.Root{{Harness: "claude", Profile: "personal", Dir: personal}, {Harness: "claude", Profile: "work", Dir: work}}}
	return f
}

type regAdapter struct{ db *statedb.StateDB }

func (r regAdapter) DeckID(_, native string) string {
	if native == sessA {
		return "deck-a"
	}
	return ""
}
func (r regAdapter) ChangedSince(time.Time) []ingest.Ref { return nil }
func (r regAdapter) Hints(_, native string) (string, string) {
	if native == sessA {
		return "ticket=SB-412 purpose=fix flaky auth test", "auth"
	}
	return "", ""
}

func (f *fixture) sweep(t *testing.T) {
	t.Helper()
	if _, err := ingest.New(f.st, ingest.Options{Roots: f.roots, Registry: regAdapter{f.reg}}).Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMatchExpr(t *testing.T) {
	cases := map[string]string{
		"SB-412":           `"SB-412"`,
		"clock skew":       `"clock" "skew"`,
		"auth OR telegram": `"auth" OR "telegram"`,
		"handle_sess*":     `"handle_sess"*`,
		`say "hi"`:         `"say" "hi"`,
		"tags:auth clock":  `"auth" "clock"`,
		"NOT":              `NOT`,
		"a:b:c":            `"b:c"`,
		"  ":               ``,
		`weird"quote`:      `"weird""quote"`,
	}
	for in, want := range cases {
		if got := MatchExpr(in); got != want {
			t.Errorf("MatchExpr(%q) = %q want %q", in, got, want)
		}
	}
	if got := CardMatchExpr("tags:auth clock"); got != `tags:"auth" "clock"` {
		t.Errorf("CardMatchExpr = %q", got)
	}
}

func TestSearch_RanksCardHitsFirstAndFilters(t *testing.T) {
	f := newFixture(t)
	f.sweep(t)
	s := New(f.st, f.stateDB)
	ctx := context.Background()

	// "auth": A has it in the title/hints (card) and bodies; B only in a
	// body. The card hit must come first.
	res, err := s.Search(ctx, SearchOptions{Query: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 2 || res.Hits[0].NativeID != sessA || !res.Hits[0].CardHit || res.Hits[1].NativeID != sessB || res.Hits[1].CardHit {
		t.Fatalf("hits = %+v", res.Hits)
	}
	if res.Hits[0].DeckID != "deck-a" || res.Hits[0].Title != "auth fix" || !strings.Contains(strings.ToLower(res.Hits[0].Snippet), "auth") {
		t.Fatalf("hit 0 = %+v", res.Hits[0])
	}
	if res.Candidates == 0 || res.CeilingHit {
		t.Fatalf("candidates %d ceiling %v", res.Candidates, res.CeilingHit)
	}
	// A hint typed via annotate outranks an incidental body mention: the
	// ticket appears only in A's hints.
	res, _ = s.Search(ctx, SearchOptions{Query: "SB-412"})
	if len(res.Hits) != 1 || res.Hits[0].NativeID != sessA {
		t.Fatalf("ticket search: %+v", res.Hits)
	}
	// Profile / harness / project / since / role filters.
	res, _ = s.Search(ctx, SearchOptions{Query: "clock", Filters: Filters{Profile: "work"}})
	if len(res.Hits) != 1 || res.Hits[0].NativeID != sessC {
		t.Fatalf("profile filter: %+v", res.Hits)
	}
	res, _ = s.Search(ctx, SearchOptions{Query: "clock", Filters: Filters{Harness: "codex"}})
	if len(res.Hits) != 0 {
		t.Fatalf("harness filter: %+v", res.Hits)
	}
	res, _ = s.Search(ctx, SearchOptions{Query: "clock", Filters: Filters{Project: "/Users/x/app"}})
	if len(res.Hits) != 1 || res.Hits[0].NativeID != sessA {
		t.Fatalf("project filter: %+v", res.Hits)
	}
	res, _ = s.Search(ctx, SearchOptions{Query: "clock", Filters: Filters{Since: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}})
	if len(res.Hits) != 0 {
		t.Fatalf("since filter: %+v", res.Hits)
	}
	// "telegram" as a user prompt exists in B; as assistant text too. Role
	// 2 (assistant) on "pinning": only A's user turn has it -> no body hit.
	res, _ = s.Search(ctx, SearchOptions{Query: "pinning", Role: 2})
	if len(res.Hits) != 0 {
		t.Fatalf("role filter: %+v", res.Hits)
	}
	res, _ = s.Search(ctx, SearchOptions{Query: "pinning", Role: 1})
	if len(res.Hits) != 1 {
		t.Fatalf("role filter user: %+v", res.Hits)
	}
	// Deck id filter.
	res, _ = s.Search(ctx, SearchOptions{Query: "clock", Filters: Filters{DeckID: "deck-a"}})
	if len(res.Hits) != 1 || res.Hits[0].NativeID != sessA {
		t.Fatalf("deck filter: %+v", res.Hits)
	}
	if _, err := s.Search(ctx, SearchOptions{Query: "   "}); err == nil {
		t.Fatal("empty query accepted")
	}
}

func TestSearch_PhraseVerifiesAndReports(t *testing.T) {
	f := newFixture(t)
	f.sweep(t)
	s := New(f.st, f.stateDB)
	// Both words occur in A and C; the literal phrase "clock skew" occurs
	// in both; "skew clock" in neither.
	res, err := s.Search(context.Background(), SearchOptions{Query: "clock skew", Phrase: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned == 0 || res.VerifiedCount != 2 {
		t.Fatalf("scanned %d verified %d: %+v", res.Scanned, res.VerifiedCount, res.Hits)
	}
	for _, h := range res.Hits {
		if h.Verified == nil || !*h.Verified {
			t.Fatalf("hit not verified: %+v", h)
		}
	}
	res, _ = s.Search(context.Background(), SearchOptions{Query: "skew clock", Phrase: true})
	if res.VerifiedCount != 0 || len(res.Hits) == 0 {
		t.Fatalf("reversed phrase: verified %d hits %d", res.VerifiedCount, len(res.Hits))
	}
	for _, h := range res.Hits {
		if h.Verified == nil || *h.Verified {
			t.Fatalf("hit wrongly verified: %+v", h)
		}
	}
}

// Hint and tag filters join state.db live: an annotation written a moment
// ago is searchable without a sweep.
func TestSearch_HintFiltersJoinStateDBLive(t *testing.T) {
	f := newFixture(t)
	f.sweep(t)
	s := New(f.st, f.stateDB)
	ctx := context.Background()
	if err := f.reg.UpsertSessionLink("deck-a", "claude", sessA, "", true); err != nil {
		t.Fatal(err)
	}
	if err := f.reg.SetSessionHint("instance", "deck-a", "ticket", "SB-412", "annotate", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.reg.AddSessionTag("harness_session", sessB, "telegram", "annotate"); err != nil {
		t.Fatal(err)
	}
	res, err := s.Search(ctx, SearchOptions{Query: "auth", Filters: Filters{Hints: map[string]string{"ticket": "SB-412"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].NativeID != sessA {
		t.Fatalf("hint filter: %+v", res.Hits)
	}
	res, err = s.Search(ctx, SearchOptions{Query: "auth", Filters: Filters{Tags: []string{"telegram"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].NativeID != sessB {
		t.Fatalf("tag filter: %+v", res.Hits)
	}
	res, _ = s.Search(ctx, SearchOptions{Query: "auth", Filters: Filters{Hints: map[string]string{"ticket": "SB-999"}}})
	if len(res.Hits) != 0 {
		t.Fatalf("unknown ticket matched: %+v", res.Hits)
	}
	if _, err := New(f.st, "").Search(ctx, SearchOptions{Query: "auth", Filters: Filters{Tags: []string{"x"}}}); err == nil {
		t.Fatal("tag filter without state.db must error, not silently ignore")
	}
	// Plain searches still work after the attached connection is released.
	if _, err := s.Search(ctx, SearchOptions{Query: "auth"}); err != nil {
		t.Fatal(err)
	}
	sessions, err := s.Sessions(ctx, Filters{Tags: []string{"telegram"}}, 10)
	if err != nil || len(sessions) != 1 || sessions[0].NativeID != sessB {
		t.Fatalf("sessions by tag: %v %+v", err, sessions)
	}
}

func TestSessionsResolveShowStatus(t *testing.T) {
	f := newFixture(t)
	f.sweep(t)
	s := New(f.st, f.stateDB)
	ctx := context.Background()
	rows, err := s.Sessions(ctx, Filters{}, 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("%v %d", err, len(rows))
	}
	if rows[0].NativeID != sessB && rows[0].NativeID != sessA {
		t.Fatalf("newest first: %+v", rows[0])
	}
	if rows[0].Path == "" || rows[0].Turns == 0 || rows[0].Preview == "" {
		t.Fatalf("row: %+v", rows[0])
	}
	// Resolve by prefix, by deck id, ambiguous, numeric.
	r, err := s.Resolve(ctx, "aaaaaaaa")
	if err != nil || r.NativeID != sessA {
		t.Fatalf("prefix: %v %+v", err, r)
	}
	r, err = s.Resolve(ctx, "deck-a")
	if err != nil || r.NativeID != sessA {
		t.Fatalf("deck: %v %+v", err, r)
	}
	if _, err := s.Resolve(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// A shared prefix across two sessions is ambiguous.
	writeSession(t, f.roots[0].Dir, "aaaaaaaa-1111-4000-8000-000000000009", "/Users/x/app", "", "hello", "hi")
	f.sweep(t)
	if _, err := s.Resolve(ctx, "aaaaaaaa"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous: %v", err)
	}
	if r, err := s.Resolve(ctx, fmt.Sprint(r.SessID)); err != nil || r.NativeID != sessA {
		t.Fatalf("numeric: %v", err)
	}
	d, err := s.Show(ctx, sessA, 2)
	if err != nil {
		t.Fatal(err)
	}
	if d.Session.Messages != 4 || len(d.Messages) != 2 || d.Truncated != 2 || d.Messages[0].Role != "user" || d.Messages[0].Class != "prompt" || d.Messages[1].Text != "The auth test fails because of clock skew on the runner." {
		t.Fatalf("show: %+v", d)
	}
	d, err = s.Show(ctx, sessA, 0)
	if err != nil || len(d.Messages) != 4 || d.Truncated != 0 {
		t.Fatalf("show all: %v %+v", err, d)
	}
	d, err = s.Show(ctx, sessA, -1)
	if err != nil || len(d.Messages) != 0 || d.Session.Hints == "" {
		t.Fatalf("show card: %v %+v", err, d)
	}
	st, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Sessions != 4 || st.Sources["ok"] != 4 || st.Cards != 4 || st.CardFTSRows != 4 || st.Messages != 12 || st.IndexedBytes == 0 || st.PendingBytes != 0 ||
		st.ByProfile["personal"] != 3 || st.ByProfile["work"] != 1 || st.ByHarness["claude"] != 4 || st.SchemaVersion == "" || st.DBBytes == 0 {
		t.Fatalf("status: %+v", st)
	}
}

func TestSnippet(t *testing.T) {
	text := strings.Repeat("filler ", 60) + "the ROOT cause was clock skew" + strings.Repeat(" tail", 60)
	sn := Snippet(text, []string{"clock"}, 80)
	if !strings.Contains(sn, "clock skew") || !strings.HasPrefix(sn, "…") || !strings.HasSuffix(sn, "…") || len(sn) > 90 {
		t.Fatalf("snippet = %q", sn)
	}
	if Snippet("short", nil, 80) != "short" {
		t.Fatal("short text")
	}
	if got := Snippet("héllo wörld", []string{"wörld"}, 4); !strings.Contains(got, "wör") && !strings.Contains(got, "w") {
		t.Fatalf("utf8: %q", got)
	}
}
