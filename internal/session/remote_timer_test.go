package session

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// #2472: the controller reads and manages each remote's own update timer
// through the remote's agent-deck. These tests stub the remote command
// (SSHRunner.runFn); nothing here opens SSH.

func timerStubRunner(reply func(args []string) ([]byte, error)) (*SSHRunner, *[][]string) {
	var calls [][]string
	r := &SSHRunner{
		Host: "u@remote", AgentDeckPath: "agent-deck", lastStderr: &lastStderrBox{},
		runFn: func(_ context.Context, args ...string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			return reply(args)
		},
	}
	return r, &calls
}

func TestFetchTimerStatus_NewRemoteAnswersJSON(t *testing.T) {
	r, calls := timerStubRunner(func([]string) ([]byte, error) {
		return []byte("A new version is available\n{\"installed\":true,\"kind\":\"systemd-legacy\",\"active\":true,\"legacy_unit\":\"agentdeck-autoupdate.timer\",\"last_run\":\"2026-10-03T00:01:44Z\",\"next_run\":\"2026-10-04T00:00:00Z\"}\n"), nil
	})
	st := r.FetchTimerStatus(context.Background())
	if got := strings.Join((*calls)[0], " "); got != "update --timer-status --json" {
		t.Fatalf("remote command = %q", got)
	}
	if st.Kind != "systemd-legacy" || !st.Installed || !st.Active || st.LastRun != "2026-10-03T00:01:44Z" || st.NextRun != "2026-10-04T00:00:00Z" {
		t.Fatalf("status = %+v", st)
	}
}

// Backward compat: an older remote prints text for --timer-status (or
// rejects the flag); the controller reports kind unknown, never a guess and
// never a failure.
func TestFetchTimerStatus_OldRemoteDegradesToUnknown(t *testing.T) {
	for name, reply := range map[string]func([]string) ([]byte, error){
		"text answer": func([]string) ([]byte, error) {
			return []byte("Update timer: not installed (run `agent-deck update --install-timer`)\n"), nil
		},
		"flag rejected": func([]string) ([]byte, error) {
			return []byte("flag provided but not defined: -timer-status\n"), errors.New("ssh command failed: exit status 2")
		},
		"unreachable": func([]string) ([]byte, error) {
			return nil, errors.New("ssh: connect to host 100.64.0.9 port 22: Operation timed out")
		},
	} {
		r, _ := timerStubRunner(reply)
		st := r.FetchTimerStatus(context.Background())
		if st.Kind != update.TimerKindUnknown || st.Installed || st.Active || st.Note == "" {
			t.Errorf("%s: status = %+v, want kind unknown with a note", name, st)
		}
	}
}

