package session

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The remote switch family is forwarded to the remote deck, which runs the
// same local switch engine there. These tests pin the exact argv the runner
// sends (no controller path or credential ever travels), how a refusal that
// the remote reports on stdout with exit status 1 becomes a typed preview,
// and how a switch result is decoded.

func TestSSHRunnerSwitchPreview_ForwardsTargetAndParsesPreview(t *testing.T) {
	var calls [][]string
	runner := &SSHRunner{runFn: func(ctx context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte(`{"source_title":"task","source_tool":"claude","source_account":"","source_account_dir":"/home/u/.claude","source_account_status":"configured","source_session_id":"abc","source_project_path":"/home/u/p","source_is_remote":false,"target_harness":"claude","target_account":"work","target_account_dir":"/home/u/.claude-work","target_account_status":"configured","capability":"native-resume","execution":"supported","fidelity_inclusions":["exact conversation file"],"fidelity_exclusions":null,"warnings":["exact conversation artifact not found"],"transcript_budget_bytes":24000}` + "\n"), nil
	}}
	preview, err := runner.SwitchPreview(context.Background(), "task-id", "claude", "work")
	if err != nil {
		t.Fatalf("SwitchPreview: %v", err)
	}
	want := "session switch-preview task-id --to-harness claude --to-account work --json"
	if len(calls) != 1 || strings.Join(calls[0], " ") != want {
		t.Fatalf("forwarded args = %v, want %q", calls, want)
	}
	if preview.Refusal != nil {
		t.Fatalf("unexpected refusal %+v", preview.Refusal)
	}
	if preview.Capability != string(CapabilityNativeResume) || preview.Execution != string(ExecutionSupported) {
		t.Fatalf("capability/execution = %q/%q", preview.Capability, preview.Execution)
	}
	if preview.TargetAccountStatus != string(AccountStatusConfigured) || preview.SourceTool != "claude" {
		t.Fatalf("preview = %+v", preview)
	}
	if len(preview.Warnings) != 1 || preview.Warnings[0] != "exact conversation artifact not found" {
		t.Fatalf("warnings = %v", preview.Warnings)
	}
}

// An empty target account is the target default and must not be sent as an
// empty --to-account token; an empty harness keeps the source harness.
func TestSSHRunnerSwitchPreview_OmitsEmptyTarget(t *testing.T) {
	var got []string
	runner := &SSHRunner{runFn: func(ctx context.Context, args ...string) ([]byte, error) {
		got = append([]string(nil), args...)
		return []byte(`{"capability":"native-resume","execution":"supported"}`), nil
	}}
	if _, err := runner.SwitchPreview(context.Background(), "task-id", "", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "session switch-preview task-id --json" {
		t.Fatalf("forwarded args = %v", got)
	}
}

// switch-preview exits 1 with the JSON still on stdout when it refuses. The
// refusal is the answer (unknown account on the remote, managed conductor,
// missing conversation id), not a transport failure.
func TestSSHRunnerSwitchPreview_RefusalIsReportedNotErrored(t *testing.T) {
	runner := &SSHRunner{runFn: func(ctx context.Context, args ...string) ([]byte, error) {
		return []byte(`{"source_title":"task","source_tool":"claude","target_harness":"claude","target_account":"nope","target_account_status":"unknown","capability":"native-resume","execution":"supported","refusal":{"code":"unknown-account","message":"account \"nope\" has no [profiles.nope.claude].config_dir in config.toml (configured accounts: personal, work)"}}` + "\n"),
			errors.New("ssh command failed: exit status 1")
	}}
	preview, err := runner.SwitchPreview(context.Background(), "task-id", "claude", "nope")
	if err != nil {
		t.Fatalf("a refusal must decode, got error %v", err)
	}
	if preview.Refusal == nil || preview.Refusal.Code != "unknown-account" {
		t.Fatalf("refusal = %+v", preview.Refusal)
	}
	if !strings.Contains(preview.Refusal.Message, "configured accounts: personal, work") {
		t.Fatalf("refusal message must carry the remote's configured slots: %q", preview.Refusal.Message)
	}
}

// A remote too old for switch-preview, an unreachable host or a session the
// remote does not know produce no JSON: that is an error, never a preview.
func TestSSHRunnerSwitchPreview_NonJSONIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		err    error
	}{
		{"old remote", "Unknown session command: switch-preview\n", errors.New("ssh command failed: exit status 1")},
		{"transport", "", errors.New("ssh: connect to host lab port 22: Connection refused")},
		{"empty ok", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &SSHRunner{runFn: func(ctx context.Context, args ...string) ([]byte, error) {
				return []byte(tc.output), tc.err
			}}
			preview, err := runner.SwitchPreview(context.Background(), "task-id", "claude", "work")
			if err == nil || preview != nil {
				t.Fatalf("preview=%v err=%v; want nil preview and an error", preview, err)
			}
		})
	}
}

