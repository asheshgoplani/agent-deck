//go:build !windows

package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseViewers(t *testing.T) {
	t.Parallel()
	out := strings.Join([]string{
		"agentdeck_a\t/dev/ttys004\t/dev/ttys004\t200x60\t1789724772\tashesh\t0",
		"agentdeck_a\t/dev/pts/3\t/dev/pts/3\t120x40\t1789724800\t\t0",
		"agentdeck_a\tcontrol-1\t\t0x0\t1789724900\tashesh\t1", // agent-deck's own pipe
		"agentdeck_b\t/dev/pts/9\t/dev/pts/9\t80x24\t0\tyasir\t0",
		"",
		"garbage line",
	}, "\n")
	owner := func(tty string) string {
		if tty == "/dev/pts/3" {
			return "yasir"
		}
		return ""
	}
	got := parseViewers(out, owner)

	require.Len(t, got, 2)
	a := got["agentdeck_a"]
	require.Len(t, a, 2, "control-mode clients are not viewers")
	// Most recently active first.
	assert.Equal(t, "/dev/pts/3", a[0].Name)
	assert.Equal(t, "yasir", a[0].User, "tmux < 3.2 reports no client_user: fall back to the tty owner")
	assert.Equal(t, 120, a[0].Width)
	assert.Equal(t, 40, a[0].Height)
	assert.Equal(t, time.Unix(1789724800, 0), a[0].Activity)
	assert.Equal(t, "ashesh", a[1].User)
	assert.Equal(t, 200, a[1].Width)

	b := got["agentdeck_b"]
	require.Len(t, b, 1)
	assert.True(t, b[0].Activity.IsZero(), "a zero client_activity is unknown, not 1970")
}

func TestViewerLabels(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	viewers := []Viewer{
		{Name: "/dev/ttys004", TTY: "/dev/ttys004", Width: 200, Height: 60, User: "ashesh", Activity: now.Add(-5 * time.Second)},
		{Name: "/dev/pts/3", TTY: "/dev/pts/3", Width: 120, Height: 40, User: "yasir", Activity: now.Add(-3 * time.Minute)},
		{Name: "/dev/pts/7", TTY: "/dev/pts/7", Width: 80, Height: 24},
	}
	assert.Equal(t, "ashesh (200x60, active 5s ago)", viewers[0].Label(now))
	assert.Equal(t, "yasir (120x40, 3m ago)", viewers[1].Label(now))
	assert.Equal(t, "pts/7 (80x24, idle)", viewers[2].Label(now), "no user: the tty names the viewer")
	assert.Equal(t, "ashesh (200x60, active 5s ago) · yasir (120x40, 3m ago) · pts/7 (80x24, idle)",
		FormatViewersAt(viewers, now))
	assert.Equal(t, "", FormatViewersAt(nil, now))
	assert.Equal(t, "2h ago", Viewer{Activity: now.Add(-2 * time.Hour)}.ActivityLabel(now))
	assert.Equal(t, "3d ago", Viewer{Activity: now.Add(-72 * time.Hour)}.ActivityLabel(now))
}

func TestViewersCached_ColdIsUnknownThenWarm(t *testing.T) {
	ResetViewersCacheForTest()
	t.Cleanup(ResetViewersCacheForTest)

	listed := make(chan struct{}, 4)
	orig := listAllViewersOnSocket
	listAllViewersOnSocket = func(socketName string) (map[string][]Viewer, error) {
		defer func() { listed <- struct{}{} }()
		return map[string][]Viewer{"agentdeck_x": {{Name: "/dev/pts/1", Width: 100, Height: 30}}}, nil
	}
	t.Cleanup(func() { listAllViewersOnSocket = orig })

	viewers, known := ViewersCached("sock", "agentdeck_x")
	assert.False(t, known, "a cold cache is unknown, never 'nobody'")
	assert.Nil(t, viewers)

	select {
	case <-listed:
	case <-time.After(2 * time.Second):
		t.Fatal("cold read must kick a background listing")
	}
	require.Eventually(t, func() bool {
		_, known := ViewersCached("sock", "agentdeck_x")
		return known
	}, 2*time.Second, 10*time.Millisecond)

	viewers, known = ViewersCached("sock", "agentdeck_x")
	assert.True(t, known)
	require.Len(t, viewers, 1)
	assert.Equal(t, 100, viewers[0].Width)

	other, known := ViewersCached("sock", "agentdeck_nobody")
	assert.True(t, known, "a session with no clients is known-empty once the socket is listed")
	assert.Empty(t, other)
}

