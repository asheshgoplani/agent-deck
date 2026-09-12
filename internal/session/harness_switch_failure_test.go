package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These fixtures are intentionally filesystem-only. They inject the two
// failures that must never turn into a destructive account switch; the full
// lifecycle cases remain held for isolated CI/container verification.
// A safe prefix advance publishes two directory entries: the retained backup
// and the replacement. Both parent-directory syncs are required after their
// file-content syncs so a power loss cannot lose either name.
func TestHarnessSwitchFailure_PrefixBackupAndReplacementSyncParentDirectory(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage.jsonl")
	destination := filepath.Join(root, "target", "conversation.jsonl")
	oldBytes := []byte("conversation A\n")
	newBytes := append(append([]byte(nil), oldBytes...), []byte("conversation B\n")...)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stage, newBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, oldBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	var directorySyncs int
	restore := SetFsyncHookForTest(func(f *os.File) error {
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if info.IsDir() {
			directorySyncs++
		}
		return f.Sync()
	})
	defer restore()
	hash, err := sha256File(stage)
	if err != nil {
		t.Fatal(err)
	}
	if err := installStagedArtifact(stage, destination, hash); err != nil {
		t.Fatal(err)
	}
	if directorySyncs != 2 {
		t.Fatalf("directory syncs = %d, want backup and replacement parent syncs", directorySyncs)
	}
}

