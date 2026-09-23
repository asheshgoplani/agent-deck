package query

import (
	"context"
	"os"
	"strings"
)

// FindLanded looks for a message sent by agent-deck in a native transcript,
// from byte offset from on: a user row (or a queued user row) whose text is
// the message. It returns that row's id — the same id timeline and follow
// use — and its timestamp. This is how a queued send proves it landed.
func FindLanded(ctx context.Context, harness, path string, from int64, text string) (id, ts string, ok bool) {
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
	p := newRowParser(harness, rowParserState{})
	_, _ = scanLines(ctx, f, from, info.Size(), func(line []byte, _ int64) error {
		for _, fr := range p.line(line) {
			if fr.Frame != "row" || fr.Row == nil || fr.Row.Kind != "user" {
				continue
			}
			if normalizeLanded(fr.Row.Body) == want {
				id, ts, ok = fr.Row.ID, fr.Row.TS, true
				return errStopScan
			}
		}
		return nil
	})
	return id, ts, ok
}

// normalizeLanded compares messages the way the terminal shows them:
// trimmed, with runs of whitespace folded.
func normalizeLanded(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
