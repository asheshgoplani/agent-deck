package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The completion ledger is written only by the transition daemon. When that
// daemon is down — on 2026-07-20 a macOS Background Task Management LWCR
// mismatch left it crash-looping with EX_CONFIG for a week — every child's
// done_status read null forever even though each worker had printed the #1186
// sentinel as its final line. `session children` could not tell "no completion"
// from "nobody recorded the completion", so every documented
// `until … done_status != null` loop hung until its harness killed it.
//
// TranscriptDoneSignal is the daemon-independent read: the sentinel lives in
// the child's own transcript, so a supervising parent can recover it directly.

func writeDoneTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func doneAssistantLine(ts, text string) string {
	return `{"type":"assistant","timestamp":"` + ts + `","message":{"content":[{"type":"text","text":"` + text + `"}]}}`
}

func TestTranscriptDoneSignal_FindsSentinelInFinalAssistantTurn(t *testing.T) {
	path := writeDoneTranscript(t,
		`{"type":"user","timestamp":"2026-07-26T10:00:00.000Z","message":{"content":"go"}}`,
		doneAssistantLine("2026-07-26T10:05:00.000Z",
			`===AGENTDECK_DONE=== status=ok summary=T3/T7/T9 landed in 3 commits`),
	)

	sig, finishedAt, ok := TranscriptDoneSignal(path)
	if !ok {
		t.Fatal("expected the sentinel to be found in the final assistant turn")
	}
	if sig.Status != "ok" {
		t.Errorf("status: got %q, want %q", sig.Status, "ok")
	}
	if sig.Summary != "T3/T7/T9 landed in 3 commits" {
		t.Errorf("summary: got %q", sig.Summary)
	}
	want := time.Date(2026, 7, 26, 10, 5, 0, 0, time.UTC)
	if !finishedAt.Equal(want) {
		t.Errorf("finishedAt: got %v, want %v", finishedAt, want)
	}
}

// The sentinel-bearing record is not reliably the literal last line: Claude
// Code appends system/attachment records after the assistant turn, and
// sidechain (subagent) traffic interleaves freely.
func TestTranscriptDoneSignal_SkipsTrailingNoiseAndSidechains(t *testing.T) {
	path := writeDoneTranscript(t,
		doneAssistantLine("2026-07-26T10:05:00.000Z", `===AGENTDECK_DONE=== status=fail summary=gate:backend red`),
		`{"type":"assistant","isSidechain":true,"timestamp":"2026-07-26T10:05:01.000Z","message":{"content":[{"type":"text","text":"subagent chatter"}]}}`,
		`{"type":"system","timestamp":"2026-07-26T10:05:02.000Z"}`,
	)

	sig, _, ok := TranscriptDoneSignal(path)
	if !ok {
		t.Fatal("trailing system/sidechain records must not hide the sentinel")
	}
	if sig.Status != "fail" {
		t.Errorf("status: got %q, want %q", sig.Status, "fail")
	}
}

// A child that finished, then was handed new work, must NOT keep reporting the
// old completion: its newest main-chain assistant turn carries no sentinel.
// This is what stops a parent reading the previous round's report as the answer
// to the current one.
func TestTranscriptDoneSignal_NoneAfterNewWorkStarts(t *testing.T) {
	path := writeDoneTranscript(t,
		doneAssistantLine("2026-07-26T10:05:00.000Z", `===AGENTDECK_DONE=== status=ok summary=round one`),
		`{"type":"user","timestamp":"2026-07-26T10:30:00.000Z","message":{"content":"now do round two"}}`,
		doneAssistantLine("2026-07-26T10:31:00.000Z", `Working on it.`),
	)

	if _, _, ok := TranscriptDoneSignal(path); ok {
		t.Fatal("a superseded completion must not be reported as current")
	}
}

func TestTranscriptDoneSignal_NoSentinel(t *testing.T) {
	path := writeDoneTranscript(t, doneAssistantLine("2026-07-26T10:05:00.000Z", "just a normal reply"))
	if _, _, ok := TranscriptDoneSignal(path); ok {
		t.Fatal("a turn without a sentinel must not report a completion")
	}
}

// A missing/unreadable transcript is the common case for non-Claude tools and
// for children that have not produced a turn yet. It must be a quiet "no", not
// an error the caller has to special-case.
func TestTranscriptDoneSignal_MissingPath(t *testing.T) {
	if _, _, ok := TranscriptDoneSignal(""); ok {
		t.Error("empty path must report no completion")
	}
	if _, _, ok := TranscriptDoneSignal(filepath.Join(t.TempDir(), "nope.jsonl")); ok {
		t.Error("missing file must report no completion")
	}
}

// A sentinel whose record carries no parseable timestamp still counts as a
// completion — the caller degrades to "unknown finish time" rather than losing
// the signal. (Staleness classification then declines to age it out, which is
// the same fail-open rule completionIsStale already applies.)
func TestTranscriptDoneSignal_MissingTimestampStillReportsCompletion(t *testing.T) {
	path := writeDoneTranscript(t,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"===AGENTDECK_DONE=== status=ok summary=no clock"}]}}`,
	)
	sig, finishedAt, ok := TranscriptDoneSignal(path)
	if !ok {
		t.Fatal("a sentinel without a timestamp is still a completion")
	}
	if sig.Status != "ok" {
		t.Errorf("status: got %q", sig.Status)
	}
	if !finishedAt.IsZero() {
		t.Errorf("finishedAt: got %v, want zero", finishedAt)
	}
}
