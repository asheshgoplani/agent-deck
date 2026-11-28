package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/voicedesk"
)

// reworkRecordingSelf is a fake agent-deck that writes its argv (one per
// line) to argsFile and prints out.
func reworkRecordingSelf(t *testing.T, out string) (self, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	self = filepath.Join(dir, "fake-agent-deck")
	argsFile = filepath.Join(dir, "args")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + argsFile + "'; done\ncat <<'EOT'\n" + out + "\nEOT\n"
	if err := os.WriteFile(self, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return self, argsFile
}

func reworkArgs(t *testing.T, f string) []string {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func reworkIsolate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

func TestReworkVoicePlanLaunchUsesGroupDefaultFolderOnly(t *testing.T) {
	reworkIsolate(t)
	self, argsFile := reworkRecordingSelf(t, `{"success":true,"name":"alex","path":"team/alex","default_path":"/w/projects/alpha","default_path_source":"explicit","explicit_default_path":"/w/projects/alpha"}`)
	d := &cliVoiceDeck{self: self, profile: "work"}
	spec, err := d.PlanLaunch(context.Background(), voicedesk.LaunchSpec{Title: "probe-one", Group: "team/alex", Profile: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Path != "/w/projects/alpha" || spec.Tool != "claude" {
		t.Fatalf("plan = %+v, want the group's explicit folder and tool claude", spec)
	}
	if got := strings.Join(reworkArgs(t, argsFile), " "); got != "-p work group show team/alex --json" {
		t.Fatalf("ran %q", got)
	}

	// Only a derived path (the group's most recent session, e.g. the
	// conductor's checkout): no folder, so no launch.
	self, _ = reworkRecordingSelf(t, `{"success":true,"default_path":"/w/conductor","default_path_source":"recent_session","explicit_default_path":""}`)
	d = &cliVoiceDeck{self: self, profile: "work"}
	if _, err := d.PlanLaunch(context.Background(), voicedesk.LaunchSpec{Title: "x", Group: "team/alex"}); !errors.Is(err, voicedesk.ErrNoFolder) {
		t.Fatalf("derived path only: err = %v, want ErrNoFolder", err)
	}
	// A relative or flag-looking folder is never passed on.
	self, _ = reworkRecordingSelf(t, `{"success":true,"explicit_default_path":"--cmd=evil"}`)
	d = &cliVoiceDeck{self: self, profile: "work"}
	if _, err := d.PlanLaunch(context.Background(), voicedesk.LaunchSpec{Title: "x", Group: "team/alex"}); !errors.Is(err, voicedesk.ErrNoFolder) {
		t.Fatalf("relative folder: err = %v, want ErrNoFolder", err)
	}
	// No group: nothing to read a folder from.
	if _, err := d.PlanLaunch(context.Background(), voicedesk.LaunchSpec{Title: "x"}); !errors.Is(err, voicedesk.ErrNoFolder) {
		t.Fatalf("no group: err = %v, want ErrNoFolder", err)
	}
}

func TestReworkVoicePlanLaunchRemoteReadsRemoteGroup(t *testing.T) {
	reworkIsolate(t)
	self, argsFile := reworkRecordingSelf(t, `{"success":true,"explicit_default_path":"/srv/beta"}`)
	d := &cliVoiceDeck{self: self, profile: "work"}
	spec, err := d.PlanLaunch(context.Background(), voicedesk.LaunchSpec{Title: "t", Group: "team", Host: "boxb"})
	if err != nil || spec.Path != "/srv/beta" {
		t.Fatalf("plan = %+v err=%v", spec, err)
	}
	if got := strings.Join(reworkArgs(t, argsFile), " "); got != "remote boxb group show team --json" {
		t.Fatalf("ran %q", got)
	}
	if _, err := remoteCommandArgs([]string{"group", "show", "team", "--json"}); err != nil {
		t.Fatalf("remote group show refused: %v", err)
	}
}

func TestReworkVoiceLaunchPassesFolderAndTool(t *testing.T) {
	reworkIsolate(t)
	self, argsFile := reworkRecordingSelf(t, `{"success":true,"id":"n1","title":"probe-one","group":"team/alex"}`)
	d := &cliVoiceDeck{self: self, profile: "work"}
	if _, err := d.Launch(context.Background(), voicedesk.LaunchSpec{Title: "probe-one", Group: "team/alex", Profile: "work", Path: "/w/projects/alpha", Tool: "claude"}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(reworkArgs(t, argsFile), " ")
	if got != "-p work launch -t probe-one -c claude -g team/alex --json /w/projects/alpha" {
		t.Fatalf("ran %q", got)
	}

	// No folder (an old stored question, a fake): never launch in a guessed folder.
	self, argsFile = reworkRecordingSelf(t, `{"success":true,"id":"n1"}`)
	d = &cliVoiceDeck{self: self, profile: "work"}
	if _, err := d.Launch(context.Background(), voicedesk.LaunchSpec{Title: "probe-one", Group: "team/alex"}); err == nil {
		t.Fatal("launch with no folder ran")
	}
	if a := reworkArgs(t, argsFile); a != nil {
		t.Fatalf("launch with no folder ran %v", a)
	}
}
