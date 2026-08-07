package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// boundaryLine renders a compact_boundary record in the exact shape Claude
// writes (captured from a real 2.1.224 transcript), so a change to that shape
// fails here rather than silently turning verification into "never compacted".
func compactBoundaryLine(uuid, trigger string, pre, post int, ts string) string {
	return `{"parentUuid":null,"logicalParentUuid":"x","isSidechain":false,` +
		`"type":"system","subtype":"compact_boundary","content":"Conversation compacted",` +
		`"level":"info","compactMetadata":{"trigger":"` + trigger + `",` +
		`"preTokens":` + strconv.Itoa(pre) + `,"postTokens":` + strconv.Itoa(post) +
		`,"cumulativeDroppedTokens":45031,"durationMs":96648},` +
		`"uuid":"` + uuid + `","timestamp":"` + ts + `"}`
}

func writeCompactTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return p
}

const compactAssistantLine = `{"type":"assistant","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":100}}}`

func TestLatestCompactBoundary_ParsesRealShape(t *testing.T) {
	p := writeCompactTranscript(t,
		compactAssistantLine,
		compactBoundaryLine("b1", "manual", 56868, 11837, "2026-08-07T17:23:30.184Z"),
		compactAssistantLine,
	)
	b, ok := LatestCompactBoundaryInTranscript(p)
	if !ok {
		t.Fatal("expected a boundary")
	}
	if b.UUID != "b1" || b.Trigger != "manual" {
		t.Fatalf("uuid/trigger = %q/%q", b.UUID, b.Trigger)
	}
	if b.PreTokens != 56868 || b.PostTokens != 11837 {
		t.Fatalf("tokens = %d -> %d", b.PreTokens, b.PostTokens)
	}
	if b.Reclaimed() != 45031 {
		t.Fatalf("reclaimed = %d, want 45031", b.Reclaimed())
	}
	if b.Duration != 96648*time.Millisecond {
		t.Fatalf("duration = %v", b.Duration)
	}
	if b.Timestamp.IsZero() {
		t.Fatal("timestamp not parsed")
	}
}

func TestLatestCompactBoundary_PicksNewest(t *testing.T) {
	p := writeCompactTranscript(t,
		compactBoundaryLine("old", "manual", 50000, 10000, "2026-08-07T17:23:30.184Z"),
		compactAssistantLine,
		compactBoundaryLine("new", "auto", 82132, 13878, "2026-08-07T17:26:42.357Z"),
		compactAssistantLine,
	)
	b, ok := LatestCompactBoundaryInTranscript(p)
	if !ok {
		t.Fatal("expected a boundary")
	}
	// Scanning backwards is the whole point: a caller that got "old" here would
	// wait out its full timeout while the compaction it asked for sat on disk.
	if b.UUID != "new" || b.Trigger != "auto" {
		t.Fatalf("got %q/%q, want new/auto", b.UUID, b.Trigger)
	}
}

func TestLatestCompactBoundary_NoneWhenNeverCompacted(t *testing.T) {
	p := writeCompactTranscript(t, compactAssistantLine, compactAssistantLine)
	if _, ok := LatestCompactBoundaryInTranscript(p); ok {
		t.Fatal("expected no boundary in an uncompacted transcript")
	}
}

func TestLatestCompactBoundary_MissingFile(t *testing.T) {
	if _, ok := LatestCompactBoundaryInTranscript(filepath.Join(t.TempDir(), "nope.jsonl")); ok {
		t.Fatal("expected ok=false for a missing transcript")
	}
}

func TestLatestCompactBoundary_IgnoresLookalikes(t *testing.T) {
	// A user pasting the string, and a malformed record, must not register as a
	// compaction — verification would then pass without anything having happened.
	p := writeCompactTranscript(t,
		`{"type":"user","message":{"content":"what does subtype \"compact_boundary\" mean?"}}`,
		`{"type":"system","subtype":"compact_boundary"`,
		compactAssistantLine,
	)
	if _, ok := LatestCompactBoundaryInTranscript(p); ok {
		t.Fatal("a mention and a truncated record must not count as a boundary")
	}
}

func TestLatestCompactBoundary_SkipsHugeLinesToReachBoundary(t *testing.T) {
	// A single multi-megabyte tool result after the boundary is the case the
	// growing read window exists for.
	huge := `{"type":"user","message":{"content":"` + strings.Repeat("x", 600*1024) + `"}}`
	p := writeCompactTranscript(t,
		compactBoundaryLine("deep", "manual", 90000, 12000, "2026-08-07T17:23:30.184Z"),
		huge,
		compactAssistantLine,
	)
	b, ok := LatestCompactBoundaryInTranscript(p)
	if !ok {
		t.Fatal("expected the boundary to be found past a 600KB line")
	}
	if b.UUID != "deep" {
		t.Fatalf("uuid = %q", b.UUID)
	}
}

func TestReclaimed_FloorsAtZero(t *testing.T) {
	// A small conversation can compact to something larger than it was. Negative
	// "savings" would render as "-3k reclaimed" in the CLI summary.
	b := CompactBoundary{PreTokens: 900, PostTokens: 1200}
	if got := b.Reclaimed(); got != 0 {
		t.Fatalf("reclaimed = %d, want 0", got)
	}
}

func TestCompactedSince_IdentityIsUUIDNotTimestamp(t *testing.T) {
	base := CompactBoundary{UUID: "b1", Timestamp: time.Now()}

	same := CompactBoundary{UUID: "b1", Timestamp: base.Timestamp}
	if compactedSince(same, true, &base) {
		t.Fatal("the same boundary must not read as a new compaction")
	}

	// Two compactions inside one timestamp tick: a timestamp comparison would
	// miss this, a UUID comparison catches it.
	fresh := CompactBoundary{UUID: "b2", Timestamp: base.Timestamp}
	if !compactedSince(fresh, true, &base) {
		t.Fatal("a different boundary at the same timestamp must count as new")
	}
}

func TestCompactedSince_NoBaselineMeansAnyBoundaryIsNew(t *testing.T) {
	if !compactedSince(CompactBoundary{UUID: "b1"}, true, nil) {
		t.Fatal("with no baseline, any boundary is a new compaction")
	}
	if compactedSince(CompactBoundary{}, false, nil) {
		t.Fatal("absent boundary must never read as a compaction")
	}
}
