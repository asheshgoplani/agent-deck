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

// TestDeliveryE2E_Issue3131_WakeNeverTypesOverParentDraft: the owner's
// condition for the two new wake paths (a kickoff reply waking a
// non-conductor parent, and [inbox] wake_parents). Both dispatch exactly
// `session send <parent> <line> --no-wait --no-tag --require-input-prompt -q`
// with AGENTDECK_SEND_MACHINE=1 (pinned by
// TestIssue3131_NewWakePathsUseTheGuardedSend). `notify-daemon --once` exits
// before its detached wake runs, so this drives that exact command against a
// real pane: a parent with typed text in its input box keeps the text and
// gets nothing, and the same wake into an empty input box lands once (the
// control, so the first half cannot pass because nothing was sent at all).
func TestDeliveryE2E_Issue3131_WakeNeverTypesOverParentDraft(t *testing.T) {
	parallelSubprocessTest(t)
	for _, tc := range []struct {
		name  string
		draft bool
	}{
		{"typed text in the input box", true},
		{"empty input box (control)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDeliveryEnv(t)
			parent := e.startFake("coordinator", "idle", "")
			e.setPhase(parent, "idle")
			draft := ""
			if tc.draft {
				draft = deliveryToken("PARENT_DRAFT") + " half written"
				keys := exec.Command("tmux", "send-keys", "-t", parent.tmux, "-l", draft)
				keys.Env = e.env
				if out, err := keys.CombinedOutput(); err != nil {
					t.Fatalf("type the draft: %v %s", err, out)
				}
				e.waitPane(parent, "the parent's draft", func(p string) bool { return strings.Contains(p, "❯ "+draft) })
			}

			line := "[INBOX] reply · " + deliveryToken("WAKE") + " worker: ok"
			out, errOut, code := e.run("", []string{"AGENTDECK_SEND_MACHINE=1"},
				"session", "send", parent.id, line, "--no-wait", "--no-tag", "--require-input-prompt", "-q")

			if tc.draft {
				if code == 0 {
					t.Fatalf("the wake into a parent with typed text must refuse: exit 0 (%s %s)", out, errOut)
				}
				time.Sleep(time.Second) // a late keystroke would show by now
				if got := parent.draft(t); got != draft {
					t.Fatalf("the parent's input box holds %q after the wake, want the typed text %q", got, draft)
				}
				if n := parent.received(t, "[INBOX]"); n != 0 {
					t.Fatalf("the wake reached a parent with typed text %d time(s)", n)
				}
				return
			}
			if code != 0 {
				t.Fatalf("the wake into an idle parent must be sent: exit %d (%s %s)", code, out, errOut)
			}
			issue2530WaitFor(t, 10*time.Second, "the wake line to reach the parent", func() bool { return parent.received(t, "[INBOX]") > 0 })
			time.Sleep(time.Second)
			if n := parent.received(t, "[INBOX]"); n != 1 {
				t.Fatalf("the wake reached the parent %d times, want 1", n)
			}
		})
	}
}

// TestDeliveryE2E_Issue3131_LaunchKickoffWiring: the real `launch` run from
// inside the parent session. It passes the linked parent (not the caller of a
// nested launch) to the tag, wires --no-tag and --no-transition-notify, and
// reports `tagged` in --json; the kickoff the child receives carries the
// envelope only when tagged.
func TestDeliveryE2E_Issue3131_LaunchKickoffWiring(t *testing.T) {
	parallelSubprocessTest(t)
	e := newDeliveryEnv(t)
	parent := e.startFake("coordinator", "idle", "")
	envelope := "[agent-deck from:" + parent.id + "]"
	for _, tc := range []struct {
		title  string
		flags  []string
		tagged bool
	}{
		{"kick-default", nil, true},
		{"kick-notag", []string{"--no-tag"}, false},
		{"kick-unreported", []string{"--no-transition-notify"}, false},
	} {
		t.Run(tc.title, func(t *testing.T) {
			project := filepath.Join(e.root, "p-"+tc.title)
			if err := os.MkdirAll(project, 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(project, ".fake-phase"), "idle", 0o600)
			task := deliveryToken("KICKOFF") + " review the diff"
			args := append([]string{"launch", project, "-t", tc.title, "-c", "claude", "--parent", parent.id,
				"-m", task, "--no-assert-done", "--json"}, tc.flags...)
			out, errOut, code := e.run("", []string{"AGENTDECK_INSTANCE_ID=" + parent.id}, args...)
			if code != 0 {
				t.Fatalf("launch: exit %d\n%s\n%s", code, out, errOut)
			}
			var res struct {
				ID      string `json:"id"`
				Message string `json:"message"`
				Tagged  *bool  `json:"tagged"`
			}
			if err := json.Unmarshal([]byte(out), &res); err != nil || res.Tagged == nil {
				t.Fatalf("launch --json: %v\n%s", err, out)
			}
			if *res.Tagged != tc.tagged || strings.HasPrefix(res.Message, envelope) != tc.tagged {
				t.Fatalf("tagged=%v message=%q, want tagged=%v", *res.Tagged, res.Message, tc.tagged)
			}
			b, err := os.ReadFile(filepath.Join(project, ".fake", "transcript"))
			if err != nil {
				t.Fatal(err)
			}
			child := &fakeTarget{transcript: string(b)}
			issue2530WaitFor(t, 15*time.Second, "the kickoff to reach the child", func() bool { return child.received(t, task) == 1 })
			if got := child.received(t, envelope); (got == 1) != tc.tagged {
				t.Fatalf("child received the envelope %d time(s), want tagged=%v", got, tc.tagged)
			}
		})
	}
}
