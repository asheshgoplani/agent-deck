package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// errRestartFreshStop aborts RestartFresh at the seam, before it kills or
// starts anything.
type errRestartFreshStop struct{}

// Follow-up 1: RestartFresh rotates the Hermes generation before it clears
// the recorded id, so a late hook from the old process (carrying the old
// generation) is rejected instead of writing the old id back.
func TestHermesRestartFreshRotatesGenerationBeforeClearing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withTempGlobalStateDB(t)
	a := NewInstanceWithTool("hermes-a", t.TempDir(), "hermes")
	old, err := a.seedHermesHookGeneration("waiting", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDA, "", true); err != nil {
		t.Fatal(err)
	}
	var atClear string
	prev := restartFreshBindingCleared
	restartFreshBindingCleared = func(i *Instance) {
		atClear, _ = hookGenerationForInstance(i.ID)
		panic(errRestartFreshStop{})
	}
	t.Cleanup(func() { restartFreshBindingCleared = prev })
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(errRestartFreshStop); !ok {
					panic(r)
				}
			}
		}()
		_ = a.RestartFresh()
	}()
	if atClear == "" || atClear == old {
		t.Fatalf("generation when the binding was cleared = %q, old = %q: the old process could still write", atClear, old)
	}
	if got := a.hermesOwnedSessionID(); got != "" {
		t.Fatalf("recorded id after the fresh clear = %q", got)
	}
}

// Follow-up 2: Start resumes the recorded conversation, as Restart does;
// the start right after RestartFresh does not.
func TestHermesStartPrefersTheRecordedSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := NewInstanceWithTool("hermes-a", t.TempDir(), "hermes")
	a.HermesSessionID = hermesSIDB // a stale sampled binding
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDA, "", true); err != nil {
		t.Fatal(err)
	}
	a.preferHermesOwnedSessionForStart()
	if cmd := a.buildHermesCommand("hermes"); !strings.Contains(cmd, "--resume "+hermesSIDA) {
		t.Fatalf("start command %q, want --resume %s (the recorded conversation)", cmd, hermesSIDA)
	}

	a.hermesStartFresh = true
	a.preferHermesOwnedSessionForStart()
	if cmd := a.buildHermesCommand("hermes"); strings.Contains(cmd, "--resume") {
		t.Fatalf("start after RestartFresh resumed: %q", cmd)
	}
	if a.hermesStartFresh {
		t.Fatal("the fresh flag must apply to one start only")
	}
}

// Follow-up 3: the subagent filter needs extra.platform. An event without it
// may record the first id but never replaces one, and a record whose cwd is
// outside the entry's paths (a nested hermes that inherited the instance id)
// is not resumed.
func TestHermesOwnedSessionHardening(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	a := NewInstanceWithTool("hermes-a", workspace, "hermes")

	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDA, workspace, false); err != nil {
		t.Fatal(err)
	}
	if got := a.hermesOwnedSessionID(); got != hermesSIDA {
		t.Fatalf("first record without platform = %q, want it kept", got)
	}
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDDelegate, workspace, false); err != nil {
		t.Fatal(err)
	}
	if got := a.hermesOwnedSessionID(); got != hermesSIDA {
		t.Fatalf("an event without platform replaced the record: %q", got)
	}
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDNew, workspace, true); err != nil {
		t.Fatal(err)
	}
	if got := a.hermesOwnedSessionID(); got != hermesSIDNew {
		t.Fatalf("a parent edge with platform did not rotate: %q", got)
	}

	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDB, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	if got := a.hermesOwnedSessionID(); got != "" {
		t.Fatalf("a record from outside the entry's paths was resumed: %q", got)
	}

	// The sampled binding applies the same cwd rule.
	a.bindHermesSessionFromHook(hermesSIDB, "hook_payload", "pre_llm_call", "cli", t.TempDir(), time.Now())
	if a.HermesSessionID == hermesSIDB {
		t.Fatal("binding accepted a candidate from outside the entry's paths")
	}
	a.bindHermesSessionFromHook(hermesSIDA, "hook_payload", "pre_llm_call", "cli", workspace, time.Now())
	if a.HermesSessionID != hermesSIDA {
		t.Fatalf("binding refused the entry's own cwd: %q", a.HermesSessionID)
	}
}

// Sidecars written before the cwd was kept are a bare JSON string.
func TestHermesOwnedSessionReadsLegacySidecar(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := NewInstanceWithTool("hermes-a", t.TempDir(), "hermes")
	path := filepath.Join(GetHooksDir(), a.ID+hermesOwnedSessionSuffix)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`"`+hermesSIDA+`"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := a.hermesOwnedSessionID(); got != hermesSIDA {
		t.Fatalf("legacy sidecar read as %q", got)
	}
}

// Follow-up 4: pruning an abandoned scope also removes its own-session
// record, so the sandbox directory goes and the record does not outlive it.
func TestPruneHermesAbandonedScopeRemovesOwnedSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "hermes-prune"
	dir := filepath.Join(GetHooksDir(), "sandbox", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{id + ".lock", id + hermesOwnedSessionSuffix} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`"`+hermesSIDA+`"`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pruneHermesAbandonedScope(id, dir)
	if _, err := os.Stat(filepath.Join(dir, id+hermesOwnedSessionSuffix)); !os.IsNotExist(err) {
		t.Fatalf("own-session record left in the abandoned scope (stat err %v)", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("abandoned sandbox scope not removed (stat err %v)", err)
	}
}
