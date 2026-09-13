package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These direct executor tests use only a temporary JSONL fixture plus injected
// store/lifecycle/observer seams. They never invoke tmux or a real harness.
type executorFakeStore struct {
	targets     map[string]*Instance
	loadErr     error
	createErr   error
	saveErr     error
	finalizeErr error
}

func (s *executorFakeStore) LoadTarget(id string) (*Instance, error) { return s.targets[id], s.loadErr }
func (s *executorFakeStore) CreateTarget(target *Instance) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.targets[target.ID] = target
	return nil
}
func (s *executorFakeStore) SaveTarget(target *Instance) error { return s.saveErr }
func (s *executorFakeStore) FinalizeTargetHandoff(source, target *Instance) error {
	if s.finalizeErr != nil {
		return s.finalizeErr
	}
	source.ArchivedAt, source.SupersededBy = time.Now(), target.ID
	target.ArchivedAt, target.Supersedes = time.Time{}, source.ID
	return nil
}

type executorFakeSourceOwnership struct {
	snapshot *SwitchSourceSnapshot
	err      error
	calls    int
}

func (v *executorFakeSourceOwnership) ValidateCrossHarnessSource(source *Instance) (SwitchSourceSnapshot, error) {
	v.calls++
	if v.err != nil {
		return SwitchSourceSnapshot{ManagementUnknown: true}, v.err
	}
	if v.snapshot != nil {
		return *v.snapshot, nil
	}
	return SnapshotSwitchSource(source, []*Instance{source}, false, false), nil
}

func executorSourceOwnership() CrossHarnessSourceOwnershipValidator {
	return &executorFakeSourceOwnership{}
}

type executorFakeLifecycle struct {
	calls int
	err   error
	plan  *FreshTargetLaunchPlan
}

func (l *executorFakeLifecycle) StartTarget(_ context.Context, _ *Instance, plan *FreshTargetLaunchPlan) error {
	l.calls++
	l.plan = cloneCrossHarnessLaunchPlan(plan)
	return l.err
}

type executorFakeObserver struct {
	calls    int
	err      error
	evidence *CrossHarnessTargetEvidence
}

func (o *executorFakeObserver) ObserveTarget(_ context.Context, target *Instance, expected FreshTargetIdentity) (CrossHarnessTargetEvidence, error) {
	o.calls++
	if o.err != nil {
		return CrossHarnessTargetEvidence{}, o.err
	}
	if o.evidence != nil {
		return *o.evidence, nil
	}
	return CrossHarnessTargetEvidence{InstanceID: target.ID, Tool: expected.Tool, SessionID: "fresh-codex-thread", Ready: true, Evidence: "fake"}, nil
}

func seededCrossHarnessExecutorSource(t *testing.T) (*UserConfig, *Instance) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	sourceHome, targetHome := filepath.Join(home, "claude-personal"), filepath.Join(home, "codex-work")
	t.Setenv("CLAUDE_CONFIG_DIR", sourceHome)
	project := filepath.Join(home, "project")
	sid := "11111111-2222-3333-4444-555555555555"
	path := filepath.Join(sourceHome, "projects", ConvertToClaudeDirName(project), sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"sessionId":"`+sid+`","type":"user","message":{"role":"user","content":"seed context"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &UserConfig{Profiles: map[string]ProfileSettings{
		"work":     {Codex: ProfileCodexSettings{ConfigDir: targetHome}},
		"personal": {Claude: ProfileClaudeSettings{ConfigDir: sourceHome}},
	}}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	return cfg, &Instance{ID: "source", Tool: "claude", Account: "personal", ProjectPath: project, Title: "source", GroupPath: "group", ClaudeSessionID: sid}
}

func TestExecuteCrossHarnessSwitch_RefusesWithoutAuthoritativeOwnershipBeforeStaging(t *testing.T) {
	cfg, source := seededCrossHarnessExecutorSource(t)
	store := &executorFakeStore{targets: map[string]*Instance{}}
	lifecycle := &executorFakeLifecycle{}
	_, err := ExecuteCrossHarnessSwitch(context.Background(), cfg, source, CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}, SourceSnapshot: &SwitchSourceSnapshot{}}, CrossHarnessSwitchDependencies{
		Store: store, Lifecycle: lifecycle, Observer: &executorFakeObserver{},
	})
	if err == nil || !strings.Contains(err.Error(), "management-unknown") || len(store.targets) != 0 || lifecycle.calls != 0 {
		t.Fatalf("missing ownership authority staged or launched: err=%v targets=%#v lifecycle=%d", err, store.targets, lifecycle.calls)
	}
}

