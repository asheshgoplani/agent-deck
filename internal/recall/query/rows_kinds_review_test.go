package query

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reviewLines1 = `{"type":"user","uuid":"u1","message":{"role":"user","content":"hello"},"timestamp":"2026-11-18T10:00:00Z"}
{"type":"assistant","uuid":"a1","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hi there"}]},"timestamp":"2026-11-18T10:00:01Z"}
{"type":"assistant","uuid":"a2","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]},"timestamp":"2026-11-18T10:00:02Z"}
`
const reviewLines2 = `{"type":"user","uuid":"r1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"BIGOUTPUT-line1\nBIGOUTPUT-line2"}]},"timestamp":"2026-11-18T10:00:03Z"}
{"type":"user","uuid":"u2","message":{"role":"user","content":"next"},"timestamp":"2026-11-18T10:00:06Z"}
`

func reviewAppend(t *testing.T, path, s string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// REVIEW: a live client polls with --since between a tool call and its
// result. The tool row was filtered out of page 1, so the client never holds
// it, yet page 2 ships its result (the bash output) as an update.
func TestReviewSinceLeaksResultOfFilteredTool(t *testing.T) {
	for _, mode := range []string{"since", "tail"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			reviewAppend(t, path, reviewLines1)
			src := RowsSource{Harness: "claude", Path: path}
			kinds := []string{"user", "assistant"}
			opts := RowsOptions{Kinds: kinds}
			if mode == "tail" {
				opts.Tail = 5
			}
			first, err := ReadRows(context.Background(), src, opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range first.Turns {
				if r.ID == "toolu_1" {
					t.Fatal("bash row passed a user,assistant filter")
				}
			}
			reviewAppend(t, path, reviewLines2)
			next, err := ReadRows(context.Background(), src, RowsOptions{Since: first.NextCursor, Kinds: kinds})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(next)
			if strings.Contains(string(b), "BIGOUTPUT") {
				t.Fatalf("result of a filtered-out bash row crossed the wire: %s", b)
			}
		})
	}
}

// REVIEW: empty filtered result still carries next_cursor and turns:[].
func TestReviewEmptyFilteredResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	reviewAppend(t, path, reviewLines1)
	src := RowsSource{Harness: "claude", Path: path}
	for _, opts := range []RowsOptions{{Kinds: []string{"compaction"}}, {Kinds: []string{"compaction"}, Tail: 3}, {Kinds: []string{"compaction"}, Limit: 1}} {
		tl, err := ReadRows(context.Background(), src, opts)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(tl)
		if tl.NextCursor == "" || !strings.Contains(string(b), `"turns":[]`) {
			t.Fatalf("%+v: %s", opts, b)
		}
		// resume: cursor must not move backwards / repull
		again, err := ReadRows(context.Background(), src, RowsOptions{Since: tl.NextCursor, Kinds: opts.Kinds})
		if err != nil || again.NextCursor != tl.NextCursor || len(again.Turns) != 0 {
			t.Fatalf("%+v resume: %v %q vs %q", opts, err, again.NextCursor, tl.NextCursor)
		}
	}
}

// REVIEW: --tail N with a filter, then --since next_cursor, picks up a later
// pending tool's result for a kept kind (bash) and the next bash row.
func TestReviewTailThenSinceKeptKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	reviewAppend(t, path, reviewLines1)
	src := RowsSource{Harness: "claude", Path: path}
	kinds := []string{"bash"}
	first, err := ReadRows(context.Background(), src, RowsOptions{Kinds: kinds, Tail: 1, SkipEmptyBody: true})
	if err != nil || len(first.Turns) != 1 || first.Turns[0].ID != "toolu_1" {
		t.Fatalf("tail: %v %v", err, idsOf(first.Turns))
	}
	reviewAppend(t, path, reviewLines2)
	next, err := ReadRows(context.Background(), src, RowsOptions{Since: first.NextCursor, Kinds: kinds, SkipEmptyBody: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Turns) != 0 || len(next.Updates) != 1 || !strings.Contains(next.Updates[0].Body, "BIGOUTPUT") {
		t.Fatalf("resume: turns %v updates %+v", idsOf(next.Turns), next.Updates)
	}
}

// REVIEW: --limit N with a filter must count kept rows, not all rows.
func TestReviewLimitCountsKeptRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	reviewAppend(t, path, reviewLines1)
	tl, err := ReadRows(context.Background(), RowsSource{Harness: "claude", Path: path}, RowsOptions{Kinds: []string{"bash"}, Limit: 1})
	if err != nil || len(tl.Turns) != 1 || tl.Turns[0].ID != "toolu_1" {
		t.Fatalf("--kinds bash --limit 1: %v %v", err, idsOf(tl.Turns))
	}
}

// REVIEW: bytes. One-shot read, and a client polling --since after every
// transcript line (live follow), filtered vs unfiltered.
func TestReviewBytesLog(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		src := RowsSource{Harness: h, Path: rowsFixture(t, h)}
		kinds := []string{"user", "assistant"}
		size := func(o RowsOptions) int {
			tl, _ := ReadRows(context.Background(), src, o)
			b, _ := json.Marshal(tl)
			return len(b)
		}
		t.Logf("REVIEWBYTES %s one-shot: unfiltered %d filtered %d", h, size(RowsOptions{}), size(RowsOptions{Kinds: kinds}))
		raw, _ := os.ReadFile(src.Path)
		lines := strings.SplitAfter(string(raw), "\n")
		path := filepath.Join(t.TempDir(), "live.jsonl")
		reviewAppend(t, path, "")
		live := RowsSource{Harness: h, Path: path}
		cursor := ""
		upd, tot, leakedUpd := 0, 0, 0
		for _, l := range lines {
			if l == "" {
				continue
			}
			reviewAppend(t, path, l)
			tl, err := ReadRows(context.Background(), live, RowsOptions{Since: cursor, Kinds: kinds})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(tl)
			tot += len(b)
			for _, u := range tl.Updates {
				ub, _ := json.Marshal(u)
				upd += len(ub)
				leakedUpd++
			}
			cursor = tl.NextCursor
		}
		t.Logf("REVIEWBYTES %s polled per line --kinds user,assistant: total %d bytes, %d update patches (%d bytes) for rows outside the allowlist or held", h, tot, leakedUpd, upd)
	}
}
