package triggers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

const pollerPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.ashesh.inbox-poller</string>
    <key>ProgramArguments</key>
    <array>
        <string>/bin/bash</string>
        <string>SCRIPT</string>
    </array>
    <key>StartInterval</key>
    <integer>300</integer>
    <key>StartCalendarInterval</key>
    <dict>
        <key>Hour</key>
        <integer>7</integer>
        <key>Minute</key>
        <integer>30</integer>
    </dict>
    <key>RunAtLoad</key>
    <true/>
</dict>
</plist>
`

const unrelatedPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>Label</key><string>com.other.backup</string>
<key>ProgramArguments</key><array><string>/usr/bin/rsync</string><string>-a</string></array>
<key>StartInterval</key><integer>3600</integer>
</dict></plist>
`

func scanFixtures(t *testing.T) (ScanDirs, map[string]Found) {
	t.Helper()
	root := t.TempDir()
	dirs := DefaultScanDirs(root)
	scripts := filepath.Join(root, "bin")

	// launchd: a script with a title target, two schedule keys.
	poll := filepath.Join(scripts, "poll.sh")
	writeFile(t, poll, "#!/bin/bash\nMSG=$(check-mail)\nagent-deck session send \"inbox-triage\" \"$MSG\" --no-wait\n")
	writeFile(t, filepath.Join(dirs.LaunchAgents, "com.ashesh.inbox-poller.plist"), strings.ReplaceAll(pollerPlist, "SCRIPT", poll))
	writeFile(t, filepath.Join(dirs.LaunchAgents, "com.other.backup.plist"), unrelatedPlist)

	// systemd service + timer, target inline with a profile.
	writeFile(t, filepath.Join(dirs.SystemdUser, "agentdeck-overnight.service"),
		"[Unit]\nDescription=overnight\n\n[Service]\nType=oneshot\nExecStart=/usr/local/bin/agent-deck -p work session send overnight-orchestrator \"tick\" --no-wait\n")
	writeFile(t, filepath.Join(dirs.SystemdUser, "agentdeck-overnight.timer"),
		"[Timer]\nOnUnitActiveSec=5min\nOnBootSec=2min\n")

	// systemd path unit, id target in the script it runs.
	watch := filepath.Join(scripts, "watch.sh")
	writeFile(t, watch, "#!/bin/sh\nexec agent-deck session send 3f2a1b4c-1712345678 \"new file\"\n")
	writeFile(t, filepath.Join(dirs.SystemdUser, "drop-watch.service"), "[Service]\nExecStart=/bin/sh "+watch+"\n")
	writeFile(t, filepath.Join(dirs.SystemdUser, "drop-watch.path"), "[Path]\nPathChanged=/home/u/inbox/new\n")

	// $VAR target resolved from an assignment in the script.
	varScript := filepath.Join(scripts, "mail.sh")
	writeFile(t, varScript, "#!/bin/bash\nTARGET=${TARGET:-mail-desk}\nagent-deck session send \"$TARGET\" \"$BODY\"\n")
	writeFile(t, filepath.Join(dirs.SystemdUser, "mail.service"), "[Service]\nExecStart=/bin/bash "+varScript+"\n")

	// $VAR target only set by the environment: unknown, never guessed.
	envScript := filepath.Join(scripts, "env.sh")
	writeFile(t, envScript, "#!/bin/bash\nagent-deck session send \"$DEST\" hi\n")
	writeFile(t, filepath.Join(dirs.SystemdUser, "envonly.service"), "[Service]\nExecStart=-/bin/bash "+envScript+"\n")

	// restart verb.
	writeFile(t, filepath.Join(dirs.SystemdUser, "tg-watchdog.service"), "[Service]\nExecStart=/usr/bin/agent-deck session restart conductor-ops\n")

	// A unit that does not call agent-deck.
	writeFile(t, filepath.Join(dirs.SystemdUser, "syncthing.service"), "[Service]\nExecStart=/usr/bin/syncthing serve\n")

	found := map[string]Found{}
	for _, f := range Scan(dirs) {
		found[f.ID()] = f
	}
	return dirs, found
}

func TestScanFixtures(t *testing.T) {
	dirs, found := scanFixtures(t)
	if len(found) != 6 {
		t.Fatalf("found %d units, want 6: %+v", len(found), found)
	}
	for _, id := range []string{"launchd:com.other.backup", "systemd:syncthing"} {
		if _, ok := found[id]; ok {
			t.Errorf("%s does not call agent-deck but was found", id)
		}
	}

	poll := found["launchd:com.ashesh.inbox-poller"]
	if poll.Verb != "send" || poll.Target != "inbox-triage" || poll.TargetBy != "title" {
		t.Errorf("launchd poller: %+v", poll)
	}
	if poll.Schedule != "every 5m, daily 07:30" {
		t.Errorf("launchd schedule = %q", poll.Schedule)
	}
	if poll.ConfigPath != filepath.Join(dirs.LaunchAgents, "com.ashesh.inbox-poller.plist") || poll.Unit != "com.ashesh.inbox-poller" {
		t.Errorf("launchd identity: %+v", poll)
	}

	night := found["systemd:agentdeck-overnight"]
	if night.Target != "overnight-orchestrator" || night.TargetBy != "title" || night.TargetProfile != "work" || night.Verb != "send" {
		t.Errorf("systemd service: %+v", night)
	}
	if night.Schedule != "every 5min, 2min after boot" {
		t.Errorf("systemd timer schedule = %q", night.Schedule)
	}

	watch := found["systemd:drop-watch"]
	if watch.Target != "3f2a1b4c-1712345678" || watch.TargetBy != "id" || watch.Schedule != "on change of new" {
		t.Errorf("path unit with id target: %+v", watch)
	}

	if mail := found["systemd:mail"]; mail.Target != "mail-desk" || mail.TargetBy != "title" {
		t.Errorf("$VAR target from assignment: %+v", mail)
	}
	if env := found["systemd:envonly"]; env.Target != "" || env.TargetBy != "" || env.Verb != "send" {
		t.Errorf("env-only target must stay empty: %+v", env)
	}
	if wd := found["systemd:tg-watchdog"]; wd.Verb != "restart" || wd.Target != "conductor-ops" {
		t.Errorf("restart verb: %+v", wd)
	}
}

func TestScanMissingDirsAndBudget(t *testing.T) {
	if got := Scan(DefaultScanDirs(filepath.Join(t.TempDir(), "nowhere"))); len(got) != 0 {
		t.Fatalf("missing dirs found %d", len(got))
	}
	if got := Scan(ScanDirs{}); len(got) != 0 {
		t.Fatalf("empty dirs found %d", len(got))
	}
}