func TestExecuteCrossHarnessSwitch_CreateFailureLeavesSourceUntouched(t *testing.T) {
	cfg, source := seededCrossHarnessExecutorSource(t)
	before := identityForInstance(source)
	store := &executorFakeStore{targets: map[string]*Instance{}, createErr: errors.New("create failed")}
	result, err := ExecuteCrossHarnessSwitch(context.Background(), cfg, source, CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}}, CrossHarnessSwitchDependencies{Store: store, Lifecycle: &executorFakeLifecycle{}, Observer: &executorFakeObserver{}, SourceOwnership: executorSourceOwnership()})
	if err == nil || result == nil || result.Target != nil || identityForInstance(source) != before || len(store.targets) != 0 {
		t.Fatalf("create failure mutated source or retained a target: result=%#v err=%v source=%#v", result, err, source)
	}
}

func TestExecuteCrossHarnessSwitch_LoadFailureReturnsResultWithoutTarget(t *testing.T) {
	cfg, source := seededCrossHarnessExecutorSource(t)
	store := &executorFakeStore{targets: map[string]*Instance{}, loadErr: errors.New("load failed")}
	result, err := ExecuteCrossHarnessSwitch(context.Background(), cfg, source, CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}}, CrossHarnessSwitchDependencies{Store: store, Lifecycle: &executorFakeLifecycle{}, Observer: &executorFakeObserver{}, SourceOwnership: executorSourceOwnership()})
	if err == nil || result == nil || result.Target != nil || result.TargetCreated {
		t.Fatalf("load failure result = %#v err=%v, want nil target without a commit", result, err)
	}
}

func TestExecuteCrossHarnessSwitch_CancelBeforeLaunchLeavesSourceUntouched(t *testing.T) {
	cfg, source := seededCrossHarnessExecutorSource(t)
	before := identityForInstance(source)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lifecycle := &executorFakeLifecycle{}
	_, err := ExecuteCrossHarnessSwitch(ctx, cfg, source, CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}}, CrossHarnessSwitchDependencies{Store: &executorFakeStore{targets: map[string]*Instance{}}, Lifecycle: lifecycle, Observer: &executorFakeObserver{}, SourceOwnership: executorSourceOwnership()})
	if !errors.Is(err, context.Canceled) || lifecycle.calls != 0 || identityForInstance(source) != before {
		t.Fatalf("cancelled switch launched or mutated source: err=%v calls=%d source=%#v", err, lifecycle.calls, source)
	}
}

func TestExecuteCrossHarnessSwitch_SuccessPersistsReadyTarget(t *testing.T) {
	cfg, source := seededCrossHarnessExecutorSource(t)
	before := identityForInstance(source)
	store := &executorFakeStore{targets: map[string]*Instance{}}
	lifecycle := &executorFakeLifecycle{}
	result, err := ExecuteCrossHarnessSwitch(context.Background(), cfg, source, CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}}, CrossHarnessSwitchDependencies{
		Store: store, Lifecycle: lifecycle, Observer: &executorFakeObserver{}, SourceOwnership: executorSourceOwnership(),
	})
	if err != nil || result == nil || !result.TargetCreated || !result.TargetStarted || !result.TargetReady || result.Pending || lifecycle.calls != 1 {
		t.Fatalf("successful executor result = %#v err=%v lifecycle=%d", result, err, lifecycle.calls)
	}
	if result.Target == nil || store.targets[result.Target.ID] != result.Target || identityForInstance(source) != before {
		t.Fatalf("ready target was not persisted distinctly or source changed: target=%#v stored=%#v source=%#v", result.Target, store.targets[result.Target.ID], source)
	}
	if lifecycle.plan == nil || lifecycle.plan.PayloadPath == "" || len(lifecycle.plan.Prompt) != 0 {
		t.Fatal("cross-harness launch must retain file-backed delivery, not an embedded prompt")
	}
	portablePayload, payloadErr := os.ReadFile(lifecycle.plan.PayloadPath)
	if payloadErr != nil || !strings.Contains(string(portablePayload), "seed context") || strings.Contains(string(portablePayload), `"sessionId"`) {
		t.Fatalf("file-backed handoff lost readable context or included raw metadata: %v", payloadErr)
	}
	journalPath, pathErr := crossHarnessJournalPath(lifecycle.plan)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	journal, journalErr := loadCrossHarnessJournal(journalPath)
	if journalErr != nil || journal == nil || journal.State != crossHarnessReady || journal.NativeSessionID != "fresh-codex-thread" {
		t.Fatalf("ready journal = %#v err=%v", journal, journalErr)
	}
}

