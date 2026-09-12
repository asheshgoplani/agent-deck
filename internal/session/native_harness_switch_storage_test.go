package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func nativeHarnessCommitResult(source *Instance, account string) *HarnessSwitchResult {
	sourceIdentity := identityForInstance(source)
	targetIdentity := sourceIdentity
	targetIdentity.Account = account
	return nativeHarnessSwitchResult(&HarnessSwitchResult{Committed: true}, sourceIdentity, targetIdentity)
}

// This exercises the native executor and real targeted storage adapter through
// A -> B -> A with the same exact session ID intentionally present in both
// account homes. Lifecycle is injected; no tmux process is created.
func TestSwitchAccountNativeRoundTripUsesBoundAccountAndScopedStorage(t *testing.T) {
	home := withTempAgentDeckHome(t, `
[profiles.personal.claude]
config_dir = "~/.claude-personal"
[profiles.spare.claude]
config_dir = "~/.claude-spare"
`)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	cfg, err := LoadUserConfig()
	require.NoError(t, err)
	project := filepath.Join(home, "project")
	const sid = "11111111-2222-3333-4444-555555555555"
	seed := []byte(`{"sessionId":"` + sid + `","type":"user","message":"duplicated native history"}` + "\n")
	for _, account := range []string{"personal", "spare"} {
		path := filepath.Join(home, ".claude-"+account, "projects", ConvertToClaudeDirName(project), sid+".jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, seed, 0o600))
	}

	originalRunning, originalStop, originalStart := nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart
	t.Cleanup(func() {
		nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart = originalRunning, originalStop, originalStart
	})
	var stops, starts int
	nativeSwitchRunning = func(*Instance) bool { return true }
	nativeSwitchStop = func(*Instance) error { stops++; return nil }
	nativeSwitchStart = func(*Instance) error { starts++; return nil }

	storage := newTestStorage(t)
	inst := &Instance{ID: "switch-source", Title: "source", ProjectPath: project, GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: sid, Status: StatusWaiting, CreatedAt: time.Now()}
	require.NoError(t, storage.Save([]*Instance{inst}))

	toSpare, err := SwitchAccount(cfg, inst, "spare", AccountSwitchOptions{})
	require.NoError(t, err)
	journal, err := loadSwitchJournal(toSpare.nativeResult.nativeJournalPath)
	require.NoError(t, err)
	require.Equal(t, switchCommitted, journal.State, "a running native lifecycle must await storage CAS acknowledgement")
	require.NoError(t, CommitAccountSwitch(storage, inst, toSpare))
	journal, err = loadSwitchJournal(toSpare.nativeResult.nativeJournalPath)
	require.NoError(t, err)
	require.Equal(t, switchCompleted, journal.State)
	require.Equal(t, "spare", inst.Account)
	toPersonal, err := SwitchAccount(cfg, inst, "personal", AccountSwitchOptions{})
	require.NoError(t, err)
	require.NoError(t, CommitAccountSwitch(storage, inst, toPersonal))
	require.Equal(t, "personal", inst.Account)
	require.Equal(t, 2, stops, "each native operation stops only its bound source")
	require.Equal(t, 2, starts, "each native operation starts once through the fake lifecycle")

	stored, err := storage.Load()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, "personal", stored[0].Account)
}

