package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #2582 follow-up: the binding in tool_data comes only from hook status
// samples, and a Hermes turn replaces its parent edges in the status file
// within milliseconds. The hook handler therefore records the id of every
// parent edge in a sidecar, and restart resumes that id first.

func TestHermesHookRecordsOwnSession(t *testing.T) {
	cases := []struct {
		event, platform, sid string
		want                 bool
	}{
		{"pre_llm_call", "cli", hermesSIDA, true},
		{"post_llm_call", "", hermesSIDA, true},
		{"on_session_start", "cli", hermesSIDA, true},
		{"pre_llm_call", "subagent", hermesSIDDelegate, false},
		{"post_llm_call", "Subagent", hermesSIDDelegate, false},
		{"pre_tool_call", "", hermesSIDDelegate, false},
		{"post_api_request", "cli", hermesSIDA, false},
		{"on_session_end", "cli", hermesSIDA, false},
		{"on_session_finalize", "cli", hermesSIDA, false},
		{"pre_llm_call", "cli", "not-a-hermes-id", false},
		{"pre_llm_call", "cli", "", false},
	}
	for _, c := range cases {
		if got := HermesHookRecordsOwnSession(c.event, c.platform, c.sid); got != c.want {
			t.Errorf("HermesHookRecordsOwnSession(%q, %q, %q) = %v, want %v", c.event, c.platform, c.sid, got, c.want)
		}
	}
}

func TestHermesRestartResumesHookHandlerRecordOverSampledBinding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := withTempGlobalStateDB(t)
	workspace := t.TempDir()
	withFakeHermes(t, `printf 'Title Workspace LastActive ID\n--- --- --- ---\nB ws 1m `+hermesSIDB+`\n'`)
	a := NewInstanceWithTool("hermes-a", workspace, "hermes")
	b := NewInstanceWithTool("hermes-b", workspace, "hermes")
	saveHermesRow(t, db, a)
	saveHermesRow(t, db, b)

	// No watcher sample bound A, but its hook handler recorded A's turn.
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDA, "", true, nil); err != nil {
		t.Fatalf("WriteHermesOwnedSessionID: %v", err)
	}
	a.prepareHermesResume()
	if cmd := a.buildHermesCommand("hermes"); !strings.Contains(cmd, "--resume "+hermesSIDA) {
		t.Fatalf("restart command %q, want --resume %s (the hook handler's record)", cmd, hermesSIDA)
	}
	if w := a.ConsumeCodexRestartWarning(); w != "" {
		t.Fatalf("restart that resumed the recorded conversation warned: %q", w)
	}

	// The record is newer than a sampled binding: after a rotation the
	// sidecar holds the new id while the sample may still show the old one.
	a.HermesSessionID = hermesSIDB
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDNew, "", true, nil); err != nil {
		t.Fatalf("WriteHermesOwnedSessionID: %v", err)
	}
	a.prepareHermesResume()
	if a.HermesSessionID != hermesSIDNew {
		t.Fatalf("resume id = %q, want the recorded %q", a.HermesSessionID, hermesSIDNew)
	}
}

func TestHermesFreshRestartForgetsOwnedSessionRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withTempGlobalStateDB(t)
	a := NewInstanceWithTool("hermes-a", t.TempDir(), "hermes")
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDA, "", true, nil); err != nil {
		t.Fatalf("WriteHermesOwnedSessionID: %v", err)
	}
	if got := a.hermesOwnedSessionID(); got != hermesSIDA {
		t.Fatalf("recorded id = %q, want %q", got, hermesSIDA)
	}
	a.clearSessionBindingForFreshStart()
	if got := a.hermesOwnedSessionID(); got != "" {
		t.Fatalf("fresh restart kept the recorded id %q", got)
	}
}

func TestHermesOwnedSessionRecordRejectsBadInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := WriteHermesOwnedSessionID("../escape", hermesSIDA, "", true, nil); err == nil {
		t.Fatal("path-like instance id accepted")
	}
	if err := WriteHermesOwnedSessionID("inst-1", "garbage id", "", true, nil); err == nil {
		t.Fatal("non-Hermes session id accepted")
	}
	if _, err := os.Stat(filepath.Join(GetHooksDir(), "inst-1"+hermesOwnedSessionSuffix)); !os.IsNotExist(err) {
		t.Fatalf("rejected write left a sidecar (stat err %v)", err)
	}
}

func TestHookArtifactIDKnowsHermesOwnedSession(t *testing.T) {
	if got := hookArtifactID("inst-1" + hermesOwnedSessionSuffix); got != "inst-1" {
		t.Fatalf("hookArtifactID = %q, want inst-1 so the sidecar is pruned with the entry", got)
	}
}