func TestSSHRunnerSwitchSession_ForwardsConfirmationAndParsesResult(t *testing.T) {
	var got []string
	runner := &SSHRunner{runFn: func(ctx context.Context, args ...string) ([]byte, error) {
		got = append([]string(nil), args...)
		return []byte(`{"success":true,"status":"success","pending":false,"operation_id":"op-1","target_id":"new-id","target_ready":true,"loss_disclosure":["native session state"]}` + "\n"), nil
	}}
	result, err := runner.SwitchSession(context.Background(), "task-id", "codex", "", true)
	if err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	want := "session switch task-id --to-harness codex --confirm-context-loss --json"
	if strings.Join(got, " ") != want {
		t.Fatalf("forwarded args = %v, want %q", got, want)
	}
	if !result.Success || result.Status != "success" || result.TargetID != "new-id" || !result.TargetReady {
		t.Fatalf("result = %+v", result)
	}
}

func TestSSHRunnerSwitchSession_NativeResultWithoutConfirmation(t *testing.T) {
	var got []string
	runner := &SSHRunner{runFn: func(ctx context.Context, args ...string) ([]byte, error) {
		got = append([]string(nil), args...)
		return []byte(`{"success":true,"status":"success","pending":false,"id":"task-id","title":"task","old_tool":"claude","new_tool":"claude","old_account":"","new_account":"work","destination_ready":true,"restarted":true}`), nil
	}}
	result, err := runner.SwitchSession(context.Background(), "task-id", "claude", "work", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "session switch task-id --to-harness claude --to-account work --json" {
		t.Fatalf("forwarded args = %v", got)
	}
	if result.NewAccount != "work" || !result.DestinationReady || !result.Restarted {
		t.Fatalf("result = %+v", result)
	}
}

// A failed switch reports its JSON error body (status, recovery flag and the
// message) instead of a bare exit status, so the TUI can say what happened
// on the remote and whether recovery is required there.
func TestSSHRunnerSwitchSession_FailureCarriesRemoteDiagnosis(t *testing.T) {
	runner := &SSHRunner{runFn: func(ctx context.Context, args ...string) ([]byte, error) {
		return []byte(`{"success":false,"error":"switch failed: target codex binary not found","code":"INVALID_OPERATION","status":"failed","pending":false,"recovery_required":true,"target_created":true}`),
			errors.New("ssh command failed: exit status 1")
	}}
	result, err := runner.SwitchSession(context.Background(), "task-id", "codex", "", true)
	if err == nil {
		t.Fatal("a failed switch must return an error")
	}
	if !strings.Contains(err.Error(), "target codex binary not found") {
		t.Fatalf("error must carry the remote message: %v", err)
	}
	if result == nil || result.Status != "failed" || !result.RecoveryRequired {
		t.Fatalf("result = %+v; the decoded failure body must accompany the error", result)
	}
}

// A pending switch (durable account/target write, native readiness not yet
// observed) exits 0 on the remote and is a result, not an error.
func TestSSHRunnerSwitchSession_PendingIsAResult(t *testing.T) {
	runner := &SSHRunner{runFn: func(ctx context.Context, args ...string) ([]byte, error) {
		return []byte(`{"success":false,"status":"pending","pending":true,"target_id":"new-id","target_ready":false,"missing_contract":"native identity"}`), nil
	}}
	result, err := runner.SwitchSession(context.Background(), "task-id", "codex", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pending || result.Status != "pending" || result.MissingContract != "native identity" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSSHRunnerSwitchSession_NonJSONIsAnError(t *testing.T) {
	runner := &SSHRunner{runFn: func(ctx context.Context, args ...string) ([]byte, error) {
		return []byte("Unknown session command: switch\n"), errors.New("ssh command failed: exit status 1")
	}}
	result, err := runner.SwitchSession(context.Background(), "task-id", "claude", "work", false)
	if err == nil || result != nil {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

// The remote's `list --json` carries the stored account slot; the row needs
// it so the Edit Session dialog can show the current slot and the switch
// confirmation can say "from → to" the way it does locally.
func TestParseRemoteSessions_CarriesAccount(t *testing.T) {
	sessions, err := parseRemoteSessions([]byte(`[{"id":"a","title":"t","tool":"claude","account":"work","status":"idle"},{"id":"b","title":"u","tool":"codex","status":"idle"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0].Account != "work" || sessions[1].Account != "" {
		t.Fatalf("sessions = %+v", sessions)
	}
}

// Account slots for the Edit Session dialog on a remote row come from the
// remote's own `accounts`; Codex slots need the harness filter, Claude keeps
// the original form so a 1.16.x remote still answers.
func TestSSHRunnerFetchAccountsForHarness(t *testing.T) {
	var got [][]string
	runner := &SSHRunner{runFn: func(ctx context.Context, args ...string) ([]byte, error) {
		got = append(got, append([]string(nil), args...))
		return []byte(`[{"name":"work","config_dir":"/x","exists":true}]`), nil
	}}
	for _, tc := range []struct{ harness, want string }{
		{"claude", "accounts --json"},
		{"codex", "accounts --harness codex --json"},
	} {
		got = nil
		names, err := runner.FetchAccountsForHarness(context.Background(), tc.harness)
		if err != nil || len(names) != 1 || names[0] != "work" {
			t.Fatalf("%s: names=%v err=%v", tc.harness, names, err)
		}
		if len(got) != 1 || strings.Join(got[0], " ") != tc.want {
			t.Fatalf("%s: forwarded %v, want %q", tc.harness, got, tc.want)
		}
	}
	names, err := (&SSHRunner{}).FetchAccountsForHarness(context.Background(), "pi")
	if err != nil || len(names) != 0 {
		t.Fatalf("pi has no named slots: names=%v err=%v", names, err)
	}
}