// A completed journal can be retried after the native source artifact is no
// longer available. Recovery must return only the bound storage mutation and
// must not restart, stop, or freshly export the old source.
func TestSwitchAccountCompletedJournalRepairsOnlyStorageBeforeFreshExport(t *testing.T) {
	home := withTempAgentDeckHome(t, `
[profiles.personal.claude]
config_dir = "~/.claude-personal"
[profiles.spare.claude]
config_dir = "~/.claude-spare"
`)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	cfg, err := LoadUserConfig()
	require.NoError(t, err)
	inst := &Instance{ID: "switch-recovery", Title: "source", ProjectPath: filepath.Join(home, "project"), GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: "11111111-2222-3333-4444-555555555555", Status: StatusWaiting, CreatedAt: time.Now()}
	source := identityForInstance(inst)
	target := source
	target.Account = "spare"
	request := switchRequestGeneration(source, SwitchPreviewTarget{Harness: "claude", Account: "spare"}, false)
	journalPath, err := switchJournalPathForRequest(inst.ID, request)
	require.NoError(t, err)
	require.NoError(t, writeSwitchJournal(journalPath, &switchJournal{Version: switchJournalVersion, State: switchCompleted, Source: source, Target: target, NoStart: false, RequestGeneration: request}))

	originalRunning, originalStop, originalStart := nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart
	t.Cleanup(func() {
		nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart = originalRunning, originalStop, originalStart
	})
	var lifecycleCalls int
	nativeSwitchRunning = func(*Instance) bool { lifecycleCalls++; return true }
	nativeSwitchStop = func(*Instance) error { lifecycleCalls++; return nil }
	nativeSwitchStart = func(*Instance) error { lifecycleCalls++; return nil }

	result, err := ExecuteHarnessSwitch(cfg, inst, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "spare"}})
	require.NoError(t, err)
	require.True(t, result.Committed)
	require.Equal(t, 0, lifecycleCalls, "completed recovery must not replay lifecycle")
	require.Equal(t, "personal", inst.Account, "storage repair owns the account mutation")

	storage := newTestStorage(t)
	require.NoError(t, storage.Save([]*Instance{inst}))
	require.NoError(t, storage.CommitNativeHarnessSwitch(inst, result))
	stored, err := storage.Load()
	require.NoError(t, err)
	require.Equal(t, "spare", stored[0].Account)
}

// A version-2 completed operation has a different request filename because
// version 3 added StorageTool to its request binding. It must still be found
// before a fresh export/lifecycle attempt, and only its exact storage CAS may
// be repaired. The copied fixture deliberately omits storage_tool.
func TestSwitchAccountLegacyCompletedV2RepairsStorageDespiteFailedV3Descendant(t *testing.T) {
	home := withTempAgentDeckHome(t, `
[profiles.personal.claude]
config_dir = "~/.claude-personal"
[profiles.spare.claude]
config_dir = "~/.claude-spare"
`)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	cfg, err := LoadUserConfig()
	require.NoError(t, err)

	inst := &Instance{ID: "legacy-v2-recovery", Title: "source", ProjectPath: filepath.Join(home, "project"), GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: "11111111-2222-3333-4444-555555555555", Status: StatusWaiting, CreatedAt: time.Now()}
	source := identityForInstance(inst)
	source.StorageTool = "" // version 2 did not persist this field.
	target := source
	target.Account, target.StorageTool = "spare", ""
	legacyPath, err := switchJournalPathForRequest(inst.ID, "v2-legacy-request")
	require.NoError(t, err)
	legacy := &switchJournal{Version: 2, OperationID: "legacy-completed", State: switchCompleted, Source: source, Target: target, NoStart: false, RequestGeneration: "v2-legacy-request"}
	require.NoError(t, writeSwitchJournal(legacyPath, legacy))
	before, err := os.ReadFile(legacyPath)
	require.NoError(t, err)

	// Reproduce the descendant failure written by version 3 after it ignored
	// this completed receipt. It must be retained, not replayed or allowed to
	// block the storage-only repair.
	v3Request := switchRequestGeneration(identityForInstance(inst), SwitchPreviewTarget{Harness: "claude", Account: "spare"}, false)
	v3Path, err := switchJournalPathForRequest(inst.ID, v3Request)
	require.NoError(t, err)
	failedV3 := &switchJournal{Version: switchJournalVersion, OperationID: switchOperationIDForRequest(inst.ID, v3Request), State: switchFailed, Source: identityForInstance(inst), Target: targetIdentityFor(inst, &SwitchPreview{TargetHarness: "claude", TargetAccount: "spare"}), NoStart: false, RequestGeneration: v3Request, Failure: "destination already contains a different conversation"}
	require.NoError(t, writeSwitchJournal(v3Path, failedV3))
	failedBefore, err := os.ReadFile(v3Path)
	require.NoError(t, err)

	originalRunning, originalStop, originalStart := nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart
	t.Cleanup(func() {
		nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart = originalRunning, originalStop, originalStart
	})
	var lifecycleCalls int
	nativeSwitchRunning = func(*Instance) bool { lifecycleCalls++; return true }
	nativeSwitchStop = func(*Instance) error { lifecycleCalls++; return nil }
	nativeSwitchStart = func(*Instance) error { lifecycleCalls++; return nil }

	result, err := ExecuteHarnessSwitch(cfg, inst, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "spare"}})
	require.NoError(t, err)
	require.True(t, result.Committed)
	require.Zero(t, lifecycleCalls, "completed v2 recovery must not export, stop, or restart")
	require.Equal(t, "claude", result.nativeSource.StorageTool, "only the documented legacy storage tool is inferred in memory")
	after, err := os.ReadFile(legacyPath)
	require.NoError(t, err)
	require.Equal(t, before, after, "historical journal evidence must remain immutable")

	failedAfter, err := os.ReadFile(v3Path)
	require.NoError(t, err)
	require.Equal(t, failedBefore, failedAfter, "the failed version-3 descendant must remain immutable evidence")

	storage := newTestStorage(t)
	require.NoError(t, storage.Save([]*Instance{inst}))
	require.NoError(t, storage.CommitNativeHarnessSwitch(inst, result))
	stored, err := storage.Load()
	require.NoError(t, err)
	require.Equal(t, "spare", stored[0].Account, "only the completed operation's scoped account CAS is repaired")
}

