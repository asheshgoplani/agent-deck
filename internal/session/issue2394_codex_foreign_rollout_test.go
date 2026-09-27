package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Issue #2394 (reproduced live with codex-cli 0.147): a Codex child launched
// without a message has no rollout until its first turn. The bootstrap disk
// scan then adopted a sibling's rollout from the same project, wrote it into
// the pane's CODEX_SESSION_ID, and `session send` persisted it: the send could
// never find its accepted generation and `session output` returned the
// sibling's conversation.

func stubCodexPaneProcessPIDs(t *testing.T, pids []int, err error) {
	t.Helper()
	restore := codexPaneProcessPIDs
	t.Cleanup(func() { codexPaneProcessPIDs = restore })
	codexPaneProcessPIDs = func(*Instance) ([]int, error) { return pids, err }
}

// seedProjectRollout writes a user rollout whose session_meta cwd is project.
func seedProjectRollout(t *testing.T, codexHome, sid, project string) {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", "2026", "09", "27")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"timestamp":"2026-09-27T00:34:19Z","type":"session_meta","payload":{"session_id":"` + sid + `","id":"` + sid + `","cwd":"` + project + `","originator":"codex-tui","source":"cli","thread_source":"user"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-09-27T00-34-19-"+sid+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newUnboundCodexForBootstrap returns an unbound Codex instance started a
// minute ago, whose process probe is on its throttle so the bootstrap disk
// scan is the next source to run.
func newUnboundCodexForBootstrap(t *testing.T) (*Instance, string) {
	t.Helper()
	inst, codexHome := newCodexGateInstance(t)
	inst.CodexStartedAt = time.Now().Add(-time.Minute).UnixMilli()
	inst.lastCodexProbeAt = time.Now()
	if err := os.MkdirAll(filepath.Join(codexHome, "thread-writer-locks"), 0o700); err != nil {
		t.Fatal(err)
	}
	return inst, codexHome
}

func TestIssue2394_BootstrapScanDoesNotAdoptForeignRolloutWhileLiveProcessOwnsNone(t *testing.T) {
	inst, codexHome := newUnboundCodexForBootstrap(t)
	foreign := uniqueSID(t)
	seedProjectRollout(t, codexHome, foreign, inst.ProjectPath)

	// Codex is running in the pane but has not created its thread yet (update
	// prompt, or composer not up): it holds neither a rollout nor a lock.
	stubCodexPaneProcessPIDs(t, []int{4242}, nil)
	stubCodexPaneOpenPaths(t, []string{"/dev/null"}, nil)

	inst.UpdateCodexSession(map[string]bool{})
	if inst.CodexSessionID != "" {
		t.Fatalf("bound a sibling's rollout %q while the live process owns no thread", inst.CodexSessionID)
	}
}

// A probe that finds the pane's Codex process but also errors on another pane
// PID (a short-lived child exiting mid-walk) is incomplete, not proof that no
// Codex runs: the disk scan must not rebind a sibling's rollout.
func TestIssue2394_BootstrapScanDoesNotAdoptForeignRolloutOnProbeError(t *testing.T) {
	inst, codexHome := newUnboundCodexForBootstrap(t)
	foreign := uniqueSID(t)
	seedProjectRollout(t, codexHome, foreign, inst.ProjectPath)

	stubCodexPaneProcessPIDs(t, []int{4242}, errors.New("codex process probe: inspect pid 4243: exit status 1"))
	stubCodexPaneOpenPaths(t, []string{"/dev/null"}, nil)

	inst.UpdateCodexSession(map[string]bool{})
	if inst.CodexSessionID != "" {
		t.Fatalf("bound a sibling's rollout %q after an incomplete process probe", inst.CodexSessionID)
	}
}

func TestIssue2394_BootstrapBindsTheThreadTheLiveProcessOwns(t *testing.T) {
	inst, codexHome := newUnboundCodexForBootstrap(t)
	foreign, own := uniqueSID(t), uniqueSID(t)
	seedProjectRollout(t, codexHome, foreign, inst.ProjectPath)

	// Fresh composer: the thread exists only as the writer lock it holds.
	stubCodexPaneProcessPIDs(t, []int{4242}, nil)
	stubCodexPaneOpenPaths(t, []string{filepath.Join(codexHome, "thread-writer-locks", own+".lock")}, nil)

	inst.UpdateCodexSession(map[string]bool{})
	if inst.CodexSessionID != own {
		t.Fatalf("CodexSessionID = %q, want the live process's own thread %q (foreign %q)", inst.CodexSessionID, own, foreign)
	}
}

// Without a live Codex process (restart of a dead pane) there is no live
// evidence, and the disk scan stays the bootstrap source it always was.
func TestIssue2394_BootstrapScanStillRunsWithoutLiveProcess(t *testing.T) {
	inst, codexHome := newUnboundCodexForBootstrap(t)
	prior := uniqueSID(t)
	seedProjectRollout(t, codexHome, prior, inst.ProjectPath)

	stubCodexPaneProcessPIDs(t, nil, nil)
	stubCodexPaneOpenPaths(t, nil, nil)

	inst.UpdateCodexSession(map[string]bool{})
	if inst.CodexSessionID != prior {
		t.Fatalf("CodexSessionID = %q, want the scanned rollout %q", inst.CodexSessionID, prior)
	}
}
