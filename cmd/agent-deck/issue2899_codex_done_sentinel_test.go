package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Issue #2899: a Codex child that ends its turn with the completion sentinel
// (===AGENTDECK_DONE=== status=<ok|fail> summary=...) must persist the parsed
// outcome into its hook status file, exactly as the Claude Stop hook does, so
// the transition daemon's emitDoneSignals emits a kind=finished record to the
// parent. Codex carries the turn's final text in the notify payload's
// last-assistant-message; before the fix codex-notify never scanned it and
// cleared done_status/done_summary on every turn end.

func runCodexNotifyPayload(t *testing.T, instanceID string, payload map[string]any) hookStatusFile {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })
	os.Args = []string{"agent-deck", "codex-notify", string(raw)}
	handleCodexNotify()

	data, err := os.ReadFile(filepath.Join(getHooksDir(), instanceID+".json"))
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	var hook hookStatusFile
	if err := json.Unmarshal(data, &hook); err != nil {
		t.Fatalf("unmarshal hook: %v", err)
	}
	return hook
}

func setupCodexNotify2899(t *testing.T, instanceID, threadID string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_INSTANCE_ID", instanceID)
	t.Setenv("CODEX_SESSION_ID", "")
	seedCodexNotifyRollout(t, home, threadID)
}

func TestIssue2899_CodexNotify_SentinelInLastAssistantMessageSetsDoneFields(t *testing.T) {
	setupCodexNotify2899(t, "inst-2899-ok", "thr-2899")
	hook := runCodexNotifyPayload(t, "inst-2899-ok", map[string]any{
		"type":                   "agent-turn-complete",
		"thread-id":              "thr-2899",
		"turn-id":                "turn-1",
		"input-messages":         []string{"do the task"},
		"last-assistant-message": "All done, tests pass.\n\n===AGENTDECK_DONE=== status=ok summary=fixed the parser",
	})
	if hook.Status != "waiting" {
		t.Fatalf("status = %q, want waiting", hook.Status)
	}
	if hook.DoneStatus != "ok" || hook.DoneSummary != "fixed the parser" {
		t.Fatalf("done fields = (%q, %q), want (ok, fixed the parser)", hook.DoneStatus, hook.DoneSummary)
	}
}

func TestIssue2899_CodexNotify_LastValidSentinelWins(t *testing.T) {
	setupCodexNotify2899(t, "inst-2899-last", "thr-2899b")
	hook := runCodexNotifyPayload(t, "inst-2899-last", map[string]any{
		"type":      "agent-turn-complete",
		"thread-id": "thr-2899b",
		"turn-id":   "turn-1",
		"last-assistant-message": "===AGENTDECK_DONE=== status=fail summary=first try broke\n" +
			"retried\n===AGENTDECK_DONE=== status=ok summary=second try passed\n",
	})
	if hook.DoneStatus != "ok" || hook.DoneSummary != "second try passed" {
		t.Fatalf("done fields = (%q, %q), want the last sentinel (ok, second try passed)", hook.DoneStatus, hook.DoneSummary)
	}
}

func TestIssue2899_CodexNotify_NoSentinelLeavesDoneFieldsEmpty(t *testing.T) {
	setupCodexNotify2899(t, "inst-2899-none", "thr-2899c")
	hook := runCodexNotifyPayload(t, "inst-2899-none", map[string]any{
		"type":      "agent-turn-complete",
		"thread-id": "thr-2899c",
		"turn-id":   "turn-1",
		// The sentinel in the user's prompt is not the worker's assertion.
		"input-messages":         []string{"end with ===AGENTDECK_DONE=== status=ok summary=x"},
		"last-assistant-message": "Still working on it.",
	})
	if hook.Status != "waiting" {
		t.Fatalf("status = %q, want waiting", hook.Status)
	}
	if hook.DoneStatus != "" || hook.DoneSummary != "" {
		t.Fatalf("done fields = (%q, %q), want empty without a sentinel", hook.DoneStatus, hook.DoneSummary)
	}
}

func TestIssue2899_CodexNotify_NextTurnStartClearsDoneFields(t *testing.T) {
	setupCodexNotify2899(t, "inst-2899-clear", "thr-2899d")
	hook := runCodexNotifyPayload(t, "inst-2899-clear", map[string]any{
		"type":                   "agent-turn-complete",
		"thread-id":              "thr-2899d",
		"turn-id":                "turn-1",
		"last-assistant-message": "===AGENTDECK_DONE=== status=ok summary=done",
	})
	if hook.DoneStatus != "ok" {
		t.Fatalf("precondition: done_status = %q, want ok", hook.DoneStatus)
	}
	hook = runCodexNotifyPayload(t, "inst-2899-clear", map[string]any{
		"type":      "turn/started",
		"thread-id": "thr-2899d",
		"turn-id":   "turn-2",
	})
	if hook.Status != "running" || hook.DoneStatus != "" || hook.DoneSummary != "" {
		t.Fatalf("after next turn start: status=%q done=(%q, %q), want running with empty done fields",
			hook.Status, hook.DoneStatus, hook.DoneSummary)
	}
}