// A retained A conversation is a byte prefix after B receives additional
// turns. Returning B -> A may advance that exact artifact, but only through
// installStagedArtifact's snapshot-and-atomic-install path.
func TestNativeSwitchRoundTripAdvancesRetainedPrefixAfterDestinationGrows(t *testing.T) {
	home := withTempAgentDeckHome(t, `
[profiles.personal.claude]
config_dir = "~/.claude-personal"
[profiles.spare.claude]
config_dir = "~/.claude-spare"
`)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	cfg, err := LoadUserConfig()
	require.NoError(t, err)

	originalRunning := nativeSwitchRunning
	t.Cleanup(func() { nativeSwitchRunning = originalRunning })
	nativeSwitchRunning = func(*Instance) bool { return false }

	project := filepath.Join(home, "project")
	require.NoError(t, os.MkdirAll(project, 0o700))
	const sid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	initial := []byte(`{"sessionId":"` + sid + `","type":"user","message":"A before switch"}` + "\n")
	grown := append(append([]byte(nil), initial...), []byte(`{"sessionId":"`+sid+`","type":"assistant","message":"B added this turn"}`+"\n")...)
	personal := filepath.Join(home, ".claude-personal", "projects", ConvertToClaudeDirName(project), sid+".jsonl")
	spare := filepath.Join(home, ".claude-spare", "projects", ConvertToClaudeDirName(project), sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(personal), 0o700))
	require.NoError(t, os.WriteFile(personal, initial, 0o600))

	storage := newTestStorage(t)
	inst := &Instance{ID: "growing-round-trip", Title: "source", ProjectPath: project, GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: sid, Status: StatusWaiting, CreatedAt: time.Now()}
	require.NoError(t, storage.Save([]*Instance{inst}))
	first, err := ExecuteHarnessSwitch(cfg, inst, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "spare"}, NoStart: true})
	require.NoError(t, err)
	require.True(t, first.Committed)
	require.Equal(t, "spare", inst.Account)
	require.Equal(t, initial, mustReadSwitchFixture(t, spare))

	// Before the CLI/TUI storage CAS succeeds, the operation is genuinely
	// uncertain and a distinct request must remain blocked.
	_, err = ExecuteHarnessSwitch(cfg, inst, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "personal"}, NoStart: true})
	require.ErrorContains(t, err, "unresolved prior operation")
	require.NoError(t, storage.CommitNativeHarnessSwitch(inst, first))

	// Simulate B receiving a turn while A's retained artifact remains the
	// exact older prefix. This is the real A -> B -> A CLI/TUI shape, not a
	// static installer-only round trip.
	require.NoError(t, os.WriteFile(spare, grown, 0o600))
	second, err := ExecuteHarnessSwitch(cfg, inst, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "personal"}, NoStart: true})
	require.NoError(t, err)
	require.True(t, second.Committed)
	require.Equal(t, "personal", inst.Account)
	require.NoError(t, storage.CommitNativeHarnessSwitch(inst, second))
	require.Equal(t, grown, mustReadSwitchFixture(t, personal))
	require.Equal(t, grown, mustReadSwitchFixture(t, spare), "source account remains preserved")
	backups, err := filepath.Glob(personal + ".bak-*")
	require.NoError(t, err)
	require.Len(t, backups, 1, "the retained destination must be durably snapshotted before advancing")
	require.Equal(t, initial, mustReadSwitchFixture(t, backups[0]))
}

