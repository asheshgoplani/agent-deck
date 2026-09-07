package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAccountingTranscript writes lines to a temp JSONL file and returns its path.
func writeAccountingTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// assistantLine builds an assistant record with the given usage counters.
func assistantLine(model string, input, output, cacheCreate, cacheRead int) string {
	return fmt.Sprintf(
		`{"type":"assistant","timestamp":"2026-07-26T10:00:00Z","message":{"model":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}`,
		model, input, output, cacheCreate, cacheRead,
	)
}

// TestParseSessionJSONL_CacheWriteTurnCountsCacheCreation is the regression this
// whole fix exists for: a cache-write turn puts nearly the entire prompt in
// cache_creation_input_tokens. Omitting it reported a 6-token context for a
// ~125k-token prompt.
func TestParseSessionJSONL_CacheWriteTurnCountsCacheCreation(t *testing.T) {
	path := writeAccountingTranscript(t, assistantLine("claude-opus-4-7", 6, 42, 125207, 0))

	analytics, err := ParseSessionJSONL(path)
	if err != nil {
		t.Fatalf("ParseSessionJSONL: %v", err)
	}

	want := 6 + 125207
	if analytics.CurrentContextTokens != want {
		t.Errorf("CurrentContextTokens = %d, want %d (input + cache_creation + cache_read)",
			analytics.CurrentContextTokens, want)
	}
}

// TestParseSessionJSONL_ContextTokensSumAllPromptSideCounters covers the mixed
// case: a cache-read turn that also creates new cache entries.
func TestParseSessionJSONL_ContextTokensSumAllPromptSideCounters(t *testing.T) {
	path := writeAccountingTranscript(t,
		assistantLine("claude-opus-4-7", 6, 10, 125207, 0),
		assistantLine("claude-opus-4-7", 12, 20, 3000, 700000),
	)

	analytics, err := ParseSessionJSONL(path)
	if err != nil {
		t.Fatalf("ParseSessionJSONL: %v", err)
	}

	// Last turn wins for the live context figure.
	if want := 12 + 3000 + 700000; analytics.CurrentContextTokens != want {
		t.Errorf("CurrentContextTokens = %d, want %d", analytics.CurrentContextTokens, want)
	}
	// Cumulative totals stay per-counter.
	if analytics.CacheWriteTokens != 125207+3000 {
		t.Errorf("CacheWriteTokens = %d, want %d", analytics.CacheWriteTokens, 125207+3000)
	}
	if analytics.CacheReadTokens != 700000 {
		t.Errorf("CacheReadTokens = %d, want 700000", analytics.CacheReadTokens)
	}
}

// TestParseSessionJSONL_UsagelessRecordDoesNotResetContext guards the trailing
// synthetic/error assistant message, which carries no usage block. Taking it as
// "the last turn" would zero a live context.
func TestParseSessionJSONL_UsagelessRecordDoesNotResetContext(t *testing.T) {
	path := writeAccountingTranscript(t,
		assistantLine("claude-opus-4-7", 6, 10, 125207, 0),
		`{"type":"assistant","timestamp":"2026-07-26T10:01:00Z","message":{"model":"<synthetic>","content":[{"type":"text"}]}}`,
	)

	analytics, err := ParseSessionJSONL(path)
	if err != nil {
		t.Fatalf("ParseSessionJSONL: %v", err)
	}

	if want := 6 + 125207; analytics.CurrentContextTokens != want {
		t.Errorf("CurrentContextTokens = %d, want %d (usage-less record must not reset it)",
			analytics.CurrentContextTokens, want)
	}
	if analytics.Model != "claude-opus-4-7" {
		t.Errorf("Model = %q, want claude-opus-4-7 (synthetic model must not win)", analytics.Model)
	}
}

