package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2582 follow-up: two Hermes entries in one directory. The binding fed
// by hook status samples rarely sees a parent turn edge, because a real turn
// replaces it in the status file within milliseconds (pre_api_request after
// pre_llm_call, on_session_end after post_llm_call), and a CLI restart reads
// no sample at all. Restart must still resume the entry's own conversation
// from what its hook handler saw, never B's, the newest in the directory.

const (
	issue2582OwnA   = "20261007_210308_fba186" // A's own conversation
	issue2582OwnB   = "20261009_173544_87f49b" // B's, newest in the directory
	issue2582ChildA = "20261010_090000_c0ffee" // a subagent delegated by A
)

// issue2582FakeHermes installs a fake `hermes` that reports B's conversation
// as the newest in any workspace and records the argv of each launch.
func issue2582FakeHermes(t *testing.T) (argsFile string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "argv.log")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  --version) echo "Hermes Agent v2026.9.1"; exit 0 ;;
  sessions) printf 'Title  Workspace  Last Active  ID\n----\nconv  proj  1m ago  %s\n'; exit 0 ;;
esac
printf 'argv:%%s\n' "$*" >> %q
exec sleep 60
`, issue2582OwnB, argsFile)
	if err := os.WriteFile(filepath.Join(dir, "hermes"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

// issue2582Launches waits until the fake hermes was launched n times and
// returns the recorded argv lines.
func issue2582Launches(t *testing.T, argsFile string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		data, _ := os.ReadFile(argsFile)
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(data) > 0 && len(lines) >= n {
			return lines
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake hermes launched %d times, want %d; argv log: %q", len(lines), n, data)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// issue2582Hook delivers one Hermes hook event the way the pane's hermes
// process does: env from the launch command, payload on stdin.
func issue2582Hook(t *testing.T, inst *session.Instance, generation, event, sessionID, platform string) {
	t.Helper()
	t.Setenv("AGENTDECK_INSTANCE_ID", inst.ID)
	t.Setenv("AGENTDECK_TOOL", "hermes")
	t.Setenv("AGENTDECK_HOOK_GENERATION", generation)
	extra := "{}"
	if platform != "" {
		extra = fmt.Sprintf(`{"platform":%q}`, platform)
	}
	runHookHandlerWithStdin(t, fmt.Sprintf(`{"hook_event_name":%q,"session_id":%q,"cwd":%q,"extra":%s}`,
		event, sessionID, inst.ProjectPath, extra))
}

func TestIssue2582HermesRestartResumesOwnConversationAfterARealTurn(t *testing.T) {
	argsFile := issue2582FakeHermes(t)
	a := session.NewInstanceWithTool("issue2582-A", t.TempDir(), "hermes")
	a.Command = "hermes"
	if err := a.Start(); err != nil {
		t.Fatalf("start A: %v", err)
	}
	t.Cleanup(func() { _ = a.Kill() })
	issue2582Launches(t, argsFile, 1)
	gen := a.HermesHookGeneration

	// One turn of A as Hermes fires it, with a delegated subagent inside.
	for _, ev := range []struct{ event, sid, platform string }{
		{"on_session_start", issue2582OwnA, "cli"},
		{"pre_llm_call", issue2582OwnA, "cli"},
		{"pre_api_request", issue2582OwnA, "cli"},
		{"pre_llm_call", issue2582ChildA, "subagent"},
		{"pre_tool_call", issue2582ChildA, ""},
		{"post_tool_call", issue2582ChildA, ""},
		{"post_llm_call", issue2582ChildA, "subagent"},
		{"post_api_request", issue2582OwnA, "cli"},
		{"post_llm_call", issue2582OwnA, "cli"},
		{"on_session_end", issue2582OwnA, "cli"},
	} {
		issue2582Hook(t, a, gen, ev.event, ev.sid, ev.platform)
	}
	// What any surface reads now: the latest status record, on_session_end.
	session.ReloadHookStatus(a)

	if err := a.Restart(); err != nil {
		t.Fatalf("restart A: %v", err)
	}
	lines := issue2582Launches(t, argsFile, 2)
	if got := lines[1]; !strings.Contains(got, "--resume "+issue2582OwnA) {
		t.Fatalf("restart of A launched %q, want --resume %s (A's own conversation; %s is B's, %s is A's subagent)",
			got, issue2582OwnA, issue2582OwnB, issue2582ChildA)
	}
	if w := a.ConsumeCodexRestartWarning(); w != "" {
		t.Fatalf("restart that resumed the owned conversation warned: %q", w)
	}

	// A late parent edge from the process the restart replaced carries the
	// old launch generation and must not rebind A to another conversation.
	issue2582Hook(t, a, gen, "pre_llm_call", issue2582OwnB, "cli")
	if err := a.Restart(); err != nil {
		t.Fatalf("second restart of A: %v", err)
	}
	lines = issue2582Launches(t, argsFile, 3)
	if got := lines[2]; !strings.Contains(got, "--resume "+issue2582OwnA) {
		t.Fatalf("second restart launched %q, want --resume %s: a stale-generation event rebound the session", got, issue2582OwnA)
	}
}
