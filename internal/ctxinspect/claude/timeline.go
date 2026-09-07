package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// This file answers one present-tense question the head parse cannot: which
// model is serving this session NOW?
//
// [ParseHead] deliberately stops at the first turn that carried the startup
// injections — that is where the anchor lives, and reading further would make
// the inventory's cost depend on conversation length. But the model identifier
// it finds there is a fact about the session's FIRST turn, and the header
// printed it as a present-tense fact about the session. On a real session that
// booted on claude-opus-5 and switched to claude-fable-5 two minutes in, the
// panel spent the rest of a multi-hour session naming a model the session was
// no longer on — and resolving the context window from it. The only record
// that can support "this session is on model X" is the transcript's most
// recent model turn, so this scan goes and reads it.
//
// The cost is one sequential pass over the file, the same price
// internal/session/analytics.go already pays to total a session's usage. It is
// kept cheap rather than bounded: every line is read with the same per-record
// byte cap as the head parse, but only lines that can possibly name a model —
// those containing the bytes `"model"` — are decoded at all, and everything
// else is discarded as it streams past.

// modelFieldMarker is the byte-level prefilter: a record that never mentions
// the word cannot carry message.model, so it is skipped without a JSON decode.
// A false positive (the word inside some captured content) merely costs the
// decode that then disqualifies it.
var modelFieldMarker = []byte(`"model"`)

// ModelSwitch is one observed change of serving model.
type ModelSwitch struct {
	// From and To are the model identifiers on either side of the change.
	From string
	To   string
	// At is the timestamp of the first record naming To, zero when that record
	// carried none.
	At time.Time
}

// ModelTimeline is what a full pass over the transcript's assistant records
// establishes about which model served the session over time.
type ModelTimeline struct {
	// First is the model named by the earliest assistant record that named
	// one. It is the same fact [Head.FirstTurn] or [Head.ModelSeen] reports,
	// re-derived here so the timeline stands on its own.
	First string
	// Current is the model named by the latest assistant record that named
	// one: the model the session is on, as far as the transcript can say.
	Current string
	// CurrentAt is that record's timestamp, zero when it carried none.
	CurrentAt time.Time
	// SwitchCount is how many times the serving model changed.
	SwitchCount int
	// LastSwitch is the change that established Current, nil when the session
	// never switched.
	LastSwitch *ModelSwitch
}

// ScanModelTimeline reads the whole transcript and returns the model timeline
// its assistant records describe.
//
// Placeholder records name themselves (Claude Code writes model "<synthetic>")
// and are ignored: a harness token is not a model, and treating an interrupted
// session's placeholders as a switch would fabricate a model change out of an
// auth failure. A record over the per-record byte cap is skipped, exactly as
// [ParseHead] skips it; the records after it still update the timeline.
//
// A transcript with no model-naming assistant record at all returns a timeline
// whose Current is empty, which is an honest unknown, not an error.
func ScanModelTimeline(path string) (*ModelTimeline, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening the Claude transcript %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	tl := &ModelTimeline{}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, truncated, rerr := readBoundedLine(r, maxRecordBytes)
		if len(line) == 0 && rerr != nil {
			if rerr == io.EOF {
				return tl, nil
			}
			return nil, fmt.Errorf("reading the Claude transcript %q: %w", path, rerr)
		}
		if !truncated && bytes.Contains(line, modelFieldMarker) {
			tl.observe(line)
		}
		if rerr == io.EOF {
			return tl, nil
		}
		if rerr != nil {
			return nil, fmt.Errorf("reading the Claude transcript %q: %w", path, rerr)
		}
	}
}

// observe folds one raw record into the timeline.
func (t *ModelTimeline) observe(line []byte) {
	var rec struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != "assistant" {
		return
	}
	model := rec.Message.Model
	if model == "" || strings.HasPrefix(model, "<") {
		return
	}
	var at time.Time
	if ts, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
		at = ts.UTC()
	}
	switch {
	case t.Current == "":
		t.First = model
	case model != t.Current:
		t.SwitchCount++
		t.LastSwitch = &ModelSwitch{From: t.Current, To: model, At: at}
	}
	t.Current, t.CurrentAt = model, at
}
