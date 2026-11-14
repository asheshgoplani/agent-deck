package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2921: TestIssue2582HermesRestartFreshStartsNewConversation failed
// under load with
//
//	restart fresh: failed to restart session fresh: start refused: this
//	session still owns processes from an earlier spawn (1 owned process(es)
//	from generation 1 are alive outside the session's pane)
//
// RestartFresh killed the old tmux session with the asynchronous Kill and
// called Start at once. Start's ownership gate (#1873) then found the old
// spawn's receipt naming a process that tmux's SIGHUP had not finished yet,
// outside any live pane, and refused. A process that ignores SIGHUP, as Claude
// Code 2.1.27+ does, makes that window certain instead of a scheduling race:
// this fake hermes ignores SIGHUP, so the old leader outlives kill-session.

func issue2921HangupImmuneHermes(t *testing.T) (argsFile string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "argv.log")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  --version) echo "Hermes Agent v2026.9.1"; exit 0 ;;
  sessions) printf 'Title  Workspace  Last Active  ID\n----\n'; exit 0 ;;
esac
printf 'argv:%%s\n' "$*" >> %q
trap '' HUP
exec sleep 60
`, argsFile)
	if err := os.WriteFile(filepath.Join(dir, "hermes"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

// RestartFresh of a running session whose process outlives tmux's SIGHUP must
// tear the old spawn down itself and start the new one, not refuse its own
// previous process as an escaped survivor.
func TestIssue2921RestartFreshOfHangupImmuneProcessStarts(t *testing.T) {
	argsFile := issue2921HangupImmuneHermes(t)
	a := session.NewInstanceWithTool("issue2921-A", t.TempDir(), "hermes")
	a.Command = "hermes"
	if err := a.Start(); err != nil {
		t.Fatalf("start A: %v", err)
	}
	t.Cleanup(func() { _ = a.KillAndWait() })
	issue2582Launches(t, argsFile, 1)

	if err := a.RestartFresh(); err != nil {
		t.Fatalf("restart fresh: %v", err)
	}
	lines := issue2582Launches(t, argsFile, 2)
	if got := lines[1]; strings.Contains(got, "--resume") {
		t.Fatalf("fresh restart launched %q, want a new conversation", got)
	}
}
