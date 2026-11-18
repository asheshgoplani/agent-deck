package query

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// keepRows is the reference filter the tests compare against: the rows a
// client would keep if it filtered the unfiltered timeline itself.
func keepRows(rows []Row, kinds []string, skipEmpty bool) []Row {
	allowed := map[string]bool{}
	for _, k := range kinds {
		allowed[k] = true
	}
	out := []Row{}
	for _, r := range rows {
		if len(kinds) > 0 && !allowed[r.Kind] {
			continue
		}
		if skipEmpty && strings.TrimSpace(r.Body) == "" && (r.Finished == nil || *r.Finished) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// samePosition reports whether two cursors resume at the same point of the
// same file. A filtered cursor also lists the open rows it dropped (D).
func samePosition(t *testing.T, a, b string) bool {
	t.Helper()
	ca, errA := decodeRowsCursor(a)
	cb, errB := decodeRowsCursor(b)
	if errA != nil || errB != nil {
		t.Fatalf("decode cursors: %v %v", errA, errB)
	}
	ca.D, cb.D = nil, nil
	return reflect.DeepEqual(ca, cb)
}

func idsOf(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// TestRowsKindsFilter (#3203): --kinds keeps only the listed row kinds and
// --skip-empty-body drops rows whose body is only whitespace. The filtered
// answer equals the unfiltered rows filtered by the client, and it carries
// next_cursor, the pre-filter resume point.
func TestRowsKindsFilter(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			src := RowsSource{Harness: harness, Path: rowsFixture(t, harness)}
			full, err := ReadRows(context.Background(), src, RowsOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				kinds     []string
				skipEmpty bool
			}{
				{[]string{"user", "assistant"}, false},
				{[]string{"bash"}, false},
				{nil, true},
				{[]string{"user", "assistant", "system"}, true},
			} {
				got, err := ReadRows(context.Background(), src, RowsOptions{Kinds: tc.kinds, SkipEmptyBody: tc.skipEmpty})
				if err != nil {
					t.Fatal(err)
				}
				want := keepRows(full.Turns, tc.kinds, tc.skipEmpty)
				if len(want) == len(full.Turns) {
					t.Fatalf("%+v: fixture has nothing to filter", tc)
				}
				if !reflect.DeepEqual(got.Turns, want) {
					t.Fatalf("%+v: got %v want %v", tc, idsOf(got.Turns), idsOf(want))
				}
				if got.NextCursor == "" || got.NextCursor != got.ThroughCursor || !samePosition(t, got.ThroughCursor, full.ThroughCursor) {
					t.Fatalf("%+v: next_cursor %q through %q (unfiltered through %q)", tc, got.NextCursor, got.ThroughCursor, full.ThroughCursor)
				}
			}
		})
	}
}

// TestRowsKindsDefaultHasNoNextCursor: with no filter nothing changes, and
// next_cursor is not serialized at all.
func TestRowsKindsDefaultHasNoNextCursor(t *testing.T) {
	tl, err := ReadRows(context.Background(), RowsSource{Harness: "claude", Path: rowsFixture(t, "claude")}, RowsOptions{Tail: 3})
	if err != nil {
		t.Fatal(err)
	}
	if tl.NextCursor != "" {
		t.Fatalf("unfiltered read set next_cursor %q", tl.NextCursor)
	}
	b, _ := json.Marshal(tl)
	if strings.Contains(string(b), "next_cursor") {
		t.Fatalf("unfiltered JSON carries next_cursor: %s", b)
	}
}