// If storage commits but its journal acknowledgement write fails, the reload
// holds the target identity rather than the source CAS identity. Recovery must
// acknowledge that exact persisted target only; it cannot replay lifecycle or
// strand the next distinct account operation behind the committed receipt.
func TestNativeHarnessSwitchAckFailureReloadsTargetAndUnblocksNextOperation(t *testing.T) {
	home := withTempAgentDeckHome(t, `
[profiles.personal.claude]
config_dir = "~/.claude-personal"
[profiles.spare.claude]
config_dir = "~/.claude-spare"
`)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	cfg, err := LoadUserConfig()
	require.NoError(t, err)
	project := filepath.Join(home, "project")
	const sid = "cccccccc-dddd-eeee-ffff-000000000000"
	seed := []byte(`{"sessionId":"` + sid + `","type":"user","message":"native history"}` + "\n")
	personal := filepath.Join(home, ".claude-personal", "projects", ConvertToClaudeDirName(project), sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(personal), 0o700))
	require.NoError(t, os.WriteFile(personal, seed, 0o600))

	originalRunning, originalStart, originalWrite := nativeSwitchRunning, nativeSwitchStart, harnessSwitchJournalWrite
	t.Cleanup(func() {
		nativeSwitchRunning, nativeSwitchStart, harnessSwitchJournalWrite = originalRunning, originalStart, originalWrite
	})
	var starts int
	nativeSwitchRunning = func(*Instance) bool { return false }
	nativeSwitchStart = func(*Instance) error { starts++; return nil }

	storage := newTestStorage(t)
	base := &Instance{ID: "ack-reload", Title: "source", ProjectPath: project, GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: sid, Status: StatusWaiting, CreatedAt: time.Now()}
	other := &Instance{ID: "unrelated", Title: "must survive", ProjectPath: project, GroupPath: "test", Tool: "shell", Command: "sh", Status: StatusIdle, CreatedAt: time.Now()}
	require.NoError(t, storage.Save([]*Instance{base, other}))

	first, err := ExecuteHarnessSwitch(cfg, base, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "spare"}, NoStart: true})
	require.NoError(t, err)
	journalBefore, err := loadSwitchJournal(first.nativeJournalPath)
	require.NoError(t, err)
	require.Equal(t, switchCommitted, journalBefore.State, "native lifecycle must not preemptively complete its journal")

	harnessSwitchJournalWrite = func(path string, journal *switchJournal) error {
		if journal.State == switchCompleted {
			return errors.New("injected storage acknowledgement failure")
		}
		return originalWrite(path, journal)
	}
	require.ErrorContains(t, storage.CommitNativeHarnessSwitch(base, first), "injected storage acknowledgement failure")
	harnessSwitchJournalWrite = originalWrite

	journalAfterFailure, err := loadSwitchJournal(first.nativeJournalPath)
	require.NoError(t, err)
	require.Equal(t, switchCommitted, journalAfterFailure.State)
	reloaded, err := storage.Load()
	require.NoError(t, err)
	var target *Instance
	for _, candidate := range reloaded {
		if candidate.ID == base.ID {
			target = candidate
		}
	}
	require.NotNil(t, target)
	require.Equal(t, "spare", target.Account, "the synthetic DB must contain the actual CAS target")

	// Requesting the next distinct operation first recovers the unacknowledged
	// target receipt. It must not start, stop, or otherwise replay lifecycle.
	recovered, err := ExecuteHarnessSwitch(cfg, target, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "personal"}, NoStart: true})
	require.NoError(t, err)
	require.True(t, recovered.nativeStorageAcknowledgement)
	require.Equal(t, 0, starts, "target recovery must not call Start")
	require.NoError(t, storage.CommitNativeHarnessSwitch(target, recovered))
	journalAfterAck, err := loadSwitchJournal(first.nativeJournalPath)
	require.NoError(t, err)
	require.Equal(t, switchCompleted, journalAfterAck.State)

	// The exact acknowledgement releases the receipt; only now can B -> A run.
	next, err := ExecuteHarnessSwitch(cfg, target, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "personal"}, NoStart: true})
	require.NoError(t, err)
	require.NoError(t, storage.CommitNativeHarnessSwitch(target, next))
	require.Equal(t, "personal", target.Account)
	require.Equal(t, 0, starts, "neither recovery nor a stopped next operation may call Start")
	stored, err := storage.Load()
	require.NoError(t, err)
	for _, candidate := range stored {
		if candidate.ID == other.ID {
			require.Equal(t, "must survive", candidate.Title, "storage-only acknowledgement must not overwrite unrelated state")
		}
	}
}