// TestParseSessionJSONL_MalformedLineRecordedAsGap: a skipped line must be
// counted, not silently dropped.
func TestParseSessionJSONL_MalformedLineRecordedAsGap(t *testing.T) {
	path := writeAccountingTranscript(t,
		assistantLine("claude-opus-4-7", 100, 10, 0, 0),
		`{"type":"assistant","message":{`, // truncated JSON
		assistantLine("claude-opus-4-7", 200, 20, 0, 0),
	)

	analytics, err := ParseSessionJSONL(path)
	if err != nil {
		t.Fatalf("ParseSessionJSONL: %v", err)
	}

	if analytics.ParseGaps != 1 {
		t.Fatalf("ParseGaps = %d, want 1", analytics.ParseGaps)
	}
	if !analytics.HasParseGaps() {
		t.Error("HasParseGaps() = false, want true")
	}
	if len(analytics.ParseGapSample) != 1 {
		t.Fatalf("ParseGapSample len = %d, want 1", len(analytics.ParseGapSample))
	}
	gap := analytics.ParseGapSample[0]
	if gap.Line != 2 {
		t.Errorf("gap.Line = %d, want 2", gap.Line)
	}
	if gap.Reason != ParseGapMalformed {
		t.Errorf("gap.Reason = %q, want %q", gap.Reason, ParseGapMalformed)
	}
	// Parsing continues past the bad line.
	if analytics.TotalTurns != 2 {
		t.Errorf("TotalTurns = %d, want 2", analytics.TotalTurns)
	}
	if analytics.CurrentContextTokens != 200 {
		t.Errorf("CurrentContextTokens = %d, want 200", analytics.CurrentContextTokens)
	}
}

// TestParseSessionJSONL_BlankLinesAreNotGaps: whitespace-only lines are normal
// file structure, not parse failures.
func TestParseSessionJSONL_BlankLinesAreNotGaps(t *testing.T) {
	path := writeAccountingTranscript(t,
		assistantLine("claude-opus-4-7", 100, 10, 0, 0),
		"",
		"   ",
		assistantLine("claude-opus-4-7", 200, 20, 0, 0),
	)

	analytics, err := ParseSessionJSONL(path)
	if err != nil {
		t.Fatalf("ParseSessionJSONL: %v", err)
	}
	if analytics.ParseGaps != 0 {
		t.Errorf("ParseGaps = %d, want 0 (blank lines are not gaps)", analytics.ParseGaps)
	}
	if analytics.TotalTurns != 2 {
		t.Errorf("TotalTurns = %d, want 2", analytics.TotalTurns)
	}
}

// TestParseSessionJSONL_OversizeLineIsGapNotTruncation is the important one:
// bufio.Scanner returns ErrTooLong and stops, which would discard every record
// after the oversize line, including the newest usage record. The reader must
// skip that one line, record a gap, and keep going.
func TestParseSessionJSONL_OversizeLineIsGapNotTruncation(t *testing.T) {
	// Shrink the cap so the fixture stays small; the code path is identical.
	orig := maxTranscriptLineBytes
	maxTranscriptLineBytes = 4096
	defer func() { maxTranscriptLineBytes = orig }()

	huge := `{"type":"user","payload":"` + strings.Repeat("x", maxTranscriptLineBytes+1024) + `"}`
	path := writeAccountingTranscript(t,
		assistantLine("claude-opus-4-7", 100, 10, 0, 0),
		huge,
		assistantLine("claude-opus-4-7", 999, 20, 0, 0),
	)

	analytics, err := ParseSessionJSONL(path)
	if err != nil {
		t.Fatalf("ParseSessionJSONL: %v", err)
	}

	if analytics.ParseGaps != 1 {
		t.Fatalf("ParseGaps = %d, want 1", analytics.ParseGaps)
	}
	if got := analytics.ParseGapSample[0].Reason; got != ParseGapOversize {
		t.Errorf("gap.Reason = %q, want %q", got, ParseGapOversize)
	}
	if analytics.ParseGapSample[0].Bytes <= maxTranscriptLineBytes {
		t.Errorf("gap.Bytes = %d, want > %d", analytics.ParseGapSample[0].Bytes, maxTranscriptLineBytes)
	}
	// The record AFTER the oversize line must still be seen.
	if analytics.CurrentContextTokens != 999 {
		t.Errorf("CurrentContextTokens = %d, want 999 (records after an oversize line must survive)",
			analytics.CurrentContextTokens)
	}
	if analytics.TotalTurns != 2 {
		t.Errorf("TotalTurns = %d, want 2", analytics.TotalTurns)
	}
}