func TestInstallUpdateTimer_NewOldAndFailingRemote(t *testing.T) {
	r, calls := timerStubRunner(func([]string) ([]byte, error) {
		return []byte(`{"action":"migrated","migrated":"agentdeck-autoupdate.timer","status":{"installed":true,"kind":"systemd","active":true}}`), nil
	})
	inst, err := r.InstallUpdateTimer(context.Background(), false)
	if err != nil || inst.Action != update.TimerActionMigrated || inst.Status.Kind != "systemd" {
		t.Fatalf("new remote: %+v, %v", inst, err)
	}
	if got := strings.Join((*calls)[0], " "); got != "update --install-timer --json" {
		t.Fatalf("remote command = %q", got)
	}
	if inst.Summary() != "migrated legacy timer agentdeck-autoupdate.timer -> agent-deck-autoupdate.timer" {
		t.Fatalf("summary = %q", inst.Summary())
	}

	r, calls = timerStubRunner(func([]string) ([]byte, error) { return []byte(`{"action":"none","status":{"kind":"systemd"}}`), nil })
	if _, err := r.InstallUpdateTimer(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join((*calls)[0], " "); got != "update --ensure-timer --json" {
		t.Fatalf("ensure command = %q", got)
	}

	// An older remote still installs through its own --install-timer and
	// answers in text: reported, not failed.
	r, _ = timerStubRunner(func([]string) ([]byte, error) {
		return []byte("✓ Update timer installed (systemd): /home/u/.config/systemd/user/agent-deck-autoupdate.timer\n  runs ...\n"), nil
	})
	inst, err = r.InstallUpdateTimer(context.Background(), false)
	if err != nil || !inst.Legacy || !strings.Contains(inst.Summary(), "predates timer migration") {
		t.Fatalf("old remote: %+v, %v", inst, err)
	}
	// Its own install leaves a legacy timer running beside the new one, so
	// the line says how to fix that (#2472 review round 2, finding 3).
	if !strings.Contains(inst.Summary(), "update the remote's agent-deck first (agent-deck remote update)") {
		t.Fatalf("old remote summary lacks the update hint: %q", inst.Summary())
	}

	// A new remote whose install failed answers JSON with an error and a
	// non-zero exit: the error is the remote's, not the ssh exit status.
	r, _ = timerStubRunner(func([]string) ([]byte, error) {
		return []byte(`{"action":"skipped","reason":"no systemd user session: Failed to connect to bus","status":{"kind":"none"},"error":"enable timer failed"}`), errors.New("ssh command failed: exit status 1")
	})
	if _, err := r.InstallUpdateTimer(context.Background(), false); err == nil || err.Error() != "enable timer failed" {
		t.Fatalf("failing remote err = %v", err)
	}
}

func TestRemoteVerbReadOnly_UpdateTimerVerbs(t *testing.T) {
	for _, args := range [][]string{{"update", "--timer-status", "--json"}, {"update", "--install-timer", "--json"}, {"update", "--ensure-timer", "--json"}} {
		if !remoteVerbReadOnly(args) {
			t.Errorf("%v is idempotent and may be retried", args)
		}
	}
	for _, args := range [][]string{{"update", "--unattended"}, {"update", "--check-now"}, {"update"}} {
		if remoteVerbReadOnly(args) {
			t.Errorf("%v installs a binary and must never be retried", args)
		}
	}
}

// A version-only observation (the TUI's hourly check, remote update) keeps
// the timer last read for that remote.
func TestRecordRemoteVersions_KeepsTheTimer(t *testing.T) {
	setupSessionXDGPathEnv(t)
	at := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	if err := RecordRemoteTimers(map[string]update.TimerStatus{"lab": {Kind: "systemd", Installed: true, Active: true}}, at); err != nil {
		t.Fatal(err)
	}
	if err := RecordRemoteVersions(map[string]RemoteVersionState{"lab": {Version: "1.16.25", Found: true, CheckedAt: at}}); err != nil {
		t.Fatal(err)
	}
	got := LoadRemoteVersions()["lab"]
	if got.Version != "1.16.25" || got.Timer == nil || got.Timer.Kind != "systemd" || !got.TimerCheckedAt.Equal(at) {
		t.Fatalf("state = %+v (timer %+v)", got, got.Timer)
	}
}

// timerStubInstaller is a remote that can be updated and asked for its
// timer.
type timerStubInstaller struct {
	stubInstaller
	ensureCalls int
	ensure      RemoteTimerInstall
	ensureErr   error
}

func (s *timerStubInstaller) FetchTimerStatus(context.Context) update.TimerStatus {
	return s.ensure.Status
}

func (s *timerStubInstaller) InstallUpdateTimer(_ context.Context, ensureOnly bool) (RemoteTimerInstall, error) {
	if !ensureOnly {
		return RemoteTimerInstall{}, errors.New("remote update must run the heal, not the explicit install")
	}
	s.ensureCalls++
	return s.ensure, s.ensureErr
}

// An explicit `remote update` also installs the updated remote's own timer;
// a heal failure is a note, never the update's failure; the unattended
// sweeps (EnsureTimer off) leave the timer to the remote.
func TestUpdateRemotes_EnsuresTheRemoteTimer(t *testing.T) {
	setupSessionXDGPathEnv(t)
	migrated := &timerStubInstaller{
		stubInstaller: stubInstaller{version: "1.16.0", found: true, platformOK: true},
		ensure:        RemoteTimerInstall{TimerEnsureResult: update.TimerEnsureResult{Action: update.TimerActionMigrated, Migrated: update.LegacySystemdTimerTimer, Status: update.TimerStatus{Kind: "systemd", Installed: true, Active: true}}},
	}
	broken := &timerStubInstaller{
		stubInstaller: stubInstaller{version: "1.15.0", found: true, platformOK: true},
		ensureErr:     errors.New("flag provided but not defined: -ensure-timer"),
	}
	runners := map[string]RemoteBinaryInstaller{"current": migrated, "old": broken}
	opts := stubReleaseOptions(nil, false)
	opts.NewRunner = func(name string, _ RemoteConfig) RemoteBinaryInstaller { return runners[name] }
	opts.EnsureTimer = true
	remotes := map[string]RemoteConfig{"current": {Host: "a@current"}, "old": {Host: "a@old"}}

	results := UpdateRemotes(context.Background(), remotes, "v1.16.0", opts)
	if results[0].Outcome != RemoteUpdateOutcomeCurrent || !strings.Contains(results[0].Note, "update timer: migrated legacy timer agentdeck-autoupdate.timer -> agent-deck-autoupdate.timer") {
		t.Fatalf("current remote: %+v", results[0])
	}
	if results[1].Outcome != RemoteUpdateOutcomeUpdated || !strings.Contains(results[1].Note, "update timer not ensured: flag provided but not defined") {
		t.Fatalf("old remote: %+v", results[1])
	}
	if st := LoadRemoteVersions()["current"].Timer; st == nil || st.Kind != "systemd" {
		t.Fatalf("cached timer = %+v", st)
	}

	migrated.ensureCalls = 0
	opts.EnsureTimer = false
	UpdateRemotes(context.Background(), remotes, "v1.16.0", opts)
	if migrated.ensureCalls != 0 {
		t.Fatal("the unattended sweeps never manage a remote's timer")
	}
}

func TestAutoEnsureUpdateTimer_Gates(t *testing.T) {
	ran := 0
	prevSettings, prevSuppressed, prevEnsure := updateTimerSettings, updateTimerSuppressed, ensureUpdateTimerOnHost
	t.Cleanup(func() {
		updateTimerSettings, updateTimerSuppressed, ensureUpdateTimerOnHost = prevSettings, prevSuppressed, prevEnsure
	})
	ensureUpdateTimerOnHost = func(*slog.Logger) (update.TimerEnsureResult, error) {
		ran++
		return update.TimerEnsureResult{Action: update.TimerActionInstalled}, nil
	}
	off := false
	updateTimerSettings = func() UpdateSettings { return UpdateSettings{ManageTimer: &off} }
	updateTimerSuppressed = func() string { return "" }
	if res, _ := AutoEnsureUpdateTimer(nil); res.Action != update.TimerActionSkipped || !strings.Contains(res.Reason, "manage_timer = false") || ran != 0 {
		t.Fatalf("manage_timer off: %+v ran=%d", res, ran)
	}
	updateTimerSettings = func() UpdateSettings { return UpdateSettings{} }
	updateTimerSuppressed = func() string { return "CI=true" }
	if res, _ := AutoEnsureUpdateTimer(nil); res.Action != update.TimerActionSkipped || ran != 0 {
		t.Fatalf("suppressed: %+v ran=%d", res, ran)
	}
	updateTimerSuppressed = func() string { return "" }
	if res, _ := AutoEnsureUpdateTimer(nil); res.Action != update.TimerActionInstalled || ran != 1 {
		t.Fatalf("default (manage_timer unset = true): %+v ran=%d", res, ran)
	}
	if !(UpdateSettings{}).GetManageTimer() {
		t.Fatal("manage_timer defaults to true")
	}
}

func TestRecordRemoteNudges_LatestPerRemote(t *testing.T) {
	setupSessionXDGPathEnv(t)
	at := time.Date(2026, 10, 3, 8, 53, 0, 0, time.UTC)
	if err := RecordRemoteNudges("1.16.23", []NudgeResult{
		{Name: "a", Outcome: NudgeOutcomeFailed, Err: errors.New("signal: killed")},
		{Name: "b", Outcome: NudgeOutcomeSent},
	}, at); err != nil {
		t.Fatal(err)
	}
	if err := RecordRemoteNudges("1.16.24", []NudgeResult{{Name: "a", Outcome: NudgeOutcomeFallback}}, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := LoadRemoteNudges()
	if len(got) != 2 {
		t.Fatalf("records = %+v", got)
	}
	if got[0].Remote != "a" || got[0].AskedVersion != "1.16.24" || !got[0].OK || got[0].Outcome != "fallback" || got[0].Error != "" {
		t.Fatalf("a = %+v", got[0])
	}
	if got[1].Remote != "b" || got[1].AskedVersion != "1.16.23" || !got[1].OK || got[1].Outcome != "nudged" {
		t.Fatalf("b = %+v", got[1])
	}
	// A fallback whose pull failed is not ok.
	if err := RecordRemoteNudges("1.16.25", []NudgeResult{{Name: "b", Outcome: NudgeOutcomeFallback, Err: errors.New("exit status 1")}}, at); err != nil {
		t.Fatal(err)
	}
	if b := LoadRemoteNudges()[1]; b.OK || b.Error != "exit status 1" {
		t.Fatalf("failed fallback = %+v", b)
	}
}
