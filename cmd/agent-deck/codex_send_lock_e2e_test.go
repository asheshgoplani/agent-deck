package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The reported failure, end to end through the real binary: while a Codex turn
// runs, one plain send waits for it to end and a second plain send to the same
// session must wait too. It used to fail after 5s with "acceptance lock timed
// out", because the first send held the lock for its whole readiness wait.
func TestCodexSendWaitingOnBusyTurnDoesNotRefuseOtherSenders(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("requires tmux")
	}
	e := newIssue2530Env(t)
	const thread = "01a0cc56-4444-7531-9a25-c53c0d193753"

	// A Codex that never leaves its turn: the composer prompt the readiness
	// wait requires never appears, so nothing is ever typed.
	bin := filepath.Join(e.root, "fakebin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(bin, "codex"), "#!/bin/sh\nwhile :; do printf '\\r• Working (3s • esc to interrupt)'; sleep 1; done\n", 0o700)
	codexHome := filepath.Join(e.root, "codex")
	e.env = append(e.env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "CODEX_HOME="+codexHome)
	writeLegacyCodexRollout(t, codexHome, thread, "21")

	var added struct {
		ID string `json:"id"`
	}
	project := filepath.Join(e.root, "proj")
	if err := json.Unmarshal([]byte(e.cli("add", "-t", "busy-codex", "-c", "codex", "--json", project)), &added); err != nil || added.ID == "" {
		t.Fatalf("add: %v", err)
	}
	e.cli("session", "start", added.ID, "--json")
	e.cli("session", "set", added.ID, "codex-session-id", thread)

	send := func(msg, timeout string) *exec.Cmd {
		cmd := exec.Command(e.bin, "-p", issue2530Profile, "session", "send", added.ID, msg, "--timeout", timeout)
		cmd.Env = e.env
		return cmd
	}
	first := send("first", "60s")
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = first.Process.Kill()
		_ = first.Wait()
	})
	time.Sleep(3 * time.Second) // the first send is past any lock it would take

	out, err := send("second", "8s").CombinedOutput()
	if err == nil {
		t.Fatalf("second send reported delivery to a pane that never showed a prompt:\n%s", out)
	}
	if strings.Contains(string(out), "acceptance lock") {
		t.Fatalf("a send waiting on the turn held every other sender off:\n%s", out)
	}
	if !strings.Contains(string(out), "timeout waiting for agent") {
		t.Fatalf("second send should have waited out its own --timeout:\n%s", out)
	}
}
