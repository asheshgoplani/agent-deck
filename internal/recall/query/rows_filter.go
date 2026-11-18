package query

import (
	"fmt"
	"slices"
	"strings"
)

// RowKinds is the row kind vocabulary a timeline filter accepts: every
// kind the native parsers and the index mapping put on a row.
var RowKinds = []string{"user", "assistant", "thinking", "tool", "bash", "edit", "read", "subagent", "todo", "question", "skill", "command", "system", "compaction", "turn_end", "other"}

// ParseRowKinds splits a --kinds allowlist. An empty list, an empty entry
// or a kind outside RowKinds is an error that names the valid kinds.
func ParseRowKinds(list string) ([]string, error) {
	if strings.TrimSpace(list) == "" {
		return nil, fmt.Errorf("recall rows: --kinds needs a comma-separated list; valid kinds: %s", strings.Join(RowKinds, ", "))
	}
	parts := strings.Split(list, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	if err := validateRowKinds(parts); err != nil {
		return nil, err
	}
	return parts, nil
}

func validateRowKinds(kinds []string) error {
	for i, k := range kinds {
		if k == "" {
			return fmt.Errorf("recall rows: --kinds has an empty kind at position %d; valid kinds: %s", i+1, strings.Join(RowKinds, ", "))
		}
		if !slices.Contains(RowKinds, k) {
			return fmt.Errorf("recall rows: unknown kind %q in --kinds; valid kinds: %s", k, strings.Join(RowKinds, ", "))
		}
	}
	return nil
}

// Filtered reports whether opts drop rows by kind or empty body.
func (o RowsOptions) Filtered() bool { return len(o.Kinds) > 0 || o.SkipEmptyBody }

// keeps reports whether row passes the filter. A row's kind and body are
// what the client would see after merging every update. --skip-empty-body
// keeps a tool call still waiting for its result (finished:false): its
// output arrives later as an update patch, and a client can only merge that
// into a row it already holds.
func (o RowsOptions) keeps(row Row) bool {
	if len(o.Kinds) > 0 && !slices.Contains(o.Kinds, row.Kind) {
		return false
	}
	return !o.SkipEmptyBody || strings.TrimSpace(row.Body) != "" || pending(row)
}

func pending(row Row) bool { return row.Finished != nil && !*row.Finished }

// keepsUpdate reports whether an update patch for a row outside this read
// may cross the wire. A patch for a row an earlier filtered read dropped
// (an id in dropped) never does: the client does not hold that row. A patch
// that names a kind outside the allowlist is dropped too. Other patches are
// for rows the client holds; the body filter does not apply to them (a
// patch carries only the fields that changed).
func (o RowsOptions) keepsUpdate(patch Row, dropped []string) bool {
	if slices.Contains(dropped, patch.ID) {
		return false
	}
	return len(o.Kinds) == 0 || patch.Kind == "" || slices.Contains(o.Kinds, patch.Kind)
}

// maxDroppedOpen bounds the dropped open ids a cursor carries. Past it the
// oldest are forgotten, and their late results may cross the wire again
// (a client ignores them), which keeps the cursor small.
const maxDroppedOpen = 64

// open reports whether a row may still change through update patches: a
// tool call waiting for its result or a message still queued.
func open(row Row) bool { return pending(row) || (row.Queued != nil && *row.Queued) }

// droppedOpen appends the open rows the filter drops, keeping the newest
// maxDroppedOpen ids.
func droppedOpen(ids []string, rows []Row, opts RowsOptions) []string {
	if !opts.Filtered() {
		return nil
	}
	for _, r := range rows {
		if open(r) && !opts.keeps(r) && !slices.Contains(ids, r.ID) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) > maxDroppedOpen {
		ids = ids[len(ids)-maxDroppedOpen:]
	}
	return ids
}

// stillDropped is the dropped-open list for the cursor after a resumed
// read: earlier ids whose rows did not close or vanish in this read, plus
// the open rows this read dropped.
func stillDropped(prev []string, set *rowSet, opts RowsOptions) []string {
	if !opts.Filtered() {
		return nil
	}
	var ids []string
	for _, id := range prev {
		if slices.Contains(set.removed, id) {
			continue
		}
		if j, ok := set.updateIdx[id]; ok {
			u := set.updates[j]
			closedTool := u.Finished != nil && *u.Finished
			delivered := u.Queued != nil && !*u.Queued
			if closedTool || delivered {
				continue
			}
		}
		ids = append(ids, id)
	}
	return droppedOpen(ids, set.list(), opts)
}

// FilterRows keeps the rows that pass opts' filter, in order. With no
// filter it returns rows unchanged.
func FilterRows(rows []Row, opts RowsOptions) []Row {
	if !opts.Filtered() {
		return rows
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		if opts.keeps(r) {
			out = append(out, r)
		}
	}
	return out
}

// filterChildren applies the filter to attached sub-agent children, so a
// kept subagent row does not carry the sidechain's dropped kinds.
func filterChildren(rows []Row, opts RowsOptions) []Row {
	if !opts.Filtered() {
		return rows
	}
	for i := range rows {
		if len(rows[i].Children) == 0 {
			continue
		}
		children := FilterRows(filterChildren(rows[i].Children, opts), opts)
		if len(children) == 0 {
			children = nil
		}
		rows[i].Children = children
	}
	return rows
}

// keptCounter counts the rows of a rowSet that pass a filter, updated per
// applied frame: a frame can add, replace, merge into (a body arriving
// later), move or remove a row, and only that row's membership can change.
type keptCounter struct {
	opts RowsOptions
	in   map[string]bool
	n    int
}

func newKeptCounter(opts RowsOptions) *keptCounter {
	return &keptCounter{opts: opts, in: map[string]bool{}}
}

func (k *keptCounter) touch(s *rowSet, f RowFrame) {
	if !k.opts.Filtered() {
		return
	}
	id := f.ID
	if f.Row != nil {
		id = f.Row.ID
	}
	i, ok := s.idx[id]
	now := ok && k.opts.keeps(s.rows[i])
	if now == k.in[id] {
		return
	}
	if now {
		k.n++
	} else {
		k.n--
	}
	k.in[id] = now
}

func filterUpdates(rows []Row, opts RowsOptions, dropped []string) []Row {
	if !opts.Filtered() || len(rows) == 0 {
		return rows
	}
	var out []Row
	for _, r := range rows {
		if opts.keepsUpdate(r, dropped) {
			out = append(out, r)
		}
	}
	return out
}

// filterIdentity is the lightweight row the older-history pass keeps when
// a filter is set: identity plus what the filter reads. The body is reduced
// to a marker with the same emptiness ("x" or " "), so merging updates
// still decides --skip-empty-body exactly as the full row would.
func filterIdentity(row Row, opts RowsOptions) Row {
	id := Row{ID: row.ID, TS: row.TS, Queued: row.Queued}
	if !opts.Filtered() {
		return id
	}
	id.Kind, id.Finished = row.Kind, row.Finished
	switch {
	case strings.TrimSpace(row.Body) != "":
		id.Body = "x"
	case row.Body != "":
		id.Body = " "
	}
	return id
}
