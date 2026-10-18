package session

import (
	"testing"
	"time"
)

// A Hermes without extra.platform: after /new the binding wins over the
// record at the first restart. That restart zeroes the binding's time, so
// the record must take the new id, or a second restart before a hook of the
// new process is seen resumes the abandoned conversation.
func TestHermesPlatformlessDoubleRestartKeepsTheNewConversation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withTempGlobalStateDB(t)
	workspace := t.TempDir()
	a := NewInstanceWithTool("hermes-dbl", workspace, "hermes")
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDA, workspace, false, nil); err != nil {
		t.Fatal(err)
	}
	a.bindHermesSessionFromHook(hermesSIDNew, "hook_payload", "pre_llm_call", "", workspace, time.Now().Add(time.Second))
	for restart := 1; restart <= 2; restart++ {
		if _, err := a.seedHermesHookGeneration("starting", false); err != nil {
			t.Fatal(err)
		}
		a.prepareHermesResume()
		if a.HermesSessionID != hermesSIDNew {
			t.Fatalf("restart %d resumes %q, want the conversation after /new %s", restart, a.HermesSessionID, hermesSIDNew)
		}
	}
	if got := a.hermesOwnedSessionID(); got != hermesSIDNew {
		t.Fatalf("record = %q, want %s", got, hermesSIDNew)
	}
}

// The binding's time is loaded from tool_data (hermes_detected_at), so the
// record-versus-binding rule also holds after agent-deck restarts.
func TestStorageLoadsTheHermesBindingTime(t *testing.T) {
	s := newTestStorage(t)
	inst := NewInstanceWithTool("hermes-load", t.TempDir(), "hermes")
	if err := s.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1790000000, 0)
	if err := s.db.WriteHermesSessionBinding(inst.ID, hermesSIDA, at); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := s.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	var got *Instance
	for _, l := range loaded {
		if l.ID == inst.ID {
			got = l
		}
	}
	if got == nil {
		t.Fatal("instance not loaded")
	}
	if got.HermesSessionID != hermesSIDA || !got.hermesBoundAt.Equal(at) {
		t.Fatalf("loaded binding %q at %v, want %s at %v", got.HermesSessionID, got.hermesBoundAt, hermesSIDA, at)
	}
}

// A RestartFresh whose Start fails (here: an account that is not
// configured, which Start refuses before anything runs) must not leave the
// next start fresh.
func TestHermesRestartFreshFailedStartClearsTheFreshFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withTempGlobalStateDB(t)
	a := NewInstanceWithTool("hermes-ff", t.TempDir(), "hermes")
	a.Account = "no-such-account"
	if err := a.RestartFresh(); err == nil {
		t.Fatal("RestartFresh with an unknown account succeeded")
	}
	if a.hermesStartFresh {
		t.Fatal("a failed fresh start left the next start fresh")
	}
}
