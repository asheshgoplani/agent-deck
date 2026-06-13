package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #1186: the transcript-tail scan behind completion-sentinel detection.
// Shared by the Stop-hook handler (immediate scan) and the transition daemon
// (flush-race rescan). The path is the injectable source: tests point it at a
// temp file, no live agent required.

// writeScanTranscript writes JSONL lines to a temp file and returns its path.
func writeScanTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// scanAssistantLine builds a transcript assistant message whose text content
// holds the supplied body.
func scanAssistantLine(t *testing.T, body string) string {
	t.Helper()
	msg := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": body}},
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal assistant line: %v", err)
	}
	return string(b)
}

const scanUserLine = `{"type":"user","message":{"role":"user","content":"next prompt"}}`

func TestScanTranscriptTailForDone_OK(t *testing.T) {
	path := writeScanTranscript(t,
		scanAssistantLine(t, "doing work"),
		scanAssistantLine(t, "all set.\n===AGENTDECK_DONE=== status=ok summary=feature shipped"),
	)
	sig, found, pending := ScanTranscriptTailForDone(path)
	if !found || pending {
		t.Fatalf("expected conclusive sentinel, got found=%v pending=%v", found, pending)
	}
	if sig.Status != "ok" || sig.Summary != "feature shipped" {
		t.Errorf("got status=%q summary=%q", sig.Status, sig.Summary)
	}
}

func TestScanTranscriptTailForDone_Fail(t *testing.T) {
	path := writeScanTranscript(t,
		scanAssistantLine(t, "===AGENTDECK_DONE=== status=fail summary=could not build"),
	)
	sig, found, pending := ScanTranscriptTailForDone(path)
	if !found || pending {
		t.Fatalf("expected conclusive sentinel, got found=%v pending=%v", found, pending)
	}
	if sig.Status != "fail" || sig.Summary != "could not build" {
		t.Errorf("got status=%q summary=%q", sig.Status, sig.Summary)
	}
}

func TestScanTranscriptTailForDone_NoSentinel(t *testing.T) {
	path := writeScanTranscript(t,
		scanAssistantLine(t, "just an ordinary mid-task turn, no sentinel here"),
	)
	if _, found, pending := ScanTranscriptTailForDone(path); found || pending {
		t.Errorf("ordinary flushed turn: want found=false pending=false, got found=%v pending=%v", found, pending)
	}
}

func TestScanTranscriptTailForDone_MalformedIgnored(t *testing.T) {
	path := writeScanTranscript(t,
		scanAssistantLine(t, "===AGENTDECK_DONE=== status=maybe summary=garbage"),
	)
	if _, found, _ := ScanTranscriptTailForDone(path); found {
		t.Errorf("expected malformed sentinel to be ignored")
	}
}

func TestScanTranscriptTailForDone_MissingFile(t *testing.T) {
	_, found, pending := ScanTranscriptTailForDone(filepath.Join(t.TempDir(), "nope.jsonl"))
	if found {
		t.Errorf("expected missing transcript to yield no sentinel, not a crash")
	}
	if pending {
		t.Errorf("missing transcript must not report pending — callers would spin on it")
	}
}

// Regression: Claude Code appends system / attachment records after the
// assistant turn (observed tail: `..., assistant, system, system`), so the
// sentinel-bearing assistant record is not the literal last transcript line.
// On a last-line-only scan, finished events never fire at all on current
// transcript formats. The scan must walk back through that noise.
func TestScanTranscriptTailForDone_TrailingSystemRecords(t *testing.T) {
	path := writeScanTranscript(t,
		`{"type":"user","message":{"role":"user","content":"do the thing"}}`,
		scanAssistantLine(t, "all done\n===AGENTDECK_DONE=== status=ok summary=fix landed"),
		`{"type":"system","subtype":"hook_result"}`,
		`{"type":"system","subtype":"turn_duration"}`,
	)
	sig, found, pending := ScanTranscriptTailForDone(path)
	if !found || pending {
		t.Fatalf("expected sentinel through trailing system records, got found=%v pending=%v", found, pending)
	}
	if sig.Status != "ok" || sig.Summary != "fix landed" {
		t.Fatalf("wrong signal parsed: %+v", sig)
	}
}

// Regression: sidechain (subagent) assistant records interleave with the main
// chain and must never be mined for a sentinel — a subagent quoting the
// sentinel marker is not the session asserting completion.
func TestScanTranscriptTailForDone_SidechainAssistantIgnored(t *testing.T) {
	path := writeScanTranscript(t,
		scanAssistantLine(t, "main turn done\n===AGENTDECK_DONE=== status=ok summary=real"),
		`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"===AGENTDECK_DONE=== status=fail summary=sidechain must be ignored"}]}}`,
		`{"type":"system","subtype":"hook_result"}`,
	)
	sig, found, pending := ScanTranscriptTailForDone(path)
	if !found || pending {
		t.Fatalf("expected main-chain sentinel behind sidechain noise, got found=%v pending=%v", found, pending)
	}
	if sig.Status != "ok" || sig.Summary != "real" {
		t.Fatalf("sidechain record leaked into detection: %+v", sig)
	}
}