// TestRowsKindsCursorResumesAcrossFilteredRows (#3203 "cursor semantics"):
// when the newest rows are filtered out, the cursor still moves past them.
// Resuming from next_cursor returns nothing new (no re-pull of the same
// window), and after an append returns exactly the new matching row.
func TestRowsKindsCursorResumesAcrossFilteredRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	lines := `{"type":"user","uuid":"u1","message":{"role":"user","content":"hello"},"timestamp":"2026-11-18T10:00:00Z"}
{"type":"assistant","uuid":"a1","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hi there"}]},"timestamp":"2026-11-18T10:00:01Z"}
{"type":"assistant","uuid":"a2","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]},"timestamp":"2026-11-18T10:00:02Z"}
{"type":"user","uuid":"r1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a\nb"}]},"timestamp":"2026-11-18T10:00:03Z"}
{"type":"system","subtype":"turn_duration","durationMs":1000,"uuid":"s1","timestamp":"2026-11-18T10:00:04Z"}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	src := RowsSource{Harness: "claude", Path: path}
	kinds := []string{"user", "assistant"}
	first, err := ReadRows(context.Background(), src, RowsOptions{Kinds: kinds})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(first.Turns); !reflect.DeepEqual(got, []string{"u1", "a1"}) {
		t.Fatalf("first read: %v", got)
	}
	if first.NextCursor == "" {
		t.Fatal("filtered read has no next_cursor")
	}
	again, err := ReadRows(context.Background(), src, RowsOptions{Since: first.NextCursor, Kinds: kinds})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Turns) != 0 || again.NextCursor != first.NextCursor {
		t.Fatalf("resume re-pulled rows or moved the cursor: %v %q", idsOf(again.Turns), again.NextCursor)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"assistant","uuid":"a3","message":{"id":"m3","role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"pwd"}}]},"timestamp":"2026-11-18T10:00:05Z"}
{"type":"user","uuid":"u2","message":{"role":"user","content":"next"},"timestamp":"2026-11-18T10:00:06Z"}
`)
	f.Close()
	next, err := ReadRows(context.Background(), src, RowsOptions{Since: again.NextCursor, Kinds: kinds})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(next.Turns); !reflect.DeepEqual(got, []string{"u2"}) {
		t.Fatalf("after append: %v", got)
	}
	for _, u := range next.Updates {
		if u.Kind != "" && u.Kind != "user" && u.Kind != "assistant" {
			t.Fatalf("update of a filtered kind crossed the wire: %+v", u)
		}
	}
}

// TestRowsKindsLimitPaging: paging --since/--limit with a filter until the
// cursor stops yields exactly the filtered full timeline; --limit counts
// rows that pass the filter.
func TestRowsKindsLimitPaging(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		src := RowsSource{Harness: harness, Path: rowsFixture(t, harness)}
		opts := RowsOptions{Kinds: []string{"user", "assistant"}}
		full, err := ReadRows(context.Background(), src, opts)
		if err != nil {
			t.Fatal(err)
		}
		set := newRowSet()
		cursor := ""
		for page := 0; ; page++ {
			if page > 100 {
				t.Fatal("cursor never settled")
			}
			tl, err := ReadRows(context.Background(), src, RowsOptions{Since: cursor, Limit: 2, Kinds: opts.Kinds})
			if err != nil {
				t.Fatal(err)
			}
			if len(tl.Turns) > 2 {
				t.Fatalf("%s: --limit 2 page has %d rows", harness, len(tl.Turns))
			}
			for _, u := range tl.Updates {
				u := u
				set.apply(RowFrame{Frame: "update", Row: &u})
			}
			for _, id := range tl.Removed {
				set.apply(RowFrame{Frame: "remove", ID: id})
			}
			for _, r := range tl.Turns {
				if r.Kind != "user" && r.Kind != "assistant" {
					t.Fatalf("%s: %s row in a filtered page", harness, r.Kind)
				}
				r := r
				set.apply(RowFrame{Frame: "row", Row: &r})
			}
			if tl.NextCursor == "" {
				t.Fatalf("%s: filtered page without next_cursor", harness)
			}
			if tl.NextCursor == cursor {
				break
			}
			cursor = tl.NextCursor
		}
		if got, want := normalizeRows(t, set.list()), normalizeRows(t, full.Turns); got != want {
			t.Fatalf("%s: paged %v, full %v", harness, got, want)
		}
	}
}

