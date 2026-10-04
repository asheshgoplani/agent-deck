package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Issue #2481: ResponseContentVersion is the cheap change signal behind
// `session output --if-version`. It must move whenever the parsed source
// moves, be stable while it does not, and be empty when the response does not
// come from one versioned file.
func TestIssue2481_ResponseContentVersionCodexRollout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "sessions", "2026", "10", "04", "rollout-test-thread-v.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	turn := func(id, msg string) string {
		return `{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + id + `"}}` + "\n" +
			`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"` + id + `","last_agent_message":"` + msg + `"}}` + "\n"
	}
	if err := os.WriteFile(path, []byte(turn("t1", "ONE")), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{Tool: "codex", CodexSessionID: "thread-v"}

	v1 := inst.ResponseContentVersion(nil)
	if v1 == "" {
		t.Fatal("codex rollout has no content version")
	}
	if again := inst.ResponseContentVersion(nil); again != v1 {
		t.Fatalf("version not stable on an unchanged rollout: %q then %q", v1, again)
	}
	resp, versioned, err := inst.GetLastResponseAtVersion(nil, v1)
	if err != nil || !versioned || resp.Content != "ONE" {
		t.Fatalf("GetLastResponseAtVersion = %+v, versioned=%v, err=%v; want ONE from the versioned rollout", resp, versioned, err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(turn("t2", "TWO")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if v2 := inst.ResponseContentVersion(nil); v2 == "" || v2 == v1 {
		t.Fatalf("version after append = %q, want a new version (was %q)", v2, v1)
	}
}

func TestIssue2481_ResponseContentVersionEmptyWithoutVersionedFile(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	for _, inst := range []*Instance{
		{Tool: "shell"},
		{Tool: "codex", CodexSessionID: "thread-missing"},
		{Tool: "claude", ProjectPath: t.TempDir()},
	} {
		if v := inst.ResponseContentVersion(nil); v != "" {
			t.Fatalf("%s instance without a versioned file has version %q", inst.Tool, v)
		}
		if _, versioned, _ := inst.GetLastResponseAtVersion(nil, ""); versioned {
			t.Fatalf("%s instance reported a versioned response without a version", inst.Tool)
		}
	}
}
