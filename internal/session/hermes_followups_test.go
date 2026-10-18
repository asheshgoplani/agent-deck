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
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDA, "", true, nil); err != nil {
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
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDA, "", true, nil); err != nil {
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

	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDA, workspace, false, nil); err != nil {
		t.Fatal(err)
	}
	if got := a.hermesOwnedSessionID(); got != hermesSIDA {
		t.Fatalf("first record without platform = %q, want it kept", got)
	}
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDDelegate, workspace, false, nil); err != nil {
		t.Fatal(err)
	}
	if got := a.hermesOwnedSessionID(); got != hermesSIDA {
		t.Fatalf("an event without platform replaced the record: %q", got)
	}
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDNew, workspace, true, nil); err != nil {
		t.Fatal(err)
	}
	if got := a.hermesOwnedSessionID(); got != hermesSIDNew {
		t.Fatalf("a parent edge with platform did not rotate: %q", got)
	}

	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDB, t.TempDir(), true, nil); err != nil {
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

// Follow-up 4 / review S2: pruning an abandoned scope moves its own-session
// record to the active scope when that has none, and removes it otherwise;
// either way the sandbox directory goes.
func TestPruneHermesAbandonedScopeMovesOrRemovesOwnedSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "hermes-prune"
	dir := filepath.Join(GetHooksDir(), "sandbox", id)
	write := func(path, sid string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`"`+sid+`"`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	abandoned := filepath.Join(dir, id+hermesOwnedSessionSuffix)
	active := filepath.Join(GetHooksDir(), id+hermesOwnedSessionSuffix)
	write(filepath.Join(dir, id+".lock"), "x")
	write(abandoned, hermesSIDA)
	pruneHermesAbandonedScope(id, dir)
	if rec, ok := readHermesOwnedSession(active); !ok || rec.ID != hermesSIDA {
		t.Fatalf("record not moved to the active scope: %+v %v", rec, ok)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("abandoned sandbox scope not removed (stat err %v)", err)
	}

	// The active scope already has one: the abandoned record is removed.
	write(filepath.Join(dir, id+".lock"), "x")
	write(abandoned, hermesSIDB)
	pruneHermesAbandonedScope(id, dir)
	if rec, _ := readHermesOwnedSession(active); rec.ID != hermesSIDA {
		t.Fatalf("active record replaced by the abandoned one: %+v", rec)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatalf("abandoned record left (stat err %v)", err)
	}
}

func sandboxedHermes(t *testing.T) *Instance {
	t.Helper()
	a := NewInstanceWithTool("hermes-sbx", t.TempDir(), "hermes")
	a.Sandbox = &SandboxConfig{Enabled: true}
	return a
}

// Review B1: inside a sandbox Hermes reports the container's cwd
// (/workspace). That is the entry's project, so its record resumes and its
// binding candidates bind; a sandboxed cwd that maps to nothing skips the rule.
func TestHermesSandboxedWorkspaceCwdResumes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withTempGlobalStateDB(t)
	for _, cwd := range []string{"/workspace", "/workspace/sub/dir", "/tmp/elsewhere-in-container"} {
		t.Run(cwd, func(t *testing.T) {
			a := sandboxedHermes(t)
			path := filepath.Join(hermesHookScope(a.ID, true), a.ID+hermesOwnedSessionSuffix)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := atomicHermesJSON(path, hermesOwnedSession{ID: hermesSIDA, Cwd: cwd, Platform: true, At: time.Now().UnixNano()}); err != nil {
				t.Fatal(err)
			}
			if got := a.hermesOwnedSessionID(); got != hermesSIDA {
				t.Fatalf("sandboxed record with cwd %s resumed %q, want %s", cwd, got, hermesSIDA)
			}
			a.bindHermesSessionFromHook(hermesSIDA, "hook_payload", "pre_llm_call", "cli", cwd, time.Now())
			if a.HermesSessionID != hermesSIDA {
				t.Fatalf("sandboxed binding with cwd %s rejected", cwd)
			}
		})
	}
}

// The /workspace mapping follows docker.WithMultiRepoPaths, and a record a
// sandboxed run wrote is still the entry's after the sandbox is turned off.
func TestHermesSandboxCwdMapping(t *testing.T) {
	root := t.TempDir()
	repoA, repoB := filepath.Join(root, "one", "api"), filepath.Join(root, "two", "api")
	a := NewInstanceWithTool("hermes-mr", repoA, "hermes")
	a.MultiRepoEnabled = true
	a.AdditionalPaths = []string{repoB}
	a.Sandbox = &SandboxConfig{Enabled: true}
	for cwd, want := range map[string]string{
		"/workspace/api/x":   filepath.Join(repoA, "x"),
		"/workspace/api-1/y": filepath.Join(repoB, "y"),
	} {
		if got, ok := a.sandboxCwdToHost(cwd); !ok || got != want {
			t.Errorf("sandboxCwdToHost(%s) = %q %v, want %q", cwd, got, ok, want)
		}
	}
	if _, ok := a.sandboxCwdToHost("/workspace/nope"); ok {
		t.Error("an unmounted /workspace name mapped")
	}

	plain := NewInstanceWithTool("hermes-plain", t.TempDir(), "hermes")
	if plain.hermesHookCwdIsForeign("/workspace/src") {
		t.Error("a record from a sandboxed run is foreign after the sandbox was turned off")
	}
	if !plain.hermesHookCwdIsForeign(t.TempDir()) {
		t.Error("a host cwd outside the entry is not foreign")
	}
}

