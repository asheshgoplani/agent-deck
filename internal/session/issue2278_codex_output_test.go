package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func isolateCodexSubmissionMarkers(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	previousRoot := agentDeckDirOverride
	agentDeckDirOverride = root
	t.Cleanup(func() { agentDeckDirOverride = previousRoot })
	return root
}

func TestCodexSubmissionMarkerIsPrivateAtomicAndContentFree(t *testing.T) {
	isolateCodexSubmissionMarkers(t)
	marker, err := PrepareCodexSubmissionMarker(
		"instance-private", "thread-private", "thread-private:turn-old",
		time.Date(2026, 9, 14, 15, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	path, err := codexSubmissionMarkerPath("thread-private")
	if err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("marker directory mode = %o, want 700", got)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("marker mode = %o, want 600", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{
		"version", "instance_id", "codex_session_id", "attempt_id",
		"prior_turn_generation", "phase", "created_at", "updated_at",
	}
	if len(fields) != len(wantFields) {
		t.Fatalf("marker fields = %v, want only %v", fields, wantFields)
	}
	for _, key := range wantFields {
		if _, ok := fields[key]; !ok {
			t.Fatalf("marker missing %q: %s", key, raw)
		}
	}
	if strings.Contains(string(raw), "prompt") || strings.Contains(string(raw), "response") || marker.AttemptID == "" {
		t.Fatalf("marker is not content-free or lacks attempt identity: %s", raw)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("atomic write left unexpected entries: %v", entries)
	}
}

func TestCodexSubmissionMarkerReconciliationFailsClosedThenClearsOnGeneration(t *testing.T) {
	isolateCodexSubmissionMarkers(t)
	marker, err := PrepareCodexSubmissionMarker(
		"instance-reconcile", "thread-reconcile", "thread-reconcile:turn-old", time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	path, err := codexSubmissionMarkerPath(marker.CodexSessionID)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := ReconcileCodexSubmissionMarker(
		marker.InstanceID, marker.CodexSessionID, marker.PriorTurnGeneration,
	)
	if err == nil || accepted != "" || !strings.Contains(err.Error(), path) ||
		!strings.Contains(err.Error(), "manually remove") {
		t.Fatalf("unchanged generation did not wedge with recovery path: accepted=%q err=%v", accepted, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("unresolved marker was removed: %v", statErr)
	}
	accepted, err = ReconcileCodexSubmissionMarker(
		marker.InstanceID, marker.CodexSessionID, "thread-reconcile:turn-new",
	)
	if err != nil || accepted != "thread-reconcile:turn-new" {
		t.Fatalf("new generation was not reconciled: accepted=%q err=%v", accepted, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("reconciled marker still exists: %v", statErr)
	}
}

func TestCodexSubmissionMarkerRejectsMalformedAndWrongOwner(t *testing.T) {
	isolateCodexSubmissionMarkers(t)
	path, err := codexSubmissionMarkerPath("thread-malformed")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"codex_session_id":"thread-malformed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileCodexSubmissionMarker("instance-malformed", "thread-malformed", ""); err == nil {
		t.Fatal("malformed marker was accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("malformed marker was removed: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	marker, err := PrepareCodexSubmissionMarker("instance-owner", "thread-malformed", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileCodexSubmissionMarker("different-instance", marker.CodexSessionID, ""); err == nil {
		t.Fatal("marker owned by another instance was accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("wrong-owner marker was removed: %v", err)
	}
}

func TestCodexSubmissionMarkerRejectsUnsafeModesAndPaths(t *testing.T) {
	root := isolateCodexSubmissionMarkers(t)
	dir := filepath.Join(root, "locks", "codex-submissions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareCodexSubmissionMarker("instance-mode", "thread-mode", "", time.Now()); err == nil {
		t.Fatal("group-readable marker directory was accepted")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker, err := PrepareCodexSubmissionMarker("instance-mode", "thread-mode", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path, err := codexSubmissionMarkerPath(marker.CodexSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readCodexSubmissionMarker(marker.CodexSessionID); err == nil {
		t.Fatal("group-readable marker file was accepted")
	}
	if _, err := codexSubmissionMarkerPath("../unsafe"); err == nil {
		t.Fatal("unsafe session identity produced a marker path")
	}
}

func TestCodexSubmissionMarkerClearsOnlyForDefinitiveOutcome(t *testing.T) {
	isolateCodexSubmissionMarkers(t)
	marker, err := PrepareCodexSubmissionMarker("instance-clear", "thread-clear", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path, err := codexSubmissionMarkerPath(marker.CodexSessionID)
	if err != nil {
		t.Fatal(err)
	}
	wrong := *marker
	wrong.AttemptID = "00000000000000000000000000000000"
	if err := ClearCodexSubmissionMarker(&wrong); err == nil {
		t.Fatal("mismatched attempt cleared another owner's marker")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("mismatched clear removed marker: %v", err)
	}
	if err := marker.MarkTransportAmbiguous(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ambiguous transport cleared marker: %v", err)
	}
	if err := ClearCodexSubmissionMarker(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("definitive clear retained marker: %v", err)
	}
}

func TestCodexSubmissionMarkerSurvivesProcessExitBeforeAndAfterSubmittedEvidence(t *testing.T) {
	const helper = "AGENT_DECK_CODEX_MARKER_CRASH_HELPER"
	if phase := os.Getenv(helper); phase != "" {
		agentDeckDirOverride = os.Getenv("AGENT_DECK_CODEX_MARKER_ROOT")
		lock, err := AcquireCodexAcceptanceLock("thread-crash", time.Second)
		if err != nil {
			os.Exit(2)
		}
		_ = lock // Deliberately leaked: process exit must release the flock.
		marker, err := PrepareCodexSubmissionMarker("instance-crash", "thread-crash", "", time.Now())
		if err != nil {
			os.Exit(3)
		}
		if phase == CodexSubmissionPhaseSubmitted {
			if err := marker.MarkSubmitted(time.Now()); err != nil {
				os.Exit(4)
			}
		}
		os.Exit(0)
	}

	for _, phase := range []string{CodexSubmissionPhasePrepared, CodexSubmissionPhaseSubmitted} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestCodexSubmissionMarkerSurvivesProcessExitBeforeAndAfterSubmittedEvidence$")
			cmd.Env = append(os.Environ(), helper+"="+phase, "AGENT_DECK_CODEX_MARKER_ROOT="+root)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("crash helper: %v\n%s", err, out)
			}
			previousRoot := agentDeckDirOverride
			agentDeckDirOverride = root
			marker, found, err := readCodexSubmissionMarker("thread-crash")
			if err != nil || !found || marker.Phase != phase {
				agentDeckDirOverride = previousRoot
				t.Fatalf("durable marker after exit = (%#v, %t, %v), want phase %q", marker, found, err, phase)
			}
			lock, err := AcquireCodexAcceptanceLock("thread-crash", time.Second)
			if err != nil {
				agentDeckDirOverride = previousRoot
				t.Fatalf("process exit did not release acceptance flock: %v", err)
			}
			lock.Release()
			agentDeckDirOverride = previousRoot
		})
	}
}

func TestCodexRolloutIsResolvableLocally(t *testing.T) {
	tests := []struct {
		name string
		inst *Instance
		want bool
	}{
		{name: "local", inst: &Instance{}, want: true},
		{name: "SSH", inst: &Instance{SSHHost: "remote"}, want: false},
		{name: "sandbox", inst: &Instance{Sandbox: &SandboxConfig{Enabled: true}}, want: false},
		{name: "nil", inst: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.inst.CodexRolloutIsResolvableLocally(); got != tt.want {
				t.Fatalf("CodexRolloutIsResolvableLocally() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestCodexOutputCarriesExactTurnForRepeatedReply(t *testing.T) {
	rollout := strings.Join([]string{
		`{"timestamp":"2026-09-14T14:59:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-old"}}`,
		`{"timestamp":"2026-09-14T15:00:00Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-old","last_agent_message":"OK"}}`,
		`{"timestamp":"2026-09-14T15:00:30Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-new"}}`,
		`{"timestamp":"2026-09-14T15:01:00Z","type":"event_msg","payload":{"type":"agent_message","message":"working","phase":"commentary"}}`,
		`{"timestamp":"2026-09-14T15:02:00Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-new","last_agent_message":"OK"}}`,
	}, "\n")
	got, err := parseCodexLastAssistantMessage(strings.Split(rollout, "\n"), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "OK" || got.CodexTurnGeneration != "thread-1:turn-new" {
		t.Fatalf("wrong correlated response: %#v", got)
	}
}

func TestCodexAcceptanceLockSerializesAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	previousRoot := agentDeckDirOverride
	agentDeckDirOverride = filepath.Join(root, "agent-deck")
	t.Cleanup(func() { agentDeckDirOverride = previousRoot })
	lock, err := AcquireCodexAcceptanceLock("thread-cross-process", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	lockPath, err := codexAcceptanceLockPath("thread-cross-process")
	if err != nil {
		t.Fatal(err)
	}

	runHelper := func(expect string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestCodexAcceptanceLockHelper$")
		cmd.Env = append(
			os.Environ(),
			"AGENT_DECK_CODEX_LOCK_HELPER="+expect,
			"AGENT_DECK_CODEX_LOCK_PATH="+lockPath,
			"AGENT_DECK_CODEX_LOCK_ROOT="+agentDeckDirOverride,
		)
		if output, runErr := cmd.CombinedOutput(); runErr != nil {
			t.Fatalf("helper %s: %v\n%s", expect, runErr, output)
		}
	}
	runHelper("blocked")
	lock.Release()
	runHelper("acquired")
}

func TestCodexAcceptanceLockHelper(t *testing.T) {
	expect := os.Getenv("AGENT_DECK_CODEX_LOCK_HELPER")
	if expect == "" {
		return
	}
	agentDeckDirOverride = os.Getenv("AGENT_DECK_CODEX_LOCK_ROOT")
	lock, err := AcquireCodexAcceptanceLock("thread-cross-process", 100*time.Millisecond)
	lockPath, pathErr := codexAcceptanceLockPath("thread-cross-process")
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if lockPath != os.Getenv("AGENT_DECK_CODEX_LOCK_PATH") {
		t.Fatalf("helper resolved a different lock path: got %q want %q", lockPath, os.Getenv("AGENT_DECK_CODEX_LOCK_PATH"))
	}
	if expect == "blocked" {
		if err == nil {
			lock.Release()
			t.Fatal("cross-process contender acquired a held acceptance lock")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("blocked contender failed for the wrong reason: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("accept after release: %v", err)
	}
	lock.Release()
}
