package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Issue #1793 end to end on a real tmux pane: the remote-walk case 4d/5b
// (`session send <id> "echo hello-walk"` to a plain shell). The command runs
// and prints, so the send is confirmed by the pane itself — output and a
// fresh prompt after the sent line — and never "NOT delivered".
//
// Needs the tmux binary (the package's TestMain isolates TMUX_TMPDIR); runs
// in the Docker test image, skips elsewhere.
func TestIssue1793_ShellSendEndToEnd_EchoIsSubmitted(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	for _, tc := range []struct {
		name   string
		noWait bool
		tun    sendExecTuning
	}{
		{name: "default", noWait: false, tun: defaultSendTuning()},
		{name: "no-wait", noWait: true, tun: noWaitSendTuning()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := tmux.NewSession("issue1793-shell-"+tc.name, t.TempDir())
			// A bare interactive bash with a fixed prompt: exactly what a
			// `-c shell` session degrades to on the remotes.
			if err := sess.Start(`env -i PATH="$PATH" HOME="$HOME" TERM=xterm PS1='walk$ ' bash --norc --noprofile -i`); err != nil {
				t.Fatalf("start shell session: %v", err)
			}
			t.Cleanup(func() { _ = sess.Kill() })
			// Wait for the prompt.
			deadline := time.Now().Add(10 * time.Second)
			for {
				if content, err := sess.CapturePaneFresh(); err == nil && strings.Contains(content, "walk$") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("shell prompt never appeared")
				}
				time.Sleep(100 * time.Millisecond)
			}

			const msg = "echo hello-walk-1793"
			res, err := executeSend(sess, "shell", msg, tc.noWait, tc.tun)
			if err != nil {
				t.Fatalf("send to a shell that ran the command must not fail (issue #1793): %v", err)
			}
			if res.delivery != deliverySubmitted {
				content, _ := sess.CapturePaneFresh()
				t.Fatalf("delivery = %q, want %q; pane:\n%s", res.delivery, deliverySubmitted, content)
			}
			content, err := sess.CapturePaneFresh()
			if err != nil {
				t.Fatalf("capture: %v", err)
			}
			// The output line, distinct from the echoed command line.
			if !strings.Contains(content, "\nhello-walk-1793") {
				t.Fatalf("the command's output is not in the pane:\n%s", content)
			}
		})
	}
}

// A shell command that has not finished within the window (no output, no
// fresh prompt) is delivered with confirmation unknown: exit 0 and the note
// names the tool. It is never a failure.
func TestIssue1793_ShellSendEndToEnd_LongCommandIsDeliveredUnconfirmed(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sess := tmux.NewSession("issue1793-shell-long", t.TempDir())
	if err := sess.Start(`env -i PATH="$PATH" HOME="$HOME" TERM=xterm PS1='walk$ ' bash --norc --noprofile -i`); err != nil {
		t.Fatalf("start shell session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if content, err := sess.CapturePaneFresh(); err == nil && strings.Contains(content, "walk$") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell prompt never appeared")
		}
		time.Sleep(100 * time.Millisecond)
	}
	res, err := executeSend(sess, "shell", "sleep 20 && echo finished-walk-1793", false, defaultSendTuning())
	if err != nil {
		t.Fatalf("a running command is not a failed send: %v", err)
	}
	if res.delivery != deliveryDelivered {
		t.Fatalf("delivery = %q, want %q", res.delivery, deliveryDelivered)
	}
	if want := "delivered; submission not confirmable for tool shell"; res.note != want {
		t.Fatalf("note = %q, want %q", res.note, want)
	}
}
