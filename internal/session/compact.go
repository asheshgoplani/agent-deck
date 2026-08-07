package session

import (
	"bytes"
	"encoding/json"
	"os"
	"time"
)

// CompactBoundary is one compaction event as recorded in a Claude transcript.
//
// It exists because "did the compaction happen?" and "were the keystrokes
// delivered?" are different questions, and only the second one the send layer
// can answer. A `/compact` typed into a busy session is queued and runs minutes
// later, after the current turn ends; the send returns `submitted` immediately
// and says nothing about the outcome. Worse, a slash command that submits
// cleanly can still report `no_evidence`, because the delivery verifier looks
// for a state transition that a no-op compaction never makes.
//
// Claude writes `{"type":"system","subtype":"compact_boundary"}` the moment a
// compaction finishes, carrying the before/after sizes. That record is the only
// positive proof, so it is what this package reads.
type CompactBoundary struct {
	// UUID identifies this specific boundary. Comparing it against a baseline
	// captured before the send is how a caller tells a fresh compaction from
	// the one already sitting at the end of the transcript.
	UUID string
	// Trigger is "manual" for an explicit /compact, "auto" for the automatic
	// one Claude runs near the context limit.
	Trigger       string
	PreTokens     int
	PostTokens    int
	DroppedTokens int
	Duration      time.Duration
	Timestamp     time.Time
}

// Reclaimed reports how many tokens the compaction freed. It floors at zero:
// a compaction that grew the context (possible when the summary is longer than
// what it replaced, on an already-small conversation) is worth reporting as
// "nothing reclaimed" rather than as a negative saving.
func (c CompactBoundary) Reclaimed() int {
	if c.PreTokens <= c.PostTokens {
		return 0
	}
	return c.PreTokens - c.PostTokens
}

type compactBoundaryEntry struct {
	Type            string `json:"type"`
	Subtype         string `json:"subtype"`
	UUID            string `json:"uuid"`
	Timestamp       string `json:"timestamp"`
	CompactMetadata struct {
		Trigger                 string `json:"trigger"`
		PreTokens               int    `json:"preTokens"`
		PostTokens              int    `json:"postTokens"`
		CumulativeDroppedTokens int    `json:"cumulativeDroppedTokens"`
		DurationMs              int    `json:"durationMs"`
	} `json:"compactMetadata"`
}

// LatestCompactBoundaryForInstance returns the newest compaction recorded in the
// instance's Claude transcript. ok is false when there is no resolvable
// transcript (non-Claude tool, no session id yet, missing file) or the
// conversation has never been compacted — callers must treat those as "no
// baseline", never as "compacted at time zero".
func LatestCompactBoundaryForInstance(inst *Instance) (CompactBoundary, bool) {
	path := ClaudeTranscriptPathForInstance(inst)
	if path == "" {
		return CompactBoundary{}, false
	}
	return LatestCompactBoundaryInTranscript(path)
}

// LatestCompactBoundaryInTranscript scans the tail of a Claude JSONL transcript
// for the newest compact_boundary record.
//
// Tail-first with a growing window, same as currentContextTokensFromTail: a
// compaction record sits near the end of the file by construction (everything
// before it was just summarised away), but one adjacent tool result can be
// megabytes, so the window grows until the record is found or the file is
// exhausted.
func LatestCompactBoundaryInTranscript(path string) (CompactBoundary, bool) {
	f, err := os.Open(path)
	if err != nil {
		return CompactBoundary{}, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return CompactBoundary{}, false
	}
	size := info.Size()

	for window := int64(contextTailReadInitial); ; window *= 4 {
		if window > contextTailReadMax {
			window = contextTailReadMax
		}
		offset := size - window
		if offset < 0 {
			offset = 0
		}
		buf := make([]byte, size-offset)
		if _, err := f.ReadAt(buf, offset); err != nil {
			return CompactBoundary{}, false
		}
		lines := bytes.Split(buf, []byte("\n"))
		// A mid-file offset almost certainly landed inside a line; drop the
		// partial first fragment so it cannot half-parse.
		if offset > 0 && len(lines) > 0 {
			lines = lines[1:]
		}
		if b, ok := lastCompactBoundary(lines); ok {
			return b, true
		}
		if offset == 0 || window >= contextTailReadMax {
			return CompactBoundary{}, false
		}
	}
}

// lastCompactBoundary returns the last compact_boundary among the given JSONL
// lines, scanning backwards so the newest wins.
func lastCompactBoundary(lines [][]byte) (CompactBoundary, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		// Cheap reject before the full unmarshal: the overwhelming majority of
		// transcript lines are assistant/user turns, and some are megabytes.
		if !bytes.Contains(line, []byte(`"compact_boundary"`)) {
			continue
		}
		var entry compactBoundaryEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Type != "system" || entry.Subtype != "compact_boundary" {
			continue
		}
		b := CompactBoundary{
			UUID:          entry.UUID,
			Trigger:       entry.CompactMetadata.Trigger,
			PreTokens:     entry.CompactMetadata.PreTokens,
			PostTokens:    entry.CompactMetadata.PostTokens,
			DroppedTokens: entry.CompactMetadata.CumulativeDroppedTokens,
			Duration:      time.Duration(entry.CompactMetadata.DurationMs) * time.Millisecond,
		}
		if ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
			b.Timestamp = ts
		}
		return b, true
	}
	return CompactBoundary{}, false
}

// CompactedSince reports whether a compaction newer than the given baseline has
// been recorded. A nil baseline means "the transcript had never been compacted",
// so any boundary at all is new.
//
// Identity is the boundary UUID rather than the timestamp: two compactions can
// land in the same clock resolution on a fast, small conversation, and a
// timestamp comparison would then silently report the old one as the new one.
func CompactedSince(inst *Instance, baseline *CompactBoundary) (CompactBoundary, bool) {
	cur, ok := LatestCompactBoundaryForInstance(inst)
	if !compactedSince(cur, ok, baseline) {
		return CompactBoundary{}, false
	}
	return cur, true
}

// compactedSince is the comparison on its own, split out so it can be tested
// without a live instance and a transcript on disk.
func compactedSince(cur CompactBoundary, found bool, baseline *CompactBoundary) bool {
	if !found {
		return false
	}
	return baseline == nil || cur.UUID != baseline.UUID
}
