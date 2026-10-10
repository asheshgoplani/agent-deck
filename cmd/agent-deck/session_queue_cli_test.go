package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// CLI contract of `session queue` (CORE-CHANGES 22). These tests drive the
// built binary only, so they also run against a core without the command.

type queueListJSON struct {
	SessionID string `json:"session_id"`
	Queue     []struct {
		ID          string `json:"id"`
		TextPreview string `json:"text_preview"`
		EnqueuedAt  string `json:"enqueued_at"`
		State       string `json:"state"`
	} `json:"queue"`
}

func TestSessionQueueListMatchesShowJSON(t *testing.T) {
	home := t.TempDir()
	id := addSessionJSON(t, home, "queue-list", "claude")
	sent, stderr, code := runAgentDeck(t, home, "session", "send", id, "hello   from\nqueue", "--queue", "--json")
	if code != 1 {
		t.Fatalf("queue to a stopped target: exit %d, stdout %s, stderr %s", code, sent, stderr)
	}
	var receipt struct {
		SendID string `json:"send_id"`
	}
	if err := json.Unmarshal([]byte(sent), &receipt); err != nil || receipt.SendID == "" {
		t.Fatalf("queued send receipt: %v %s", err, sent)
	}
	var listed queueListJSON
	got, stderr, code := runAgentDeck(t, home, "session", "queue", "list", id, "--json")
	if code != 0 || json.Unmarshal([]byte(got), &listed) != nil {
		t.Fatalf("queue list: exit %d, stdout %s, stderr %s", code, got, stderr)
	}
	if listed.SessionID != id || len(listed.Queue) != 1 || listed.Queue[0].ID != receipt.SendID ||
		listed.Queue[0].TextPreview != "hello from queue" || listed.Queue[0].EnqueuedAt == "" || listed.Queue[0].State != "failed" {
		t.Fatalf("queue list fields: %+v", listed)
	}
	var shown queueListJSON
	got, stderr, code = runAgentDeck(t, home, "session", "show", id, "--json")
	if code != 0 || json.Unmarshal([]byte(got), &shown) != nil {
		t.Fatalf("session show: exit %d, stdout %s, stderr %s", code, got, stderr)
	}
	if !reflect.DeepEqual(shown.Queue, listed.Queue) {
		t.Fatalf("show queue %+v does not match list %+v", shown.Queue, listed.Queue)
	}
	// Human output: one line per entry.
	got, _, code = runAgentDeck(t, home, "session", "queue", "list", id)
	if code != 0 || !strings.Contains(got, receipt.SendID) || !strings.Contains(got, "failed") {
		t.Fatalf("queue list text: exit %d, %s", code, got)
	}
}

func TestSessionQueueControlUnknownIDJSON(t *testing.T) {
	home := t.TempDir()
	for _, operation := range []string{"release", "cancel"} {
		stdout, stderr, code := runAgentDeck(t, home, "session", "queue", operation, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "--json")
		if code != 0 {
			t.Fatalf("%s unknown id: exit %d, stdout %s, stderr %s", operation, code, stdout, stderr)
		}
		var result struct {
			ID      string `json:"id"`
			Outcome string `json:"outcome"`
		}
		if err := json.Unmarshal([]byte(stdout), &result); err != nil || result.ID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" || result.Outcome != "not_found" {
			t.Fatalf("%s unknown id result: %v %s", operation, err, stdout)
		}
	}
	for _, operation := range []string{"release", "cancel", "list"} {
		stdout, stderr, code := runAgentDeck(t, home, "session", "queue", operation, "--help")
		if code != 0 || !strings.Contains(stdout+stderr, "--json") {
			t.Fatalf("%s help: exit %d, stdout %s, stderr %s", operation, code, stdout, stderr)
		}
	}
	stdout, _, code := runAgentDeck(t, home, "session", "queue", "--help")
	if code != 0 || !strings.Contains(stdout, "release <id>") || !strings.Contains(stdout, "cancel <id>") {
		t.Fatalf("queue help: exit %d, %s", code, stdout)
	}
	if _, _, code := runAgentDeck(t, home, "session", "queue", "remove", "x"); code != 2 {
		t.Fatalf("unknown queue verb: exit %d, want 2", code)
	}
}