// Review S2: after a sandbox toggle the record written in the old scope is
// still resumed (the generation seed prunes the old scope before
// prepareHermesResume reads).
func TestHermesSandboxToggleKeepsResume(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withTempGlobalStateDB(t)
	a := NewInstanceWithTool("hermes-toggle", t.TempDir(), "hermes")
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDA, a.ProjectPath, true, nil); err != nil {
		t.Fatal(err)
	}
	a.Sandbox = &SandboxConfig{Enabled: true}
	if _, err := a.seedHermesHookGeneration("starting", false); err != nil {
		t.Fatal(err)
	}
	a.prepareHermesResume()
	if a.HermesSessionID != hermesSIDA {
		t.Fatalf("after the toggle restart resumes %q, want %s", a.HermesSessionID, hermesSIDA)
	}
}

// Review S3: a record written from events without extra.platform cannot
// follow /new or a compression; a newer binding wins over it.
func TestHermesPlatformlessRecordLosesToANewerBinding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withTempGlobalStateDB(t)
	workspace := t.TempDir()
	a := NewInstanceWithTool("hermes-np", workspace, "hermes")
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDA, workspace, false, nil); err != nil {
		t.Fatal(err)
	}
	// /new: the parent's next turn edge (sampled) carries a new id.
	a.bindHermesSessionFromHook(hermesSIDNew, "hook_payload", "pre_llm_call", "", workspace, time.Now().Add(time.Second))
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDNew, workspace, false, nil); err != nil {
		t.Fatal(err)
	}
	a.prepareHermesResume()
	if a.HermesSessionID != hermesSIDNew {
		t.Fatalf("restart resumes %q, want the conversation after /new %s", a.HermesSessionID, hermesSIDNew)
	}

	// A record that carried a platform keeps precedence over the binding.
	b := NewInstanceWithTool("hermes-p", workspace, "hermes")
	if err := WriteHermesOwnedSessionID(b.ID, hermesSIDA, workspace, true, nil); err != nil {
		t.Fatal(err)
	}
	b.HermesSessionID, b.hermesBoundAt = hermesSIDB, time.Now().Add(time.Hour)
	b.prepareHermesResume()
	if b.HermesSessionID != hermesSIDA {
		t.Fatalf("platform record lost to the binding: %q", b.HermesSessionID)
	}
}

// The binding's time survives a reload, so S3 also holds after a reboot.
func TestReadHermesDetectedAtFromToolData(t *testing.T) {
	if got := ReadHermesDetectedAtFromToolData([]byte(`{"hermes_detected_at":1790000000}`)); !got.Equal(time.Unix(1790000000, 0)) {
		t.Fatalf("detected at = %v", got)
	}
	if !ReadHermesDetectedAtFromToolData(nil).IsZero() {
		t.Fatal("empty tool_data gave a time")
	}
}

// Review S4: a nested hermes outside the entry's directories cannot
// overwrite the entry's record (the hook checks the seed's cwd roots).
func TestWriteHermesOwnedSessionIDChecksTheCwdRoots(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	a := NewInstanceWithTool("hermes-s4", workspace, "hermes")
	roots := a.hermesCwdRoots()
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDA, filepath.Join(workspace, "pkg"), true, roots); err != nil {
		t.Fatal(err)
	}
	if err := WriteHermesOwnedSessionID(a.ID, hermesSIDB, t.TempDir(), true, roots); err != nil {
		t.Fatal(err)
	}
	if got := a.hermesOwnedSessionID(); got != hermesSIDA {
		t.Fatalf("record = %q, want the parent's %s", got, hermesSIDA)
	}
	if got := sandboxedHermes(t).hermesCwdRoots(); len(got) != 1 || got[0] != "/workspace" {
		t.Fatalf("sandboxed roots = %v, want [/workspace]", got)
	}
	if err := WriteHermesOwnedSessionID("hermes-s4b", hermesSIDA, "/workspace/x", true, []string{"/workspace"}); err != nil {
		t.Fatal(err)
	}
	if rec, ok := readHermesOwnedSession(filepath.Join(GetHooksDir(), "hermes-s4b"+hermesOwnedSessionSuffix)); !ok || rec.ID != hermesSIDA {
		t.Fatal("a sandboxed hook under /workspace was not recorded")
	}
}
