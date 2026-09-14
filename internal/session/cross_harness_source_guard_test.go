package session

import (
	"context"
	"strings"
	"testing"
)

func TestCrossHarnessSourceGuard_ManagedAndDependentSourcesRefuseWithoutSideEffects(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*Instance) *SwitchSourceSnapshot
		code      string
	}{
		{
			name: "conductor",
			configure: func(source *Instance) *SwitchSourceSnapshot {
				source.IsConductor = true
				return nil // the authoritative fake reads the source row itself.
			},
			code: "managed-conductor",
		},
		{
			name: "watcher bridge target",
			configure: func(source *Instance) *SwitchSourceSnapshot {
				snapshot := SnapshotSwitchSource(source, []*Instance{source}, true, false)
				return &snapshot
			},
			code: "watcher-bridge-target",
		},
		{
			name: "dependent children",
			configure: func(source *Instance) *SwitchSourceSnapshot {
				child := &Instance{ID: "child", ParentSessionID: source.ID}
				snapshot := SnapshotSwitchSource(source, []*Instance{source, child}, false, false)
				return &snapshot
			},
			code: "dependent-children",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, source := seededCrossHarnessExecutorSource(t)
			before := identityForInstance(source)
			store := &executorFakeStore{targets: map[string]*Instance{}}
			lifecycle := &executorFakeLifecycle{}
			snapshot := tc.configure(source)
			_, err := ExecuteCrossHarnessSwitch(context.Background(), cfg, source, CrossHarnessSwitchOptions{
				Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}, SourceSnapshot: snapshot,
			}, CrossHarnessSwitchDependencies{Store: store, Lifecycle: lifecycle, Observer: &executorFakeObserver{}, SourceOwnership: &executorFakeSourceOwnership{snapshot: snapshot}})
			if err == nil || !strings.Contains(err.Error(), tc.code) {
				t.Fatalf("guard error = %v, want refusal %q", err, tc.code)
			}
			if len(store.targets) != 0 || lifecycle.calls != 0 || identityForInstance(source) != before {
				t.Fatalf("guard had side effects: targets=%#v lifecycle=%d source=%#v", store.targets, lifecycle.calls, source)
			}
		})
	}
}

func TestCrossHarnessSourceGuard_OrdinaryChildTransfersAndPreservesParent(t *testing.T) {
	cfg, source := seededCrossHarnessExecutorSource(t)
	source.ParentSessionID = "ordinary-parent"
	snapshot := SnapshotSwitchSource(source, []*Instance{source}, false, false)
	store := &executorFakeStore{targets: map[string]*Instance{}}
	result, err := ExecuteCrossHarnessSwitch(context.Background(), cfg, source, CrossHarnessSwitchOptions{
		Target: SwitchPreviewTarget{Harness: "codex", Account: "work"}, SourceSnapshot: &snapshot,
	}, CrossHarnessSwitchDependencies{Store: store, Lifecycle: &executorFakeLifecycle{}, Observer: &executorFakeObserver{}, SourceOwnership: &executorFakeSourceOwnership{snapshot: &snapshot}})
	if err != nil || result == nil || result.Target == nil {
		t.Fatalf("ordinary child transfer = %#v, %v", result, err)
	}
	if result.Target.ParentSessionID != source.ParentSessionID {
		t.Fatalf("target parent = %q, want preserved %q", result.Target.ParentSessionID, source.ParentSessionID)
	}
}

func TestCrossHarnessSourceGuard_RevalidatesBeforeStagingAndFinalCommit(t *testing.T) {
	benign := SwitchSourceSnapshot{}
	dependent := SwitchSourceSnapshot{HasChildren: true}
	for _, tc := range []struct {
		name          string
		states        []SwitchSourceSnapshot
		wantLifecycle int
		wantTargets   int
	}{
		{
			name: "dependent added before staging", states: []SwitchSourceSnapshot{benign, dependent},
		},
		{
			name: "dependent added before final commit", states: []SwitchSourceSnapshot{benign, benign, dependent},
			wantLifecycle: 1, wantTargets: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, source := seededCrossHarnessExecutorSource(t)
			store := &executorFakeStore{targets: map[string]*Instance{}}
			lifecycle := &executorFakeLifecycle{}
			calls := 0
			authority := CrossHarnessSourceOwnershipValidatorFunc(func(*Instance) (SwitchSourceSnapshot, error) {
				if calls >= len(tc.states) {
					t.Fatalf("unexpected ownership validation call %d", calls+1)
				}
				state := tc.states[calls]
				calls++
				return state, nil
			})
			result, err := ExecuteCrossHarnessSwitch(context.Background(), cfg, source, CrossHarnessSwitchOptions{
				Target: SwitchPreviewTarget{Harness: "codex", Account: "work"},
			}, CrossHarnessSwitchDependencies{Store: store, Lifecycle: lifecycle, Observer: &executorFakeObserver{}, SourceOwnership: authority})
			if err == nil || !strings.Contains(err.Error(), "dependent-children") || lifecycle.calls != tc.wantLifecycle || len(store.targets) != tc.wantTargets || source.IsArchived() {
				t.Fatalf("stale ownership state was not refused safely: result=%#v err=%v lifecycle=%d targets=%#v source=%#v", result, err, lifecycle.calls, store.targets, source)
			}
			if tc.wantTargets != 0 && (result == nil || result.Target == nil || !result.Target.IsArchived() || result.Target.Supersedes != source.ID) {
				t.Fatalf("final ownership conflict did not retain a recoverable original/target: result=%#v source=%#v", result, source)
			}
		})
	}
}

func TestCrossHarnessSourceGuard_NativeSameHarnessIsUnaffected(t *testing.T) {
	cfg, source := seededCrossHarnessExecutorSource(t)
	source.IsConductor = true
	child := &Instance{ID: "child", ParentSessionID: source.ID}
	snapshot := SnapshotSwitchSource(source, []*Instance{source, child}, true, false)
	preview := PreviewSwitchWithMaxBytesAndSnapshot(cfg, source, SwitchPreviewTarget{Harness: "claude", Account: "personal"}, DefaultHandoffMaxChars, &snapshot)
	if preview.Refusal != nil || preview.Capability != CapabilityNativeResume {
		t.Fatalf("native same-harness preview was widened by cross-harness guard: %#v", preview)
	}
}