func TestExecuteCrossHarnessSwitch_BindsExactPlanAndNeverReplaysUncertainLaunch(t *testing.T) {
	cfg, source := seededCrossHarnessExecutorSource(t)
	store := &executorFakeStore{targets: map[string]*Instance{}}
	lifecycle := &executorFakeLifecycle{err: errors.New("spawn outcome unknown")}
	observer := &executorFakeObserver{err: context.DeadlineExceeded}
	result, err := ExecuteCrossHarnessSwitch(context.Background(), cfg, source, CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}, Timeout: time.Millisecond}, CrossHarnessSwitchDependencies{Store: store, Lifecycle: lifecycle, Observer: observer, SourceOwnership: executorSourceOwnership()})
	if !errors.Is(err, ErrCrossHarnessPending) || result == nil || !result.Pending || lifecycle.calls != 1 || lifecycle.plan == nil || lifecycle.plan.TargetHome != cfg.GetProfileCodexConfigDir("work") || len(lifecycle.plan.NativeArgs) == 0 {
		t.Fatalf("uncertain launch did not retain immutable recovery state: result=%#v err=%v calls=%d plan=%#v", result, err, lifecycle.calls, lifecycle.plan)
	}
	_, err = ExecuteCrossHarnessSwitch(context.Background(), cfg, source, CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}, Timeout: time.Millisecond}, CrossHarnessSwitchDependencies{Store: store, Lifecycle: lifecycle, Observer: observer, SourceOwnership: executorSourceOwnership()})
	if !errors.Is(err, ErrCrossHarnessPending) || lifecycle.calls != 1 || observer.calls < 2 {
		t.Fatalf("retry replayed uncertain prompt delivery: err=%v lifecycle=%d observer=%d", err, lifecycle.calls, observer.calls)
	}
}

func TestExecuteCrossHarnessSwitch_RejectsWrongEvidenceAndConfigDrift(t *testing.T) {
	cfg, source := seededCrossHarnessExecutorSource(t)
	before := identityForInstance(source)
	store := &executorFakeStore{targets: map[string]*Instance{}}
	lifecycle := &executorFakeLifecycle{}
	wrong := &CrossHarnessTargetEvidence{InstanceID: "other-target", Tool: "codex", SessionID: "fresh-codex-thread", Ready: true}
	_, err := ExecuteCrossHarnessSwitch(context.Background(), cfg, source, CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}}, CrossHarnessSwitchDependencies{Store: store, Lifecycle: lifecycle, Observer: &executorFakeObserver{evidence: wrong}, SourceOwnership: executorSourceOwnership()})
	if err == nil || lifecycle.calls != 1 || identityForInstance(source) != before {
		t.Fatalf("wrong target/account identity evidence was accepted or source mutated: err=%v calls=%d source=%#v", err, lifecycle.calls, source)
	}
	// An immutable journal cannot be resumed with a different configured home.
	cfg.Profiles["work"] = ProfileSettings{Codex: ProfileCodexSettings{ConfigDir: filepath.Join(t.TempDir(), "different-home")}}
	_, err = ExecuteCrossHarnessSwitch(context.Background(), cfg, source, CrossHarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}}, CrossHarnessSwitchDependencies{Store: store, Lifecycle: lifecycle, Observer: &executorFakeObserver{}, SourceOwnership: executorSourceOwnership()})
	if err == nil || lifecycle.calls != 1 {
		t.Fatalf("config drift was allowed to re-resolve/replay target: err=%v calls=%d", err, lifecycle.calls)
	}
}
