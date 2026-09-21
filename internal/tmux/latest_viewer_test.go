//go:build !windows

package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil/multiclienttmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Boxed shared view (2026-09-21 field report): two people on one session, one
// of them sees it drawn in a small box of dots although window-size is
// `latest`. tmux never sizes a window from a control-mode client, but it does
// hand such a client the window's *latest* slot (on attach, and when the latest
// person leaves), and with two or more people attached a control client in
// that slot freezes the window at whatever size it had.

func TestParseLatestCandidates_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		out    string
		window string
		want   []latestCandidate
	}{
		{
			name:   "a person on the window, status line on",
			out:    "101\tattached,focused,UTF-8\t1790001877\t200\t56\t@1\ton\n",
			window: "@1",
			want:   []latestCandidate{{pid: 101, activity: 1790001877, cols: 200, rows: 55}},
		},
		{
			name: "agent-deck's control client is never a candidate",
			out: "101\tattached,UTF-8\t10\t140\t34\t@1\ton\n" +
				"102\tattached,focused,control-mode,UTF-8\t20\t80\t\t@1\ton\n",
			window: "@1",
			want:   []latestCandidate{{pid: 101, activity: 10, cols: 140, rows: 33}},
		},
		{
			name:   "an ignore-size client is never a candidate",
			out:    "103\tattached,ignore-size,UTF-8\t30\t120\t40\t@1\ton\n",
			window: "@1",
			want:   nil,
		},
		{
			name:   "a client on another window is not a candidate",
			out:    "104\tattached,UTF-8\t40\t120\t40\t@2\ton\n",
			window: "@1",
			want:   nil,
		},
		{
			name: "status off and multi-line status",
			out: "105\tattached,UTF-8\t50\t100\t30\t@1\toff\n" +
				"106\tattached,UTF-8\t60\t100\t30\t@1\t2\n",
			window: "@1",
			want: []latestCandidate{
				{pid: 105, activity: 50, cols: 100, rows: 30},
				{pid: 106, activity: 60, cols: 100, rows: 28},
			},
		},
		{
			name:   "malformed lines are skipped",
			out:    "garbage\n\nx\tattached\t1\t2\t3\t@1\ton\n107\tattached\t1\t0\t3\t@1\ton\n",
			window: "@1",
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, parseLatestCandidates(tt.out, tt.window))
		})
	}
}