// TestRowsKindsTailAndBefore: --tail N with a filter answers the last N
// rows that pass it (not the last N rows, filtered), and --before paging
// with the same filter walks back through exactly the filtered timeline.
func TestRowsKindsTailAndBefore(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			src := RowsSource{Harness: harness, Path: rowsFixture(t, harness)}
			// Kinds with at least three rows in each fixture.
			kinds := map[string][]string{"claude": {"user", "assistant"}, "codex": {"bash", "tool", "system"}}[harness]
			full, err := ReadRows(context.Background(), src, RowsOptions{})
			if err != nil {
				t.Fatal(err)
			}
			want := keepRows(full.Turns, kinds, false)
			if len(want) < 3 {
				t.Fatalf("fixture has %d %v rows", len(want), kinds)
			}
			tail, err := ReadRows(context.Background(), src, RowsOptions{Tail: 2, Kinds: kinds})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tail.Turns, want[len(want)-2:]) {
				t.Fatalf("--tail 2 --kinds %v: %v want %v", kinds, idsOf(tail.Turns), idsOf(want[len(want)-2:]))
			}
			if tail.NextCursor == "" || tail.NextCursor != tail.ThroughCursor {
				t.Fatalf("tail next_cursor %q through %q", tail.NextCursor, tail.ThroughCursor)
			}
			rows := append([]Row(nil), tail.Turns...)
			page := tail
			for pages := 0; page.BeforeCursor != ""; pages++ {
				if pages > len(full.Turns) {
					t.Fatal("before cursor did not terminate")
				}
				page, err = ReadRows(context.Background(), src, RowsOptions{Before: page.BeforeCursor, Limit: 1, Kinds: kinds})
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Turns) > 1 {
					t.Fatalf("unbounded page: %v", idsOf(page.Turns))
				}
				rows = append(page.Turns, rows...)
			}
			if !reflect.DeepEqual(rows, want) {
				t.Fatalf("filtered backward pages %v want %v", idsOf(rows), idsOf(want))
			}
		})
	}
}

// TestParseRowKinds: the allowlist parser accepts the row vocabulary and
// names every valid kind when it refuses one.
func TestParseRowKinds(t *testing.T) {
	got, err := ParseRowKinds(" user, assistant ")
	if err != nil || !reflect.DeepEqual(got, []string{"user", "assistant"}) {
		t.Fatalf("ParseRowKinds: %v %v", got, err)
	}
	for _, bad := range []string{"user,bogus", "user,,assistant", ""} {
		_, err := ParseRowKinds(bad)
		if err == nil {
			t.Fatalf("accepted %q", bad)
		}
		if bad == "user,bogus" {
			for _, k := range []string{`"bogus"`, "user", "assistant", "turn_end", "other"} {
				if !strings.Contains(err.Error(), k) {
					t.Fatalf("error %q does not name %s", err, k)
				}
			}
		}
	}
	if err := ValidateRowsOptions(RowsOptions{Kinds: []string{"nope"}}); err == nil {
		t.Fatal("ValidateRowsOptions accepted an unknown kind")
	}
}

