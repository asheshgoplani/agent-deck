package query

import (
	"slices"
	"testing"
)

// A filtered follow holds back the row frames the filter drops and every
// later update or remove for them, and the cursor it returns for a
// held-back frame lists the dropped rows that are still open, so a
// filtered timeline read resumed from it holds their patches back too.
func TestFollowFilter_DropsRowsAndTheirPatches(t *testing.T) {
	cursor := encodeRowsCursor(rowsCursor{P: "/t.jsonl", O: 10, A: "a"})
	f := NewFollowFilter(cursor, RowsOptions{Kinds: []string{"user", "assistant"}})
	pendingTool := Row{ID: "tool-1", Kind: "bash", Finished: boolPtr(false)}
	frames := []struct {
		in   RowFrame
		send bool
	}{
		{RowFrame{Frame: "row", Row: &Row{ID: "u1", Kind: "user", Body: "hi"}}, true},
		{RowFrame{Frame: "row", Row: &pendingTool, Cursor: cursor}, false},
		{RowFrame{Frame: "update", Row: &Row{ID: "u1", Body: "hi again"}}, true},
		{RowFrame{Frame: "update", Row: &Row{ID: "tool-1", Body: "out"}}, false},
		{RowFrame{Frame: "status", LiveStatus: &LiveStatus{Running: true}}, true},
		{RowFrame{Frame: "remove", ID: "tool-1"}, false},
		{RowFrame{Frame: "remove", ID: "u1"}, true},
		{RowFrame{Frame: "resync_required", Reason: "source_moved"}, true},
	}
	for i, fr := range frames {
		out, send := f.Apply(fr.in)
		if send != fr.send {
			t.Fatalf("frame %d (%s %+v): send = %v, want %v", i, fr.in.Frame, fr.in.Row, send, fr.send)
		}
		if i == 1 {
			c, err := decodeRowsCursor(out.Cursor)
			if err != nil || !slices.Equal(c.D, []string{"tool-1"}) {
				t.Fatalf("held-back cursor D = %v (err %v), want [tool-1]", c.D, err)
			}
		}
	}
}

// Dropped ids carried in the after cursor (an earlier filtered read) are
// held back, and a closing patch stops listing the row in later cursors.
func TestFollowFilter_ResumesCursorDroppedIDs(t *testing.T) {
	after := encodeRowsCursor(rowsCursor{P: "/t.jsonl", O: 10, A: "a", D: []string{"tool-9"}})
	f := NewFollowFilter(after, RowsOptions{SkipEmptyBody: true})
	out, send := f.Apply(RowFrame{Frame: "update", Row: &Row{ID: "tool-9", Finished: boolPtr(true)}, Cursor: after})
	if send {
		t.Fatal("patch for a row the earlier read dropped was sent")
	}
	if c, _ := decodeRowsCursor(out.Cursor); len(c.D) != 0 {
		t.Fatalf("closed row still listed: D = %v", c.D)
	}
	if out, send := f.Apply(RowFrame{Frame: "row", Row: &Row{ID: "a1", Kind: "assistant", Body: " "}}); send {
		t.Fatalf("empty-body row sent: %+v", out)
	}
}

// With no filter every frame passes unchanged.
func TestFollowFilter_Unfiltered(t *testing.T) {
	f := NewFollowFilter("", RowsOptions{})
	in := RowFrame{Frame: "row", Row: &Row{ID: "x", Kind: "bash"}, Cursor: "c"}
	if out, send := f.Apply(in); !send || out.Cursor != "c" || out.Row != in.Row {
		t.Fatalf("unfiltered Apply = %+v, %v", out, send)
	}
}

// Start is the after cursor in the form Apply hands out: unchanged with no
// filter, carrying its dropped-open ids with one, and "" for a value that
// is not a cursor.
func TestFollowFilter_Start(t *testing.T) {
	after := encodeRowsCursor(rowsCursor{P: "/t.jsonl", O: 10, A: "a", D: []string{"tool-9"}})
	if got := NewFollowFilter(after, RowsOptions{}).Start(); got != after {
		t.Fatalf("unfiltered Start = %q, want after unchanged", got)
	}
	c, err := decodeRowsCursor(NewFollowFilter(after, RowsOptions{SkipEmptyBody: true}).Start())
	if err != nil || c.O != 10 || !slices.Equal(c.D, []string{"tool-9"}) {
		t.Fatalf("filtered Start = %+v (err %v), want offset 10 and D [tool-9]", c, err)
	}
	if got := NewFollowFilter("not-a-cursor", RowsOptions{}).Start(); got != "" {
		t.Fatalf("Start for an invalid cursor = %q, want empty", got)
	}
}