func TestHarnessSwitchFailure_ConflictingDestinationIsRefused(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage.jsonl")
	destination := filepath.Join(root, "target", "conversation.jsonl")
	if err := os.WriteFile(stage, []byte(`{"sessionId":"source","message":"new"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte(`{"sessionId":"other","message":"do not overwrite"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installStagedArtifact(stage, destination, "not-the-destination-hash"); err == nil {
		t.Fatal("conflicting destination must be refused")
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"sessionId":"other","message":"do not overwrite"}` {
		t.Fatalf("conflicting destination changed: %q", got)
	}
}

// A destination that is not an older byte prefix of the exact staged source
// could be another conversation or a newer branch. Both cases must be bounded
// refusals: neither source nor destination is replaced or deleted.
func TestHarnessSwitchFailure_DivergedOrNewerDestinationIsPreserved(t *testing.T) {
	for name, destinationBytes := range map[string][]byte{
		"diverged": []byte("conversation A\nindependent destination\n"),
		"newer":    []byte("conversation A\nsource turn\ndestination later turn\n"),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			stage := filepath.Join(root, "stage.jsonl")
			destination := filepath.Join(root, "target", "conversation.jsonl")
			sourceBytes := []byte("conversation A\nsource turn\n")
			if err := os.WriteFile(stage, sourceBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(destination, destinationBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			hash, err := sha256File(stage)
			if err != nil {
				t.Fatal(err)
			}
			err = installStagedArtifact(stage, destination, hash)
			if err == nil || !strings.Contains(err.Error(), "divergent or newer") {
				t.Fatalf("install error = %v, want bounded divergent/newer refusal", err)
			}
			gotDestination, err := os.ReadFile(destination)
			if err != nil || string(gotDestination) != string(destinationBytes) {
				t.Fatalf("destination changed after refusal: %q, %v", gotDestination, err)
			}
			gotStage, err := os.ReadFile(stage)
			if err != nil || string(gotStage) != string(sourceBytes) {
				t.Fatalf("source stage changed after refusal: %q, %v", gotStage, err)
			}
			backups, err := filepath.Glob(destination + ".bak-*")
			if err != nil || len(backups) != 0 {
				t.Fatalf("refusal must not create a replacement backup: %v, %v", backups, err)
			}
		})
	}
}

// This regression injects the failure at the committed-journal boundary after
// a source stop. It deliberately uses no tmux: the restart seam records the
// identity it would restart and returns a rollback error for reporting.
func TestHarnessSwitchFailure_CommittedJournalFailureRestoresRunningNativeSource(t *testing.T) {
	originalStart, originalWrite := nativeSwitchStart, harnessSwitchJournalWrite
	t.Cleanup(func() {
		nativeSwitchStart, harnessSwitchJournalWrite = originalStart, originalWrite
	})

	inst := &Instance{ID: "native-source", Tool: "codex", Account: "personal", Command: "codex", ProjectPath: "/project", Title: "source"}
	journal := &switchJournal{Version: 1, OperationID: "op", Source: identityForInstance(inst)}
	var restartCalls int
	nativeSwitchStart = func(got *Instance) error {
		restartCalls++
		if got.Tool != "codex" || got.Account != "personal" || got.Command != "codex" {
			t.Fatalf("restart identity = %#v, want original Codex source", got)
		}
		return errors.New("injected source restart failure")
	}
	harnessSwitchJournalWrite = func(string, *switchJournal) error {
		return errors.New("injected committed journal failure")
	}

	err := commitNativeSwitchAccount(filepath.Join(t.TempDir(), "switch.json"), journal, inst, "work", true)
	if err == nil || !strings.Contains(err.Error(), "injected committed journal failure") || !strings.Contains(err.Error(), "source restore failed: injected source restart failure") {
		t.Fatalf("commit error = %v, want journal and rollback failures", err)
	}
	if restartCalls != 1 {
		t.Fatalf("source restart calls = %d, want 1", restartCalls)
	}
	if inst.Account != "personal" || inst.Tool != "codex" || inst.Command != "codex" {
		t.Fatalf("source was not restored: %#v", inst)
	}
	if journal.State != switchFailed || !strings.Contains(journal.Failure, "source restore failed") {
		t.Fatalf("failed journal did not preserve rollback error: %#v", journal)
	}
}

// A stopped or --no-start source has no lifecycle to restart, but it still
// must be restored before the failed operation is returned to the caller.
func TestHarnessSwitchFailure_CommittedJournalFailureRestoresStoppedNativeSource(t *testing.T) {
	originalStart, originalWrite := nativeSwitchStart, harnessSwitchJournalWrite
	t.Cleanup(func() {
		nativeSwitchStart, harnessSwitchJournalWrite = originalStart, originalWrite
	})

	inst := &Instance{ID: "native-source", Tool: "codex", Account: "personal", Command: "codex", ProjectPath: "/project", Title: "source"}
	journal := &switchJournal{Version: 1, OperationID: "op", Source: identityForInstance(inst)}
	var restartCalls int
	nativeSwitchStart = func(*Instance) error {
		restartCalls++
		return nil
	}
	harnessSwitchJournalWrite = func(string, *switchJournal) error {
		return errors.New("injected committed journal failure")
	}

	err := commitNativeSwitchAccount(filepath.Join(t.TempDir(), "switch.json"), journal, inst, "work", false)
	if err == nil || !strings.Contains(err.Error(), "injected committed journal failure") {
		t.Fatalf("commit error = %v, want journal failure", err)
	}
	if restartCalls != 0 {
		t.Fatalf("source restart calls = %d, want 0 for stopped source", restartCalls)
	}
	if inst.Account != "personal" || inst.Tool != "codex" || inst.Command != "codex" {
		t.Fatalf("stopped source was not restored: %#v", inst)
	}
	if journal.State != switchFailed {
		t.Fatalf("journal state = %q, want %q", journal.State, switchFailed)
	}
}

func TestHarnessSwitchRecovery_DurableBindingSurvivesDiskRoundTripAndStatusRefresh(t *testing.T) {
	startedLocal := time.Date(2026, time.September, 10, 9, 30, 0, 123456789, time.FixedZone("local-test", 2*60*60))
	inst := &Instance{ID: "instance-a", Tool: "claude", Account: "", ClaudeSessionID: "native-a", ProjectPath: "/project-a", Title: "test2", GroupPath: "tmp", Command: "claude", Status: StatusWaiting, LastStartedAt: startedLocal}
	source := identityForInstance(inst)
	journal := &switchJournal{Version: switchJournalVersion, State: switchPrepared, Source: source, Target: switchIdentity{Tool: "claude", Account: "work"}, NoStart: false}
	journal.RequestGeneration = switchRequestGeneration(source, SwitchPreviewTarget{Account: " work "}, false)

	// JSON removes location/monotonic details. The durable digest must still
	// match an explicit UTC reload, and omission of --to-harness must mean the
	// same target as explicit --to-harness claude.
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	var loaded switchJournal
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	inst.Status = StatusIdle
	inst.LastStartedAt = startedLocal.UTC()
	preview := &SwitchPreview{TargetHarness: "claude", TargetAccount: "work"}
	if !journalMatchesRequest(&loaded, inst, preview, HarnessSwitchOptions{Target: SwitchPreviewTarget{Account: "work"}}) {
		t.Fatalf("prepared retry was rejected after JSON/UTC/status refresh: %#v", loaded)
	}
	legacy := loaded
	legacy.Version, legacy.RequestGeneration = 1, "pre-canonical-time-hash"
	if !journalMatchesRequest(&legacy, inst, preview, HarnessSwitchOptions{Target: SwitchPreviewTarget{Account: "work"}}) {
		t.Fatalf("version-1 prepared retry was rejected after JSON/UTC/status refresh: %#v", legacy)
	}
	if got, want := switchRequestGeneration(source, SwitchPreviewTarget{Account: "work"}, false), switchRequestGeneration(source, SwitchPreviewTarget{Harness: "claude", Account: "work"}, false); got != want {
		t.Fatalf("default and explicit native harness hashes differ: %s != %s", got, want)
	}

	modal := CaptureSwitchModalIdentity(&Instance{ID: "instance-a", Tool: "claude", Account: "", ClaudeSessionID: "native-a", ProjectPath: "/project-a", Title: "test2", GroupPath: "tmp", Command: "claude", Status: StatusWaiting, LastStartedAt: startedLocal})
	if modal.Matches(inst) {
		t.Fatal("UI stale-modal guard accepted a status/metadata change that it must make the user reconfirm")
	}
}

func TestHarnessSwitchRecovery_RefusesChangedDurableSourceIncarnation(t *testing.T) {
	started := time.Date(2026, time.September, 10, 7, 30, 0, 0, time.UTC)
	base := &Instance{ID: "instance-a", Tool: "claude", Account: "personal", ClaudeSessionID: "native-a", ProjectPath: "/project-a", Title: "test2", GroupPath: "tmp", Command: "claude", Status: StatusWaiting, LastStartedAt: started}
	journal := &switchJournal{Version: switchJournalVersion, State: switchPrepared, Source: identityForInstance(base), Target: switchIdentity{Tool: "claude", Account: "work"}, NoStart: false}
	journal.RequestGeneration = switchRequestGeneration(journal.Source, SwitchPreviewTarget{Harness: "claude", Account: "work"}, false)
	preview := &SwitchPreview{TargetHarness: "claude", TargetAccount: "work"}
	for name, mutate := range map[string]func(*Instance){
		"native ID":          func(inst *Instance) { inst.ClaudeSessionID = "native-b" },
		"account":            func(inst *Instance) { inst.Account = "other" },
		"working directory":  func(inst *Instance) { inst.ProjectPath = "/project-b" },
		"title":              func(inst *Instance) { inst.Title = "renamed externally" },
		"group":              func(inst *Instance) { inst.GroupPath = "moved/externally" },
		"command":            func(inst *Instance) { inst.Command = "claude --custom-command" },
		"launch incarnation": func(inst *Instance) { inst.LastStartedAt = inst.LastStartedAt.Add(time.Nanosecond) },
	} {
		t.Run(name, func(t *testing.T) {
			inst := &Instance{ID: base.ID, Tool: base.Tool, Account: base.Account, ClaudeSessionID: base.ClaudeSessionID, ProjectPath: base.ProjectPath, Title: base.Title, GroupPath: base.GroupPath, Command: base.Command, Status: base.Status, LastStartedAt: base.LastStartedAt}
			mutate(inst)
			if journalMatchesRequest(journal, inst, preview, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "work"}}) {
				t.Fatalf("recovery accepted changed %s: %#v", name, inst)
			}
			if !strings.Contains(journalRequestMismatchReason(journal, inst, preview, HarnessSwitchOptions{}), "will not be replayed") {
				t.Fatalf("changed %s did not produce actionable no-replay refusal", name)
			}
		})
	}
}

func TestHarnessSwitchRecovery_AccountRoundTripGetsDistinctOperation(t *testing.T) {
	started := time.Date(2026, time.September, 10, 7, 30, 0, 0, time.UTC)
	fromA := switchIdentity{InstanceID: "instance-a", Tool: "claude", ClaudeID: "native-a", SessionID: "native-a", Account: "A", ProjectPath: "/project", WorkingDir: "/project", LastStartedAt: started}
	fromB := fromA
	fromB.Account = "B"
	toB := switchRequestGeneration(fromA, SwitchPreviewTarget{Harness: "claude", Account: "B"}, false)
	toA := switchRequestGeneration(fromB, SwitchPreviewTarget{Harness: "claude", Account: "A"}, false)
	if toA == toB || switchOperationIDForRequest("instance-a", toA) == switchOperationIDForRequest("instance-a", toB) {
		t.Fatal("A -> B -> A reused the first operation journal identity")
	}
}

// This takes a stable source lock as a deterministic fake transaction barrier.
// The public native and cross-harness entry points target different harnesses,
// but neither may reach even its preflight while that source transaction holds
// the lock. Both paths then stop at validation; no lifecycle is exercised.
func TestHarnessSwitchSourceLock_SerializesDistinctNativeAndCrossHarnessRequests(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))

	source := &Instance{ID: "same-source", Tool: "claude", Command: "claude", ProjectPath: filepath.Join(home, "project"), Title: "source", ClaudeSessionID: "native-a"}
	barrier, err := acquireHarnessSwitchSourceLock(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Release()

	nativeStarted, crossStarted := make(chan struct{}), make(chan struct{})
	nativeDone, crossDone := make(chan error, 1), make(chan error, 1)
	go func() {
		close(nativeStarted)
		_, callErr := ExecuteHarnessSwitch(nil, source, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "work"}, NoStart: true})
		nativeDone <- callErr
	}()
	go func() {
		close(crossStarted)
		_, callErr := ExecuteCrossHarnessSwitch(context.Background(), nil, source, CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex"}, NoStart: true}, CrossHarnessSwitchDependencies{})
		crossDone <- callErr
	}()
	<-nativeStarted
	<-crossStarted
	select {
	case err := <-nativeDone:
		t.Fatalf("native request bypassed the source lock: %v", err)
	case err := <-crossDone:
		t.Fatalf("cross-harness request bypassed the source lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	barrier.Release()
	barrier = nil

	if err := <-nativeDone; err == nil || !strings.Contains(err.Error(), "switch refused") {
		t.Fatalf("native request after lock release = %v, want preflight refusal", err)
	}
	if err := <-crossDone; err == nil || !strings.Contains(err.Error(), "target store is required") {
		t.Fatalf("cross-harness request after lock release = %v, want dependency refusal", err)
	}
}

func TestHarnessSwitchFailure_CodexPreparedJournalWritePrecedesStaging(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))

	sourceHome := filepath.Join(home, "codex-personal")
	targetHome := filepath.Join(home, "codex-work")
	t.Setenv("CODEX_HOME", sourceHome)
	const sid = "33333333-4444-5555-6666-777777777777"
	sourcePath := filepath.Join(sourceHome, "sessions", "2026", "09", "10", "rollout-20260910-"+sid+".jsonl")
	seed := []byte(`{"type":"session_meta","payload":{"id":"` + sid + `"}}` + "\n")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{ID: "codex-source", Tool: "codex", Account: "", Command: "codex", ProjectPath: filepath.Join(home, "project"), Title: "source", CodexSessionID: sid}
	cfg := &UserConfig{Profiles: map[string]ProfileSettings{
		"work": {Codex: ProfileCodexSettings{ConfigDir: targetHome}},
	}}
	originalWrite := harnessSwitchJournalWrite
	t.Cleanup(func() { harnessSwitchJournalWrite = originalWrite })
	var writeCalls int
	harnessSwitchJournalWrite = func(string, *switchJournal) error {
		writeCalls++
		return errors.New("injected prepared journal write failure")
	}

	_, err := ExecuteHarnessSwitch(cfg, inst, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}, NoStart: true})
	if err == nil || !strings.Contains(err.Error(), "persist prepared Codex switch: injected prepared journal write failure") {
		t.Fatalf("first durable-write failure = %v", err)
	}
	if writeCalls != 1 {
		t.Fatalf("prepared journal writes = %d, want 1", writeCalls)
	}
	got, readErr := os.ReadFile(sourcePath)
	if readErr != nil || string(got) != string(seed) {
		t.Fatalf("source changed after prepared-write failure: %q, %v", got, readErr)
	}
	stageRoot, stageErr := switchStageRoot(inst.ID)
	if stageErr != nil {
		t.Fatal(stageErr)
	}
	if _, statErr := os.Stat(stageRoot); !os.IsNotExist(statErr) {
		t.Fatalf("staging exists after prepared-write failure: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(targetHome, "sessions")); !os.IsNotExist(statErr) {
		t.Fatalf("target artifact exists after prepared-write failure: %v", statErr)
	}
}

func TestHarnessSwitchFailure_RecoveryRejectsChangedIdentity(t *testing.T) {
	root := t.TempDir()
	journalPath := filepath.Join(root, "switch.json")
	journal := &switchJournal{
		Version:     1,
		OperationID: "op",
		State:       switchStaged,
		Source:      switchIdentity{InstanceID: "instance-a", ProjectPath: "/project-a", Title: "title-a"},
		Target:      switchIdentity{Tool: "claude", Account: "work"},
	}
	if err := writeSwitchJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSwitchJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	inst := &Instance{ID: "instance-b", ProjectPath: "/project-a", Title: "title-a"}
	preview := &SwitchPreview{TargetHarness: "claude", TargetAccount: "work"}
	if loaded == nil || journalMatchesRequest(loaded, inst, preview) {
		t.Fatal("recovery must reject a changed source identity")
	}
}
