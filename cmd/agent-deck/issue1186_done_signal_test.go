package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #1186: a worker asserts task completion by printing a completion
// sentinel. On the Stop hook edge, agent-deck scans the transcript tail for
// that sentinel and persists the parsed outcome into the hook status file so
// the daemon can emit a distinct "finished" event to the parent. These tests
// cover the cmd-side detection + persistence; the scan itself lives in
// internal/session (transcript_done_scan_test.go), shared with the daemon's
// flush-race rescan, and the daemon-side emit lives in internal/session too.

// writeClaudeTranscript writes JSONL lines to a transcript file under the
// test HOME's ~/.claude (so detectDoneSentinel's containment guard passes)
// and returns its path. Callers must have pointed HOME at a temp dir.
func writeClaudeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("user home: %v", err)
	}
	dir := filepath.Join(home, ".claude", "projects", "p")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	path := filepath.Join(dir, "transcript.jsonl")
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// assistantLine builds a transcript assistant message whose text content holds
// the supplied body.
func assistantLine(t *testing.T, body string) string {
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

// stopPayload builds a Stop hook payload pointing at the given transcript.
func stopPayload(t *testing.T, transcriptPath string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"hook_event_name": "Stop",
		"transcript_path": transcriptPath,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func TestDetectDoneSentinel_FlushedSentinel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeClaudeTranscript(t,
		`{"type":"user","message":{"role":"user","content":"do the thing"}}`,
		assistantLine(t, "all done\n===AGENTDECK_DONE=== status=ok summary=fix landed"),
		`{"type":"system","subtype":"hook_result"}`,
	)
	res := detectDoneSentinel(stopPayload(t, path))
	if res.signal == nil {
		t.Fatalf("expected parsed sentinel, got %+v", res)
	}
	if res.signal.Status != "ok" || res.signal.Summary != "fix landed" {
		t.Errorf("got status=%q summary=%q", res.signal.Status, res.signal.Summary)
	}
	if res.pendingTranscript != "" {
		t.Errorf("flushed tail must not report pending, got %q", res.pendingTranscript)
	}
}

// Regression for the flush race (one-turn lag): Claude Code can fire the Stop
// hook BEFORE appending the turn's final assistant record, leaving a user
// record as the newest main-chain line. Scanning past it would deliver the
// PREVIOUS turn's sentinel. The hook must instead report the transcript as
// pending — and not sleep, because the Stop hook runs synchronously (#1225).
func TestDetectDoneSentinel_UnflushedTailReportsPending(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeClaudeTranscript(t,
		assistantLine(t, "previous turn\n===AGENTDECK_DONE=== status=fail summary=stale previous sentinel"),
		`{"type":"user","message":{"role":"user","content":"next prompt"}}`,
	)
	res := detectDoneSentinel(stopPayload(t, path))
	if res.signal != nil {
		t.Fatalf("previous turn's sentinel leaked through the unflushed tail: %+v", *res.signal)
	}
	if res.pendingTranscript != path {
		t.Fatalf("expected pending transcript %q, got %q", path, res.pendingTranscript)
	}
}

func TestDetectDoneSentinel_RejectsPathOutsideClaudeDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Sentinel-bearing transcript OUTSIDE ~/.claude: containment guard must
	// reject it without reading.
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(path, []byte(assistantLine(t, "===AGENTDECK_DONE=== status=ok summary=spoof")+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	res := detectDoneSentinel(stopPayload(t, path))
	if res.signal != nil || res.pendingTranscript != "" {
		t.Fatalf("path outside ~/.claude must yield a zero result, got %+v", res)
	}
}

func TestWriteHookStatus_PersistsDoneFields(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	instanceID := "inst-done"

	done := session.DoneSignal{Status: "ok", Summary: "done and dusted"}
	writeHookStatus(instanceID, "waiting", "sess-1", "Stop", done)

	data, err := os.ReadFile(filepath.Join(getHooksDir(), instanceID+".json"))
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal hook file: %v", err)
	}
	if parsed["done_status"] != "ok" {
		t.Errorf("done_status = %v, want ok", parsed["done_status"])
	}
	if parsed["done_summary"] != "done and dusted" {
		t.Errorf("done_summary = %v, want %q", parsed["done_summary"], "done and dusted")
	}
}

func TestWriteHookStatus_NoDoneFieldsWhenAbsent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	instanceID := "inst-nodone"

	writeHookStatus(instanceID, "waiting", "sess-2", "Stop")

	data, err := os.ReadFile(filepath.Join(getHooksDir(), instanceID+".json"))
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal hook file: %v", err)
	}
	if _, present := parsed["done_status"]; present {
		t.Errorf("done_status should be omitted for ordinary Stop, got %v", parsed["done_status"])
	}
	if _, present := parsed["transcript_path"]; present {
		t.Errorf("transcript_path should be omitted for ordinary Stop, got %v", parsed["transcript_path"])
	}
}

// An unflushed tail at Stop time persists the transcript path (and no done
// fields) so the daemon can finish the scan on its poll loop.
func TestWriteHookStatusWithScan_PersistsPendingTranscript(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	instanceID := "inst-pending"

	pendingPath := filepath.Join(tmpHome, ".claude", "projects", "p", "transcript.jsonl")
	writeHookStatusWithScan(instanceID, "waiting", "sess-3", "Stop", doneScanResult{pendingTranscript: pendingPath})

	data, err := os.ReadFile(filepath.Join(getHooksDir(), instanceID+".json"))
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal hook file: %v", err)
	}
	if parsed["transcript_path"] != pendingPath {
		t.Errorf("transcript_path = %v, want %q", parsed["transcript_path"], pendingPath)
	}
	if _, present := parsed["done_status"]; present {
		t.Errorf("done_status should be absent on a pending scan, got %v", parsed["done_status"])
	}
}
