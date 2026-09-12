package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

// These are synthetic observer fixtures only. They do not start a harness,
// inspect the real home, use tmux, or read a production transcript.
func TestNativeCrossHarnessObserver_CorrelatesClaudeCodexAndPiEvidence(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		harness  string
		session  string
		event    string
		artifact CrossHarnessNativeArtifact
		wantKind string
	}{
		{name: "claude hook plus transcript", harness: "claude", session: "11111111-1111-4111-8111-111111111111", event: "SessionStart", artifact: CrossHarnessNativeArtifact{Path: "fixture-claude.jsonl", SessionID: "11111111-1111-4111-8111-111111111111", ModifiedAt: now.Add(time.Second), Ready: true}, wantKind: "claude-native-hook"},
		{name: "codex notify", harness: "codex", session: "codex-thread-1", event: "thread.started", wantKind: "codex-native-notify"},
		{name: "pi native session", harness: "pi", session: "22222222-2222-4222-8222-222222222222", artifact: CrossHarnessNativeArtifact{Path: "fixture-pi/session.jsonl", SessionID: "22222222-2222-4222-8222-222222222222", ModifiedAt: now.Add(time.Second), Ready: true}, wantKind: "pi-native-session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := &Instance{ID: "target-" + tc.harness, Tool: tc.harness, LastStartedAt: now}
			expected := FreshTargetIdentity{InstanceID: target.ID, Tool: tc.harness, SessionID: tc.session}
			reader := CrossHarnessTargetEvidenceReader{
				ProcessAlive: func(*Instance) (bool, error) { return true, nil },
				ReadHookStatus: func(string) (*HookStatus, time.Time, error) {
					fresh := now.Add(time.Second)
					return &HookStatus{Status: "waiting", SessionID: tc.session, Event: tc.event, UpdatedAt: fresh}, fresh, nil
				},
				ReadNativeArtifact: func(*Instance, FreshTargetIdentity) (CrossHarnessNativeArtifact, error) {
					return tc.artifact, nil
				},
			}
			got, ready := observeCrossHarnessEvidence(now, target, expected, now, reader)
			if !ready || !got.Ready || got.InstanceID != target.ID || got.Tool != tc.harness || got.SessionID != tc.session || got.Evidence != tc.wantKind {
				t.Fatalf("observer evidence = %#v, ready=%v", got, ready)
			}
		})
	}
}

func TestNativeCrossHarnessObserver_RejectsStaleWrongInstanceAndArtifact(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	target := &Instance{ID: "target-claude", Tool: "claude", LastStartedAt: now}
	expected := FreshTargetIdentity{InstanceID: target.ID, Tool: "claude", SessionID: "11111111-1111-4111-8111-111111111111"}
	base := CrossHarnessTargetEvidenceReader{
		ProcessAlive: func(*Instance) (bool, error) { return true, nil },
		ReadHookStatus: func(string) (*HookStatus, time.Time, error) {
			return &HookStatus{Status: "waiting", SessionID: expected.SessionID, Event: "SessionStart", UpdatedAt: now}, now, nil
		},
		ReadNativeArtifact: func(*Instance, FreshTargetIdentity) (CrossHarnessNativeArtifact, error) {
			return CrossHarnessNativeArtifact{Path: "old.jsonl", SessionID: expected.SessionID, ModifiedAt: now.Add(-time.Second), Ready: true}, nil
		},
	}
	if _, ready := observeCrossHarnessEvidence(now, target, expected, now, base); ready {
		t.Fatal("stale artifact was accepted")
	}
	equalTimestamp := base
	equalTimestamp.ReadNativeArtifact = func(*Instance, FreshTargetIdentity) (CrossHarnessNativeArtifact, error) {
		return CrossHarnessNativeArtifact{Path: "equal.jsonl", SessionID: expected.SessionID, ModifiedAt: now, Ready: true}, nil
	}
	if _, ready := observeCrossHarnessEvidence(now, target, expected, now, equalTimestamp); ready {
		t.Fatal("artifact and hook evidence at the launch timestamp was accepted")
	}
	wrong := expected
	wrong.InstanceID = "other-target"
	if _, ready := observeCrossHarnessEvidence(now, target, wrong, now, base); ready {
		t.Fatal("wrong target instance was accepted")
	}
	wrongArtifact := base
	wrongArtifact.ReadNativeArtifact = func(*Instance, FreshTargetIdentity) (CrossHarnessNativeArtifact, error) {
		return CrossHarnessNativeArtifact{Path: "wrong.jsonl", SessionID: "different", ModifiedAt: now, Ready: true}, nil
	}
	if _, ready := observeCrossHarnessEvidence(now, target, expected, now, wrongArtifact); ready {
		t.Fatal("wrong native artifact identity was accepted")
	}
}

