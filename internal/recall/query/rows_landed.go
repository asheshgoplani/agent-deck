package query

import (
	"context"
	"os"
	"strings"
	"time"
)

// landedClockSkew is how far a row's timestamp may precede the send and
// still count: transcript and worker clocks are the same machine's, but the
// harness stamps the row when it reads the composer.
const landedClockSkew = 2 * time.Second

// FindLanded looks for a message sent by agent-deck in a native transcript,
// from byte offset from on, and returns the id of the row that proves it
// was delivered — the same id timeline and follow use — and its timestamp.
// This is how a queued send proves it landed. Evidence is:
//
//   - a user row whose text is the message (a slash command's command row
//     for a "/name args" message);
//   - a Claude queued message (queue-operation enqueue) only once it is
//     absorbed into the running turn. An enqueue alone is not delivery: the
//     entry can be removed again without ever reaching the model.
//
// Rows stamped before notBefore (less a small skew) are an earlier message
// with the same text, never this send; a zero notBefore disables the check.
func FindLanded(ctx context.Context, harness, path string, from int64, text string, notBefore time.Time) (id, ts string, ok bool) {
	if !SupportsDirectRows(harness) {
		return "", "", false
	}
	want := normalizeLanded(text)
	if want == "" {
		return "", "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < from {
		return "", "", false
	}
	fresh := func(rowTS string) bool {
		if notBefore.IsZero() {
			return true
		}
		t, err := time.Parse(time.RFC3339Nano, rowTS)
		return err == nil && !t.Before(notBefore.Add(-landedClockSkew))
	}
	pending := map[string]bool{} // enqueued copies of the message, by row id
	p := newRowParser(harness, rowParserState{})
	_, _ = scanLines(ctx, f, from, info.Size(), func(line []byte, _ int64) error {
		for _, fr := range p.line(line) {
			switch {
			case fr.Frame == "remove":
				delete(pending, fr.ID)
			case fr.Row == nil:
			case fr.Frame == "update" && pending[fr.Row.ID] && fr.Row.Queued != nil && !*fr.Row.Queued:
				// The enqueued copy was absorbed into the turn.
				if fresh(fr.Row.TS) {
					id, ts, ok = fr.Row.ID, fr.Row.TS, true
					return errStopScan
				}
			case fr.Frame != "row" || !landedMatch(fr.Row, want):
			case fr.Row.Queued != nil && *fr.Row.Queued:
				if fresh(fr.Row.TS) {
					pending[fr.Row.ID] = true
				}
			case fresh(fr.Row.TS):
				id, ts, ok = fr.Row.ID, fr.Row.TS, true
				return errStopScan
			}
		}
		return nil
	})
	return id, ts, ok
}

// landedMatch reports whether a row carries the sent text.
func landedMatch(r *Row, want string) bool {
	switch r.Kind {
	case "user":
		return normalizeLanded(r.Body) == want
	case "command":
		return strings.HasPrefix(want, "/") && normalizeLanded(r.Title) == want
	}
	return false
}

// normalizeLanded compares messages the way the terminal shows them:
// trimmed, with runs of whitespace folded.
func normalizeLanded(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
