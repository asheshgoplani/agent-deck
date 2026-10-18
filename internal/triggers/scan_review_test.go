package triggers

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// A heartbeat-style script passes its profile and target through shell
// variables and tags itself with --source heartbeat:<name>. The scan must
// resolve both variables and key the row on the --source, so the scanned
// unit and the sends it tags are one row (review of #2738, M1).
func TestScanResolvesProfileVarAndMergesOnSource(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "heartbeat.sh")
	writeFile(t, script, `#!/bin/bash
# Heartbeat for conductor: ops (profile: work)
SESSION="conductor-ops"
PROFILE="work"
if ! agent-deck -p "$PROFILE" conductor status "ops" --json 2>/dev/null | grep -q heartbeat; then exit 0; fi
if SEND_ERROR=$(agent-deck -p "$PROFILE" session send "$SESSION" "$MSG" --no-wait -q --source "heartbeat:ops" 2>&1); then
  exit 0
fi
`)
	sys := filepath.Join(dir, "systemd")
	writeFile(t, filepath.Join(sys, "agent-deck-conductor-heartbeat-ops.service"), "[Service]\nExecStart=/bin/bash "+script+"\n")
	writeFile(t, filepath.Join(sys, "agent-deck-conductor-heartbeat-ops.timer"), "[Timer]\nOnUnitActiveSec=15min\n")

	found := Scan(ScanDirs{SystemdUser: sys})
	if len(found) != 1 {
		t.Fatalf("found %d units, want 1: %+v", len(found), found)
	}
	f := found[0]
	if f.TargetProfile != "work" || f.Target != "conductor-ops" {
		t.Fatalf("target = %q profile = %q, want conductor-ops in work", f.Target, f.TargetProfile)
	}
	if f.ID() != "heartbeat:ops" || f.TriggerKind() != KindHeartbeat || f.Name() != "ops" {
		t.Fatalf("id/kind/name = %s/%s/%s, want heartbeat:ops/heartbeat/ops", f.ID(), f.TriggerKind(), f.Name())
	}
	tagged := Resolve("heartbeat:ops", "", Env{Getenv: func(string) string { return "" }})
	if tagged.ID() != f.ID() {
		t.Fatalf("a tagged heartbeat send keys %s, the scanned unit %s: two rows", tagged.ID(), f.ID())
	}

	db := openDB(t)
	if err := ApplyScan(db, "", "box", found); err != nil {
		t.Fatal(err)
	}
	views, err := Load(db, NewResolver([]ProfileSessions{{Profile: "work", Sessions: []Resolved{{SessionID: "aaaaaaaa-1", Title: "conductor-ops"}}}}), "box", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Health == HealthDeadTarget || !views[0].Runnable {
		t.Fatalf("heartbeat row: %+v", views)
	}
	if cmd, err := RunCommand(views[0], 501); err != nil || strings.Join(cmd, " ") != "systemctl --user start agent-deck-conductor-heartbeat-ops.service" {
		t.Fatalf("run = %v %v", cmd, err)
	}
}

// A dynamic value the script cannot resolve stays unknown, never literal.
func TestScanUnresolvableProfileIsUnknown(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "x.service"), "[Service]\nExecStart=/usr/bin/agent-deck -p \"$(cat /etc/p)\" session send ops hi\n")
	found := Scan(ScanDirs{SystemdUser: dir})
	if len(found) != 1 || found[0].TargetProfile != "" || found[0].Target != "ops" {
		t.Fatalf("profile = %+v", found)
	}
}

// Another app's plist with a weekday out of range must not crash the scan
// (review of #2738, M2), and one bad file never hides the others.
func TestScanBadWeekdayDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	plist := func(label string, weekday string) string {
		return `<?xml version="1.0"?><plist version="1.0"><dict>
<key>Label</key><string>` + label + `</string>
<key>ProgramArguments</key><array><string>/usr/local/bin/agent-deck</string><string>session</string><string>send</string><string>ops</string><string>tick</string></array>
<key>StartCalendarInterval</key><dict><key>Weekday</key><integer>` + weekday + `</integer><key>Hour</key><integer>9</integer><key>Minute</key><integer>0</integer></dict>
</dict></plist>`
	}
	writeFile(t, filepath.Join(dir, "a.plist"), plist("a", "-1"))
	writeFile(t, filepath.Join(dir, "b.plist"), plist("b", "7"))
	found := Scan(ScanDirs{LaunchAgents: dir})
	if len(found) != 2 {
		t.Fatalf("found %+v", found)
	}
	if found[0].Schedule != "weekly Sat 09:00" || found[1].Schedule != "weekly Sun 09:00" {
		t.Fatalf("schedules = %q, %q", found[0].Schedule, found[1].Schedule)
	}
}