// Version-1 nonterminal journals lack a stable request binding. They remain
// preserved evidence, but must never be adopted or replayed even when their
// fields appear to match the caller's request.
func TestNativeHarnessSwitchUncertainV1JournalIsRefusedWithoutLifecycleReplay(t *testing.T) {
	home := withTempAgentDeckHome(t, `
[profiles.personal.claude]
config_dir = "~/.claude-personal"
[profiles.spare.claude]
config_dir = "~/.claude-spare"
`)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	cfg, err := LoadUserConfig()
	require.NoError(t, err)
	inst := &Instance{ID: "uncertain-v1", Title: "source", ProjectPath: filepath.Join(home, "project"), GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: "11111111-2222-3333-4444-555555555555", Status: StatusWaiting, CreatedAt: time.Now()}
	legacyPath, err := switchJournalPath(inst.ID)
	require.NoError(t, err)
	require.NoError(t, writeSwitchJournal(legacyPath, &switchJournal{Version: 1, State: switchPrepared, Source: identityForInstance(inst), Target: targetIdentityFor(inst, &SwitchPreview{TargetHarness: "claude", TargetAccount: "spare"}), NoStart: true, RequestGeneration: "legacy"}))

	originalRunning, originalStop, originalStart := nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart
	t.Cleanup(func() {
		nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart = originalRunning, originalStop, originalStart
	})
	var lifecycleCalls int
	nativeSwitchRunning = func(*Instance) bool { lifecycleCalls++; return false }
	nativeSwitchStop = func(*Instance) error { lifecycleCalls++; return nil }
	nativeSwitchStart = func(*Instance) error { lifecycleCalls++; return nil }
	_, err = ExecuteHarnessSwitch(cfg, inst, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "spare"}, NoStart: true})
	require.ErrorContains(t, err, "unsupported uncertain legacy version-1 journal")
	require.Zero(t, lifecycleCalls)
	preserved, readErr := loadSwitchJournal(legacyPath)
	require.NoError(t, readErr)
	require.Equal(t, switchPrepared, preserved.State)
}

func mustReadSwitchFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

// The native lifecycle can change a pane's status before its CLI/TUI caller
// reaches storage. That monitor-owned update must survive the account commit;
// a full stale registry save used to reject this exact sequence.
func TestCommitNativeHarnessSwitch_PreservesConcurrentMonitorAndUnrelatedEdits(t *testing.T) {
	storage := newTestStorage(t)
	base := &Instance{ID: "switch-source", Title: "source", ProjectPath: t.TempDir(), GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: "native-source", Status: StatusWaiting, CreatedAt: time.Now()}
	other := &Instance{ID: "unrelated", Title: "other", ProjectPath: t.TempDir(), GroupPath: "test", Tool: "shell", Command: "sh", Status: StatusIdle, CreatedAt: time.Now()}
	require.NoError(t, storage.Save([]*Instance{base, other}))

	switcher, err := storage.Load()
	require.NoError(t, err)
	monitor, err := storage.Load()
	require.NoError(t, err)
	for _, inst := range monitor {
		if inst.ID == base.ID {
			inst.Status = StatusRunning
		} else {
			inst.Title = "unrelated edit"
		}
	}
	require.NoError(t, storage.Save(monitor))

	source := switcher[0]
	if source.ID != base.ID {
		source = switcher[1]
	}
	result := nativeHarnessCommitResult(source, "spare")
	source.Account = "spare" // mirrors the lifecycle backend's in-memory commit
	require.NoError(t, storage.CommitNativeHarnessSwitch(source, result))

	got, err := storage.Load()
	require.NoError(t, err)
	byID := make(map[string]*Instance, len(got))
	for _, inst := range got {
		byID[inst.ID] = inst
	}
	require.Equal(t, "spare", byID[base.ID].Account)
	require.Equal(t, StatusRunning, byID[base.ID].Status, "monitor status must not block or be overwritten")
	require.Equal(t, "unrelated edit", byID[other.ID].Title, "unrelated row edits must survive")
}