func TestPickLatestViewer_Table(t *testing.T) {
	t.Parallel()
	colleague := latestCandidate{pid: 1, activity: 100, cols: 140, rows: 33}
	maintainer := latestCandidate{pid: 2, activity: 200, cols: 200, rows: 55}
	twin := latestCandidate{pid: 3, activity: 300, cols: 140, rows: 33}
	tests := []struct {
		name       string
		candidates []latestCandidate
		cols, rows int
		want       latestCandidate
		wantOK     bool
	}{
		{name: "nobody", cols: 80, rows: 24},
		{name: "one person: tmux sizes the window to them by itself", candidates: []latestCandidate{colleague}, cols: 92, rows: 49},
		{
			name:       "the window fits one person: keep it on them, even the less recent one",
			candidates: []latestCandidate{colleague, maintainer},
			cols:       140, rows: 33,
			want: colleague, wantOK: true,
		},
		{
			name:       "the window fits several: the most recently active of them",
			candidates: []latestCandidate{colleague, maintainer, twin},
			cols:       140, rows: 33,
			want: twin, wantOK: true,
		},
		{
			name:       "the window fits nobody (frozen at a departed viewer's size): the most recently active",
			candidates: []latestCandidate{colleague, maintainer},
			cols:       92, rows: 49,
			want: maintainer, wantOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := pickLatestViewer(tt.candidates, tt.cols, tt.rows)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// requireWindowStays polls the harness window until it is want; it fails
// with the last size seen (the frozen size when the bug is present).
func requireHarnessWindow(t *testing.T, h *multiclienttmux.Harness, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	got := ""
	for time.Now().Before(deadline) {
		if w, hgt, err := h.WindowSize(); err == nil {
			got = fmt.Sprintf("%dx%d", w, hgt)
			if got == want {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("window = %s, want %s (clients: %s)", got, want, harnessClients(h))
}

func harnessClients(h *multiclienttmux.Harness) string {
	out, _ := exec.Command("tmux", "-S", h.SocketPath, "list-clients", "-F",
		"#{client_width}x#{client_height}:#{client_flags}").CombinedOutput()
	return strings.Join(strings.Fields(string(out)), " ")
}

// TestPipeControlClientNeverFreezesSharedWindow_Integration is the field
// report on a real server: a colleague (140x34) and the maintainer (200x56)
// share a session that agent-deck's PipeManager keeps a control pipe on, a
// third viewer (92x50) looks in and leaves. tmux gives the departed viewer's
// latest slot to the most recently active remaining client, which is the
// control pipe (it attached after both people and never types), and then
// computes no size at all: without the fix both people are left with a 92x49
// box. The window must go to a person, never stay at a departed client's size
// or take the control client's.
func TestPipeControlClientNeverFreezesSharedWindow_Integration(t *testing.T) {
	h := multiclienttmux.NewNamed(t, "boxed")
	require.NoError(t, h.AddClient(140, 34)) // colleague
	require.NoError(t, h.AddClient(200, 56)) // maintainer
	requireHarnessWindow(t, h, "200x55")

	pm := NewPipeManager(context.Background(), nil)
	t.Cleanup(pm.Close)
	require.NoError(t, pm.Connect(h.SessionName, h.SocketName))
	require.True(t, waitFor(3*time.Second, func() bool { return pm.IsConnected(h.SessionName) }))
	requireHarnessWindow(t, h, "200x55") // the pipe's attach changes nothing on screen

	require.NoError(t, h.AddClient(92, 50)) // a third viewer looks in
	requireHarnessWindow(t, h, "92x49")
	require.NoError(t, h.DetachClient(2)) // ... and leaves
	requireHarnessWindow(t, h, "200x55")
}

// TestPipeControlClientAttachDoesNotTakeLatest_Integration: the pipe attaches
// while three people are on the session (the PipeManager pins a pipe to every
// attached session, so it connects right after the people it follows). The
// attach itself hands the control client the window's latest slot; when the
// person the window follows leaves, the window must follow a remaining person.
func TestPipeControlClientAttachDoesNotTakeLatest_Integration(t *testing.T) {
	h := multiclienttmux.NewNamed(t, "boxed-attach")
	require.NoError(t, h.AddClient(140, 34))
	require.NoError(t, h.AddClient(200, 56))
	require.NoError(t, h.AddClient(92, 50))
	requireHarnessWindow(t, h, "92x49")

	pm := NewPipeManager(context.Background(), nil)
	t.Cleanup(pm.Close)
	require.NoError(t, pm.Connect(h.SessionName, h.SocketName))
	require.True(t, waitFor(3*time.Second, func() bool { return pm.IsConnected(h.SessionName) }))
	requireHarnessWindow(t, h, "92x49") // the latest person keeps the window

	require.NoError(t, h.DetachClient(2))
	requireHarnessWindow(t, h, "200x55")
}

// TestPipeConnectHealsWindowPolicy_Integration: a session created by an older
// build still carries `largest` (or rc.6's `smallest`) until something applies
// Deck's policy again. A PipeManager given the policy applies it when it
// connects, so every session a deck follows converges on one policy without
// a restart, and the user's [tmux] override still wins.
func TestPipeConnectHealsWindowPolicy_Integration(t *testing.T) {
	for _, tt := range []struct {
		name      string
		old       string
		overrides map[string]string
		want      string
	}{
		{name: "largest from a pre-1.16.11 build", old: "largest", want: windowSizeDefault(hostTmuxVersionString())},
		{name: "smallest from rc.6", old: "smallest", want: windowSizeDefault(hostTmuxVersionString())},
		{name: "a [tmux] override wins", old: "largest", overrides: map[string]string{"window-size": "smallest"}, want: "smallest"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := multiclienttmux.NewNamed(t, "policy")
			out, err := exec.Command("tmux", "-S", h.SocketPath,
				"set-option", "-w", "-t", h.SessionName, "window-size", tt.old).CombinedOutput()
			require.NoError(t, err, string(out))

			pm := NewPipeManager(context.Background(), nil)
			t.Cleanup(pm.Close)
			pm.SetSharedViewOverrides(func() map[string]string { return tt.overrides })
			require.NoError(t, pm.Connect(h.SessionName, h.SocketName))

			out, err = exec.Command("tmux", "-S", h.SocketPath,
				"show-options", "-wv", "-t", h.SessionName, "window-size").CombinedOutput()
			require.NoError(t, err, string(out))
			assert.Equal(t, tt.want, strings.TrimSpace(string(out)))
		})
	}
}