// TestParseSessionJSONL_GapSampleIsCapped keeps a pathological file from
// retaining unbounded diagnostics while still counting every gap.
func TestParseSessionJSONL_GapSampleIsCapped(t *testing.T) {
	lines := make([]string, 0, maxParseGapSamples+5)
	for i := 0; i < maxParseGapSamples+5; i++ {
		lines = append(lines, "{not json")
	}
	path := writeAccountingTranscript(t, lines...)

	analytics, err := ParseSessionJSONL(path)
	if err != nil {
		t.Fatalf("ParseSessionJSONL: %v", err)
	}
	if analytics.ParseGaps != maxParseGapSamples+5 {
		t.Errorf("ParseGaps = %d, want %d", analytics.ParseGaps, maxParseGapSamples+5)
	}
	if len(analytics.ParseGapSample) != maxParseGapSamples {
		t.Errorf("ParseGapSample len = %d, want %d (capped)", len(analytics.ParseGapSample), maxParseGapSamples)
	}
}

// TestParseSessionJSONL_CleanTranscriptHasNoGaps is the negative control: the
// warning must not fire on a healthy transcript.
func TestParseSessionJSONL_CleanTranscriptHasNoGaps(t *testing.T) {
	path := writeAccountingTranscript(t,
		assistantLine("claude-opus-4-7", 100, 10, 0, 0),
		`{"type":"user","message":{"content":"hi"}}`,
	)

	analytics, err := ParseSessionJSONL(path)
	if err != nil {
		t.Fatalf("ParseSessionJSONL: %v", err)
	}
	if analytics.HasParseGaps() {
		t.Errorf("HasParseGaps() = true (gaps=%d), want false", analytics.ParseGaps)
	}
	if analytics.ParseGapSample != nil {
		t.Errorf("ParseGapSample = %v, want nil", analytics.ParseGapSample)
	}
}

// TestParseSessionJSONL_NoTrailingNewline covers the last line of a file that
// the harness is still appending to.
func TestParseSessionJSONL_NoTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	body := assistantLine("claude-opus-4-7", 100, 10, 0, 0) + "\n" +
		assistantLine("claude-opus-4-7", 555, 20, 0, 0) // no trailing \n
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	analytics, err := ParseSessionJSONL(path)
	if err != nil {
		t.Fatalf("ParseSessionJSONL: %v", err)
	}
	if analytics.TotalTurns != 2 {
		t.Errorf("TotalTurns = %d, want 2", analytics.TotalTurns)
	}
	if analytics.CurrentContextTokens != 555 {
		t.Errorf("CurrentContextTokens = %d, want 555", analytics.CurrentContextTokens)
	}
	if analytics.ParseGaps != 0 {
		t.Errorf("ParseGaps = %d, want 0", analytics.ParseGaps)
	}
}

// TestParseSessionJSONL_EmptyFile must not error and must not invent gaps.
func TestParseSessionJSONL_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	analytics, err := ParseSessionJSONL(path)
	if err != nil {
		t.Fatalf("ParseSessionJSONL: %v", err)
	}
	if analytics.ParseGaps != 0 || analytics.TotalTurns != 0 {
		t.Errorf("gaps=%d turns=%d, want 0/0", analytics.ParseGaps, analytics.TotalTurns)
	}
}

// TestContextWindowForModel_ExportedMatchesInternal keeps the exported wrapper
// honest so other packages resolve windows from this one table.
func TestContextWindowForModel_ExportedMatchesInternal(t *testing.T) {
	for _, model := range []string{
		"claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6",
		"claude-opus-4-20250514", "claude-3-5-sonnet", "MiniMax-M2.5-highspeed",
		"totally-unknown-model", "",
	} {
		if got, want := ContextWindowForModel(model), contextWindowForModel(model); got != want {
			t.Errorf("ContextWindowForModel(%q) = %d, want %d", model, got, want)
		}
	}
}
