package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// `session send` to a shell sitting at its prompt, with the real binary in a
// throwaway profile: it returns once the command ran. Before the fix every
// send first sat out the readiness floor meant for agent TUIs, about 3.8 s
// per send.
//
// The bound is on the median of five sends, about three times the time after
// the fix and well under the time before it, so one slow send on a busy CI
// host does not trip it.
func TestSendE2E_IdleShellSendDoesNotWaitOutTheAgentReadyFloor(t *testing.T) {
	for _, tool := range []string{"tmux", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("requires %s", tool)
		}
	}
	e := newIssue2530Env(t)
	t.Cleanup(e.killDetached)
	project := filepath.Join(e.root, "proj", "idle")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	e.cli("launch", project, "-c", "bash", "-t", "idle")
	pane := func() string { return e.cli("session", "output", "--pane", "-q", "idle") }
	waitPane := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(pane(), want) {
			if time.Now().After(deadline) {
				t.Fatalf("the pane never showed %q:\n%s", want, pane())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitPane("$")
	// Let the shell sit at its prompt so the first send meets an idle one.
	time.Sleep(2 * time.Second)

	var ms []int64
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		marker := "send-marker-f7-" + n
		start := time.Now()
		out := e.cli("session", "send", "idle", "echo "+marker)
		ms = append(ms, time.Since(start).Milliseconds())
		if strings.Contains(out, "NOT delivered") {
			t.Fatalf("send %s was reported as not delivered:\n%s", n, out)
		}
		// The command's output line, distinct from the echoed command.
		waitPane("\n" + marker)
		time.Sleep(2 * time.Second)
	}
	t.Logf("session send to an idle shell, 5 sends: %v ms", ms)
	sorted := append([]int64(nil), ms...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if median, limit := sorted[len(sorted)/2], int64(2500); median > limit {
		t.Errorf("median session send to an idle shell = %d ms (all: %v), want under %d ms", median, ms, limit)
	}
}
