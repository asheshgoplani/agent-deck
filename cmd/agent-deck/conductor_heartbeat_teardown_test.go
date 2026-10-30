package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestConductorTeardownReportsHeartbeatOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := session.SaveConductorMeta(&session.ConductorMeta{
		Name: "ops", Profile: "default", HeartbeatEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	if out, stderr, code := runAgentDeck(t, home, "conductor", "teardown", "ops", "--json"); code != 0 {
		t.Fatalf("teardown exited %d: %s %s", code, out, stderr)
	}
	out, stderr, code := runAgentDeck(t, home, "conductor", "status", "ops", "--json")
	if code != 0 {
		t.Fatalf("status exited %d: %s %s", code, out, stderr)
	}
	var status struct {
		Conductors []struct {
			Heartbeat bool `json:"heartbeat"`
		} `json:"conductors"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("decode status: %v: %s", err, out)
	}
	if len(status.Conductors) != 1 || status.Conductors[0].Heartbeat {
		t.Fatalf("status after teardown must report heartbeat off: %s", out)
	}
	meta, err := session.LoadConductorMeta("ops")
	if err != nil || meta.HeartbeatEnabled {
		t.Fatalf("persisted heartbeat flag after teardown: meta=%+v err=%v", meta, err)
	}
}

func TestConductorHeartbeatTickCLIReadsInboxAndRules(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := session.SaveConductorMeta(&session.ConductorMeta{Name: "ops", Profile: "default"}); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runAgentDeck(t, home, "add", "-t", "conductor-ops", "-c", "claude", "--no-parent", "--json", project)
	if code != 0 {
		t.Fatalf("add conductor: %d %s %s", code, out, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil || added.ID == "" {
		t.Fatalf("add response: %v %s", err, out)
	}
	for _, group := range []string{"ops/sub", "other"} {
		out, stderr, code = runAgentDeck(t, home, "add", "-t", "child-"+strings.ReplaceAll(group, "/", "-"), "-c", "claude", "-g", group, "--no-parent", "--json", project)
		if code != 0 {
			t.Fatalf("add %s: %d %s %s", group, code, out, stderr)
		}
	}
	inbox := session.InboxPathFor(added.ID)
	if err := os.MkdirAll(filepath.Dir(inbox), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inbox, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := filepath.Join(home, "rules.md")
	if err := os.WriteFile(rules, []byte("rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, code = runAgentDeck(t, home, "conductor", "heartbeat-tick", "ops", "--rules", rules)
	if code != 0 || !strings.Contains(out, "Inbox: 1 pending") || !strings.Contains(out, "Read heartbeat rules from "+rules) || !strings.Contains(out, "1 idle, 0 error, 0 stopped.") {
		t.Fatalf("first tick: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	first := strings.TrimSuffix(out, "\n")
	out, stderr, code = runAgentDeck(t, home, "conductor", "heartbeat-tick", "ops", "--rules", rules)
	if code != 0 || strings.TrimSuffix(out, "\n") != first {
		t.Fatalf("unconfirmed send must retry: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	out, stderr, code = runAgentDeck(t, home, "conductor", "heartbeat-tick", "ops", "--rules", rules, "--commit-message", first)
	if code != 0 || out != "" {
		t.Fatalf("commit confirmed send: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	out, stderr, code = runAgentDeck(t, home, "conductor", "heartbeat-tick", "ops", "--rules", rules)
	if code != 0 || out != "" {
		t.Fatalf("unchanged tick: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if err := os.WriteFile(inbox, []byte("{\"source_remote\":\"build-box\",\"child_session_id\":\"remote-child\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, code = runAgentDeck(t, home, "conductor", "heartbeat-tick", "ops", "--rules", rules)
	if code != 0 || !strings.Contains(out, "Inbox: 1 pending") {
		t.Fatalf("replacement record at same count: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

// Archived children stay out of the tick's counts, while the conductor's inbox
// still wakes it (the same completion records TestNotifyTransition_ArchivedChild
// StillCommittedToInbox pins on the write side).
func TestConductorHeartbeatTickLeavesOutArchivedChildren(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := session.SaveConductorMeta(&session.ConductorMeta{Name: "ops", Profile: "default"}); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runAgentDeck(t, home, "add", "-t", "conductor-ops", "-c", "claude", "--no-parent", "--json", project)
	if code != 0 {
		t.Fatalf("add conductor: %d %s %s", code, out, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil || added.ID == "" {
		t.Fatalf("add response: %v %s", err, out)
	}
	for _, title := range []string{"live-child", "old-child"} {
		out, stderr, code = runAgentDeck(t, home, "add", "-t", title, "-c", "claude", "-g", "ops", "--no-parent", project)
		if code != 0 {
			t.Fatalf("add %s: %d %s %s", title, code, out, stderr)
		}
	}
	out, stderr, code = runAgentDeck(t, home, "session", "archive", "old-child")
	if code != 0 {
		t.Fatalf("archive: %d %s %s", code, out, stderr)
	}
	inbox := session.InboxPathFor(added.ID)
	if err := os.MkdirAll(filepath.Dir(inbox), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inbox, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, code = runAgentDeck(t, home, "conductor", "heartbeat-tick", "ops")
	if code != 0 || !strings.Contains(out, "Inbox: 1 pending") || !strings.Contains(out, "0 waiting, 0 running, 1 idle, 0 error, 0 stopped.") {
		t.Fatalf("tick: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

// A real pane and the real hook path: `session start` on an archived session
// brings it back with the archive flag still set, and its Stop hook then makes
// it waiting. That waiting child must not wake the conductor; the same child
// unarchived must.
func TestConductorHeartbeatTickArchivedWaitingChildDoesNotWake(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	project := filepath.Join(home, "project")
	fakeBin := filepath.Join(home, "fakebin")
	for _, dir := range []string{project, fakeBin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Stand-in claude: keeps a live pane without touching any real agent CLI.
	if err := os.WriteFile(filepath.Join(fakeBin, "claude"), []byte("#!/bin/sh\nexec sleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := session.SaveConductorMeta(&session.ConductorMeta{Name: "ops", Profile: "default"}); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")}
	run := func(extra []string, stdin string, args ...string) string {
		t.Helper()
		out, stderr, code := runAgentDeckEnv(t, home, stdin, append(append([]string{}, env...), extra...), args...)
		if code != 0 {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, code, out, stderr)
		}
		return out
	}
	out := run(nil, "", "add", "-t", "worker", "-c", "claude", "-g", "ops", "--no-parent", "--json", project)
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil || added.ID == "" {
		t.Fatalf("add response: %v %s", err, out)
	}
	t.Cleanup(func() { _, _, _ = runAgentDeckEnv(t, home, "", env, "session", "stop", added.ID) })
	stopHook := func() {
		t.Helper()
		run([]string{"AGENTDECK_INSTANCE_ID=" + added.ID}, `{"hook_event_name":"Stop","session_id":"s1"}`, "hook-handler")
	}

	run(nil, "", "session", "start", "worker")
	stopHook()
	if out := run(nil, "", "conductor", "heartbeat-tick", "ops"); !strings.Contains(out, "Waiting sessions: worker") {
		t.Fatalf("control: live waiting child must wake, stdout=%q", out)
	}

	run(nil, "", "session", "archive", "worker")
	run(nil, "", "session", "start", "worker")
	stopHook()
	listed := run(nil, "", "list", "--json")
	if !strings.Contains(listed, `"status": "waiting"`) || !strings.Contains(listed, `"archived": true`) {
		t.Fatalf("setup: want an archived waiting worker, list=%s", listed)
	}
	if out := run(nil, "", "conductor", "heartbeat-tick", "ops"); out != "" {
		t.Fatalf("archived waiting child woke the conductor: %q", out)
	}
}

// #2348 x #2354: teardown of a conductor whose meta.json names an agent this
// build doesn't know (older binary, newer data) must complete like any other.
func TestConductorTeardownUnknownAgentCompletes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove bool
	}{{"keep", false}, {"remove", true}} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			dir, err := session.ConductorNameDir("future")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			meta := `{"name":"future","agent":"futurebot","profile":"default","heartbeat_enabled":true,"heartbeat_interval":15}`
			if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
				t.Fatal(err)
			}

			args := []string{"conductor", "teardown", "future"}
			if tc.remove {
				args = append(args, "--remove")
			}
			out, stderr, code := runAgentDeck(t, home, args...)
			if code != 0 || !strings.Contains(out, "Teardown complete.") {
				t.Fatalf("teardown remove=%v: exit=%d stdout=%q stderr=%q", tc.remove, code, out, stderr)
			}
			if tc.remove {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("teardown --remove must remove the conductor dir: %v", err)
				}
				return
			}
			loaded, err := session.LoadConductorMeta("future")
			if err != nil || loaded.HeartbeatEnabled || loaded.Agent != "futurebot" {
				t.Fatalf("after teardown: meta=%+v err=%v", loaded, err)
			}
		})
	}
}
