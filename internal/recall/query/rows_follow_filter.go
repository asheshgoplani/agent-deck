package query

import "slices"

// maxFollowDropped bounds the row ids a filtered follow remembers as not
// held by its client. Past it the oldest are forgotten; their late update
// or remove frames may then cross the wire, and a client ignores frames for
// ids it does not hold.
const maxFollowDropped = 1024

// FollowFilter applies a timeline read's Kinds / SkipEmptyBody filter to
// FollowRows frames, so a filtered stream carries what filtered timeline
// pages would:
//   - a row frame passes when the row passes the filter (sub-agent
//     children filtered too); otherwise its id is remembered as dropped;
//   - an update or remove frame for a dropped row (from this stream, or
//     listed in the after cursor by an earlier filtered read) is held back,
//     and an update naming a kind outside Kinds is held back as on a read;
//   - status, delivery and resync_required frames always pass.
//
// Cursors keep the next_cursor contract: Apply returns every frame's
// cursor, rewritten to list the dropped rows that are still open, even
// when it holds the frame back. The caller must still hand that cursor to
// the client, or a client whose last rows were all filtered out resumes
// from an older cursor and re-reads the same window.
type FollowFilter struct {
	after   string
	opts    RowsOptions
	dropped []string        // ids the client does not hold, newest last
	open    map[string]bool // dropped ids whose row may still change
}

// NewFollowFilter starts a filter for a follow that resumes at after.
func NewFollowFilter(after string, opts RowsOptions) *FollowFilter {
	f := &FollowFilter{after: after, opts: opts, open: map[string]bool{}}
	if c, err := decodeRowsCursor(after); err == nil {
		for _, id := range c.D {
			f.drop(id, true)
		}
	}
	return f
}

// Start is the cursor the follow resumes from, in the form Apply hands
// out, or "" when after is not a cursor (the follow then answers
// resync_required). A stream sends it before any frame, so a client that
// drops before the first frame still resumes at the right place.
func (f *FollowFilter) Start() string {
	if _, err := decodeRowsCursor(f.after); err != nil {
		return ""
	}
	if !f.opts.Filtered() {
		return f.after
	}
	return f.cursor(f.after)
}

// Apply reports whether fr goes to the client and returns it with its
// cursor rewritten. A held-back frame's cursor is the resume point too.
func (f *FollowFilter) Apply(fr RowFrame) (RowFrame, bool) {
	if !f.opts.Filtered() {
		return fr, true
	}
	send := true
	switch fr.Frame {
	case "row":
		if fr.Row == nil {
			break
		}
		if !f.opts.keeps(*fr.Row) {
			f.drop(fr.Row.ID, open(*fr.Row))
			send = false
			break
		}
		f.forget(fr.Row.ID)
		fr.Row = f.withChildren(*fr.Row)
	case "update":
		if fr.Row == nil {
			break
		}
		if slices.Contains(f.dropped, fr.Row.ID) {
			if closes(*fr.Row) {
				delete(f.open, fr.Row.ID)
			}
			send = false
			break
		}
		if !f.opts.keepsUpdate(*fr.Row, nil) {
			send = false
			break
		}
		fr.Row = f.withChildren(*fr.Row)
	case "remove":
		if slices.Contains(f.dropped, fr.ID) {
			f.forget(fr.ID)
			send = false
		}
	}
	if fr.Cursor != "" {
		fr.Cursor = f.cursor(fr.Cursor)
	}
	return fr, send
}

func (f *FollowFilter) withChildren(row Row) *Row {
	if len(row.Children) > 0 {
		row.Children = filterChildren([]Row{row}, f.opts)[0].Children
	}
	return &row
}

// closes reports whether an update patch ends a row's open state: a tool
// result arrived or a queued message was delivered.
func closes(patch Row) bool {
	return (patch.Finished != nil && *patch.Finished) || (patch.Queued != nil && !*patch.Queued)
}

func (f *FollowFilter) drop(id string, isOpen bool) {
	f.forget(id)
	f.dropped = append(f.dropped, id)
	if isOpen {
		f.open[id] = true
	}
	if len(f.dropped) > maxFollowDropped {
		delete(f.open, f.dropped[0])
		f.dropped = f.dropped[1:]
	}
}

func (f *FollowFilter) forget(id string) {
	if i := slices.Index(f.dropped, id); i >= 0 {
		f.dropped = slices.Delete(f.dropped, i, i+1)
	}
	delete(f.open, id)
}

// cursor rewrites a follow cursor so a filtered timeline read resumed from
// it also holds back patches for the open rows this stream dropped.
func (f *FollowFilter) cursor(s string) string {
	c, err := decodeRowsCursor(s)
	if err != nil {
		return s
	}
	c.D = nil
	for _, id := range f.dropped {
		if f.open[id] {
			c.D = append(c.D, id)
		}
	}
	if len(c.D) > maxDroppedOpen {
		c.D = c.D[len(c.D)-maxDroppedOpen:]
	}
	return encodeRowsCursor(c)
}
