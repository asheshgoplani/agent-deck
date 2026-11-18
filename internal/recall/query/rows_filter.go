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
// what the client would see after merging every update.
func (o RowsOptions) keeps(row Row) bool {
	if len(o.Kinds) > 0 && !slices.Contains(o.Kinds, row.Kind) {
		return false
	}
	return !o.SkipEmptyBody || strings.TrimSpace(row.Body) != ""
}

// keepsUpdate reports whether an update patch for a row the client already
// holds may cross the wire. A patch rarely names its row's kind; one that
// names a kind outside the allowlist is dropped, and the body filter does
// not apply (a patch carries only the fields that changed).
func (o RowsOptions) keepsUpdate(patch Row) bool {
	return len(o.Kinds) == 0 || patch.Kind == "" || slices.Contains(o.Kinds, patch.Kind)
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

func filterUpdates(rows []Row, opts RowsOptions) []Row {
	if !opts.Filtered() || len(rows) == 0 {
		return rows
	}
	var out []Row
	for _, r := range rows {
		if opts.keepsUpdate(r) {
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
	id.Kind = row.Kind
	switch {
	case strings.TrimSpace(row.Body) != "":
		id.Body = "x"
	case row.Body != "":
		id.Body = " "
	}
	return id
}
