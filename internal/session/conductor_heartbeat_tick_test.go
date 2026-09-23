package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Issue #2348: a delivered heartbeat is a turn that re-reads the conductor's
// whole conversation. These tests pin that a tick sends a bounded message only
// when something actionable changed, and nothing otherwise.

const heartbeatTickMaxBytes = 1024

func setupHeartbeatTickTest(t *testing.T, name string) {
	t.Helper()
	home := setupConductorTest(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	dir, err := ConductorNameDir(name)
	if err != nil {
		t.Fatalf("ConductorNameDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir conductor dir: %v", err)
	}
}

// runTick does one full tick the way `conductor heartbeat-tick` does: load the
// persisted state, build, persist. It returns the bytes the tick would send.
func runTick(t *testing.T, in HeartbeatTickInput) string {
	t.Helper()
	msg, next := BuildHeartbeatTick(in, LoadHeartbeatTickState(in.Name))
	if err := SaveHeartbeatTickState(in.Name, next); err != nil {
		t.Fatalf("SaveHeartbeatTickState: %v", err)
	}
	return msg
}

func TestHeartbeatTick_UnchangedStateBytesStayFlat(t *testing.T) {
	const name = "ops"
	setupHeartbeatTickTest(t, name)
	rules := filepath.Join(t.TempDir(), "HEARTBEAT_RULES.md")
	if err := os.WriteFile(rules, []byte(strings.Repeat("rule\n", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	in := HeartbeatTickInput{
		Name: name,
		Sessions: []HeartbeatSessionView{
			{Title: "api-fix", Status: StatusWaiting, Path: "/src/api"},
			{Title: "frontend", Status: StatusRunning, Path: "/src/app"},
			{Title: "docs", Status: StatusIdle, Path: "/src/docs"},
		},
		RulesPath:  rules,
		RulesStamp: HeartbeatRulesStamp(rules),
	}

	// 96 ticks = one day at the reporter's 15-minute interval.
	const ticks = 96
	perTick := make([]int, ticks)
	total := 0
	for i := range perTick {
		perTick[i] = len(runTick(t, in))
		total += perTick[i]
	}

	if perTick[0] == 0 || perTick[0] > heartbeatTickMaxBytes {
		t.Fatalf("first tick sent %d bytes, want 1..%d", perTick[0], heartbeatTickMaxBytes)
	}
	for i := 1; i < ticks; i++ {
		if perTick[i] != 0 {
			t.Fatalf("tick %d sent %d bytes with nothing changed, want 0 (per-tick bytes: %v)", i+1, perTick[i], perTick)
		}
	}
	if total != perTick[0] {
		t.Fatalf("%d unchanged ticks sent %d bytes total, want exactly the first tick's %d", ticks, total, perTick[0])
	}
}

func TestHeartbeatTick_DeliversOnlyChanges(t *testing.T) {
	const name = "ops"
	setupHeartbeatTickTest(t, name)
	rules := filepath.Join(t.TempDir(), "HEARTBEAT_RULES.md")
	if err := os.WriteFile(rules, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := HeartbeatTickInput{Name: name, RulesPath: rules, RulesStamp: HeartbeatRulesStamp(rules)}

	if msg := runTick(t, in); msg != "" {
		t.Fatalf("nothing waiting and empty inbox must send nothing, got %q", msg)
	}

	in.Sessions = []HeartbeatSessionView{{Title: "api-fix", Status: StatusWaiting, Path: "/src/api"}}
	first := runTick(t, in)
	if !strings.HasPrefix(first, ConductorBridgeHeartbeatPrefix) || !IsConductorHeartbeatMessage(first) {
		t.Fatalf("delivered tick must carry the heartbeat prefix, got %q", first)
	}
	if !strings.Contains(first, "api-fix (project: /src/api)") || !strings.Contains(first, "Read heartbeat rules from "+rules) {
		t.Fatalf("first delivery must name the waiting session and the rules path, got %q", first)
	}
	if strings.Contains(first, "inbox drain") {
		t.Fatalf("empty inbox must not ask for a drain, got %q", first)
	}

	in.InboxPending = 2
	withInbox := runTick(t, in)
	if !strings.Contains(withInbox, "Inbox: 2 pending") {
		t.Fatalf("new inbox records must be delivered with a drain hint, got %q", withInbox)
	}
	if strings.Contains(withInbox, "Read heartbeat rules from") || !strings.Contains(withInbox, "Heartbeat rules unchanged") {
		t.Fatalf("unchanged rules must not be re-read, got %q", withInbox)
	}

	// Rules edited: the next delivery asks for a re-read again.
	if err := os.WriteFile(rules, []byte("v2 with more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(rules, future, future)
	in.RulesStamp = HeartbeatRulesStamp(rules)
	in.InboxPending = 0
	if msg := runTick(t, in); !strings.Contains(msg, "Read heartbeat rules from "+rules) {
		t.Fatalf("changed rules file must be re-read, got %q", msg)
	}

	// Resolved, then the same session waits again: delivered again.
	in.Sessions = nil
	if msg := runTick(t, in); msg != "" {
		t.Fatalf("resolved state must send nothing, got %q", msg)
	}
	in.Sessions = []HeartbeatSessionView{{Title: "api-fix", Status: StatusWaiting, Path: "/src/api"}}
	if msg := runTick(t, in); msg == "" {
		t.Fatal("a session that waits again after being resolved must be delivered")
	}
}

func TestHeartbeatTick_RemotePullFailureDoesNotWake(t *testing.T) {
	in := HeartbeatTickInput{Name: "ops"}
	state := HeartbeatTickState{RemoteFailed: true}
	for tick := 0; tick < 5; tick++ {
		msg, next := BuildHeartbeatTick(in, state)
		if len(msg) != 0 {
			t.Fatalf("unreachable remote tick %d sent %d bytes", tick, len(msg))
		}
		state = next
	}
	in.InboxPending, in.InboxDigest = 1, "new-record"
	msg, state := BuildHeartbeatTick(in, state)
	if msg == "" {
		t.Fatal("new talkback record must wake once despite failed remote")
	}
	if msg, _ = BuildHeartbeatTick(in, state); msg != "" {
		t.Fatalf("unchanged record woke again: %q", msg)
	}
}

func TestUninstallHeartbeatDaemon_StopFailureKeepsEnabled(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd stop failure fixture")
	}
	setupHeartbeatTickTest(t, "ops")
	if err := SaveConductorMeta(&ConductorMeta{Name: "ops", Profile: "default", HeartbeatEnabled: true}); err != nil {
		t.Fatal(err)
	}
	timer, err := SystemdHeartbeatTimerPath("ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(timer), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(timer, []byte("[Timer]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if err := UninstallHeartbeatDaemon("ops"); err == nil {
		t.Fatal("failed timer stop must be reported")
	}
	meta, err := LoadConductorMeta("ops")
	if err != nil || !meta.HeartbeatEnabled {
		t.Fatalf("failed timer stop must keep heartbeat enabled: meta=%+v err=%v", meta, err)
	}
}

func TestLaunchdHeartbeatInstallWritesFreshPlist(t *testing.T) {
	setupHeartbeatTickTest(t, "ops")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "agent-deck"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if err := installHeartbeatDaemonLaunchd("ops", 15); err != nil {
		t.Fatal(err)
	}
	plist, err := HeartbeatPlistPath("ops")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); err != nil {
		t.Fatalf("fresh install must write plist: %v", err)
	}
}

func TestLaunchdHeartbeatUninstallRefusesFailedUnload(t *testing.T) {
	setupHeartbeatTickTest(t, "ops")
	plist, err := HeartbeatPlistPath("ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte("plist"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if err := uninstallHeartbeatDaemonLaunchd("ops"); err == nil {
		t.Fatal("failed unload must be reported")
	}
	if _, err := os.Stat(plist); err != nil {
		t.Fatalf("failed unload must preserve plist: %v", err)
	}
}

func TestHeartbeatScriptChecksNamedConductorFlag(t *testing.T) {
	if !strings.Contains(conductorHeartbeatScript, `conductor status "{NAME}" --json`) {
		t.Fatal("heartbeat script must check this conductor's heartbeat flag")
	}
}

// TestHeartbeatScript_UnchangedTicksSendNothing runs the rendered heartbeat.sh
// against a fake agent-deck and measures the bytes handed to `session send`.
// The pre-#2348 script sent its full static prompt on every tick.
func TestHeartbeatScript_UnchangedTicksSendNothing(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	sent := filepath.Join(home, "sent.log")
	ticked := filepath.Join(home, "ticked")
	// The fake tick reports a change once, then nothing, like BuildHeartbeatTick
	// does for an unchanged conversation.
	fake := `#!/bin/bash
while [ "$1" = "-p" ]; do shift 2; done
case "$1 $2" in
  "conductor status") echo '{"conductors": [{"heartbeat": true}]}' ;;
  "session show") echo '{"status": "idle"}' ;;
  "conductor heartbeat-tick")
    if [ ! -f "` + ticked + `" ]; then touch "` + ticked + `"; echo "[HEARTBEAT] [ops] Status: 1 waiting."; fi ;;
  "session send") printf '%s' "$4" >> "` + sent + `" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "agent-deck"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(home, "heartbeat.sh")
	if err := os.WriteFile(script, []byte(renderConductorHeartbeatScript("ops", "default")), 0o755); err != nil {
		t.Fatal(err)
	}

	const ticks = 10
	var perTick []int
	prev := 0
	for i := 0; i < ticks; i++ {
		cmd := exec.Command("bash", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tick %d: %v\n%s", i+1, err, out)
		}
		data, _ := os.ReadFile(sent)
		perTick = append(perTick, len(data)-prev)
		prev = len(data)
	}
	if perTick[0] == 0 {
		t.Fatalf("first tick must deliver the change, per-tick bytes: %v", perTick)
	}
	for i := 1; i < ticks; i++ {
		if perTick[i] != 0 {
			t.Fatalf("unchanged tick %d sent %d bytes, want 0 (per-tick bytes: %v)", i+1, perTick[i], perTick)
		}
	}
}

func TestHeartbeatScript_FailedSendRetriesNextTick(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	conductorDir, err := ConductorNameDir("ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(conductorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := `#!/bin/bash
while [ "$1" = "-p" ]; do shift 2; done
case "$1 $2" in
  "conductor status") echo '{"conductors": [{"heartbeat": true}]}' ;;
  "session show") echo '{"status": "idle"}' ;;
  "conductor heartbeat-tick")
    if [[ "$5" = --commit-message=* ]]; then touch "$HOME/committed";
    elif [ ! -f "$HOME/committed" ]; then echo '[HEARTBEAT] remote arrival'; fi ;;
  "session send")
    if [ ! -f "$HOME/failed" ]; then touch "$HOME/failed"; echo 'send failed' >&2; exit 1; fi
    printf '%s' "$4" >> "$HOME/sent" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "agent-deck"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(home, "heartbeat.sh")
	if err := os.WriteFile(script, []byte(renderConductorHeartbeatScript("ops", "default")), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		cmd := exec.Command("bash", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		_, _ = cmd.CombinedOutput()
	}
	logData, err := os.ReadFile(filepath.Join(conductorDir, "heartbeat.log"))
	if err != nil || strings.Count(string(logData), "heartbeat: send failed") != 1 {
		t.Fatalf("send failure must produce one visible log line: log=%q err=%v", logData, err)
	}
	data, err := os.ReadFile(filepath.Join(home, "sent"))
	if err != nil || string(data) != "[HEARTBEAT] remote arrival" {
		t.Fatalf("failed send must retry once then dedup: sent=%q err=%v", data, err)
	}
}
