package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNativeCrossHarnessObserver_PiFilesystemFixture verifies the production
// Pi adapter against a synthetic native JSONL document. It does not use the
// real HOME, a process, or tmux; the process check is injected separately.
func TestNativeCrossHarnessObserver_PiFilesystemFixture(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := &Instance{ID: "fixture-pi-target", Tool: "pi"}
	path := filepath.Join(home, ".pi", "agent-deck", target.ID, "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"session","version":3,"id":"pi-native"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	target.LastStartedAt = now
	artifact, err := readCrossHarnessNativeArtifact(target, FreshTargetIdentity{InstanceID: target.ID, Tool: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.SessionID != "pi-native" || !artifact.Ready || artifact.Path != path {
		t.Fatalf("Pi filesystem evidence = %#v", artifact)
	}
}

func TestNativeCrossHarnessObserver_PiSnapshotCorrelatesTimestampedArtifact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "project")
	target := &Instance{ID: "snapshot-pi-target", Tool: "pi", ProjectPath: project}
	dir := filepath.Join(home, ".pi", "agent-deck", target.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 10, 19, 18, 0, 0, time.UTC)
	current := now
	observer := &NativeCrossHarnessTargetObserver{
		Reader: CrossHarnessTargetEvidenceReader{
			ProcessAlive:        func(*Instance) (bool, error) { return true, nil },
			SnapshotPiDirectory: snapshotPiSessionDirectory,
		},
		PollInterval: time.Millisecond,
		Now:          func() time.Time { return current },
	}
	baseline := observer.PrepareTargetObservation(target, FreshTargetIdentity{InstanceID: target.ID, Tool: "pi"})
	if baseline.PiDirectorySnapshot == nil || len(baseline.PiDirectorySnapshot) != 0 {
		t.Fatalf("unexpected Pi prelaunch snapshot: %#v", baseline)
	}
	path := filepath.Join(dir, "2026-01-02T03-04-05-006Z_01999999-9999-7999-8999-999999999901.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session","version":3,"id":"pi-fresh","cwd":"`+project+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now.Add(time.Second), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	current = now.Add(2 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	evidence, err := observer.ObserveTarget(ctx, target, FreshTargetIdentity{InstanceID: target.ID, Tool: "pi"})
	if err != nil || !evidence.Ready || evidence.ArtifactPath != path || evidence.SessionID != "pi-fresh" {
		t.Fatalf("timestamped Pi readiness = %#v, %v", evidence, err)
	}
}

func TestNativeCrossHarnessObserver_EvidenceGatesArePerTool(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	for _, harness := range []string{"claude", "codex", "pi"} {
		t.Run(harness, func(t *testing.T) {
			target := &Instance{ID: "target-" + harness, Tool: harness, LastStartedAt: now}
			expected := FreshTargetIdentity{InstanceID: target.ID, Tool: harness, SessionID: "native-target"}
			reader := CrossHarnessTargetEvidenceReader{
				ProcessAlive: func(*Instance) (bool, error) { return false, nil },
				ReadHookStatus: func(string) (*HookStatus, time.Time, error) {
					return &HookStatus{Status: "waiting", SessionID: "native-target", Event: "SessionStart", UpdatedAt: now}, now, nil
				},
				ReadNativeArtifact: func(*Instance, FreshTargetIdentity) (CrossHarnessNativeArtifact, error) {
					return CrossHarnessNativeArtifact{Path: "stale-or-wrong.jsonl", SessionID: "native-target", ModifiedAt: now.Add(-time.Minute), Ready: true}, nil
				},
			}
			if _, ready := observeCrossHarnessEvidence(now, target, expected, now, reader); ready {
				t.Fatal("dead process or stale artifact evidence was accepted")
			}
		})
	}
}