// TestRowsKindsTailGrowsWindowForSparseKinds: when the kept rows sit far
// before the last 256 KB window, --tail with a filter keeps widening the
// window until it has N kept rows instead of answering from the tail only.
func TestRowsKindsTailGrowsWindowForSparseKinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	var b strings.Builder
	b.WriteString(`{"type":"user","uuid":"u1","message":{"role":"user","content":"first"},"timestamp":"2026-11-18T10:00:00Z"}` + "\n")
	b.WriteString(`{"type":"user","uuid":"u2","message":{"role":"user","content":"second"},"timestamp":"2026-11-18T10:00:01Z"}` + "\n")
	pad := strings.Repeat("x", 900)
	for i := 0; b.Len() < 1<<20; i++ {
		id := fmt.Sprintf("t%06d", i)
		b.WriteString(`{"type":"assistant","uuid":"` + id + `","message":{"id":"m` + id + `","role":"assistant","content":[{"type":"tool_use","id":"toolu_` + id + `","name":"Bash","input":{"command":"echo ` + pad + `"}}]},"timestamp":"2026-11-18T10:00:02Z"}` + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	src := RowsSource{Harness: "claude", Path: path}
	tl, err := ReadRows(context.Background(), src, RowsOptions{Tail: 2, Kinds: []string{"user"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(tl.Turns); !reflect.DeepEqual(got, []string{"u1", "u2"}) {
		t.Fatalf("--tail 2 --kinds user over 1 MB: %v", got)
	}
	older, err := ReadRows(context.Background(), src, RowsOptions{Before: tl.BeforeCursor, Limit: 5, Kinds: []string{"user"}})
	if err != nil || len(older.Turns) != 0 || older.BeforeCursor != "" {
		t.Fatalf("older page before the first user row: %v %q %v", idsOf(older.Turns), older.BeforeCursor, err)
	}
}

// TestRowsKindsFilterSidechainChildren: a kept subagent row's attached
// children are filtered too, so the sidechain's dropped kinds do not ride
// along in the payload.
func TestRowsKindsFilterSidechainChildren(t *testing.T) {
	src := RowsSource{Harness: "claude", Path: rowsFixture(t, "claude")}
	full, err := ReadRows(context.Background(), src, RowsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	parent, ok := rowByID(full.Turns, "toolu_agent1")
	if !ok || len(parent.Children) == 0 {
		t.Fatalf("fixture subagent row has no children: %+v", parent)
	}
	kinds := []string{"subagent", "assistant"}
	want := keepRows(parent.Children, kinds, false)
	if len(want) == len(parent.Children) {
		t.Fatalf("fixture children have nothing to filter: %v", kindsOf(parent.Children))
	}
	for _, opts := range []RowsOptions{{Kinds: kinds}, {Kinds: kinds, Tail: 50}} {
		tl, err := ReadRows(context.Background(), src, opts)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := rowByID(tl.Turns, "toolu_agent1")
		if !ok {
			t.Fatalf("%+v: subagent row dropped", opts)
		}
		if len(want) == 0 {
			want = nil
		}
		if !reflect.DeepEqual(got.Children, want) {
			t.Fatalf("%+v: children %v want %v", opts, kindsOf(got.Children), kindsOf(want))
		}
	}
}

// TestKeptCounterTracksMembership: the --limit counter follows a row in
// and out of the filter as frames replace it, fill its body later, move it
// (queue absorb) or remove it, without rescanning the set.
func TestKeptCounterTracksMembership(t *testing.T) {
	f := func(frame string, r Row) RowFrame {
		if frame == "remove" {
			return RowFrame{Frame: frame, ID: r.ID}
		}
		return RowFrame{Frame: frame, Row: &r}
	}
	set := newRowSet()
	k := newKeptCounter(RowsOptions{Kinds: []string{"bash", "user"}, SkipEmptyBody: true})
	for i, step := range []struct {
		fr   RowFrame
		want int
	}{
		{f("row", Row{ID: "b1", Kind: "bash"}), 0},
		{f("row", Row{ID: "b2", Kind: "bash", Body: "  "}), 0},
		{f("update", Row{ID: "b1", Body: "ls"}), 1},
		{f("update", Row{ID: "b2", Body: "pwd"}), 2},
		{f("row", Row{ID: "s1", Kind: "system", Body: "x"}), 2},
		{f("row", Row{ID: "q1", Kind: "user", Body: "queued", Queued: boolPtr(true)}), 3},
		{f("row", Row{ID: "u9", Kind: "user", Body: "later"}), 4},
		{f("update", Row{ID: "q1", Queued: boolPtr(false), TS: "2026-11-18T10:00:00Z"}), 4},
		{f("row", Row{ID: "b1", Kind: "bash", Body: ""}), 3},
		{f("remove", Row{ID: "q1"}), 2},
		{f("update", Row{ID: "unknown", Body: "x"}), 2},
	} {
		set.apply(step.fr)
		k.touch(set, step.fr)
		if k.n != step.want || k.n != len(FilterRows(set.list(), k.opts)) {
			t.Fatalf("step %d: counter %d, want %d (rescan %d)", i, k.n, step.want, len(FilterRows(set.list(), k.opts)))
		}
	}
}

// TestRowsSkipEmptyBodyKeepsPendingToolCall: a tool call whose output has
// not arrived yet is kept by --skip-empty-body, so the result patch a later
// --since read carries lands on a row the client already holds. Once the
// call has finished with no output, a fresh read drops it.
func TestRowsSkipEmptyBodyKeepsPendingToolCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	call := `{"type":"assistant","uuid":"a1","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"toolu_p","name":"Bash","input":{"command":"make"}}]},"timestamp":"2026-11-18T10:00:00Z"}` + "\n"
	if err := os.WriteFile(path, []byte(call), 0o600); err != nil {
		t.Fatal(err)
	}
	src := RowsSource{Harness: "claude", Path: path}
	opts := RowsOptions{SkipEmptyBody: true}
	first, err := ReadRows(context.Background(), src, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(first.Turns); !reflect.DeepEqual(got, []string{"toolu_p"}) {
		t.Fatalf("pending tool call not kept: %v", got)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"user","uuid":"r1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_p","content":"built ok"}]},"timestamp":"2026-11-18T10:00:01Z"}` + "\n")
	f.Close()
	next, err := ReadRows(context.Background(), src, RowsOptions{Since: first.NextCursor, SkipEmptyBody: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Updates) != 1 || next.Updates[0].ID != "toolu_p" || next.Updates[0].Body != "built ok" {
		t.Fatalf("result patch for the held row: %+v", next.Updates)
	}

	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	data := call + `{"type":"user","uuid":"r1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_p","content":""}]},"timestamp":"2026-11-18T10:00:01Z"}` + "\n"
	if err := os.WriteFile(empty, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, o := range []RowsOptions{opts, {SkipEmptyBody: true, Tail: 5}} {
		done, err := ReadRows(context.Background(), RowsSource{Harness: "claude", Path: empty}, o)
		if err != nil {
			t.Fatal(err)
		}
		if len(done.Turns) != 0 {
			t.Fatalf("%+v: finished call with no output kept: %v", o, idsOf(done.Turns))
		}
	}
}

// TestRowsKindsDroppedOpenIDsInCursor: a filtered cursor remembers the open
// rows it dropped (a tool call waiting for its result) so the next --since
// page can drop their result patches; the id leaves the cursor once the
// result has been swallowed, the list is bounded, and an unfiltered cursor
// never carries it.
func TestRowsKindsDroppedOpenIDsInCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	call := func(id string) string {
		return `{"type":"assistant","uuid":"a` + id + `","message":{"id":"m` + id + `","role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"Bash","input":{"command":"ls"}}]},"timestamp":"2026-11-18T10:00:00Z"}` + "\n"
	}
	result := func(id string) string {
		return `{"type":"user","uuid":"r` + id + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"out"}]},"timestamp":"2026-11-18T10:00:01Z"}` + "\n"
	}
	if err := os.WriteFile(path, []byte(`{"type":"user","uuid":"u1","message":{"role":"user","content":"go"},"timestamp":"2026-11-18T09:59:59Z"}`+"\n"+call("toolu_a")), 0o600); err != nil {
		t.Fatal(err)
	}
	src := RowsSource{Harness: "claude", Path: path}
	kinds := []string{"user", "assistant"}
	dropped := func(cursor string) []string {
		c, err := decodeRowsCursor(cursor)
		if err != nil {
			t.Fatal(err)
		}
		return c.D
	}
	first, err := ReadRows(context.Background(), src, RowsOptions{Kinds: kinds})
	if err != nil {
		t.Fatal(err)
	}
	if got := dropped(first.NextCursor); !reflect.DeepEqual(got, []string{"toolu_a"}) {
		t.Fatalf("open dropped ids after page 1: %v", got)
	}
	if plain, _ := ReadRows(context.Background(), src, RowsOptions{}); dropped(plain.ThroughCursor) != nil {
		t.Fatalf("unfiltered cursor carries dropped ids: %v", dropped(plain.ThroughCursor))
	}
	reviewAppend(t, path, result("toolu_a"))
	next, err := ReadRows(context.Background(), src, RowsOptions{Since: first.NextCursor, Kinds: kinds})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Updates) != 0 || dropped(next.NextCursor) != nil {
		t.Fatalf("result patch %v sent, or closed id kept in cursor %v", next.Updates, dropped(next.NextCursor))
	}

	var many strings.Builder
	for i := 0; i < maxDroppedOpen+10; i++ {
		many.WriteString(call(fmt.Sprintf("toolu_%03d", i)))
	}
	reviewAppend(t, path, many.String())
	capped, err := ReadRows(context.Background(), src, RowsOptions{Since: next.NextCursor, Kinds: kinds})
	if err != nil {
		t.Fatal(err)
	}
	if got := dropped(capped.NextCursor); len(got) != maxDroppedOpen || got[len(got)-1] != fmt.Sprintf("toolu_%03d", maxDroppedOpen+9) {
		t.Fatalf("dropped ids not capped to the newest %d: %d ids, last %v", maxDroppedOpen, len(got), got[len(got)-1:])
	}
}
