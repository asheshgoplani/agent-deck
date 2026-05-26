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
// cover the cmd-side detection + persistence; the daemon-side emit lives in
// internal/session. The transcript source is injectable (a file path) so the
// tests don't need a live agent.

// writeTranscript writes JSONL lines to a temp file and returns its path.
func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
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

func TestScanTranscriptForDone_OK(t *testing.T) {
	path := writeTranscript(t,
		assistantLine(t, "doing work"),
		assistantLine(t, "all set.\n===AGENTDECK_DONE=== status=ok summary=feature shipped"),
	)
	sig, ok := scanTranscriptForDone(path)
	if !ok {
		t.Fatalf("expected sentinel detected in transcript")
	}
	if sig.Status != "ok" || sig.Summary != "feature shipped" {
		t.Errorf("got status=%q summary=%q", sig.Status, sig.Summary)
	}
}

func TestScanTranscriptForDone_Fail(t *testing.T) {
	path := writeTranscript(t,
		assistantLine(t, "===AGENTDECK_DONE=== status=fail summary=could not build"),
	)
	sig, ok := scanTranscriptForDone(path)
	if !ok {
		t.Fatalf("expected sentinel detected")
	}
	if sig.Status != "fail" || sig.Summary != "could not build" {
		t.Errorf("got status=%q summary=%q", sig.Status, sig.Summary)
	}
}

func TestScanTranscriptForDone_NoSentinel(t *testing.T) {
	path := writeTranscript(t,
		assistantLine(t, "just an ordinary mid-task turn, no sentinel here"),
	)
	if _, ok := scanTranscriptForDone(path); ok {
		t.Errorf("expected no sentinel for ordinary turn")
	}
}

func TestScanTranscriptForDone_MalformedIgnored(t *testing.T) {
	path := writeTranscript(t,
		assistantLine(t, "===AGENTDECK_DONE=== status=maybe summary=garbage"),
	)
	if _, ok := scanTranscriptForDone(path); ok {
		t.Errorf("expected malformed sentinel to be ignored")
	}
}

func TestScanTranscriptForDone_NonAssistantLastLine(t *testing.T) {
	// A user/tool line as the tail must not be mined for a sentinel.
	userLine := `{"type":"user","message":{"role":"user","content":"===AGENTDECK_DONE=== status=ok summary=spoofed"}}`
	path := writeTranscript(t, userLine)
	if _, ok := scanTranscriptForDone(path); ok {
		t.Errorf("expected non-assistant tail to yield no sentinel")
	}
}

func TestScanTranscriptForDone_MissingFile(t *testing.T) {
	if _, ok := scanTranscriptForDone(filepath.Join(t.TempDir(), "nope.jsonl")); ok {
		t.Errorf("expected missing transcript to yield no sentinel, not a crash")
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
}