// A queued send that failed at once (nothing typed) has nothing to cancel:
// the control answers not_found with the record, never cancelled or
// already_sent.
func TestSessionQueueControlFailedEntryIsNotPending(t *testing.T) {
	home := t.TempDir()
	id := addSessionJSON(t, home, "queue-failed", "claude")
	sent, _, _ := runAgentDeck(t, home, "session", "send", id, "never typed", "--queue", "--json")
	var receipt struct {
		SendID string `json:"send_id"`
	}
	if err := json.Unmarshal([]byte(sent), &receipt); err != nil || receipt.SendID == "" {
		t.Fatalf("queued send receipt: %v %s", err, sent)
	}
	for _, operation := range []string{"cancel", "release"} {
		stdout, stderr, code := runAgentDeck(t, home, "session", "queue", operation, receipt.SendID, "--json")
		var result struct {
			ID, Outcome, State, Reason string
		}
		if code != 0 || json.Unmarshal([]byte(stdout), &result) != nil {
			t.Fatalf("%s: exit %d, stdout %s, stderr %s", operation, code, stdout, stderr)
		}
		if result.ID != receipt.SendID || result.Outcome != "not_found" || result.State != "failed" || result.Reason != "target not running" {
			t.Fatalf("%s failed entry: %+v", operation, result)
		}
	}
}

// fakeQueueRemoteSSH puts an `ssh` shim first on PATH. mode "old" answers
// like a remote without `session queue`; mode "new" answers queue list with
// a JSON body. Every remote command line is appended to the returned log.
func fakeQueueRemoteSSH(t *testing.T, mode string) string {
	t.Helper()
	shim := t.TempDir()
	log := filepath.Join(shim, "calls.log")
	script := `#!/bin/sh
for cmd; do :; done
printf '%s\n' "$cmd" >> ` + log + `
case "$cmd" in
  *" version")
    printf 'Agent Deck v1.16.27\n'
    exit 0 ;;
  *"'session' 'queue'"*)
    if [ "` + mode + `" = old ]; then
      printf 'Usage: agent-deck session <command> [options]\n'
      printf 'Error: unknown session command: queue\n' >&2
      exit 1
    fi
    printf '{"session_id":"remote-id","queue":[]}\n'
    exit 0 ;;
esac
printf 'unexpected remote command: %s\n' "$cmd" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(shim, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func writeQueueRemoteConfig(t *testing.T, home string) {
	t.Helper()
	cfg := filepath.Join(home, ".config", "agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[remotes.lab]\nhost = 'queue-host'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteSessionQueueForwards(t *testing.T) {
	home := t.TempDir()
	writeQueueRemoteConfig(t, home)
	callLog := fakeQueueRemoteSSH(t, "new")
	stdout, stderr, code := runAgentDeck(t, home, "remote", "lab", "session", "queue", "list", "--json", "remote-id")
	if code != 0 || strings.TrimSpace(stdout) != `{"session_id":"remote-id","queue":[]}` {
		t.Fatalf("forwarded list: exit %d, stdout %s, stderr %s", code, stdout, stderr)
	}
	calls, _ := os.ReadFile(callLog)
	if !strings.Contains(string(calls), "'session' 'queue' 'list' '--json' 'remote-id'") {
		t.Fatalf("remote did not receive the queue list: %s", calls)
	}
	// Verbs outside list/release/cancel never reach SSH.
	_, stderr, code = runAgentDeck(t, home, "remote", "lab", "session", "queue", "remove", "x")
	if code != 2 || !strings.Contains(stderr, "unsupported remote command") {
		t.Fatalf("queue remove was forwarded: exit %d, stderr %s", code, stderr)
	}
}

// An older remote refuses before it resolves anything; the controller turns
// its usage text into the one refusal line a client feature-detects.
func TestRemoteSessionQueueOlderRemoteRefuses(t *testing.T) {
	home := t.TempDir()
	writeQueueRemoteConfig(t, home)
	fakeQueueRemoteSSH(t, "old")
	want := `session queue is unsupported on this remote "lab" (it runs v1.16.27); update its agent-deck with 'agent-deck remote update lab'`
	stdout, stderr, code := runAgentDeck(t, home, "remote", "lab", "session", "queue", "cancel", "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if code != 1 || stdout != "" || strings.TrimSpace(stderr) != "Error: "+want {
		t.Fatalf("older remote text: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	stdout, stderr, code = runAgentDeck(t, home, "remote", "lab", "session", "queue", "release", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "--json")
	var got struct {
		Error         string `json:"error"`
		Remote        string `json:"remote"`
		RemoteVersion string `json:"remote_version"`
	}
	if code != 1 || json.Unmarshal([]byte(stdout), &got) != nil || got.Error != want || got.Remote != "lab" || got.RemoteVersion != "1.16.27" {
		t.Fatalf("older remote JSON: exit %d, stdout %s, stderr %s", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, "Usage:") || strings.Contains(stderr, "unknown session command") {
		t.Fatalf("raw remote output leaked: %s %s", stdout, stderr)
	}
}
