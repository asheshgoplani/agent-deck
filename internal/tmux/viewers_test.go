//go:build !windows

package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
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

func TestViewersCached_ListingErrorIsUnknownUntilRetry(t *testing.T) {
	ResetViewersCacheForTest()
	t.Cleanup(ResetViewersCacheForTest)

	var calls atomic.Int32
	orig := listAllViewersOnSocket
	listAllViewersOnSocket = func(socketName string) (map[string][]Viewer, error) {
		if calls.Add(1) == 1 {
			return map[string][]Viewer{"agentdeck_x": {{Name: "/dev/pts/1"}}}, nil
		}
		return nil, assert.AnError
	}
	t.Cleanup(func() { listAllViewersOnSocket = orig })

	ViewersCached("sock", "agentdeck_x")
	require.Eventually(t, func() bool { _, known := ViewersCached("sock", "agentdeck_x"); return known }, 2*time.Second, 10*time.Millisecond)

	// Force a due entry and a failing refresh.
	expireViewersCacheForTest("sock")
	ViewersCached("sock", "agentdeck_x")
	require.Eventually(t, func() bool {
		_, known := ViewersCached("sock", "agentdeck_x")
		return calls.Load() >= 2 && !known
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, int32(2), calls.Load())

	viewers, known := ViewersCached("sock", "agentdeck_x")
	assert.False(t, known, "a failed refresh is 'unknown', never a stale answer and never 'nobody'")
	assert.Nil(t, viewers)
}

// TestViewersCached_DeadSocketIsRateLimited is the render loop on a socket
// whose server is down: 100 reads inside one TTL start one listing, and a
// repeated failure backs off rather than retrying every TTL.
func TestViewersCached_DeadSocketIsRateLimited(t *testing.T) {
	ResetViewersCacheForTest()
	t.Cleanup(ResetViewersCacheForTest)

	var calls atomic.Int32
	orig := listAllViewersOnSocket
	listAllViewersOnSocket = func(socketName string) (map[string][]Viewer, error) {
		calls.Add(1)
		return nil, errors.New("no server running on /tmp/tmux-501/dead")
	}
	t.Cleanup(func() { listAllViewersOnSocket = orig })

	for i := 0; i < 100; i++ {
		_, known := ViewersCached("dead", "agentdeck_x")
		assert.False(t, known)
		time.Sleep(4 * time.Millisecond) // 100 renders in ~400 ms, well inside one TTL
	}
	assert.Equal(t, int32(1), calls.Load(), "one listing per socket per TTL, even when it fails")

	viewersCacheMu.Lock()
	entry := viewersCache["dead"]
	firstDelay := entry.nextRefresh.Sub(entry.refreshedAt)
	viewersCacheMu.Unlock()
	assert.Equal(t, viewersCacheTTL, firstDelay, "the first failure is retried after one TTL")

	// The next failure doubles the wait; the one after doubles it again, up
	// to the cap.
	want := viewersCacheTTL
	for _, n := range []int32{2, 3, 4, 5, 6} {
		expireViewersCacheForTest("dead")
		ViewersCached("dead", "agentdeck_x")
		require.Eventually(t, func() bool { return calls.Load() == n }, 2*time.Second, 5*time.Millisecond)
		want = min(want*2, viewersCacheMaxBackoff)
		viewersCacheMu.Lock()
		got := entry.nextRefresh.Sub(entry.refreshedAt)
		viewersCacheMu.Unlock()
		assert.Equal(t, want, got, "failure %d", n)
	}

	// A listing that lands resets the backoff and warms the entry.
	listAllViewersOnSocket = func(socketName string) (map[string][]Viewer, error) {
		calls.Add(1)
		return map[string][]Viewer{}, nil
	}
	expireViewersCacheForTest("dead")
	ViewersCached("dead", "agentdeck_x")
	require.Eventually(t, func() bool { _, known := ViewersCached("dead", "agentdeck_x"); return known }, 2*time.Second, 5*time.Millisecond)
	viewersCacheMu.Lock()
	got := entry.nextRefresh.Sub(entry.refreshedAt)
	viewersCacheMu.Unlock()
	assert.Equal(t, viewersCacheTTL, got, "success resets the backoff")
}

// TestViewersCached_OneListingInFlightPerSocket: readers arriving while a
// listing is still running never start a second one.
func TestViewersCached_OneListingInFlightPerSocket(t *testing.T) {
	ResetViewersCacheForTest()
	t.Cleanup(ResetViewersCacheForTest)

	var calls atomic.Int32
	release := make(chan struct{})
	orig := listAllViewersOnSocket
	listAllViewersOnSocket = func(socketName string) (map[string][]Viewer, error) {
		calls.Add(1)
		<-release
		return nil, assert.AnError
	}
	t.Cleanup(func() { listAllViewersOnSocket = orig })

	for i := 0; i < 50; i++ {
		ViewersCached("slow", "agentdeck_x")
	}
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 2*time.Second, time.Millisecond)
	expireViewersCacheForTest("slow") // due again, but still in flight
	for i := 0; i < 50; i++ {
		ViewersCached("slow", "agentdeck_x")
	}
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, int32(1), calls.Load())
	close(release)
	require.Eventually(t, func() bool {
		viewersCacheMu.Lock()
		defer viewersCacheMu.Unlock()
		return !viewersCache["slow"].refreshing
	}, 2*time.Second, 5*time.Millisecond)
}