// A FIFO named like a unit must not hang the scan (review nit).
func TestScanSkipsFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.service"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	writeFile(t, filepath.Join(dir, "ok.service"), "[Service]\nExecStart=/usr/bin/agent-deck session send ops hi\n")
	done := make(chan []Found, 1)
	go func() { done <- Scan(ScanDirs{SystemdUser: dir}) }()
	select {
	case found := <-done:
		if len(found) != 1 || found[0].Unit != "ok" {
			t.Fatalf("found %+v", found)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scan hung on a FIFO")
	}
}

// agent-deck named only in a comment is not a trigger; %h is the home dir.
func TestScanIgnoresCommentsAndExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, "bin", "retro.sh"), "#!/bin/sh\n# uses agent-deck notes, never sends\necho hi\n")
	writeFile(t, filepath.Join(home, "bin", "poll.sh"), "#!/bin/sh\nagent-deck session send inbox \"new mail\"\n")
	sys := filepath.Join(home, ".config", "systemd", "user")
	writeFile(t, filepath.Join(sys, "retro.service"), "[Service]\nExecStart=/bin/sh %h/bin/retro.sh\n")
	writeFile(t, filepath.Join(sys, "poll.service"), "[Service]\nExecStart=/bin/sh %h/bin/poll.sh\n")
	found := Scan(ScanDirs{SystemdUser: sys})
	if len(found) != 1 || found[0].Unit != "poll" || found[0].Target != "inbox" {
		t.Fatalf("found %+v", found)
	}
}

// A muted fire must not make a failing trigger look healthy (review 5).
func TestHealthIgnoresMutedFires(t *testing.T) {
	db := openDB(t)
	src := Source{Kind: "job", Name: "poller"}
	now := time.Now()
	for i, outcome := range []string{OutcomeFailed, OutcomeMuted} {
		if err := Record(db, "", "box", Fire{Source: src, Message: string(rune('a' + i)), Outcome: outcome}); err != nil {
			t.Fatal(err)
		}
	}
	views, err := Load(db, nil, "box", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Health != HealthFailing || views[0].LastOutcome != OutcomeMuted {
		t.Fatalf("health after failed+muted = %+v", views)
	}
}

// A job's mute holds whatever --source it passes (review minor).
func TestResolveKeepsUnitForNamedSource(t *testing.T) {
	env := Env{Getenv: func(k string) string {
		if k == "XPC_SERVICE_NAME" {
			return "com.example.poller"
		}
		return ""
	}}
	src := Resolve("channel:custom", "", env)
	if src.ID() != "channel:custom" || src.UnitID() != "launchd:com.example.poller" {
		t.Fatalf("id %s unit %s", src.ID(), src.UnitID())
	}
	bare := Resolve("poller", "", env)
	if bare.ID() != "launchd:com.example.poller" || bare.UnitID() != bare.ID() {
		t.Fatalf("bare id %s unit %s", bare.ID(), bare.UnitID())
	}
}

func openDB(t *testing.T) *statedb.StateDB {
	t.Helper()
	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Units that only run agent-deck for something other than waking a session
// (update, notes), or only name it in a path, are not triggers.
func TestScanSkipsNonWakingAgentDeckUnits(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "agent-deck-autoupdate.service"), "[Service]\nExecStart=/usr/local/bin/agent-deck update --yes\n")
	writeFile(t, filepath.Join(dir, "retro.service"), "[Service]\nExecStart=/usr/bin/python3 /home/u/.agent-deck/retro/run.py --out /home/u/agent-deck-notes\n")
	writeFile(t, filepath.Join(dir, "wake.service"), "[Service]\nExecStart=/usr/local/bin/agent-deck session start ops\n")
	found := Scan(ScanDirs{SystemdUser: dir})
	if len(found) != 1 || found[0].Unit != "wake" || found[0].Verb != "start" {
		t.Fatalf("found %+v", found)
	}
}