func TestNativeCrossHarnessObserver_PiBaselineSurvivesObserverRecreation(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	target := &Instance{ID: "target-pi", Tool: "pi", LastStartedAt: now}
	artifact := CrossHarnessNativeArtifact{Path: "fixture-pi/session.jsonl", SessionID: "22222222-2222-4222-8222-222222222222", ModifiedAt: now.Add(-time.Second), Ready: true}
	expected := FreshTargetIdentity{InstanceID: target.ID, Tool: "pi", NativeSessionPath: artifact.Path}
	reader := CrossHarnessTargetEvidenceReader{
		ProcessAlive:       func(*Instance) (bool, error) { return true, nil },
		ReadNativeArtifact: func(*Instance, FreshTargetIdentity) (CrossHarnessNativeArtifact, error) { return artifact, nil },
	}
	first := &NativeCrossHarnessTargetObserver{Reader: reader, Now: func() time.Time { return now }, PollInterval: time.Millisecond}
	baseline := first.PrepareTargetObservation(target, expected)
	// Recreate the observer as recovery does. A same-ID, newly touched file is
	// still the pre-launch incarnation and must not complete an uncertain start.
	artifact.ModifiedAt = now.Add(time.Second)
	second := &NativeCrossHarnessTargetObserver{Reader: reader, Now: func() time.Time { return now.Add(time.Second) }, PollInterval: time.Millisecond}
	second.RestoreTargetObservation(target, baseline)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Millisecond)
	defer cancel()
	if got, err := second.ObserveTarget(ctx, target, expected); got.Ready || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recreated observer accepted prior Pi identity: evidence=%#v err=%v", got, err)
	}
	artifact.SessionID = "33333333-3333-4333-8333-333333333333"
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	got, err := second.ObserveTarget(ctx, target, expected)
	if err != nil || !got.Ready || got.SessionID != artifact.SessionID {
		t.Fatalf("recreated observer did not accept fresh Pi identity: evidence=%#v err=%v", got, err)
	}
}

// An absent pre-launch Pi artifact has no native launch token or immutable
// session ID to survive recovery. Even a later valid-looking artifact at the
// operation-scoped path must remain pending rather than be attributed by mtime.
func TestNativeCrossHarnessObserver_PiAbsentBaselineRejectsLaterArtifactAfterRecreation(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	target := &Instance{ID: "target-pi-absent", Tool: "pi", LastStartedAt: now}
	expected := FreshTargetIdentity{InstanceID: target.ID, Tool: "pi", NativeSessionPath: "fixture-pi-absent/session.jsonl"}
	artifact := CrossHarnessNativeArtifact{}
	reader := CrossHarnessTargetEvidenceReader{
		ProcessAlive:       func(*Instance) (bool, error) { return true, nil },
		ReadNativeArtifact: func(*Instance, FreshTargetIdentity) (CrossHarnessNativeArtifact, error) { return artifact, nil },
	}
	first := &NativeCrossHarnessTargetObserver{Reader: reader, Now: func() time.Time { return now }, PollInterval: time.Millisecond}
	baseline := first.PrepareTargetObservation(target, expected)
	if baseline.Artifact.Path != "" {
		t.Fatalf("absent Pi baseline unexpectedly has artifact %#v", baseline.Artifact)
	}

	// This models an unrelated later file appearing after an uncertain launch.
	artifact = CrossHarnessNativeArtifact{Path: expected.NativeSessionPath, SessionID: "44444444-4444-4444-8444-444444444444", ModifiedAt: now.Add(time.Second), Ready: true}
	second := &NativeCrossHarnessTargetObserver{Reader: reader, Now: func() time.Time { return now.Add(time.Second) }, PollInterval: time.Millisecond}
	second.RestoreTargetObservation(target, baseline)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Millisecond)
	defer cancel()
	got, err := second.ObserveTarget(ctx, target, expected)
	if got.Ready || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("later Pi artifact was attributed without durable correlation: evidence=%#v err=%v", got, err)
	}
}

func TestNativeCrossHarnessTargetObserver_TimeoutAndCancellationRemainUnready(t *testing.T) {
	target := &Instance{ID: "target-codex", Tool: "codex", LastStartedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
	expected := FreshTargetIdentity{InstanceID: target.ID, Tool: "codex", SessionID: "stable-agent-deck-target-id"}
	observer := &NativeCrossHarnessTargetObserver{
		Reader: CrossHarnessTargetEvidenceReader{
			ProcessAlive:   func(*Instance) (bool, error) { return false, nil },
			ReadHookStatus: func(string) (*HookStatus, time.Time, error) { return nil, time.Time{}, nil },
		},
		PollInterval: time.Millisecond,
		Now:          func() time.Time { return target.LastStartedAt },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Millisecond)
	defer cancel()
	got, err := observer.ObserveTarget(ctx, target, expected)
	if got.Ready || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout result = %#v, err=%v", got, err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	got, err = observer.ObserveTarget(cancelled, target, expected)
	if got.Ready || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result = %#v, err=%v", got, err)
	}
}