func TestViewersCached_ListingErrorKeepsLastKnown(t *testing.T) {
	ResetViewersCacheForTest()
	t.Cleanup(ResetViewersCacheForTest)

	calls := 0
	orig := listAllViewersOnSocket
	listAllViewersOnSocket = func(socketName string) (map[string][]Viewer, error) {
		calls++
		if calls == 1 {
			return map[string][]Viewer{"agentdeck_x": {{Name: "/dev/pts/1"}}}, nil
		}
		return nil, assert.AnError
	}
	t.Cleanup(func() { listAllViewersOnSocket = orig })

	ViewersCached("sock", "agentdeck_x")
	require.Eventually(t, func() bool { _, known := ViewersCached("sock", "agentdeck_x"); return known }, 2*time.Second, 10*time.Millisecond)

	// Force a stale entry and a failing refresh.
	viewersCacheMu.Lock()
	viewersCache["sock"].refreshedAt = time.Now().Add(-time.Minute)
	viewersCacheMu.Unlock()
	ViewersCached("sock", "agentdeck_x")
	require.Eventually(t, func() bool { return calls >= 2 }, 2*time.Second, 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)

	viewers, known := ViewersCached("sock", "agentdeck_x")
	assert.True(t, known)
	assert.Len(t, viewers, 1, "a failed refresh keeps the last listing rather than flapping to nobody")
}

// TestListViewers_Integration lists two pty clients of different sizes and
// ignores the session's own control-mode pipe.
func TestListViewers_Integration(t *testing.T) {
	s := newSharedViewSession(t, "viewers")

	none, err := ListViewers(context.Background(), s.SocketName, s.Name)
	require.NoError(t, err)
	assert.Empty(t, none)

	attachSharedViewClient(t, s.SocketName, s.Name, 120, 40)
	attachSharedViewClient(t, s.SocketName, s.Name, 200, 60)
	// A control-mode client, as the deck's pipe manager opens.
	control := exec.Command("tmux", "-L", s.SocketName, "-C", "attach-session", "-t", s.Name)
	stdin, err := control.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, control.Start())
	t.Cleanup(func() { _ = stdin.Close(); _ = control.Process.Kill(); _, _ = control.Process.Wait() })

	var viewers []Viewer
	require.Eventually(t, func() bool {
		viewers, err = ListViewers(context.Background(), s.SocketName, s.Name)
		return err == nil && len(viewers) == 2
	}, 5*time.Second, 50*time.Millisecond, "two terminals, no control pipe: %v %v", viewers, err)

	for _, v := range viewers {
		assert.NotEmpty(t, v.Name)
		assert.NotEmpty(t, v.DisplayName())
	}
	assert.ElementsMatch(t, []int{120, 200}, []int{viewers[0].Width, viewers[1].Width})

	all, err := ListAllViewers(context.Background(), s.SocketName)
	require.NoError(t, err)
	assert.Len(t, all[s.Name], 2)
}

// TestAnnounceOtherViewers_Integration: the client that just attached is
// told who else is there, on its own status line, and nobody is detached.
func TestAnnounceOtherViewers_Integration(t *testing.T) {
	s := newSharedViewSession(t, "notice")
	attachSharedViewClient(t, s.SocketName, s.Name, 120, 40)
	waitWindowSize(t, s, "120x39")

	others := s.prepareSharedAttach(context.Background())
	require.Len(t, others, 1)

	attachSharedViewClient(t, s.SocketName, s.Name, 200, 60)
	waitWindowSize(t, s, "200x59")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.announceOtherViewers(ctx, others, 3*time.Second)

	viewers, err := ListViewers(context.Background(), s.SocketName, s.Name)
	require.NoError(t, err)
	require.Len(t, viewers, 2, "announcing never detaches anyone")
	newcomer := viewers[0]
	if newcomer.Name == others[0].Name {
		newcomer = viewers[1]
	}

	// display-message logs what it showed and to which client.
	out, err := s.tmuxCmd("show-messages").Output()
	require.NoError(t, err)
	log := string(out)
	assert.Contains(t, log, newcomer.Name+" message: also viewing: "+others[0].DisplayName()+" (120x40, ",
		"the notice names the earlier viewer and goes to the new client only")
	assert.NotContains(t, log, others[0].Name+" message: also viewing")
}
