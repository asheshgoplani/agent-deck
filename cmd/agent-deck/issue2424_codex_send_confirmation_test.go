package main

import (
	"strings"
	"sync/atomic"
	"testing"
)

// Issue #2424: `session send --no-wait` to a Codex lane exited 1 ("never
// confirmed submitted ... the body is visible but the agent never began
// processing it") while the lane's transcript showed the message and Codex
// was already working on it. These replay that shape through the Codex
// arrival loop (the path every non-Claude send takes) and pin the verdicts.

const issue2424LaneMessage = "Stand down on the current task and switch to the retry lane. " +
	"Rebase fix/lane-a-retry on origin/main, rerun the failing integration suite with -count=1, " +
	"and report the three slowest tests with their timings and the exact command you ran. " +
	"Do not push anything and do not open a pull request until I confirm the numbers. " +
	"If the rebase conflicts in internal/session, stop and describe the conflict instead of resolving it. " +
	"Keep the existing worktree; do not create a new one. When you are done, reply with one short paragraph " +
	"that names the branch head, the suite result, and anything you skipped."

// issue2424CodexPane renders a Codex 0.15x frame: earlier transcript, then
// the optional extra transcript rows, then (when composerBody is non-empty)
// the composer holding that body, else the idle placeholder composer.
func issue2424CodexPane(transcript []string, composerBody string) string {
	rows := []string{
		"• Ran go test ./internal/session/...",
		"  └ ok  	github.com/example/lane/internal/session	4.112s",
		"",
	}
	rows = append(rows, transcript...)
	rows = append(rows, "")
	if composerBody == "" {
		rows = append(rows, "› Ask Codex to do anything")
	} else {
		rows = append(rows, issue2424CodexCell(composerBody)...)
	}
	rows = append(rows, "", "  gpt-6-sol · ~/work/lane-a · Context 70% left · Context 30% used · weekly 94% left · 258K window")
	return strings.Join(rows, "\n") + "\n"
}

// issue2424CodexCell is a Codex user-message cell: "› " at column 0, wrapped
// rows indented two columns.
func issue2424CodexCell(msg string) []string {
	const width = 96
	var rows []string
	for j := 0; j < len(msg); j += width {
		prefix := "  "
		if j == 0 {
			prefix = "› "
		}
		rows = append(rows, prefix+msg[j:min(j+width, len(msg))])
	}
	return rows
}

func issue2424Working(elapsed string) string {
	return "• Working (" + elapsed + " • esc to interrupt)"
}

func TestIssue2424_CodexSendConfirmation(t *testing.T) {
	msg := issue2424LaneMessage
	cell := issue2424CodexCell(msg)
	withCell := func(tail ...string) []string { return append(append(append([]string{}, cell...), ""), tail...) }

	cases := []struct {
		name string
		// status is what the target's spike-filtered status reports
		// throughout; "active" before the send disables the idle-to-active
		// shortcut, exactly as on a lane that was already working.
		status   string
		baseline string
		frames   []string
		want     string
		wantErr  bool
	}{
		{
			name:     "occurrence 3: busy lane, message in transcript, Working (3m 32s)",
			status:   "active",
			baseline: issue2424CodexPane([]string{issue2424Working("3m 30s")}, ""),
			frames:   []string{issue2424CodexPane(withCell(issue2424Working("3m 32s")), "")},
			want:     deliverySubmitted,
		},
		{
			name:     "occurrence 4: message in transcript, Working (6s), no status edge seen",
			status:   "waiting",
			baseline: issue2424CodexPane([]string{"─ Worked for 2m 07s ─────────────────────────────"}, ""),
			frames:   []string{issue2424CodexPane(withCell(issue2424Working("6s")), "")},
			want:     deliverySubmitted,
		},
		{
			name:     "occurrence 2: Working appears a few checks after the body",
			status:   "waiting",
			baseline: issue2424CodexPane(nil, ""),
			frames: []string{
				issue2424CodexPane(nil, msg),
				issue2424CodexPane(nil, msg),
				issue2424CodexPane(withCell(issue2424Working("0s")), ""),
			},
			want: deliverySubmitted,
		},
		{
			name:     "#1793: body still in the composer after every check",
			status:   "waiting",
			baseline: issue2424CodexPane(nil, ""),
			frames:   []string{issue2424CodexPane(nil, msg)},
			want:     deliveryTypedNotSubmitted,
			wantErr:  true,
		},
		{
			name:     "#1793 on a working lane: body still in the composer",
			status:   "active",
			baseline: issue2424CodexPane([]string{issue2424Working("3m 30s")}, ""),
			frames:   []string{issue2424CodexPane([]string{issue2424Working("3m 32s")}, msg)},
			want:     deliveryTypedNotSubmitted,
			wantErr:  true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mock := &mockSendRetryTarget{
				statuses: []string{c.status},
				// The first capture is the composer-draft guard's (Codex is
				// guarded since #2536); the arrival loop's baseline follows.
				panes: append([]string{c.baseline, c.baseline}, c.frames...),
			}
			res, err := executeSend(mock, "codex", msg, true, sendExecTuning{retry: noWaitSendOptionsNoDelay()})
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v (delivery %q)", err, c.wantErr, res.delivery)
			}
			if res.delivery != c.want {
				t.Fatalf("delivery = %q, want %q", res.delivery, c.want)
			}
			if n := atomic.LoadInt32(&mock.sendKeysCalls); n != 1 {
				t.Fatalf("SendKeysAndEnter called %d times, want exactly 1", n)
			}
			fields := res.jsonFields()
			wantConfirmation, wantSubmitted := "failed", false
			if c.want == deliverySubmitted {
				wantConfirmation, wantSubmitted = "confirmed", true
			}
			if fields["delivery"] != c.want || fields["confirmation"] != wantConfirmation || fields["submitted"] != wantSubmitted {
				t.Fatalf("--json fields = %v, want delivery=%s confirmation=%s submitted=%v", fields, c.want, wantConfirmation, wantSubmitted)
			}
		})
	}
}

// Claude lanes are unchanged: the Codex evidence is read only for Codex.
// The busy-Claude shape from #1978 (hook busy before and after, body newly
// on screen, no queued-messages placeholder) stays delivered, unconfirmed.
func TestIssue2424_ClaudeLaneVerdictUnchanged(t *testing.T) {
	const msg = "PROBE reply with only OK"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{busyPaneNoBody(), busyPaneBodyNoAffordance(msg)},
	}
	opts := queuedOpts(hookSeq(probeBusy))
	opts.tool = "claude"
	delivery, err := sendWithRetryTarget(mock, msg, false, opts)
	if err != nil || delivery != deliveryDelivered {
		t.Fatalf("delivery=%q err=%v, want %q with no error", delivery, err, deliveryDelivered)
	}
}

func noWaitSendOptionsNoDelay() sendRetryOptions {
	opts := noWaitSendOptions()
	opts.checkDelay = 0
	return opts
}