// Regression for the flush race (one-turn lag): when the newest main-chain
// record is a user record, the just-stopped turn's assistant reply has not
// been appended yet. Scanning past it would return the PREVIOUS turn's
// sentinel — observed live as a deterministic one-turn delivery lag, which
// means a worker's final (sentinel) turn never delivers. The scan must stop
// at the user record and report pending.
func TestScanTranscriptTailForDone_UnflushedTailPending(t *testing.T) {
	path := writeScanTranscript(t,
		scanAssistantLine(t, "previous turn\n===AGENTDECK_DONE=== status=fail summary=stale previous sentinel"),
		scanUserLine,
	)
	sig, found, pending := ScanTranscriptTailForDone(path)
	if found {
		t.Fatalf("previous turn's sentinel leaked through the unflushed tail: %+v", sig)
	}
	if !pending {
		t.Fatalf("unflushed tail must report pending so the daemon retries")
	}
}

// Sidechain noise appended after the user prompt (subagents streaming while
// the main assistant reply is still unflushed) must not mask the pending
// state — and must not be mined for a sentinel either.
func TestScanTranscriptTailForDone_SidechainAfterUserStillPending(t *testing.T) {
	path := writeScanTranscript(t,
		scanAssistantLine(t, "previous turn"),
		scanUserLine,
		`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"===AGENTDECK_DONE=== status=ok summary=sidechain spoof"}]}}`,
	)
	sig, found, pending := ScanTranscriptTailForDone(path)
	if found {
		t.Fatalf("sidechain sentinel leaked: %+v", sig)
	}
	if !pending {
		t.Fatalf("expected pending while the main-chain reply is unflushed")
	}
}

// A window with no main-chain turn record at all is treated as pending: the
// flushed record, once appended, lands at the very end of the file and the
// next scan sees it immediately.
func TestScanTranscriptTailForDone_NoMainChainInWindow(t *testing.T) {
	path := writeScanTranscript(t,
		`{"type":"system","subtype":"hook_result"}`,
		`{"type":"system","subtype":"turn_duration"}`,
	)
	if _, found, pending := ScanTranscriptTailForDone(path); found || !pending {
		t.Fatalf("want found=false pending=true for a window without main-chain records, got found=%v pending=%v", found, pending)
	}
}

// The pending sentinel turn flushing to the file resolves the scan — the
// daemon-side retry sequence in miniature.
func TestScanTranscriptTailForDone_FlushResolvesPending(t *testing.T) {
	path := writeScanTranscript(t,
		scanAssistantLine(t, "previous turn"),
		scanUserLine,
	)
	if _, found, pending := ScanTranscriptTailForDone(path); found || !pending {
		t.Fatalf("precondition: want pending before flush, got found=%v pending=%v", found, pending)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open transcript for append: %v", err)
	}
	if _, err := f.WriteString(scanAssistantLine(t, "done now\n===AGENTDECK_DONE=== status=ok summary=landed after flush") + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()

	sig, found, pending := ScanTranscriptTailForDone(path)
	if !found || pending {
		t.Fatalf("flushed sentinel not picked up: found=%v pending=%v", found, pending)
	}
	if sig.Status != "ok" || sig.Summary != "landed after flush" {
		t.Fatalf("wrong signal after flush: %+v", sig)
	}
}

func TestValidateTranscriptPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	good := filepath.Join(home, ".claude", "projects", "p", "t.jsonl")
	if cleaned, ok := ValidateTranscriptPath(good); !ok || cleaned != good {
		t.Errorf("expected %q accepted, got ok=%v cleaned=%q", good, ok, cleaned)
	}
	if _, ok := ValidateTranscriptPath(""); ok {
		t.Errorf("empty path must be rejected")
	}
	if _, ok := ValidateTranscriptPath("../../etc/passwd"); ok {
		t.Errorf("traversal path must be rejected")
	}
	if _, ok := ValidateTranscriptPath(filepath.Join(home, "elsewhere", "t.jsonl")); ok {
		t.Errorf("path outside ~/.claude must be rejected")
	}
}

func TestTranscriptTailLines_BoundsAndOrder(t *testing.T) {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, scanAssistantLine(t, strings.Repeat("x", 10)))
	}
	path := writeScanTranscript(t, lines...)
	got, err := TranscriptTailLines(path, 25)
	if err != nil {
		t.Fatalf("TranscriptTailLines: %v", err)
	}
	if len(got) != 25 {
		t.Fatalf("want 25 trailing lines, got %d", len(got))
	}
	if got[len(got)-1] != lines[len(lines)-1] {
		t.Errorf("last returned line is not the file's last line")
	}
}
