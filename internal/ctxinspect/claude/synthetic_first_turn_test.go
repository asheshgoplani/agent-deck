package claude

import (
	"strings"
	"testing"
)

// The shape a real interrupted or auth-failed Claude Code session has on disk:
// two <synthetic> assistant records carrying usage.input_tokens 0, then the
// session's actual model turns.
//
// Four live sessions on a maintainer's deck looked exactly like this, and every
// one of them reported "no model identifier was recorded for this session" and
// "this session has not completed a model turn yet" while holding 187, 124, 114
// and 86 completed turns naming claude-fable-5. The static fixture matrix could
// not have caught it: all eighteen of its worlds were transcripts somebody
// imagined, and nobody imagines a synthetic zero-usage preamble.
const (
	recSyntheticZeroUsage = `{"type":"assistant","sessionId":"s1","message":{"model":"<synthetic>",` +
		`"usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	recRealTurnAfterSynthetic = `{"type":"assistant","sessionId":"s1","message":{"model":"claude-fable-5",` +
		`"usage":{"input_tokens":1200,"cache_creation_input_tokens":300,"cache_read_input_tokens":500}}}`
)

// syntheticPreamble is that shape as records, ready for writeTranscript.
func syntheticPreamble() []string {
	return []string{recSyntheticZeroUsage, recSyntheticZeroUsage, recRealTurnAfterSynthetic}
}

// TestParseHead_SyntheticPreambleDoesNotConsumeTheFirstTurn is the regression
// test for the fabricated-reason bug. An unusable record must not spend the
// only attempt at reading a turn.
func TestParseHead_SyntheticPreambleDoesNotConsumeTheFirstTurn(t *testing.T) {
	head, err := ParseHead(writeTranscript(t, syntheticPreamble()...))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.FirstTurn == nil {
		t.Fatal("FirstTurn is nil: the synthetic zero-usage records consumed the only attempt, so the report will claim this session never completed a model turn")
	}
	if got, want := head.FirstTurn.Model, "claude-fable-5"; got != want {
		t.Errorf("FirstTurn.Model = %q, want %q — the model is what resolves the context window, so losing it costs the gauge as well as the label", got, want)
	}
	if got, want := head.FirstTurn.PromptTokens(), 2000; got != want {
		t.Errorf("FirstTurn.PromptTokens() = %d, want %d", got, want)
	}
}

// TestParseHead_UnusableTurnsWarnOnce keeps the caveat list readable: a
// transcript full of usage-less records must not produce one caveat per record.
func TestParseHead_UnusableTurnsWarnOnce(t *testing.T) {
	head, err := ParseHead(writeTranscript(t, syntheticPreamble()...))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	zeroWarnings := 0
	for _, w := range head.Warnings {
		if strings.Contains(w, "zero prompt total") {
			zeroWarnings++
		}
	}
	if zeroWarnings > 1 {
		t.Errorf("got %d zero-prompt warnings, want at most 1: a run of unusable records must warn once, not once each", zeroWarnings)
	}
}

// TestParseHead_LaterTurnDoesNotOverwriteAGoodOne guards the other direction:
// once a usable turn is on record, a later pre-block turn must not replace it.
// Only the boundary record may do that.
func TestParseHead_LaterTurnDoesNotOverwriteAGoodOne(t *testing.T) {
	head, err := ParseHead(writeTranscript(t,
		`{"type":"assistant","sessionId":"s1","message":{"model":"claude-opus-5","usage":{"input_tokens":1000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		`{"type":"assistant","sessionId":"s1","message":{"model":"claude-opus-5","usage":{"input_tokens":9999,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.FirstTurn == nil {
		t.Fatal("FirstTurn is nil")
	}
	if got, want := head.FirstTurn.PromptTokens(), 1000; got != want {
		t.Errorf("FirstTurn.PromptTokens() = %d, want %d: the first usable turn must be kept, not the last one seen", got, want)
	}
}