func TestCommitNativeHarnessSwitch_RefusesMeaningfulSourceConflicts(t *testing.T) {
	for name, mutate := range map[string]func(*Instance){
		"account":   func(inst *Instance) { inst.Account = "other" },
		"tool":      func(inst *Instance) { inst.Tool = "shell" },
		"command":   func(inst *Instance) { inst.Command = "claude --other" },
		"path":      func(inst *Instance) { inst.ProjectPath = t.TempDir() },
		"native ID": func(inst *Instance) { inst.ClaudeSessionID = "native-other" },
	} {
		t.Run(name, func(t *testing.T) {
			storage := newTestStorage(t)
			base := &Instance{ID: "switch-source", Title: "source", ProjectPath: t.TempDir(), GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: "native-source", Status: StatusWaiting, CreatedAt: time.Now()}
			require.NoError(t, storage.Save([]*Instance{base}))
			switcher, err := storage.Load()
			require.NoError(t, err)
			concurrent, err := storage.Load()
			require.NoError(t, err)
			mutate(concurrent[0])
			require.NoError(t, storage.Save(concurrent))

			result := nativeHarnessCommitResult(switcher[0], "spare")
			switcher[0].Account = "spare"
			require.ErrorContains(t, storage.CommitNativeHarnessSwitch(switcher[0], result), "source identity conflict")
			got, err := storage.Load()
			require.NoError(t, err)
			require.NotEqual(t, "spare", got[0].Account, "conflicted target must not be written")
		})
	}
}

func TestCompletedNativeJournal_AllowsScopedStorageRepairFromExactSource(t *testing.T) {
	storage := newTestStorage(t)
	base := &Instance{ID: "switch-source", Title: "source", ProjectPath: t.TempDir(), GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: "native-source", Status: StatusWaiting, CreatedAt: time.Now()}
	require.NoError(t, storage.Save([]*Instance{base}))
	inst, err := storage.Load()
	require.NoError(t, err)
	source := identityForInstance(inst[0])
	target := source
	target.Account = "spare"
	preview := &SwitchPreview{TargetHarness: "claude", TargetAccount: "spare"}
	journal := &switchJournal{Version: switchJournalVersion, State: switchCompleted, Source: source, Target: target, NoStart: false, RequestGeneration: switchRequestGeneration(source, SwitchPreviewTarget{Harness: "claude", Account: "spare"}, false)}

	// A lifecycle-complete journal matches its untouched source solely to repair
	// the scoped registry mutation; ExecuteHarnessSwitch returns this result
	// instead of repeating lifecycle work.
	require.True(t, journalMatchesRequest(journal, inst[0], preview, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "spare"}}))
	require.NoError(t, storage.CommitNativeHarnessSwitch(inst[0], resultFromCompletedJournal(preview, journal)))
	got, err := storage.Load()
	require.NoError(t, err)
	require.Equal(t, "spare", got[0].Account)
}

func TestCommitNativeHarnessSwitch_WriteFailureLeavesRecoverableExactMutation(t *testing.T) {
	storage := newTestStorage(t)
	base := &Instance{ID: "switch-source", Title: "source", ProjectPath: t.TempDir(), GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: "native-source", Status: StatusWaiting, CreatedAt: time.Now()}
	require.NoError(t, storage.Save([]*Instance{base}))
	inst, err := storage.Load()
	require.NoError(t, err)
	result := nativeHarnessCommitResult(inst[0], "spare")
	inst[0].Account = "spare"

	_, err = storage.db.DB().Exec("CREATE TRIGGER reject_native_switch BEFORE UPDATE ON instances BEGIN SELECT RAISE(ABORT, 'injected native switch write failure'); END")
	require.NoError(t, err)
	require.ErrorContains(t, storage.CommitNativeHarnessSwitch(inst[0], result), "injected native switch write failure")
	_, err = storage.db.DB().Exec("DROP TRIGGER reject_native_switch")
	require.NoError(t, err)

	// This is the same committed mutation a completed journal returns on retry;
	// it repairs storage without re-running stop/start/prompt lifecycle work.
	require.NoError(t, storage.CommitNativeHarnessSwitch(inst[0], result))
	got, err := storage.Load()
	require.NoError(t, err)
	require.Equal(t, "spare", got[0].Account)
}