func TestViewersListingIsEmpty(t *testing.T) {
	t.Parallel()
	exit := func(stderr string) error {
		return &exec.ExitError{ProcessState: &os.ProcessState{}, Stderr: []byte(stderr + "\n")}
	}
	assert.True(t, viewersListingIsEmpty(exit("no server running on /tmp/tmux-501/dead")))
	assert.True(t, viewersListingIsEmpty(exit("error connecting to /tmp/tmux-501/dead (No such file or directory)")))
	assert.True(t, viewersListingIsEmpty(exit("can't find session: agentdeck_gone")))
	assert.False(t, viewersListingIsEmpty(exit("error connecting to /tmp/tmux-0/default (Permission denied)")))
	assert.False(t, viewersListingIsEmpty(exit("lost server")))
	assert.False(t, viewersListingIsEmpty(exit("protocol version mismatch (client 8, server 7)")))
	assert.False(t, viewersListingIsEmpty(context.DeadlineExceeded))
	assert.False(t, viewersListingIsEmpty(errors.New("no server running")), "only tmux's own exit carries the verdict")
}

// TestListViewers_NothingThereIsNobody: a socket with no server and a
// session that does not exist both answer "nobody", not "unknown", so
// `session viewers`, `list --json` and the badge agree on a stopped session.
func TestListViewers_NothingThereIsNobody(t *testing.T) {
	skipIfNoTmuxBinary(t)
	ctx := context.Background()

	dead := fmt.Sprintf("agentdeck-viewers-dead-%d", os.Getpid())
	viewers, err := ListViewers(ctx, dead, "agentdeck_stopped")
	require.NoError(t, err, "no server on the socket: tmux answered, nobody is there")
	assert.Empty(t, viewers)
	all, err := ListAllViewers(ctx, dead)
	require.NoError(t, err)
	assert.NotNil(t, all)
	assert.Empty(t, all)

	s := newSharedViewSession(t, "alive")
	viewers, err = ListViewers(ctx, s.SocketName, "agentdeck_no_such_session")
	require.NoError(t, err, "a live server without that session: nobody")
	assert.Empty(t, viewers)
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
	assert.Contains(t, log, "; the window follows whoever types", "the notice says what the size policy does")
	assert.NotContains(t, log, others[0].Name+" message: also viewing")
}

// TestViewerNotice_CountsAsSubprocess: the notice and the post-attach fit
// diagnostic spawn tmux through the counted runners, so `health --json`
// tmux_calls sees them like every other call.
func TestViewerNotice_CountsAsSubprocess(t *testing.T) {
	s := newSharedViewSession(t, "counted")
	attachSharedViewClient(t, s.SocketName, s.Name, 120, 40)
	waitWindowSize(t, s, "120x39")
	viewers, err := ListViewers(context.Background(), s.SocketName, s.Name)
	require.NoError(t, err)
	require.Len(t, viewers, 1)

	before := SubprocessStarts()
	s.showViewerNotice(context.Background(), viewers[0], "counted")
	assert.Equal(t, before+1, SubprocessStarts(), "showViewerNotice is one counted tmux spawn")

	before = SubprocessStarts()
	s.logSharedViewFit(context.Background(), 200, 60)
	assert.GreaterOrEqual(t, SubprocessStarts(), before+1, "logSharedViewFit's display-message is counted")
}
